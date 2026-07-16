/*
 Copyright 2021 The Volcano Authors.

 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package allocate

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"volcano.sh/apis/pkg/apis/scheduling"
	"volcano.sh/volcano/cmd/scheduler/app/options"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/metrics"
	"volcano.sh/volcano/pkg/scheduler/util"
	commonutil "volcano.sh/volcano/pkg/util"
)

type allocateContext struct {
	queues              *util.PriorityQueue                 // queue of *api.QueueInfo
	jobsByQueue         map[api.QueueID]*util.PriorityQueue // queue of *api.JobInfo
	jobWorksheet        map[api.JobID]*JobWorksheet
	tasksNoHardTopology map[api.JobID]*util.PriorityQueue // queue of *api.TaskInfo, job without any hard network topology policy use this queue
}

type JobWorksheet struct {
	subJobs          *util.PriorityQueue // queue of *api.SubJobInfo
	subJobWorksheets map[api.SubJobID]*SubJobWorksheet
	requiredSubJobs  sets.Set[api.SubJobID]
}

func (w *JobWorksheet) ShallowCopyFrom(another *JobWorksheet) {
	if another == nil {
		return
	}
	w.subJobs = another.subJobs
	w.subJobWorksheets = another.subJobWorksheets
	w.requiredSubJobs = another.requiredSubJobs
}

func (w *JobWorksheet) Empty() bool {
	return w.subJobs == nil || w.subJobs.Empty()
}

func (w *JobWorksheet) Clone() *JobWorksheet {
	subJobWorksheets := make(map[api.SubJobID]*SubJobWorksheet)
	for subJobID, tasks := range w.subJobWorksheets {
		subJobWorksheets[subJobID] = tasks.Clone()
	}
	return &JobWorksheet{
		subJobs:          w.subJobs.Clone(),
		subJobWorksheets: subJobWorksheets,
		requiredSubJobs:  w.requiredSubJobs.Clone(),
	}
}

type SubJobWorksheet struct {
	tasks *util.PriorityQueue // queue of *api.TaskInfo
}

func (w *SubJobWorksheet) ShallowCopyFrom(another *SubJobWorksheet) {
	if another == nil {
		return
	}
	w.tasks = another.tasks
}

func (w *SubJobWorksheet) Empty() bool {
	return w.tasks == nil || w.tasks.Empty()
}

func (w *SubJobWorksheet) Clone() *SubJobWorksheet {
	return &SubJobWorksheet{
		tasks: w.tasks.Clone(),
	}
}

type Action struct {
	session *framework.Session
	// configured flag for error cache
	enablePredicateErrorCache bool

	recorder *Recorder
}

// subJobPlacementCandidate is the recoverable result of one SubJob dry-run
// placement. Topology plugins provide candidate HyperNodes and scores; the
// allocate action owns the statement and worksheet state used to try them.
type subJobPlacementCandidate struct {
	hyperNode          string
	stmt               *framework.Statement
	worksheet          *SubJobWorksheet
	allocatedHyperNode string
	score              float64
	gradient           int
}

type jobAllocationSolution struct {
	stmt                         *framework.Statement
	worksheet                    *JobWorksheet
	score                        float64
	allocatedHyperNode           string
	softTopologyTargetSubJobs    int
	softTopologyAllocatedSubJobs int
	subJobDecisions              map[api.SubJobID]string
}

type topologySearchContext struct {
	bestScoreByState map[string]float64
	exploredStates   int
	prunedStates     int
}

func newTopologySearchContext() *topologySearchContext {
	return &topologySearchContext{bestScoreByState: make(map[string]float64)}
}

func (ctx *topologySearchContext) shouldExplore(state string, cumulativeScore float64) bool {
	if bestScore, found := ctx.bestScoreByState[state]; found && bestScore >= cumulativeScore {
		ctx.prunedStates++
		return false
	}
	ctx.bestScoreByState[state] = cumulativeScore
	ctx.exploredStates++
	return true
}

func New() *Action {
	return &Action{
		enablePredicateErrorCache: true, // default to enable it
	}
}

func (alloc *Action) Name() string {
	return "allocate"
}

func (alloc *Action) Initialize() {}

func (alloc *Action) parseArguments(ssn *framework.Session) {
	arguments := framework.GetArgOfActionFromConf(ssn.Configurations, alloc.Name())
	arguments.GetBool(&alloc.enablePredicateErrorCache, conf.EnablePredicateErrCacheKey)
}

func (alloc *Action) Execute(ssn *framework.Session) {
	klog.V(5).Infof("Enter Allocate ...")
	defer klog.V(5).Infof("Leaving Allocate ...")

	alloc.parseArguments(ssn)

	// the allocation for pod may have many stages
	// 1. pick a queue named Q (using ssn.QueueOrderFn)
	// 2. pick a job named J from Q (using ssn.JobOrderFn)
	// 3. pick a task T from J (using ssn.TaskOrderFn)
	// 4. use predicateFn to filter out node that T can not be allocated on.
	// 5. use ssn.NodeOrderFn to judge the best node and assign it to T

	alloc.session = ssn
	logHyperNodeTiers(ssn)
	alloc.recorder = NewRecorder()
	actx := alloc.buildAllocateContext()
	klog.V(3).Infof("Try to allocate resource to %d Queues", actx.queues.Len())
	alloc.allocateResources(actx)
}

func (alloc *Action) buildAllocateContext() *allocateContext {
	ssn := alloc.session

	actx := &allocateContext{
		queues:              util.NewPriorityQueue(ssn.QueueOrderFn), // queues sort queues by QueueOrderFn.
		jobsByQueue:         make(map[api.QueueID]*util.PriorityQueue),
		jobWorksheet:        make(map[api.JobID]*JobWorksheet),
		tasksNoHardTopology: make(map[api.JobID]*util.PriorityQueue),
	}

	for _, job := range ssn.Jobs {
		// If not config enqueue action, change Pending pg into Inqueue state to avoid blocking job scheduling.
		if job.IsPending() {
			if conf.EnabledActionMap["enqueue"] {
				klog.V(4).Infof("Job <%s/%s> Queue <%s> skip allocate, reason: job status is pending.",
					job.Namespace, job.Name, job.Queue)
				continue
			} else {
				klog.V(4).Infof("Job <%s/%s> Queue <%s> status update from pending to inqueue, reason: no enqueue action is configured.",
					job.Namespace, job.Name, job.Queue)
				job.PodGroup.Status.Phase = scheduling.PodGroupInqueue
			}
		}

		if vr := ssn.JobValid(job); vr != nil && !vr.Pass {
			klog.V(4).Infof("Job <%s/%s> Queue <%s> skip allocate, reason: %v, message %v", job.Namespace, job.Name, job.Queue, vr.Reason, vr.Message)
			continue
		}

		if _, found := ssn.Queues[job.Queue]; !found {
			klog.Warningf("Skip adding Job <%s/%s> because its queue %s is not found",
				job.Namespace, job.Name, job.Queue)
			continue
		}

		if !ssn.HyperNodesReadyToSchedule && (job.ContainsNetworkTopology() || job.WithTopologyAffinity()) {
			klog.V(4).Infof("Job <%s/%s> Queue <%s> skip allocate, reason: hyperNodes are not ready for scheduling",
				job.Namespace, job.Name, job.Queue)
			continue
		}

		worksheet := alloc.organizeJobWorksheet(job)
		if worksheet.Empty() {
			continue
		}

		if _, found := actx.jobsByQueue[job.Queue]; !found {
			actx.jobsByQueue[job.Queue] = util.NewPriorityQueue(ssn.JobOrderFn)
			actx.queues.Push(ssn.Queues[job.Queue])
		}

		klog.V(4).Infof("Added Job <%s/%s> into Queue <%s>", job.Namespace, job.Name, job.Queue)
		actx.jobsByQueue[job.Queue].Push(job)
		actx.jobWorksheet[job.UID] = worksheet

		// Jobs that do not need HyperNode-level allocation use actx.tasksNoHardTopology.
		if !job.RequiresHyperNodeAllocate() {
			if subJobWorksheet, exist := worksheet.subJobWorksheets[job.DefaultSubJobID()]; exist {
				actx.tasksNoHardTopology[job.UID] = subJobWorksheet.tasks
			}
		}
	}

	return actx
}

func (alloc *Action) organizeJobWorksheet(job *api.JobInfo) *JobWorksheet {
	ssn := alloc.session

	subJobs := make([]*api.SubJobInfo, 0, len(job.SubJobs))
	subJobCountMap := map[api.SubJobGID]int32{}
	for _, subJob := range job.SubJobs {
		if ssn.SubJobReady(job, subJob) {
			// Record the number of subJobs that have been satisfied in subGroupPolicy
			subJobCountMap[subJob.GID]++
		} else {
			// Filter out subJobs that are already ready.
			subJobs = append(subJobs, subJob)
		}
	}
	slices.SortFunc(subJobs, func(l, r *api.SubJobInfo) int {
		if !ssn.SubJobOrderFn(l, r) {
			return 1
		}
		return -1
	})
	// Find the smallest set of subJobs that meets the requirements for job execution.
	requireSubJobs := sets.Set[api.SubJobID]{}
	for _, subJob := range subJobs {
		if subJobCountMap[subJob.GID] < job.MinSubJobs[subJob.GID] {
			requireSubJobs.Insert(subJob.UID)
			subJobCountMap[subJob.GID]++
		}
	}
	jWorksheet := &JobWorksheet{
		subJobs: util.NewPriorityQueue(func(l, r interface{}) bool {
			lv := l.(*api.SubJobInfo)
			rv := r.(*api.SubJobInfo)

			lreq := requireSubJobs.Has(lv.UID)
			rreq := requireSubJobs.Has(rv.UID)
			if lreq != rreq {
				return lreq
			}
			return ssn.SubJobOrderFn(l, r)
		}),
		subJobWorksheets: make(map[api.SubJobID]*SubJobWorksheet),
		requiredSubJobs:  requireSubJobs,
	}

	for subJobID, subJob := range job.SubJobs {
		sjWorksheet := &SubJobWorksheet{
			tasks: util.NewPriorityQueue(ssn.TaskOrderFn),
		}

		for _, task := range subJob.TaskStatusIndex[api.Pending] {
			// Skip tasks whose pod are scheduling gated
			if task.SchGated {
				continue
			}

			// Skip BestEffort task in 'allocate' action.
			if task.Resreq.IsEmpty() {
				klog.V(4).Infof("Task <%v/%v> is BestEffort task, skip it.",
					task.Namespace, task.Name)
				continue
			}
			sjWorksheet.tasks.Push(task)
		}

		if !sjWorksheet.Empty() {
			jWorksheet.subJobs.Push(subJob)
			jWorksheet.subJobWorksheets[subJobID] = sjWorksheet
		}
	}

	return jWorksheet
}

func (alloc *Action) allocateResources(actx *allocateContext) {
	ssn := alloc.session

	queues := actx.queues
	for {
		if queues.Empty() {
			break
		}

		queue := queues.Pop().(*api.QueueInfo)

		if ssn.Overused(queue) {
			klog.V(3).Infof("Queue <%s> is overused, ignore it.", queue.Name)
			continue
		}

		jobs, found := actx.jobsByQueue[queue.UID]
		if !found || jobs.Empty() {
			klog.V(4).Infof("Can not find jobs for queue %s.", queue.Name)
			continue
		}

		job := jobs.Pop().(*api.JobInfo)
		updateJobTier(ssn.HyperNodeTierNameMap, job)
		// Currently, both hard-mode network topology scheduling and subjob level scheduling use allocateForJob.
		// TODO: In the future, we may need to unify the logic of network topology-aware scheduling and normal scheduling.
		if job.RequiresHyperNodeAllocate() {
			jobWorksheet := actx.jobWorksheet[job.UID]

			klog.V(3).Infof("Try to allocate resource for job requires hyperNode allocate, queue=%s, job=%s, allocatedHyperNode=%s, subJobNum=%d",
				queue.Name, job.UID, job.AllocatedHyperNode, jobWorksheet.subJobs.Len())
			stmt := alloc.allocateForJob(job, jobWorksheet, ssn.HyperNodes[framework.ClusterTopHyperNode])
			if stmt != nil && ssn.JobReady(job) { // do not commit stmt when job is pipelined
				stmt.Commit()
				ssn.MarkJobDirty(job.UID)
				alloc.recorder.UpdateDecisionToJob(job, ssn.HyperNodes)

				// There are still left tasks that need to be allocated when min available < replicas, put the job back
				if !jobWorksheet.Empty() {
					jobs.Push(job)
				}
			}
		} else {
			subJob, sjExist := job.SubJobs[job.DefaultSubJobID()]
			tasks, tasksExist := actx.tasksNoHardTopology[job.UID]
			if sjExist && tasksExist {
				klog.V(3).Infof("Try to allocate resource, queue=%s, job=%s, taskNum=%d", queue.Name, job.UID, tasks.Len())
				stmt := alloc.allocateResourcesForTasks(subJob, tasks, framework.ClusterTopHyperNode)
				if stmt != nil && ssn.JobReady(job) { // do not commit stmt when job is pipelined
					stmt.Commit()

					// There are still left tasks that need to be allocated when min available < replicas, put the job back
					if tasks.Len() > 0 {
						jobs.Push(job)
					}
				}
			} else {
				klog.Errorf("Can not find default subJob or tasks for job, job=%s, subJobExist=%t, tasksExist=%t",
					job.UID, sjExist, tasksExist)
			}
		}

		// Put back the queue to priority queue after job's resource allocating finished,
		// To ensure that the priority of the queue is calculated based on the latest resource allocation situation.
		queues.Push(queue)
	}
}

func (alloc *Action) allocateForJob(job *api.JobInfo, jobWorksheet *JobWorksheet, hyperNodeToAllocate *api.HyperNodeInfo) *framework.Statement {
	ssn := alloc.session

	if jobWorksheet == nil || jobWorksheet.Empty() {
		klog.V(4).Infof("Empty job worksheet, job=%s", job.UID)
		return nil
	}

	alloc.recorder.SnapshotSubJobStatus(job, jobWorksheet)

	hyperNodeGradients, gradientStats := ssn.HyperNodeGradientForJobFn(job, hyperNodeToAllocate)
	hyperNodeGradients, resourceStats := FilterGradientsByMinResource(
		ssn, hyperNodeGradients, job.GetMinResources(), job.AllocatedHyperNode,
	)
	job.SetHyperNodeFitErrors(gradientStats, resourceStats, job.GetMinResources(),
		ssn.HyperNodesSetByTier, ssn.HyperNodeTierNameMap, ssn.HyperNodes)
	// jobHyperNodeBaseline is the job-level HyperNode diagnostic written to JobFitErrors.
	// SubJob gradient failures append to it via MergeSubJobHyperNodeFitErrors; each HyperNode
	// dry-run resets JobFitErrors back to this baseline before trying subJobs.
	jobHyperNodeBaseline := job.JobFitErrors
	if len(hyperNodeGradients) == 0 {
		// No candidate HyperNodes: event carries job-level summary only; subJob allocate is skipped.
		klog.V(3).Infof("No hyperNode gradient for job, job=%s, fitError=%s", job.UID, job.JobFitErrors)
		return nil
	}
	klog.V(3).Infof("HyperNode screening for job, job=%s, fitError=%s", job.UID, job.JobFitErrors)
	if gradientStats != nil && len(gradientStats.ExcludedByReason) > 0 {
		klog.V(3).Infof("HyperNode excluded by plugin, job=%s, excluded=%v", job.UID, gradientStats.ExcludedByReason)
	}
	if resourceStats != nil && len(resourceStats.ExcludedByReason) > 0 {
		klog.V(3).Infof("HyperNode excluded by minResource, job=%s, excluded=%v", job.UID, resourceStats.ExcludedByReason)
	}
	workingSet := buildTopologyWorkingSet(jobWorksheet)
	solutions := make(map[string]*jobAllocationSolution)
	for gradient, hyperNodes := range hyperNodeGradients {
		for _, hyperNode := range hyperNodes {
			// Clone jobWorksheet and rest job's fit err to make sure it's a clean cache when everytime filter a hyperNode and do not affect each other between hyperNodes.
			job.JobFitErrors = jobHyperNodeBaseline // drop subJob overlay from prior HyperNode attempt
			job.ResetFitErr()
			jobWorksheetCopy := jobWorksheet.Clone()
			klog.V(3).Infof("Try to allocate resource for job in hyperNode, job=%s, hyperNode=%s, tierLayer=%d", job.UID, hyperNode.Name, gradient)

			solution := alloc.allocateForJobInHyperNode(
				job, jobWorksheetCopy, hyperNode, jobHyperNodeBaseline, workingSet.Clone(),
			)
			// reset the subJobs to initial status
			alloc.recorder.RecoverSubJobStatus(job)

			if solution == nil || solution.stmt == nil || len(solution.stmt.Operations()) == 0 {
				klog.V(3).Infof("Try to allocate resource for job in hyperNode fail, job=%s, hyperNode=%s, tierLayer=%d, reason=no allocatable solution",
					job.UID, hyperNode.Name, gradient)
				continue // skip recording this empty solution
			}
			klog.V(3).Infof("Try to allocate resource for job in hyperNode success, job=%s, hyperNode=%s, tierLayer=%d",
				job.UID, hyperNode.Name, gradient)
			solutions[hyperNode.Name] = solution
			for subJobID, subJobHyperNode := range solution.subJobDecisions {
				alloc.recorder.SaveSubJobDecision(job.UID, hyperNode.Name, subJobID, subJobHyperNode)
			}
		}

	}

	if len(solutions) == 0 {
		klog.V(5).Infof("Cannot find any solution for job, job=%s, fitError=%s", job.UID, job.JobFitErrors)
		return nil
	}

	bestHyperNode, err := alloc.selectBestHyperNodeForJob(solutions, job)
	if err != nil {
		klog.Errorf("Cannot find best hyper node for job, job=%s, err=%v", job.UID, err)
		return nil
	}
	return alloc.recoverJobAllocationSolution(job, jobWorksheet, bestHyperNode, solutions[bestHyperNode])
}

func (alloc *Action) recoverJobAllocationSolution(
	job *api.JobInfo,
	jobWorksheet *JobWorksheet,
	hyperNode string,
	solution *jobAllocationSolution,
) *framework.Statement {
	if solution == nil || solution.stmt == nil {
		return nil
	}

	finalStmt := framework.NewStatement(alloc.session)
	if err := finalStmt.RecoverOperations(solution.stmt); err != nil {
		klog.Errorf("Failed to recover operations, job=%s, hyperNode=%s, err=%v", job.UID, hyperNode, err)
		finalStmt.Discard()
		return nil
	}

	jobWorksheet.ShallowCopyFrom(solution.worksheet)
	alloc.recorder.SaveJobDecision(job.UID, hyperNode)
	klog.V(3).Infof("Allocate job to hyperNode success, job=%s, hyperNode=%s", job.UID, hyperNode)

	return finalStmt
}

func buildTopologyWorkingSet(jobWorksheet *JobWorksheet) sets.Set[api.SubJobID] {
	workingSet := sets.New[api.SubJobID]()
	if jobWorksheet == nil || jobWorksheet.subJobs == nil {
		return workingSet
	}

	pending := jobWorksheet.subJobs.Clone()
	var first api.SubJobID
	for !pending.Empty() {
		subJob := pending.Pop().(*api.SubJobInfo)
		if first == "" {
			first = subJob.UID
		}
		if jobWorksheet.requiredSubJobs.Has(subJob.UID) {
			workingSet.Insert(subJob.UID)
		}
	}
	if workingSet.Len() == 0 && first != "" {
		workingSet.Insert(first)
	}
	return workingSet
}

func (alloc *Action) allocateForJobInHyperNode(
	job *api.JobInfo,
	jobWorksheet *JobWorksheet,
	hyperNode *api.HyperNodeInfo,
	jobHyperNodeBaseline string,
	workingSet sets.Set[api.SubJobID],
) *jobAllocationSolution {
	if job.ContainsHardSubGroupTopologyAffinity() {
		searchContext := newTopologySearchContext()
		solution, err := alloc.searchSubJobAllocations(
			job, jobWorksheet, hyperNode, jobHyperNodeBaseline, workingSet, sets.New[api.SubJobID](),
			searchContext, 0,
		)
		klog.V(3).Infof("Topology allocation search completed, job=%s, hyperNode=%s, exploredStates=%d, prunedStates=%d",
			job.UID, hyperNode.Name, searchContext.exploredStates, searchContext.prunedStates)
		if err != nil {
			klog.Errorf("Search subJob allocations failed, job=%s, hyperNode=%s, err=%v", job.UID, hyperNode.Name, err)
			return nil
		}
		return solution
	}

	var stmtList []*framework.Statement
	var subJobsAllocationScore float64
	processed := sets.New[api.SubJobID]()
	failed := sets.New[api.SubJobID]()
	subJobDecisions := make(map[api.SubJobID]string)
	ssn := alloc.session
	for !jobWorksheet.subJobs.Empty() {
		ready := ssn.JobReady(job) || ssn.JobPipelined(job)
		if ready && workingSet.Difference(processed).Len() == 0 {
			break
		}
		subJob := popNextTransactionSubJob(jobWorksheet, workingSet, processed, failed, ready)
		if subJob == nil {
			break
		}
		subJobWorksheet := jobWorksheet.subJobWorksheets[subJob.UID]

		stmt, allocationScore := alloc.allocateForSubJob(subJob, subJobWorksheet, hyperNode, jobHyperNodeBaseline)

		if stmt != nil && len(stmt.Operations()) > 0 {
			stmtList = append(stmtList, stmt)
			subJobsAllocationScore += allocationScore
			processed.Insert(subJob.UID)
			subJobDecisions[subJob.UID] = subJob.AllocatedHyperNode
			// push back when subJob is ready and remain pending task
			if !subJobWorksheet.Empty() {
				jobWorksheet.subJobs.Push(subJob)
			}
		} else {
			if !subJobWorksheet.Empty() {
				jobWorksheet.subJobs.Push(subJob)
			}
			processed.Insert(subJob.UID)
			failed.Insert(subJob.UID)
			if workingSet.Has(subJob.UID) {
				break
			}
		}
	}

	mergedStmt := framework.SaveOperations(stmtList...)
	if len(mergedStmt.Operations()) == 0 {
		return nil
	}
	if !ssn.JobReady(job) && !ssn.JobPipelined(job) {
		klog.V(3).Infof("Try to allocate resource for job in hyperNode fail, job=%s, hyperNode=%s, reason=job not ready or pipelined",
			job.UID, hyperNode.Name)
		for _, stmt := range stmtList {
			stmt.Discard()
		}
		return nil
	}

	for _, stmt := range stmtList {
		stmt.Discard()
	}
	return &jobAllocationSolution{
		stmt:                         mergedStmt,
		worksheet:                    jobWorksheet,
		score:                        subJobsAllocationScore,
		allocatedHyperNode:           job.AllocatedHyperNode,
		softTopologyTargetSubJobs:    workingSet.Len(),
		softTopologyAllocatedSubJobs: processed.Intersection(workingSet).Len(),
		subJobDecisions:              subJobDecisions,
	}
}

func popNextTransactionSubJob(
	jobWorksheet *JobWorksheet,
	workingSet sets.Set[api.SubJobID],
	processed sets.Set[api.SubJobID],
	failed sets.Set[api.SubJobID],
	workingSetOnly bool,
) *api.SubJobInfo {
	if jobWorksheet == nil || jobWorksheet.subJobs == nil {
		return nil
	}

	skipped := make([]*api.SubJobInfo, 0)
	var selected *api.SubJobInfo
	for !jobWorksheet.subJobs.Empty() {
		subJob := jobWorksheet.subJobs.Pop().(*api.SubJobInfo)
		if failed.Has(subJob.UID) ||
			workingSetOnly && (processed.Has(subJob.UID) || !workingSet.Has(subJob.UID)) {
			skipped = append(skipped, subJob)
			continue
		}
		selected = subJob
		break
	}
	for _, subJob := range skipped {
		jobWorksheet.subJobs.Push(subJob)
	}
	return selected
}

// searchSubJobAllocations compares feasible placements for the SubJobs needed
// by the current gang transaction. A ready SubJob is processed at most once;
// remaining replicas stay in the worksheet for a later transaction.
func (alloc *Action) searchSubJobAllocations(
	job *api.JobInfo,
	jobWorksheet *JobWorksheet,
	hyperNodeForJob *api.HyperNodeInfo,
	jobHyperNodeBaseline string,
	workingSet sets.Set[api.SubJobID],
	processed sets.Set[api.SubJobID],
	searchContext *topologySearchContext,
	cumulativeScore float64,
) (*jobAllocationSolution, error) {
	ssn := alloc.session
	if searchContext == nil {
		searchContext = newTopologySearchContext()
	}
	state := topologySearchStateKey(job, jobWorksheet, processed)
	if !searchContext.shouldExplore(state, cumulativeScore) {
		return nil, nil
	}
	if ssn.JobReady(job) || ssn.JobPipelined(job) {
		if workingSet.Difference(processed).Len() == 0 {
			var clonedWorksheet *JobWorksheet
			if jobWorksheet != nil {
				clonedWorksheet = jobWorksheet.Clone()
			}
			return &jobAllocationSolution{
				stmt:                         framework.NewStatement(ssn),
				worksheet:                    clonedWorksheet,
				allocatedHyperNode:           job.AllocatedHyperNode,
				softTopologyTargetSubJobs:    workingSet.Len(),
				softTopologyAllocatedSubJobs: processed.Intersection(workingSet).Len(),
				subJobDecisions:              map[api.SubJobID]string{},
			}, nil
		}
	}
	if jobWorksheet == nil || jobWorksheet.Empty() {
		return nil, nil
	}

	remainingWorksheet := jobWorksheet.Clone()
	ready := ssn.JobReady(job) || ssn.JobPipelined(job)
	subJob := popNextTransactionSubJob(
		remainingWorksheet, workingSet, processed, sets.New[api.SubJobID](), ready,
	)
	if subJob == nil {
		return nil, nil
	}
	subJobWorksheet := remainingWorksheet.subJobWorksheets[subJob.UID]
	options, err := alloc.collectSubJobPlacementCandidates(subJob, subJobWorksheet, hyperNodeForJob, jobHyperNodeBaseline)
	if err != nil {
		return nil, err
	}

	var bestSolution *jobAllocationSolution
	for _, option := range options {
		placementBeforeTry := captureHyperNodePlacement(job, subJob)
		finalStmt := framework.NewStatement(ssn)
		if err := finalStmt.RecoverOperations(option.stmt); err != nil {
			restoreHyperNodePlacement(job, subJob, placementBeforeTry)
			return nil, err
		}

		subJob.AllocatedHyperNode = option.allocatedHyperNode
		updateJobAllocatedHyperNodeFromSubJob(ssn, job, subJob, option.allocatedHyperNode)

		nextWorksheet := remainingWorksheet.Clone()
		nextWorksheet.subJobWorksheets[subJob.UID] = option.worksheet.Clone()
		if !option.worksheet.Empty() {
			nextWorksheet.subJobs.Push(subJob)
		}
		nextProcessed := processed.Clone()
		nextProcessed.Insert(subJob.UID)

		childSolution, err := alloc.searchSubJobAllocations(
			job, nextWorksheet, hyperNodeForJob, jobHyperNodeBaseline, workingSet, nextProcessed,
			searchContext, cumulativeScore+option.score,
		)
		if err != nil {
			finalStmt.Discard()
			restoreHyperNodePlacement(job, subJob, placementBeforeTry)
			return nil, err
		}
		if childSolution != nil {
			combinedStmt := framework.SaveOperations(finalStmt, childSolution.stmt)
			decisions := make(map[api.SubJobID]string, len(childSolution.subJobDecisions)+1)
			for subJobID, hyperNode := range childSolution.subJobDecisions {
				decisions[subJobID] = hyperNode
			}
			decisions[subJob.UID] = option.allocatedHyperNode

			allocatedHyperNode := childSolution.allocatedHyperNode
			candidate := &jobAllocationSolution{
				stmt:                         combinedStmt,
				worksheet:                    childSolution.worksheet,
				score:                        option.score + childSolution.score,
				allocatedHyperNode:           allocatedHyperNode,
				softTopologyTargetSubJobs:    workingSet.Len(),
				softTopologyAllocatedSubJobs: nextProcessed.Intersection(workingSet).Len(),
				subJobDecisions:              decisions,
			}
			if bestSolution == nil || alloc.jobAllocationSolutionLess(
				job,
				subJobDecisionKey(candidate.subJobDecisions),
				candidate,
				subJobDecisionKey(bestSolution.subJobDecisions),
				bestSolution,
			) {
				bestSolution = candidate
			}

			finalStmt.Discard()
			restoreHyperNodePlacement(job, subJob, placementBeforeTry)
			continue
		}

		finalStmt.Discard()
		restoreHyperNodePlacement(job, subJob, placementBeforeTry)
	}
	return bestSolution, nil
}

func topologySearchStateKey(
	job *api.JobInfo,
	jobWorksheet *JobWorksheet,
	processed sets.Set[api.SubJobID],
) string {
	parts := make([]string, 0)
	if job != nil {
		parts = append(parts, "job="+job.AllocatedHyperNode)

		subJobIDs := make([]string, 0, len(job.SubJobs))
		for subJobID := range job.SubJobs {
			subJobIDs = append(subJobIDs, string(subJobID))
		}
		sort.Strings(subJobIDs)
		for _, subJobID := range subJobIDs {
			parts = append(parts, "subJob="+subJobID+"@"+job.SubJobs[api.SubJobID(subJobID)].AllocatedHyperNode)
		}

		taskIDs := make([]string, 0, len(job.Tasks))
		for taskID := range job.Tasks {
			taskIDs = append(taskIDs, string(taskID))
		}
		sort.Strings(taskIDs)
		for _, taskID := range taskIDs {
			task := job.Tasks[api.TaskID(taskID)]
			parts = append(parts, fmt.Sprintf("task=%s@%s#%s", taskID, task.NodeName, task.Status))
		}
	}

	processedIDs := make([]string, 0, processed.Len())
	for subJobID := range processed {
		processedIDs = append(processedIDs, string(subJobID))
	}
	sort.Strings(processedIDs)
	parts = append(parts, "processed="+strings.Join(processedIDs, ","))

	if jobWorksheet != nil {
		worksheetIDs := make([]string, 0, len(jobWorksheet.subJobWorksheets))
		for subJobID := range jobWorksheet.subJobWorksheets {
			worksheetIDs = append(worksheetIDs, string(subJobID))
		}
		sort.Strings(worksheetIDs)
		for _, subJobID := range worksheetIDs {
			worksheet := jobWorksheet.subJobWorksheets[api.SubJobID(subJobID)]
			if worksheet == nil || worksheet.tasks == nil {
				parts = append(parts, "pending="+subJobID+":")
				continue
			}
			pending := worksheet.tasks.Clone()
			pendingTaskIDs := make([]string, 0, pending.Len())
			for !pending.Empty() {
				pendingTaskIDs = append(pendingTaskIDs, string(pending.Pop().(*api.TaskInfo).UID))
			}
			sort.Strings(pendingTaskIDs)
			parts = append(parts, "pending="+subJobID+":"+strings.Join(pendingTaskIDs, ","))
		}
	}

	return strings.Join(parts, "|")
}

func subJobDecisionKey(decisions map[api.SubJobID]string) string {
	if len(decisions) == 0 {
		return ""
	}
	ids := make([]string, 0, len(decisions))
	for subJobID := range decisions {
		ids = append(ids, string(subJobID))
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, id+"="+decisions[api.SubJobID(id)])
	}
	return strings.Join(parts, ",")
}

func (alloc *Action) allocateForSubJob(
	subJob *api.SubJobInfo,
	subJobWorksheet *SubJobWorksheet,
	hyperNodeForJob *api.HyperNodeInfo,
	jobHyperNodeBaseline string,
) (*framework.Statement, float64) {
	ssn := alloc.session
	job := ssn.Jobs[subJob.Job]
	options, err := alloc.collectSubJobPlacementCandidates(
		subJob, subJobWorksheet, hyperNodeForJob, jobHyperNodeBaseline,
	)
	if err != nil {
		klog.Errorf("Collect allocation options for subJob failed, job=%s, subJob=%s, err=%v", subJob.Job, subJob.UID, err)
		return nil, 0
	}
	if len(options) == 0 {
		return nil, 0
	}
	best := options[0]
	finalStmt := framework.NewStatement(ssn)
	if err := finalStmt.RecoverOperations(best.stmt); err != nil {
		klog.Errorf("Failed to recover operations, subJob=%s, hyperNode=%s, err=%v", subJob.UID, best.hyperNode, err)
		return nil, 0
	}

	subJob.AllocatedHyperNode = best.allocatedHyperNode
	updateJobAllocatedHyperNodeFromSubJob(ssn, job, subJob, best.allocatedHyperNode)
	subJobWorksheet.ShallowCopyFrom(best.worksheet)
	alloc.recorder.SaveSubJobDecision(subJob.Job, hyperNodeForJob.Name, subJob.UID, best.allocatedHyperNode)
	klog.V(3).Infof("Allocate subJob to hyperNode success, subJob=%s, searchHyperNode=%s, score=%v, allocatedHyperNode=%s",
		subJob.UID, best.hyperNode, best.score, best.allocatedHyperNode)
	return finalStmt, best.score
}

func (alloc *Action) collectSubJobPlacementCandidates(
	subJob *api.SubJobInfo,
	subJobWorksheet *SubJobWorksheet,
	hyperNodeForJob *api.HyperNodeInfo,
	jobHyperNodeBaseline string,
) ([]*subJobPlacementCandidate, error) {
	ssn := alloc.session
	job := ssn.Jobs[subJob.Job]
	if subJobWorksheet == nil || subJobWorksheet.Empty() {
		return nil, nil
	}

	hyperNodeGradients, gradientStats := ssn.HyperNodeGradientForSubJobFn(subJob, hyperNodeForJob)
	hyperNodeGradients, resourceStats := FilterGradientsByMinResource(
		ssn, hyperNodeGradients, subJob.GetMinResources(), subJob.AllocatedHyperNode,
	)
	if len(hyperNodeGradients) == 0 {
		job.MergeSubJobHyperNodeFitErrors(jobHyperNodeBaseline, subJob.UID, gradientStats, resourceStats,
			subJob.GetMinResources(), ssn.HyperNodesSetByTier, ssn.HyperNodeTierNameMap, ssn.HyperNodes)
		klog.V(3).Infof("No hyperNode gradient for subJob, job=%s, subJob=%s, fitError=%s", subJob.Job, subJob.UID, job.JobFitErrors)
		return nil, nil
	}

	var candidates []*subJobPlacementCandidate
	for gradient, hyperNodes := range hyperNodeGradients {
		for _, hyperNode := range hyperNodes {
			job.ResetSubJobFitErr(subJob.UID)
			subJobWorksheetCopy := subJobWorksheet.Clone()
			placementBeforeTry := captureHyperNodePlacement(job, subJob)

			klog.V(3).Infof("Try SubJob placement candidate, job=%s, subJob=%s, taskNum=%d, hyperNode=%s, tierLayer=%d",
				subJob.Job, subJob.UID, subJobWorksheetCopy.tasks.Len(), hyperNode.Name, gradient)
			stmt := alloc.allocateResourcesForTasks(subJob, subJobWorksheetCopy.tasks, hyperNode.Name)

			if stmt != nil && len(stmt.Operations()) > 0 {
				candidates = append(candidates, &subJobPlacementCandidate{
					hyperNode:          hyperNode.Name,
					stmt:               framework.SaveOperations(stmt),
					worksheet:          subJobWorksheetCopy,
					allocatedHyperNode: subJob.AllocatedHyperNode,
					gradient:           gradient,
				})
				stmt.Discard()
			}
			restoreHyperNodePlacement(job, subJob, placementBeforeTry)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if err := alloc.scoreSubJobPlacementCandidates(candidates, subJob); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (alloc *Action) scoreSubJobPlacementCandidates(candidates []*subJobPlacementCandidate, subJob *api.SubJobInfo) error {
	ssn := alloc.session
	candidateHyperNodeGroups := make(map[string][]*api.NodeInfo, len(candidates))
	for _, candidate := range candidates {
		effectiveHyperNode := effectiveHyperNodeForSubJobPlacement(candidate)
		candidateHyperNodeGroups[effectiveHyperNode] = ssn.RealNodesList[effectiveHyperNode]
	}

	hyperNodeScores, err := util.PrioritizeHyperNodes(candidateHyperNodeGroups, subJob, ssn.HyperNodeOrderMapFn)
	if err != nil {
		return fmt.Errorf("prioritize hyperNodes for subJob %s fail: %w", subJob.UID, err)
	}
	scoreByHyperNode := make(map[string]float64, len(candidates))
	for score, hyperNodes := range hyperNodeScores {
		for _, hyperNode := range hyperNodes {
			scoreByHyperNode[hyperNode] = score
		}
	}
	for _, candidate := range candidates {
		candidate.score = scoreByHyperNode[effectiveHyperNodeForSubJobPlacement(candidate)]
	}

	slices.SortStableFunc(candidates, func(left, right *subJobPlacementCandidate) int {
		if left.gradient != right.gradient {
			return left.gradient - right.gradient
		}
		if left.score > right.score {
			return -1
		}
		if left.score < right.score {
			return 1
		}
		if left.hyperNode < right.hyperNode {
			return -1
		}
		if left.hyperNode > right.hyperNode {
			return 1
		}
		return 0
	})
	return nil
}

func effectiveHyperNodeForSubJobPlacement(candidate *subJobPlacementCandidate) string {
	if candidate == nil {
		return ""
	}
	if candidate.allocatedHyperNode != "" {
		return candidate.allocatedHyperNode
	}
	return candidate.hyperNode
}

// selectBestHyperNodeForJob returns the best HyperNode for the job by combining
// subJob scores with the final job-level soft topology placement from dry-run.
func (alloc *Action) selectBestHyperNodeForJob(solutions map[string]*jobAllocationSolution, job *api.JobInfo) (string, error) {
	bestHyperNode := ""
	var bestSolution *jobAllocationSolution
	for hyperNode, solution := range solutions {
		if solution == nil {
			continue
		}
		if bestHyperNode == "" || alloc.jobAllocationSolutionLess(job, hyperNode, solution, bestHyperNode, bestSolution) {
			bestHyperNode = hyperNode
			bestSolution = solution
		}
	}

	if bestHyperNode == "" {
		return "", fmt.Errorf("no solution found for job %s", job.UID)
	}

	return bestHyperNode, nil
}

func (alloc *Action) jobAllocationSolutionLess(
	job *api.JobInfo,
	leftHyperNode string,
	left *jobAllocationSolution,
	rightHyperNode string,
	right *jobAllocationSolution,
) bool {
	softMode, preferredTier := jobSoftTopologyPreferredTier(job)
	var leftRank, rightRank jobSoftTopologyRank
	if softMode {
		leftRank = alloc.jobSoftTopologyRank(left.allocatedHyperNode, preferredTier)
		rightRank = alloc.jobSoftTopologyRank(right.allocatedHyperNode, preferredTier)
		if leftRank.preferred != rightRank.preferred {
			return leftRank.preferred
		}
	}

	if left.score != right.score {
		return left.score > right.score
	}
	if softMode {
		// A soft preferred tier is a boundary, not an implicit absolute
		// compactness priority. Once both solutions are inside (or outside) the
		// boundary, their merged soft/plugin score decides before compactness.
		if left.softTopologyAllocatedSubJobs != right.softTopologyAllocatedSubJobs {
			return left.softTopologyAllocatedSubJobs > right.softTopologyAllocatedSubJobs
		}
		if leftRank.tier != rightRank.tier {
			return leftRank.tier < rightRank.tier
		}
	}
	leftCandidate, leftFound := alloc.session.HyperNodes[leftHyperNode]
	rightCandidate, rightFound := alloc.session.HyperNodes[rightHyperNode]
	if leftFound && rightFound && leftCandidate.Tier() != rightCandidate.Tier() {
		return leftCandidate.Tier() < rightCandidate.Tier()
	}
	return leftHyperNode < rightHyperNode
}

type jobSoftTopologyRank struct {
	preferred bool
	tier      int
}

func (alloc *Action) jobSoftTopologyRank(allocatedHyperNode string, preferredTier int) jobSoftTopologyRank {
	rank := jobSoftTopologyRank{tier: int(^uint(0) >> 1)}
	if allocatedHyperNode == "" {
		return rank
	}
	hni, found := alloc.session.HyperNodes[allocatedHyperNode]
	if !found {
		return rank
	}
	rank.tier = hni.Tier()
	rank.preferred = rank.tier <= preferredTier
	return rank
}

func jobSoftTopologyPreferredTier(job *api.JobInfo) (bool, int) {
	if job == nil || job.PodGroup == nil || job.PodGroup.Spec.NetworkTopology == nil ||
		job.PodGroup.Spec.NetworkTopology.HighestTierAllowed == nil {
		return false, 0
	}
	return job.PodGroup.Spec.NetworkTopology.Mode == scheduling.SoftNetworkTopologyMode,
		*job.PodGroup.Spec.NetworkTopology.HighestTierAllowed
}

func (alloc *Action) allocateResourcesForTasks(subJob *api.SubJobInfo, tasks *util.PriorityQueue, hyperNode string) *framework.Statement {
	ssn := alloc.session

	job := ssn.Jobs[subJob.Job]
	queue := ssn.Queues[job.Queue]
	nodes, exist := ssn.RealNodesList[hyperNode]
	if !exist || len(nodes) == 0 {
		klog.V(4).Infof("There is no node in hyperNode, job=%s, hyperNode=%s", job.UID, hyperNode)
		return nil
	}

	stmt := framework.NewStatement(ssn)
	ph := util.NewPredicateHelper()

	allocatedHyperNode := subJob.AllocatedHyperNode
	trackHyperNodePlacement := shouldTrackHyperNodePlacement(ssn, subJob)
	placementAtStart := captureHyperNodePlacement(job, subJob)

	for !tasks.Empty() {
		task := tasks.Pop().(*api.TaskInfo)
		if !ssn.Allocatable(queue, task) {
			klog.V(3).Infof("Queue <%s> is overused when considering task <%s>, ignore it.", queue.Name, task.Name)
			continue
		}

		// check if the task with its spec has already predicates failed
		if job.TaskHasFitErrors(subJob.UID, task) {
			msg := fmt.Sprintf("Task %s with role spec %s has already predicated failed, skip", task.Name, task.TaskRole)
			klog.V(5).Info(msg)
			fitErrors := api.NewFitErrors()
			fitErrors.SetError(msg)
			job.NodesFitErrors[task.UID] = fitErrors
			continue
		}

		klog.V(3).Infof("There are <%d> nodes for Job <%v/%v>", len(nodes), job.Namespace, job.Name)

		if err := ssn.PrePredicateFn(task); err != nil {
			klog.V(3).Infof("PrePredicate for task %s/%s failed for: %v", task.Namespace, task.Name, err)
			fitErrors := api.NewFitErrors()
			for _, ni := range nodes {
				fitErrors.SetNodeError(ni.Name, err)
			}
			job.NodesFitErrors[task.UID] = fitErrors
			break
		}

		var predicateNodes []*api.NodeInfo
		var fitErrors *api.FitErrors

		// "NominatedNodeName" can potentially be set in a previous scheduling cycle as a result of preemption.
		// This node is likely the only candidate that will fit the pod, and hence we try it first before iterating over all nodes.
		if len(task.Pod.Status.NominatedNodeName) > 0 {
			if nominatedNodeInfo, ok := ssn.Nodes[task.Pod.Status.NominatedNodeName]; ok && task.InitResreq.LessEqual(nominatedNodeInfo.FutureIdle(), api.Zero) {
				predicateNodes, fitErrors = ph.PredicateNodes(task, []*api.NodeInfo{nominatedNodeInfo}, alloc.predicate, alloc.enablePredicateErrorCache, ssn.NodesInShard)
			}
		}

		// If the nominated node is not found or the nominated node is not suitable for the task, we need to find a suitable node for the task from all nodes.
		if len(predicateNodes) == 0 {
			predicateNodes, fitErrors = ph.PredicateNodes(task, nodes, alloc.predicate, alloc.enablePredicateErrorCache, ssn.NodesInShard)
		}

		if len(predicateNodes) == 0 {
			// TODO: Need to add PostFilter extension point implementation here. For example, the DRA plugin includes the PostFilter extension point,
			// but the DRA's PostFilter only occurs in extreme error conditions: Suppose a pod uses two claims. In the first scheduling attempt,
			// a node is picked and PreBind manages to update the first claim so that it is allocated and reserved for the pod.
			// But then updating the second claim fails (e.g., apiserver down) and the scheduler has to retry. During the next pod scheduling attempt,
			// the original node is no longer usable for other reasons. Other nodes are not usable either because of the allocated claim.
			// The DRA scheduler plugin detects that and then when scheduling fails (= no node passed filtering), it recovers by de-allocating the allocated claim in PostFilter.
			if fitErrors != nil && hyperNode != framework.ClusterTopHyperNode {
				fitErrors.SetHyperNode(hyperNode)
			}
			job.NodesFitErrors[task.UID] = fitErrors
			// Assume that all left tasks are allocatable, but can not meet gang-scheduling min member,
			// so we should break from continuously allocating.
			// otherwise, should continue to find other allocatable task
			if job.NeedContinueAllocating(subJob.UID) {
				continue
			} else {
				break
			}
		}

		if trackHyperNodePlacement {
			task.JobAllocatedHyperNode = allocatedHyperNode
		}

		bestNode, _ := alloc.prioritizeNodes(ssn, task, predicateNodes)
		if bestNode == nil {
			continue
		}

		if err := alloc.allocateResourcesForTask(stmt, task, bestNode, job); err != nil {
			klog.Errorf("Allocate resources for task fail, task=%s, err=%v", task.Name, err)
			continue
		}

		if trackHyperNodePlacement {
			allocatedHyperNode = getNewAllocatedHyperNode(ssn, bestNode.Name, allocatedHyperNode)
			subJob.AllocatedHyperNode = allocatedHyperNode
			updateJobAllocatedHyperNodeFromSubJob(ssn, job, subJob, allocatedHyperNode)
		}

		if ssn.SubJobReady(job, subJob) {
			break
		}
	}

	if ssn.SubJobReady(job, subJob) {
		klog.V(3).Infof("SubJob ready, return statement, job=%s, subJob=%s", job.UID, subJob.UID)
		if subJob.IsSoftTopologyMode() {
			subJob.AllocatedHyperNode = allocatedHyperNode
		}
		return stmt
	} else if ssn.SubJobPipelined(job, subJob) {
		klog.V(3).Infof("SubJob pipelined, return statement, job=%s, subJob=%s", job.UID, subJob.UID)
		return stmt
	}

	stmt.Discard()
	if trackHyperNodePlacement {
		restoreHyperNodePlacement(job, subJob, placementAtStart)
	}
	return nil
}

func updateJobTier(hyperNodeTierNameMap api.HyperNodeTierNameMap, job *api.JobInfo) {
	klog.V(4).Infof("updateJobTier, job=%s, hyperNodeTierNameMap=%v", job.UID, hyperNodeTierNameMap)
	if job.PodGroup.Spec.NetworkTopology != nil && job.PodGroup.Spec.NetworkTopology.HighestTierName != "" && job.PodGroup.Spec.NetworkTopology.HighestTierAllowed == nil {
		if tier, ok := hyperNodeTierNameMap[job.PodGroup.Spec.NetworkTopology.HighestTierName]; ok {
			job.PodGroup.Spec.NetworkTopology.HighestTierAllowed = &tier
			job.PodGroup.Spec.NetworkTopology.HighestTierName = ""
		} else {
			klog.Warningf("The tier corresponding to highestTierName %s is not found, job <%s>",
				job.PodGroup.Spec.NetworkTopology.HighestTierName, job.UID)
		}
	}
	for _, subGroupPolicy := range job.PodGroup.Spec.SubGroupPolicy {
		if subGroupPolicy.NetworkTopology != nil && subGroupPolicy.NetworkTopology.HighestTierName != "" && subGroupPolicy.NetworkTopology.HighestTierAllowed == nil {
			if tier, ok := hyperNodeTierNameMap[subGroupPolicy.NetworkTopology.HighestTierName]; ok {
				subGroupPolicy.NetworkTopology.HighestTierAllowed = &tier
				subGroupPolicy.NetworkTopology.HighestTierName = ""
			} else {
				klog.Warningf("The tier corresponding to highestTierName %s in subGroupPolicy %s is not found, job <%s>",
					subGroupPolicy.NetworkTopology.HighestTierName, subGroupPolicy.Name, job.UID)
			}
		}
	}
}

// getNewAllocatedHyperNode Obtain the newly allocated hyperNode for the job in soft topology mode
func getNewAllocatedHyperNode(ssn *framework.Session, bestNode string, jobAllocatedHyperNode string) string {
	hyperNode := util.FindHyperNodeForNode(bestNode, ssn.RealNodesList, ssn.HyperNodesTiers, ssn.HyperNodesSetByTier)
	if hyperNode != "" {
		if jobAllocatedHyperNode == "" {
			return hyperNode
		}
		return ssn.HyperNodes.GetLCAHyperNode(hyperNode, jobAllocatedHyperNode)
	}
	return jobAllocatedHyperNode
}

type hyperNodePlacement struct {
	jobAllocatedHyperNode    string
	subJobAllocatedHyperNode string
}

func captureHyperNodePlacement(job *api.JobInfo, subJob *api.SubJobInfo) hyperNodePlacement {
	return hyperNodePlacement{
		jobAllocatedHyperNode:    job.AllocatedHyperNode,
		subJobAllocatedHyperNode: subJob.AllocatedHyperNode,
	}
}

func restoreHyperNodePlacement(job *api.JobInfo, subJob *api.SubJobInfo, placement hyperNodePlacement) {
	job.AllocatedHyperNode = placement.jobAllocatedHyperNode
	subJob.AllocatedHyperNode = placement.subJobAllocatedHyperNode
}

func shouldTrackHyperNodePlacement(ssn *framework.Session, subJob *api.SubJobInfo) bool {
	return subJob.WithNetworkTopology() || ssn.HyperNodesReadyToSchedule
}

func updateJobAllocatedHyperNodeFromSubJob(
	ssn *framework.Session,
	job *api.JobInfo,
	subJob *api.SubJobInfo,
	subJobAllocatedHyperNode string,
) {
	if !shouldTrackHyperNodePlacement(ssn, subJob) || subJobAllocatedHyperNode == "" {
		return
	}

	jobAllocatedHyperNode := subJobAllocatedHyperNode
	if job.AllocatedHyperNode != "" {
		jobAllocatedHyperNode = ssn.HyperNodes.GetLCAHyperNode(job.AllocatedHyperNode, subJobAllocatedHyperNode)
	}
	if job.AllocatedHyperNode == jobAllocatedHyperNode {
		return
	}
	job.AllocatedHyperNode = jobAllocatedHyperNode
	ssn.MarkJobDirty(job.UID)
}

// prioritizeNodes selects the highest score node.
func (alloc *Action) prioritizeNodes(ssn *framework.Session, task *api.TaskInfo, predicateNodes []*api.NodeInfo) (*api.NodeInfo, float64) {
	// Candidate nodes are divided into two gradients:
	// - the first gradient node: a list of free nodes that satisfy the task resource request;
	// - The second gradient node: the node list whose sum of node idle resources and future idle meets the task resource request;
	// Score the first gradient node first. If the first gradient node meets the requirements, ignore the second gradient node list,
	// otherwise, score the second gradient node and select the appropriate node.
	shardingMode := options.ServerOpts.ShardingMode
	var candidateNodes [][]*api.NodeInfo
	var idleCandidateNodes []*api.NodeInfo
	var futureIdleCandidateNodes []*api.NodeInfo
	var idleCandidateNodesInOtherShards []*api.NodeInfo
	var futureIdleCandidateNodesInOtherShards []*api.NodeInfo
	for _, n := range predicateNodes {
		if task.InitResreq.LessEqual(n.Idle, api.Zero) {
			if shardingMode == commonutil.SoftShardingMode && !ssn.NodesInShard.Has(n.Name) {
				idleCandidateNodesInOtherShards = append(idleCandidateNodesInOtherShards, n)
			} else {
				idleCandidateNodes = append(idleCandidateNodes, n)
			}
		} else if task.InitResreq.LessEqual(n.FutureIdle(), api.Zero) {
			if shardingMode == commonutil.SoftShardingMode && !ssn.NodesInShard.Has(n.Name) {
				futureIdleCandidateNodesInOtherShards = append(futureIdleCandidateNodesInOtherShards, n)
			} else {
				futureIdleCandidateNodes = append(futureIdleCandidateNodes, n)
			}
		} else {
			klog.V(5).Infof("Predicate filtered node %v, idle: %v and future idle: %v do not meet the requirements of task: %v",
				n.Name, n.Idle, n.FutureIdle(), task.Name)
		}
	}

	// To allocate to nodes with enough resource and nodes within shard of this scheduler first, allocation of Pod follow below order:
	// 1. Node with IDLE resource in shard for this scheduler
	// 2. Node with IDLE resource in shard for other scheduler  (empty if sharding mode is not soft)
	// 3. Node with Future IDLE resource in shard for this scheduler
	// 4. Node with Future IDLE resource in shard for other scheduler (empty if sharding mode is not soft)
	candidateNodes = append(candidateNodes, idleCandidateNodes)
	candidateNodes = append(candidateNodes, idleCandidateNodesInOtherShards)
	candidateNodes = append(candidateNodes, futureIdleCandidateNodes)
	candidateNodes = append(candidateNodes, futureIdleCandidateNodesInOtherShards)

	var bestNode *api.NodeInfo
	var higestScore float64
	for index, nodes := range candidateNodes {
		if klog.V(5).Enabled() {
			for _, node := range nodes {
				klog.V(5).Infof("node %v, idle: %v, future idle: %v", node.Name, node.Idle, node.FutureIdle())
			}
		}
		switch {
		case len(nodes) == 0:
			klog.V(5).Infof("Task: %v, no matching node is found in the candidateNodes（index: %d） list.", task.Name, index)
		case len(nodes) == 1: // If only one node after predicate, just use it.
			bestNode = nodes[0]
		case len(nodes) > 1: // If more than one node after predicate, using "the best" one
			nodeScores := util.PrioritizeNodes(task, nodes, ssn.BatchNodeOrderFn, ssn.NodeOrderMapFn, ssn.NodeOrderReduceFn)

			bestNode = ssn.BestNodeFn(task, nodeScores)
			if bestNode == nil {
				bestNode, higestScore = util.SelectBestNodeAndScore(nodeScores)
			}
		}

		// If a proper node is found in idleCandidateNodes, skip futureIdleCandidateNodes and directly return the node information.
		if bestNode != nil {
			break
		}
	}
	return bestNode, higestScore
}

func (alloc *Action) allocateResourcesForTask(stmt *framework.Statement, task *api.TaskInfo, node *api.NodeInfo, job *api.JobInfo) (err error) {
	// Allocate idle resource to the task.
	if task.InitResreq.LessEqual(node.Idle, api.Zero) {
		klog.V(3).Infof("Binding Task <%v/%v> to node <%v>", task.Namespace, task.Name, node.Name)
		if err = stmt.Allocate(task, node); err != nil {
			klog.Errorf("Failed to bind Task %v on %v in Session %v, err: %v",
				task.UID, node.Name, alloc.session.UID, err)
			if rollbackErr := stmt.UnAllocate(task); rollbackErr != nil {
				klog.Errorf("Failed to unallocate Task %v on %v in Session %v for %v.",
					task.UID, node.Name, alloc.session.UID, rollbackErr)
			}
		} else {
			metrics.UpdateE2eSchedulingDurationByJob(job.Name, string(job.Queue), job.Namespace, metrics.Duration(job.CreationTimestamp.Time))
			metrics.UpdateE2eSchedulingLastTimeByJob(job.Name, string(job.Queue), job.Namespace, time.Now())
		}
		return
	}

	klog.V(3).Infof("Predicates failed in allocate for task <%s/%s> on node <%s> with limited resources",
		task.Namespace, task.Name, node.Name)

	// Allocate releasing resource to the task if any.
	if task.InitResreq.LessEqual(node.FutureIdle(), api.Zero) {
		klog.V(3).Infof("Pipelining Task <%v/%v> to node <%v> for <%v> on <%v>",
			task.Namespace, task.Name, node.Name, task.InitResreq, node.Releasing)
		if err = stmt.Pipeline(task, node.Name, false); err != nil {
			klog.Errorf("Failed to pipeline Task %v on %v in Session %v for %v.",
				task.UID, node.Name, alloc.session.UID, err)
		} else {
			metrics.UpdateE2eSchedulingDurationByJob(job.Name, string(job.Queue), job.Namespace, metrics.Duration(job.CreationTimestamp.Time))
			metrics.UpdateE2eSchedulingLastTimeByJob(job.Name, string(job.Queue), job.Namespace, time.Now())
		}
	}
	return
}

func (alloc *Action) predicate(task *api.TaskInfo, node *api.NodeInfo) error {
	// Check for Resource Predicate
	var statusSets api.StatusSets
	if ok, resources := task.InitResreq.LessEqualWithResourcesName(node.FutureIdle(), api.Zero); !ok {
		statusSets = append(statusSets, &api.Status{Code: api.Unschedulable, Reason: api.WrapInsufficientResourceReason(resources)})
		return api.NewFitErrWithStatus(task, node, statusSets...)
	}
	return alloc.session.PredicateForAllocateAction(task, node)
}

func logHyperNodeTiers(ssn *framework.Session) {
	if len(ssn.HyperNodesSetByTier) == 0 {
		return
	}
	total, tierCount, listing := api.FormatHyperNodeTierListing(
		ssn.HyperNodesTiers, ssn.HyperNodesSetByTier, ssn.HyperNodeTierNameMap, ssn.HyperNodes,
	)
	klog.V(3).Infof("HyperNode tiers in session %v: tierCount=%d total=%d; %s", ssn.UID, tierCount, total, listing)
}

// FilterGradientsByMinResource drops HyperNodes that cannot satisfy minResource by aggregating
// node idle/futureIdle under each HyperNode. Skipped when allocatedHyperNode is set.
func FilterGradientsByMinResource(
	ssn *framework.Session,
	gradients [][]*api.HyperNodeInfo,
	minResource *api.Resource,
	allocatedHyperNode string,
) ([][]*api.HyperNodeInfo, *api.HyperNodeMinResourceFilterStats) {
	if allocatedHyperNode != "" || minResource == nil || len(gradients) == 0 {
		return gradients, nil
	}

	stats := &api.HyperNodeMinResourceFilterStats{
		FinalByTier:      make(map[int]int),
		ExcludedByTier:   make(map[int]int),
		ExcludedByReason: make(map[string]string),
	}
	filtered := make([][]*api.HyperNodeInfo, 0, len(gradients))
	for _, layer := range gradients {
		survivors := make([]*api.HyperNodeInfo, 0, len(layer))
		for _, hn := range layer {
			if hyperNodeSatisfiesMinResource(ssn, hn.Name, minResource) {
				stats.FinalByTier[hn.Tier()]++
				survivors = append(survivors, hn)
			} else {
				stats.ExcludedByTier[hn.Tier()]++
				stats.ExcludedByReason[hn.Name] = fmt.Sprintf("minResource (%s)", minResource.String())
			}
		}
		if len(survivors) > 0 {
			filtered = append(filtered, survivors)
		}
	}
	if len(filtered) > 0 {
		return filtered, stats
	}
	return nil, stats
}

func hyperNodeSatisfiesMinResource(ssn *framework.Session, hyperNodeName string, minResource *api.Resource) bool {
	nodes, ok := ssn.RealNodesSet[hyperNodeName]
	if !ok || nodes.Len() == 0 {
		return true
	}

	idle := api.EmptyResource()
	futureIdle := api.EmptyResource()
	for nodeName := range nodes {
		node, found := ssn.Nodes[nodeName]
		if !found {
			continue
		}
		idle.Add(node.Idle)
		futureIdle.Add(node.FutureIdle())
	}
	return minResource.LessEqual(idle, api.Zero) || minResource.LessEqual(futureIdle, api.Zero)
}

func (alloc *Action) UnInitialize() {}
