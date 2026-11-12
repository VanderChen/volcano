/*
Probe harness for issue 004 (topology scheduling refactor re-examination).
Test-only. Not part of the Volcano source tree; copied into
pkg/scheduler/actions/allocate/ of a throwaway worktree to run.
*/

package allocate

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	topologyv1alpha1 "volcano.sh/apis/pkg/apis/topology/v1alpha1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/plugins/gang"
	networktopologyaware "volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware"
	"volcano.sh/volcano/pkg/scheduler/plugins/predicates"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

const probePluginName = "probe-counter"

// probeCounters records how often the framework invoked each extension point.
type probeCounters struct {
	// one PrePredicate call per (task, candidate) dry-run attempt
	prePredicate atomic.Int64
	predicate    atomic.Int64
	nodeOrder    atomic.Int64
	allocate     atomic.Int64
	deallocate   atomic.Int64

	mu sync.Mutex
	// one entry per HyperNodeOrderFn invocation
	hyperNodeOrderCohorts [][]string
	// number of tasks of the scored job that are in Allocated state at scoring time
	allocatedWhenScored []int
}

func (c *probeCounters) String() string {
	return fmt.Sprintf("taskAttempts=%d predicate=%d nodeOrder=%d allocate=%d deallocate=%d hyperNodeOrderCalls=%d",
		c.prePredicate.Load(), c.predicate.Load(), c.nodeOrder.Load(), c.allocate.Load(), c.deallocate.Load(), len(c.hyperNodeOrderCohorts))
}

type probePlugin struct{ c *probeCounters }

func (p *probePlugin) Name() string { return probePluginName }

func (p *probePlugin) OnSessionOpen(ssn *framework.Session) {
	ssn.AddPrePredicateFn(p.Name(), func(*api.TaskInfo) error {
		p.c.prePredicate.Add(1)
		return nil
	})
	ssn.AddPredicateFn(p.Name(), func(*api.TaskInfo, *api.NodeInfo) error {
		p.c.predicate.Add(1)
		return nil
	})
	ssn.AddNodeOrderFn(p.Name(), func(*api.TaskInfo, *api.NodeInfo) (float64, error) {
		p.c.nodeOrder.Add(1)
		return 0, nil
	})
	ssn.AddHyperNodeOrderFn(p.Name(), func(subJob *api.SubJobInfo, hyperNodes map[string][]*api.NodeInfo) (map[string]float64, error) {
		cohort := make([]string, 0, len(hyperNodes))
		for name := range hyperNodes {
			cohort = append(cohort, name)
		}
		sort.Strings(cohort)
		allocated := 0
		if job := ssn.Jobs[subJob.Job]; job != nil {
			allocated = len(job.TaskStatusIndex[api.Allocated])
		}
		p.c.mu.Lock()
		p.c.hyperNodeOrderCohorts = append(p.c.hyperNodeOrderCohorts, cohort)
		p.c.allocatedWhenScored = append(p.c.allocatedWhenScored, allocated)
		p.c.mu.Unlock()
		return nil, nil
	})
	ssn.AddEventHandler(&framework.EventHandler{
		AllocateFunc:   func(*framework.Event) { p.c.allocate.Add(1) },
		DeallocateFunc: func(*framework.Event) { p.c.deallocate.Add(1) },
	})
}

func (p *probePlugin) OnSessionClose(*framework.Session) {}

func probePluginBuilders(c *probeCounters, extra map[string]framework.PluginBuilder) map[string]framework.PluginBuilder {
	builders := map[string]framework.PluginBuilder{
		gang.PluginName:                 gang.New,
		predicates.PluginName:           predicates.New,
		networktopologyaware.PluginName: networktopologyaware.New,
		probePluginName: func(framework.Arguments) framework.Plugin {
			return &probePlugin{c: c}
		},
	}
	for name, builder := range extra {
		builders[name] = builder
	}
	return builders
}

func probeTiers(extra ...conf.PluginOption) []conf.Tier {
	enabled := true
	plugins := []conf.PluginOption{
		{
			Name:                gang.PluginName,
			EnabledJobOrder:     &enabled,
			EnabledJobReady:     &enabled,
			EnabledJobPipelined: &enabled,
			EnabledJobStarving:  &enabled,
			EnabledSubJobReady:  &enabled,
			EnabledSubJobOrder:  &enabled,
			// Enabled by default in production (plugins/defaults.go). Without it
			// ssn.SubJobPipelined is vacuously true and a partially placed
			// SubJob counts as a successful dry-run.
			EnabledSubJobPipelined: &enabled,
		},
		{Name: predicates.PluginName, EnabledPredicate: &enabled},
		{
			Name:                     networktopologyaware.PluginName,
			EnabledNodeOrder:         &enabled,
			EnabledHyperNodeOrder:    &enabled,
			EnabledHyperNodeGradient: &enabled,
		},
		{
			Name:                  probePluginName,
			EnabledPredicate:      &enabled,
			EnabledNodeOrder:      &enabled,
			EnabledHyperNodeOrder: &enabled,
		},
	}
	plugins = append(plugins, extra...)
	return []conf.Tier{{Plugins: plugins}}
}

// probeTopo is a regular tree: one tier-2 zone per `zones`, `racks` tier-1
// racks per zone, `nodesPerRack` nodes per rack. With more than one zone a
// tier-3 root "dc" is added.
type probeTopo struct {
	nodes     []*v1.Node
	hyperMap  map[string]*api.HyperNodeInfo
	setByTier map[int]sets.Set[string]
	realNodes map[string]sets.Set[string]
	racks     []string
}

func rackName(zone, rack int) string { return fmt.Sprintf("z%d-r%02d", zone, rack) }

func nodeName(zone, rack, node int) string { return fmt.Sprintf("%s-n%d", rackName(zone, rack), node) }

// rackOfNode maps "z0-r03-n1" to "z0-r03".
func rackOfNode(node string) string {
	idx := strings.LastIndex(node, "-n")
	if idx < 0 {
		return ""
	}
	return node[:idx]
}

func buildProbeTopo(zones, racks, nodesPerRack int, cpu, mem string, mutate func(zone, rack, node int, n *v1.Node)) *probeTopo {
	topo := &probeTopo{
		hyperMap:  map[string]*api.HyperNodeInfo{},
		setByTier: map[int]sets.Set[string]{1: sets.New[string](), 2: sets.New[string]()},
		realNodes: map[string]sets.Set[string]{},
	}
	var zoneMembers []api.MemberConfig
	allNodes := sets.New[string]()
	for z := 0; z < zones; z++ {
		zone := fmt.Sprintf("z%d", z)
		zoneNodes := sets.New[string]()
		var rackMembers []api.MemberConfig
		for r := 0; r < racks; r++ {
			rack := rackName(z, r)
			rackNodes := sets.New[string]()
			var nodeMembers []api.MemberConfig
			for n := 0; n < nodesPerRack; n++ {
				name := nodeName(z, r, n)
				node := util.BuildNode(name, api.BuildResourceList(cpu, mem, []api.ScalarResource{{Name: "pods", Value: "110"}}...), map[string]string{})
				if mutate != nil {
					mutate(z, r, n, node)
				}
				topo.nodes = append(topo.nodes, node)
				rackNodes.Insert(name)
				nodeMembers = append(nodeMembers, api.MemberConfig{Name: name, Type: topologyv1alpha1.MemberTypeNode, Selector: "exact"})
			}
			topo.hyperMap[rack] = api.NewHyperNodeInfo(api.BuildHyperNode(rack, 1, nodeMembers), api.ParentOpt(zone))
			topo.setByTier[1].Insert(rack)
			topo.realNodes[rack] = rackNodes
			topo.racks = append(topo.racks, rack)
			zoneNodes = zoneNodes.Union(rackNodes)
			rackMembers = append(rackMembers, api.MemberConfig{Name: rack, Type: topologyv1alpha1.MemberTypeHyperNode, Selector: "exact"})
		}
		if zones > 1 {
			topo.hyperMap[zone] = api.NewHyperNodeInfo(api.BuildHyperNode(zone, 2, rackMembers), api.ParentOpt("dc"))
		} else {
			topo.hyperMap[zone] = api.NewHyperNodeInfo(api.BuildHyperNode(zone, 2, rackMembers))
		}
		topo.setByTier[2].Insert(zone)
		topo.realNodes[zone] = zoneNodes
		allNodes = allNodes.Union(zoneNodes)
		zoneMembers = append(zoneMembers, api.MemberConfig{Name: zone, Type: topologyv1alpha1.MemberTypeHyperNode, Selector: "exact"})
	}
	if zones > 1 {
		topo.hyperMap["dc"] = api.NewHyperNodeInfo(api.BuildHyperNode("dc", 3, zoneMembers))
		topo.setByTier[3] = sets.New[string]("dc")
		topo.realNodes["dc"] = allNodes
	}
	return topo
}

func (topo *probeTopo) apply(tc *uthelper.TestCommonStruct) {
	tc.Nodes = topo.nodes
	tc.HyperNodesMap = topo.hyperMap
	tc.HyperNodesSetByTier = topo.setByTier
	tc.HyperNodes = topo.realNodes
}

// hardJob builds a PodGroup with hard network topology and `tasks` identical pending pods.
func hardJob(name string, tasks int, cpu, mem string, highestTier int, mutate func(i int, pod *v1.Pod)) (*schedulingv1.PodGroup, []*v1.Pod) {
	pg := util.BuildPodGroupWithNetWorkTopologies(name, "c1", "", "q1", int32(tasks), nil, schedulingv1.PodGroupInqueue, "hard", highestTier)
	pods := make([]*v1.Pod, 0, tasks)
	for i := 0; i < tasks; i++ {
		pod := util.BuildPod("c1", fmt.Sprintf("%s-p%02d", name, i), "", v1.PodPending, api.BuildResourceList(cpu, mem), name,
			map[string]string{"volcano.sh/task-spec": "worker"}, nil)
		if mutate != nil {
			mutate(i, pod)
		}
		pods = append(pods, pod)
	}
	return pg, pods
}

// fillerPods occupies nodes with running pods of an unrelated PodGroup.
func fillerPods(nodes []string, cpu, mem string) (*schedulingv1.PodGroup, []*v1.Pod) {
	pg := util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)
	pods := make([]*v1.Pod, 0, len(nodes))
	for i, node := range nodes {
		pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node, v1.PodRunning, api.BuildResourceList(cpu, mem), "filler",
			map[string]string{"volcano.sh/task-spec": "filler"}, nil))
	}
	return pg, pods
}

type probeResult struct {
	counters *probeCounters
	// pod name -> node name for every task of every job that left Pending
	placed map[string]string
	// job UID -> snapshot of diagnostics
	jobFitErrors   map[api.JobID]string
	fitError       map[api.JobID]string
	nodesFitErrors map[api.JobID]map[string]string
}

// racks returns the sorted distinct racks used by pods whose name has the prefix.
func (r *probeResult) racks(prefix string) []string {
	used := sets.New[string]()
	for pod, node := range r.placed {
		if strings.HasPrefix(pod, prefix) {
			used.Insert(rackOfNode(node))
		}
	}
	return sets.List(used)
}

func (r *probeResult) count(prefix string) int {
	n := 0
	for pod := range r.placed {
		if strings.HasPrefix(pod, prefix) {
			n++
		}
	}
	return n
}

// runProbe opens a session, optionally lets the caller inspect it before the
// allocate action, runs allocate once and captures placements and diagnostics.
func runProbe(tb testing.TB, tc *uthelper.TestCommonStruct, tiers []conf.Tier, extra map[string]framework.PluginBuilder,
	action *Action, before func(ssn *framework.Session)) *probeResult {
	tb.Helper()
	res := &probeResult{
		counters:       &probeCounters{},
		placed:         map[string]string{},
		jobFitErrors:   map[api.JobID]string{},
		fitError:       map[api.JobID]string{},
		nodesFitErrors: map[api.JobID]map[string]string{},
	}
	tc.Plugins = probePluginBuilders(res.counters, extra)
	ssn := tc.RegisterSession(tiers, nil)
	defer tc.Close()
	if before != nil {
		before(ssn)
		// the callback may have invoked order functions; start counting from zero
		res.counters.mu.Lock()
		res.counters.hyperNodeOrderCohorts = nil
		res.counters.allocatedWhenScored = nil
		res.counters.mu.Unlock()
	}
	if action == nil {
		action = New()
	}
	tc.Run([]framework.Action{action})
	for jobID, job := range ssn.Jobs {
		for _, task := range job.Tasks {
			if task.Pod == nil || task.Pod.Status.Phase != v1.PodPending {
				continue
			}
			if task.Status == api.Pending || task.NodeName == "" {
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
	return res
}
