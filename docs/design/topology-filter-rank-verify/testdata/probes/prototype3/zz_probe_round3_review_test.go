/*
Round-3 probes for issue 004 (both baselines): the findings of the round-2
design review. The scenarios RT1-RT3 were contributed by the
independent proposal analyst (see subagents/review-analyst-round2) and are
re-run here against the corrected prototype.
Needs probes/common, probes/history, probes/prototype, probes/prototype2 and
the round-3 prototype patch.
*/

package allocate

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// rtRun is runProbe plus a hook that sees the session after the action ran.
func rtRun(tb testing.TB, tc *uthelper.TestCommonStruct, action *Action, inspect func(ssn *framework.Session)) *probeResult {
	tb.Helper()
	res := &probeResult{
		counters:       &probeCounters{},
		placed:         map[string]string{},
		jobFitErrors:   map[api.JobID]string{},
		fitError:       map[api.JobID]string{},
		nodesFitErrors: map[api.JobID]map[string]string{},
	}
	tc.Plugins = probePluginBuilders(res.counters, nil)
	ssn := tc.RegisterSession(probeTiers(), nil)
	defer tc.Close()
	tc.Run([]framework.Action{action})
	for jobID, job := range ssn.Jobs {
		for _, task := range job.Tasks {
			if task.Pod == nil || task.Pod.Status.Phase != v1.PodPending || task.Status == api.Pending || task.NodeName == "" {
				continue
			}
			res.placed[task.Name] = task.NodeName
		}
		res.jobFitErrors[jobID] = job.JobFitErrors
		res.fitError[jobID] = job.FitError()
		perTask := map[string]string{}
		for taskID, fitErrors := range job.NodesFitErrors {
			if fitErrors == nil {
				continue
			}
			name := string(taskID)
			if task := job.Tasks[taskID]; task != nil {
				name = task.Name
			}
			perTask[name] = fitErrors.Error()
		}
		res.nodesFitErrors[jobID] = perTask
	}
	if inspect != nil {
		inspect(ssn)
	}
	return res
}

func rtUnmodified() *Action {
	a := New()
	a.rankThenVerify, a.fitBound, a.deterministicTies = false, false, false
	a.repairBudget, a.repairOrder = 0, ""
	return a
}

func rtWorkerPolicy(minGroups int32) schedulingv1.SubGroupPolicySpec {
	size, tier := int32(1), 1
	return schedulingv1.SubGroupPolicySpec{
		Name: "worker", MatchLabelKeys: []string{"idx"}, SubGroupSize: &size, MinSubGroups: &minGroups,
		NetworkTopology: &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier},
	}
}

// rtSubJobTie: racks a and b are equally used (2 of 4 CPU free each). The
// flexible SubJob, handled first, fits either rack; the pinned SubJob fits
// only rack a and needs all that is free there. Which rack the flexible
// SubJob takes is a tie.
func rtSubJobTie(job string) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0), nodeName(0, 1, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup(job, "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{rtWorkerPolicy(2)}
	flexible := util.BuildPod("c1", job+"-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil)
	pinned := util.BuildPod("c1", job+"-pinned", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "1"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "subjob-tie", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// rtNodeTie: one rack, two identical nodes with 4 CPU. The small task
// (2 CPU, handled first) fits either node; the big task (4 CPU) is pinned to
// node 0. Which node the small task takes is a tie.
func rtNodeTie(job string) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 1, 2, "4", "16Gi", func(_, _, node int, n *v1.Node) {
		if node == 0 {
			n.Labels["only"] = "a"
		}
	})
	pg := util.BuildPodGroupWithNetWorkTopologies(job, "c1", "", "q1", 2, nil, schedulingv1.PodGroupInqueue, "hard", 1)
	small := util.BuildPod("c1", job+"-a-small", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "small"}, nil)
	big := util.BuildPod("c1", job+"-b-big", "", v1.PodPending, api.BuildResourceList("4", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "big"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "node-tie", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: []*v1.Pod{small, big}, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// RT1: today's random tie-break lets a Job whose success depends on a tie
// schedule after a few cycles. A tie key that is fixed for ever would leave
// half of such Jobs unschedulable for ever. With the round in the tie key the
// first attempt is fixed and every further cycle tries another order.
func TestProto3RT1TieRound(t *testing.T) {
	const names, cycles = 16, 12
	defer func() { tieRound = 0 }()
	scenarios := []struct {
		name string
		gen  func(job string) *uthelper.TestCommonStruct
	}{
		{"SubJob-level tie", rtSubJobTie},
		{"node-level tie inside one unit", rtNodeTie},
	}
	type cfg struct {
		name   string
		action func() *Action
		rounds bool
	}
	configs := []cfg{
		{"today: random ties", rtUnmodified, false},
		{"ranked, tie key without round", func() *Action { return modeRankedFit.action() }, false},
		{"ranked, tie key with round", func() *Action { return modeRankedFit.action() }, true},
		{"ranked, tie key with round, retry(4)", func() *Action {
			a := modeRankedFit.action()
			a.repairBudget, a.repairOrder = 4, "reserve"
			return a
		}, true},
	}
	for _, s := range scenarios {
		t.Logf("%s: %d job names, %d scheduling cycles each", s.name, names, cycles)
		t.Logf("| configuration | jobs scheduled in the first cycle | jobs scheduled within %d cycles | jobs never scheduled | cycles until scheduled (mean / max) |", cycles)
		t.Logf("|---|---|---|---|---|")
		for _, c := range configs {
			first, within, never, sum, worst := 0, 0, 0, 0, 0
			for j := 0; j < names; j++ {
				job := fmt.Sprintf("job%02d", j)
				scheduledAt := 0
				for cycle := 1; cycle <= cycles; cycle++ {
					tieRound = 0
					if c.rounds {
						tieRound = cycle - 1
					}
					res := rtRun(t, s.gen(job), c.action(), nil)
					switch got := res.count(job + "-"); got {
					case 2:
						scheduledAt = cycle
					case 0:
					default:
						t.Errorf("%s %s: partial gang placed (%d/2)", c.name, job, got)
					}
					if scheduledAt > 0 {
						break
					}
				}
				switch {
				case scheduledAt == 0:
					never++
				default:
					within++
					sum += scheduledAt
					worst = max(worst, scheduledAt)
					if scheduledAt == 1 {
						first++
					}
				}
			}
			mean := "-"
			if within > 0 {
				mean = fmt.Sprintf("%.1f / %d", float64(sum)/float64(within), worst)
			}
			t.Logf("| %s | %d | %d | %d | %s |", c.name, first, within, never, mean)
			if c.rounds && never > 0 {
				t.Errorf("%s / %s: %d jobs never scheduled although today's random ties schedule them", s.name, c.name, never)
			}
		}
	}
}

// rtInfeasible: a strict gang of three without a legal placement: two pinned
// SubJobs need 2 CPU each in rack a, which has 2 CPU free.
func rtInfeasible() *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup("job", "c1", "q1", 3, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{rtWorkerPolicy(3)}
	flexible := util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil)
	pinned1 := util.BuildPod("c1", "job-pinned1", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "1"}, map[string]string{"only": "a"})
	pinned2 := util.BuildPod("c1", "job-pinned2", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "2"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "infeasible", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned1, pinned2}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// RT2: a Job that stays unschedulable must show the same failure report with
// and without retries, and retries must not repeat a search on the same nodes.
func TestProto3RT2FailureReportAfterRetry(t *testing.T) {
	var baseFitError string
	var baseTasks map[string]string
	for _, budget := range []int{0, 4, 16} {
		action := modeRankedFit.action()
		action.repairBudget, action.repairOrder = budget, "reserve"
		res := rtRun(t, rtInfeasible(), action, nil)
		fitError, tasks := shortFitError(res.fitError["c1/job"]), res.nodesFitErrors["c1/job"]
		t.Logf("retry budget %d: placed %d/3; retries=%d; flagged search-incomplete=%v", budget, res.count("job-"), len(action.trace.Repairs), action.trace.RepairBudgetExhausted)
		t.Logf("    fitError: %s", fitError)
		names := make([]string, 0, len(tasks))
		for name := range tasks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			t.Logf("    task fit error: %s: %s", name, tasks[name])
		}
		seen := map[string]bool{}
		for _, repair := range action.trace.Repairs {
			t.Logf("    %s", repair)
			key := strings.Join(repair.First, ",")
			if seen[key] {
				t.Errorf("budget %d: order [%s] was retried twice", budget, key)
			}
			seen[key] = true
		}
		if budget == 0 {
			baseFitError, baseTasks = fitError, tasks
			continue
		}
		if fitError != baseFitError || !reflect.DeepEqual(tasks, baseTasks) {
			t.Errorf("budget %d: failure report differs from the report without retries", budget)
		}
		if len(action.trace.RepairBudgetExhausted) > 0 {
			t.Errorf("budget %d: flagged search-incomplete although every order was tried", budget)
		}
	}
}

// rtElastic: minMember 2 of 3 single-pod SubGroups. Rack a has 2 CPU free,
// rack b 4. The flexible SubJob needs 2 CPU anywhere, the two pinned SubJobs
// need 1 CPU each in rack a.
func rtElastic() *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup("job", "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{rtWorkerPolicy(2)}
	flexible := util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil)
	pinned1 := util.BuildPod("c1", "job-pinned1", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "1"}, map[string]string{"only": "a"})
	pinned2 := util.BuildPod("c1", "job-pinned2", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "2"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "elastic", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned1, pinned2}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// RT3: after a retry succeeds, a SubJob that got no pod must not carry a
// placement recorded by the failed normal dry-run.
func TestProto3RT3NoStaleSubJobDecision(t *testing.T) {
	for _, budget := range []int{0, 4} {
		action := modeRankedFit.action()
		action.repairBudget, action.repairOrder = budget, "reserve"
		var state []string
		res := rtRun(t, rtElastic(), action, func(ssn *framework.Session) {
			job := ssn.Jobs["c1/job"]
			ids := make([]string, 0, len(job.SubJobs))
			for id := range job.SubJobs {
				ids = append(ids, string(id))
			}
			sort.Strings(ids)
			for _, id := range ids {
				subJob := job.SubJobs[api.SubJobID(id)]
				placed := 0
				for _, task := range subJob.Tasks {
					if task.NodeName != "" {
						placed++
					}
				}
				state = append(state, fmt.Sprintf("%s AllocatedHyperNode=%q placed=%d", id, subJob.AllocatedHyperNode, placed))
				if placed == 0 && subJob.AllocatedHyperNode != "" {
					t.Errorf("budget %d: %s has no pod but AllocatedHyperNode=%q", budget, id, subJob.AllocatedHyperNode)
				}
			}
		})
		t.Logf("retry budget %d: placed %d/3 %v", budget, res.count("job-"), res.placed)
		for _, repair := range action.trace.Repairs {
			t.Logf("    %s", repair)
		}
		for _, line := range state {
			t.Logf("    %s", line)
		}
		if budget > 0 && res.count("job-") != 3 {
			t.Errorf("budget %d: placed %d/3; the optional pod fits rack b", budget, res.count("job-"))
		}
	}
}
