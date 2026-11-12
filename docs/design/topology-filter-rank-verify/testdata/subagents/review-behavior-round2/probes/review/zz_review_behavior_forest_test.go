package allocate

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/plugins/gang"
	"volcano.sh/volcano/pkg/scheduler/plugins/predicates"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// RB11 (dimension 4): SubJob records of an elastic SubGroup Job, with and
// without required anti-affinity, on the general path and on the forest path.
func TestReviewRB11ElasticSubJobRecords(t *testing.T) {
	build := func(noPAA bool) *uthelper.TestCommonStruct {
		v := bcVariants[0]
		v.noPAA = noPAA
		tc := buildBCScenario(v)
		pg, _ := rvTarget(tc, "pg-myq", "myq-")
		pg.Spec.MinMember = 2
		one := int32(1)
		for i := range pg.Spec.SubGroupPolicy {
			pg.Spec.SubGroupPolicy[i].MinSubGroups = &one
		}
		return tc
	}
	for _, noPAA := range []bool{false, true} {
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
			out := rvRun(t, build(noPAA), cfg)
			job := out.jobs["c1/pg-myq"]
			t.Logf("noPAA=%t [%s] placed=%s", noPAA, cfg.name, rvPlaced(out.res, "myq-"))
			t.Logf("    %s", rvJobState(job))
			for id, subJob := range job.SubJobs {
				for _, task := range subJob.Tasks {
					if task.NodeName == "" {
						continue
					}
					if rack := rackOfNode(task.NodeName); subJob.AllocatedHyperNode != rack {
						t.Logf("    MISMATCH subJob %s (hard tier 1): pod on %s, recorded AllocatedHyperNode=%q", id, task.NodeName, subJob.AllocatedHyperNode)
					}
				}
			}
		}
	}
}

// rvForestScopePlugin is the fake plugin of podgroup_antiaffinity_forest_test.go
// with its Job-level result declared as a usable region.
type rvForestScopePlugin struct {
	podGroupAntiAffinityForestTestPluginImpl
}

func (p *rvForestScopePlugin) OnSessionOpen(ssn *framework.Session) {
	p.podGroupAntiAffinityForestTestPluginImpl.OnSessionOpen(ssn)
	ssn.AddHyperNodeGradientForJobFn(podGroupAntiAffinityForestTestPlugin, func(job *api.JobInfo, _ *api.HyperNodeInfo) api.HyperNodeGradientResult {
		if !job.ContainsHardPodGroupAntiAffinity() {
			return api.HyperNodeGradientAbstain()
		}
		result := api.HyperNodeGradientConstrain([][]*api.HyperNodeInfo{{ssn.HyperNodes["b-t2"], ssn.HyperNodes["c-t2"]}})
		result.Scope = true
		return result
	})
}

// rvForestCase replays runCandidateForestActionTest and returns the assertions that fail.
func rvForestCase(tc candidateForestActionTestCase, scopePlugin bool) (failures []string, summary string) {
	tier1, tier2, tier3, hyperNodes, realNodes, nodes := buildPodGroupAntiAffinityForestTopology()
	trueValue := true
	tiers := []conf.Tier{{Plugins: []conf.PluginOption{
		{Name: gang.PluginName, EnabledJobOrder: &trueValue, EnabledJobReady: &trueValue, EnabledJobPipelined: &trueValue,
			EnabledJobStarving: &trueValue, EnabledSubJobReady: &trueValue, EnabledSubJobOrder: &trueValue},
		{Name: predicates.PluginName, EnabledPredicate: &trueValue},
		{Name: podGroupAntiAffinityForestTestPlugin, EnabledHyperNodeGradient: &trueValue, EnabledHyperNodeOrder: &trueValue},
	}}}
	subJobTopologyMode := tc.subJobTopologyMode
	if subJobTopologyMode == "" {
		subJobTopologyMode = string(schedulingv1.HardNetworkTopologyMode)
	}
	pg := util.BuildPodGroupWithSubGroupPolicy("target", "default", "", "q1", int32(tc.minMember), nil, schedulingv1.PodGroupInqueue,
		tc.jobTopologyMode, tc.jobHighestTier,
		[]schedulingv1.SubGroupPolicySpec{util.BuildSubGroupPolicyWithMinSubGroups("worker", []string{"volcano.sh/shard-id"}, subJobTopologyMode, 1, 1, int32(tc.podCount))})
	tierTwo := int32(2)
	pg.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
		Required: []schedulingv1.PodGroupAffinityTerm{{
			PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"topology.volcano.sh/group": "llama-70b-prod"}},
			TopologyTier:     &tierTwo,
		}},
	}}
	pods := make([]*v1.Pod, 0, tc.podCount)
	for shard := 0; shard < tc.podCount; shard++ {
		pods = append(pods, util.BuildPod("default", fmt.Sprintf("target-%d", shard), "", v1.PodPending,
			api.BuildResourceList("100m", "128Mi", []api.ScalarResource{{Name: string(forestTestAscendResource), Value: "6"}}...),
			"target", map[string]string{"volcano.sh/task-spec": "worker", "volcano.sh/shard-id": fmt.Sprintf("%d", shard)}, nil))
	}
	var plugin framework.PluginBuilder = func(framework.Arguments) framework.Plugin { return &podGroupAntiAffinityForestTestPluginImpl{} }
	if scopePlugin {
		plugin = func(framework.Arguments) framework.Plugin { return &rvForestScopePlugin{} }
	}
	test := uthelper.TestCommonStruct{
		Name:                tc.name,
		Plugins:             map[string]framework.PluginBuilder{gang.PluginName: gang.New, predicates.PluginName: predicates.New, podGroupAntiAffinityForestTestPlugin: plugin},
		PodGroups:           []*schedulingv1.PodGroup{pg},
		Pods:                pods,
		Nodes:               nodes,
		HyperNodesSetByTier: map[int]sets.Set[string]{1: tier1, 2: tier2, 3: tier3},
		HyperNodesMap:       hyperNodes,
		HyperNodes:          realNodes,
		Queues:              []*schedulingv1.Queue{util.BuildQueue("q1", 1, nil)},
		ExpectBindsNum:      tc.podCount,
		MinimalBindCheck:    true,
	}
	ssn := test.RegisterSession(tiers, nil)
	defer test.Close()
	action := New()
	action.deterministicTies = true
	test.Run([]framework.Action{action})
	if err := test.CheckAll(0); err != nil {
		failures = append(failures, "bind check: "+err.Error())
	}
	job := ssn.Jobs[api.JobID("default/target")]
	if job.AllocatedHyperNode != tc.wantJobPlacement {
		failures = append(failures, fmt.Sprintf("job.AllocatedHyperNode=%q want %q", job.AllocatedHyperNode, tc.wantJobPlacement))
	}
	if decision := action.recorder.jobDecisions[job.UID]; decision != tc.wantJobPlacement {
		failures = append(failures, fmt.Sprintf("recorded job decision=%q want %q", decision, tc.wantJobPlacement))
	}
	var keys []string
	for key := range action.recorder.subJobDecisions[job.UID] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != tc.wantJobPlacement {
			failures = append(failures, fmt.Sprintf("recorded SubJob decision keys=%v want only %q", keys, tc.wantJobPlacement))
			break
		}
	}
	gotNodes := sets.New[string]()
	for _, task := range job.Tasks {
		if task.NodeName == "a-node-0" || task.NodeName == "a-node-1" {
			failures = append(failures, fmt.Sprintf("task %s leaked into excluded domain A node %s", task.UID, task.NodeName))
		}
		if task.NodeName != "" {
			gotNodes.Insert(task.NodeName)
		}
	}
	if tc.wantNodes != nil && !gotNodes.Equal(tc.wantNodes) {
		failures = append(failures, fmt.Sprintf("nodes=%v want %v", sets.List(gotNodes), sets.List(tc.wantNodes)))
	}
	return failures, fmt.Sprintf("nodes=%v AllocatedHyperNode=%q subJobDecisionKeys=%v", sets.List(gotNodes), job.AllocatedHyperNode, keys)
}

// RB12 (dimension 7): the dev-1.14 forest tests replayed with a fake plugin
// that declares its Job-level result as a usable region.
func TestReviewRB12ForestTestsUnderRegion(t *testing.T) {
	cases := []candidateForestActionTestCase{
		{name: "TestAllocatePodGroupAntiAffinityAcrossCandidateForest", podCount: 4, minMember: 4, wantJobPlacement: "shared-t3",
			wantNodes: sets.New[string]("b-node-0", "b-node-1", "c-node-0", "c-node-1")},
		{name: "TestCandidateForestUsesActualPlacementInsteadOfEnvelope", podCount: 2, minMember: 2, wantJobPlacement: "b-t2",
			wantNodes: sets.New[string]("b-node-0", "b-node-1")},
		{name: "TestCandidateForestPreservesNativeSoftJobTopologyFallback", podCount: 4, minMember: 4,
			jobTopologyMode: string(schedulingv1.SoftNetworkTopologyMode), jobHighestTier: 2, wantJobPlacement: "shared-t3",
			wantNodes: sets.New[string]("b-node-0", "b-node-1", "c-node-0", "c-node-1")},
		{name: "TestCandidateForestSupportsNativeSoftSubGroups", podCount: 4, minMember: 4,
			subJobTopologyMode: string(schedulingv1.SoftNetworkTopologyMode), wantJobPlacement: "shared-t3",
			wantNodes: sets.New[string]("b-node-0", "b-node-1", "c-node-0", "c-node-1")},
	}
	type mode struct {
		name                         string
		scopePlugin, union, fallback bool
	}
	modes := []mode{
		{"forest (as committed)", false, false, false},
		{"plugin declares region, switch off", true, false, false},
		{"option a: forest first", true, true, true},
		{"option b: region replaces forest", true, true, false},
	}
	for _, tc := range cases {
		t.Logf("### %s", tc.name)
		for _, m := range modes {
			prevU, prevF := framework.ProbeScopeUnion, framework.ProbeScopeFallback
			framework.ProbeScopeUnion, framework.ProbeScopeFallback = m.union, m.fallback
			failures, summary := rvForestCase(tc, m.scopePlugin)
			framework.ProbeScopeUnion, framework.ProbeScopeFallback = prevU, prevF
			verdict := "all assertions hold"
			if len(failures) > 0 {
				verdict = "FAILS: " + strings.Join(failures, "; ")
			}
			t.Logf("  [%s] %s | %s", m.name, verdict, summary)
		}
	}
}

// RB13 (dimension 6): cost of composing the region and of deriving the node
// set as the number of HyperNodes grows.
func TestReviewRB13ScopeCostAtScale(t *testing.T) {
	t.Logf("| zones x racks x nodes | HyperNodes | nodes | gradient call, switch off | gradient call, switch on | setScope | whole session off | option a | option b |")
	t.Logf("|---|---|---|---|---|---|---|---|---|")
	for _, shape := range [][3]int{{5, 20, 2}, {10, 50, 2}, {20, 100, 2}} {
		zones, racks, perRack := shape[0], shape[1], shape[2]
		build := func() *uthelper.TestCommonStruct {
			topo := buildProbeTopo(zones, racks, perRack, "8", "64Gi", nil)
			anchor := util.BuildPodGroup("anchor", "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
			anchor.Labels = map[string]string{groupLabel: groupValue}
			pods := []*v1.Pod{util.BuildPod("c1", "anchor-0", nodeName(0, 0, 0), v1.PodRunning, api.BuildResourceList("1", "1Gi"), "anchor",
				map[string]string{"volcano.sh/task-spec": "anchor"}, nil)}
			target := util.BuildPodGroup("job", "c1", "q1", 4, nil, schedulingv1.PodGroupInqueue)
			target.Labels = map[string]string{groupLabel: groupValue}
			tierTwo := int32(2)
			target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
				Required: []schedulingv1.PodGroupAffinityTerm{{
					PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{groupLabel: groupValue}}, TopologyTier: &tierTwo}}}}
			for i := 0; i < 4; i++ {
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("job-p%d", i), "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
					map[string]string{"volcano.sh/task-spec": "worker"}, nil))
			}
			tc := &uthelper.TestCommonStruct{Name: "scale", PodGroups: []*schedulingv1.PodGroup{anchor, target}, Pods: pods, Queues: probeQueue()}
			topo.apply(tc)
			return tc
		}
		var gradOff, gradOn, setScope time.Duration
		hyperNodes := 0
		tiers, extra := historyTiers()
		runProbe(t, build(), tiers, extra, nil, func(ssn *framework.Session) {
			job := ssn.Jobs["c1/job"]
			root := ssn.HyperNodes[framework.ClusterTopHyperNode]
			hyperNodes = len(ssn.HyperNodes)
			previous := framework.ProbeScopeUnion
			defer func() { framework.ProbeScopeUnion = previous }()
			const rounds = 5
			var stats *api.HyperNodeGradientStats
			framework.ProbeScopeUnion = false
			start := time.Now()
			for i := 0; i < rounds; i++ {
				ssn.HyperNodeGradientForJobFn(job, root)
			}
			gradOff = time.Since(start) / rounds
			framework.ProbeScopeUnion = true
			start = time.Now()
			for i := 0; i < rounds; i++ {
				_, stats = ssn.HyperNodeGradientForJobFn(job, root)
			}
			gradOn = time.Since(start) / rounds
			action := New()
			action.session = ssn
			start = time.Now()
			for i := 0; i < rounds; i++ {
				action.setScope(job, stats)
			}
			setScope = time.Since(start) / rounds
		})
		var whole []string
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
			start := time.Now()
			out := rvRun(t, build(), cfg)
			whole = append(whole, fmt.Sprintf("%v (placed %d, taskAttempts %d)", time.Since(start).Round(time.Millisecond), out.res.count("job-"), out.res.counters.prePredicate.Load()))
		}
		t.Logf("| %dx%dx%d | %d | %d | %v | %v | %v | %s | %s | %s |", zones, racks, perRack, hyperNodes, zones*racks*perRack,
			gradOff.Round(time.Microsecond), gradOn.Round(time.Microsecond), setScope.Round(time.Microsecond), whole[0], whole[1], whole[2])
	}
}
