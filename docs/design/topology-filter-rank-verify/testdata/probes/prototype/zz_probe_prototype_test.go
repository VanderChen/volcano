/*
Prototype probes for issue 004: Filter -> Rank -> Verify versus the
exhaustive dry-run path. Requires the prototype patch (rank_verify.go).
*/

package allocate

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/plugins/gang"
	networktopologyaware "volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware"
	"volcano.sh/volcano/pkg/scheduler/plugins/nodeorder"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

type protoMode struct {
	name           string
	rank, fit, det bool
}

var (
	modeExhaustive    = protoMode{name: "exhaustive", det: false}
	modeExhaustiveDet = protoMode{name: "exhaustive+detTies", det: true}
	modeRanked        = protoMode{name: "ranked", rank: true}
	modeRankedFit     = protoMode{name: "ranked+fitBound", rank: true, fit: true}
)

func (m protoMode) action() *Action {
	a := New()
	a.rankThenVerify, a.fitBound, a.deterministicTies = m.rank, m.fit, m.det
	a.trace = &DecisionTrace{}
	return a
}

// Q1: same scenario as P1, three modes.
func TestProtoQ1FanOut(t *testing.T) {
	const tasks, nodesPerRack = 4, 4
	t.Logf("| racks | mode | predicate | nodeOrder | allocate | deallocate | hyperNodeOrder calls |")
	t.Logf("|---|---|---|---|---|---|---|")
	for _, racks := range []int{1, 4, 16, 32} {
		for _, mode := range []protoMode{modeExhaustive, modeRanked} {
			topo := buildProbeTopo(1, racks, nodesPerRack, "8", "16Gi", nil)
			pg, pods := hardJob("job", tasks, "8", "16Gi", 1, nil)
			tc := &uthelper.TestCommonStruct{Name: "fanout", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: pods, Queues: probeQueue()}
			topo.apply(tc)
			res := runProbe(t, tc, probeTiers(), nil, mode.action(), nil)
			if got := res.count("job-"); got != tasks {
				t.Fatalf("racks=%d mode=%s: placed %d tasks, want %d", racks, mode.name, got, tasks)
			}
			c := res.counters
			t.Logf("| %d | %s | %d | %d | %d | %d | %d |", racks, mode.name, c.predicate.Load(), c.nodeOrder.Load(),
				c.allocate.Load(), c.deallocate.Load(), len(c.hyperNodeOrderCohorts))
			if mode.rank && (c.allocate.Load() != tasks || c.deallocate.Load() != 0) {
				t.Errorf("racks=%d ranked: allocate=%d deallocate=%d, want %d and 0", racks, c.allocate.Load(), c.deallocate.Load(), tasks)
			}
		}
	}
}

// Q1c: nested fan-out (two SubGroups, each a whole rack, job within one zone).
func TestProtoQ1cNestedFanOut(t *testing.T) {
	t.Logf("| zones x racks | mode | predicate | allocate | deallocate | hyperNodeOrder calls |")
	t.Logf("|---|---|---|---|---|---|")
	for _, shape := range [][2]int{{2, 4}, {4, 8}} {
		for _, mode := range []protoMode{modeExhaustive, modeRanked} {
			zones, racks := shape[0], shape[1]
			topo := buildProbeTopo(zones, racks, 2, "8", "16Gi", nil)
			pg := util.BuildPodGroupWithSubGroupPolicy("job", "c1", "", "q1", 4, nil, schedulingv1.PodGroupInqueue, "hard", 2,
				[]schedulingv1.SubGroupPolicySpec{util.BuildSubGroupPolicyWithSubGroupSize("worker", []string{"volcano.sh/task-spec"}, "hard", 1, 2)})
			var pods []*v1.Pod
			for g := 0; g < 2; g++ {
				for i := 0; i < 2; i++ {
					pods = append(pods, util.BuildPod("c1", fmt.Sprintf("job-g%d-p%d", g, i), "", v1.PodPending, api.BuildResourceList("8", "16Gi"), "job",
						map[string]string{"volcano.sh/task-spec": fmt.Sprintf("worker-%d", g)}, nil))
				}
			}
			tc := &uthelper.TestCommonStruct{Name: "nested", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: pods, Queues: probeQueue()}
			topo.apply(tc)
			res := runProbe(t, tc, probeTiers(), nil, mode.action(), nil)
			if got := res.count("job-"); got != 4 {
				t.Fatalf("%dx%d mode=%s: placed %d tasks, want 4", zones, racks, mode.name, got)
			}
			c := res.counters
			t.Logf("| %dx%d | %s | %d | %d | %d | %d |", zones, racks, mode.name, c.predicate.Load(), c.allocate.Load(), c.deallocate.Load(),
				len(c.hyperNodeOrderCohorts))
		}
	}
}

// Q3: identical candidates must always resolve to the same rack.
func TestProtoQ3Determinism(t *testing.T) {
	hist := map[string]int{}
	for i := 0; i < 40; i++ {
		topo := buildProbeTopo(1, 4, 2, "8", "16Gi", nil)
		pg, pods := hardJob("job", 2, "8", "16Gi", 1, nil)
		tc := &uthelper.TestCommonStruct{Name: "tie", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: pods, Queues: probeQueue()}
		topo.apply(tc)
		res := runProbe(t, tc, probeTiers(), nil, modeRanked.action(), nil)
		placement := make([]string, 0, len(res.placed))
		for pod, node := range res.placed {
			placement = append(placement, pod+"@"+node)
		}
		sort.Strings(placement)
		hist[strings.Join(placement, ",")]++
	}
	t.Logf("ranked, identical racks, 40 sessions: %v", hist)
	if len(hist) != 1 {
		t.Errorf("ranked mode produced %d distinct placements, want 1", len(hist))
	}
}

func checkTraceInvariants(t *testing.T, trace *DecisionTrace) {
	t.Helper()
	for _, record := range trace.Records {
		counts := record.Count()
		total := counts[VerdictSelected] + counts[VerdictFailedVerify] + counts[VerdictPruned] + counts[VerdictNotEvaluated]
		if total != len(record.Candidates) {
			t.Errorf("%s: verdicts do not reconcile: %v vs %d candidates", record.Scope, counts, len(record.Candidates))
		}
		if counts[VerdictSelected] > 1 {
			t.Errorf("%s: %d candidates selected", record.Scope, counts[VerdictSelected])
		}
		seenSelected := false
		for i, c := range record.Candidates {
			if c.Rank != i+1 {
				t.Errorf("%s: candidate %s has rank %d at position %d", record.Scope, c.HyperNode, c.Rank, i)
			}
			if i > 0 && record.Candidates[i-1].Score < c.Score {
				t.Errorf("%s: candidates out of order at %d", record.Scope, i)
			}
			switch c.Verdict {
			case VerdictSelected:
				seenSelected = true
			case VerdictFailedVerify, VerdictPruned:
				if c.Reason == "" {
					t.Errorf("%s: candidate %s rejected without a reason", record.Scope, c.HyperNode)
				}
				if seenSelected {
					t.Errorf("%s: candidate %s evaluated after the winner", record.Scope, c.HyperNode)
				}
			case VerdictNotEvaluated:
				if !seenSelected {
					t.Errorf("%s: candidate %s skipped although no better candidate was selected", record.Scope, c.HyperNode)
				}
			}
		}
	}
}

// Q4: the trace is the explanation. Scenario of P5 (nothing fits) and a
// scenario where the best-ranked rack fails verification.
func TestProtoQ4Trace(t *testing.T) {
	t.Run("nothing fits", func(t *testing.T) {
		topo := buildProbeTopo(1, 3, 4, "8", "16Gi", func(_, rack, _ int, n *v1.Node) {
			if rack == 0 {
				n.Spec.Taints = []v1.Taint{{Key: "dedicated", Value: "other", Effect: v1.TaintEffectNoSchedule}}
			}
		})
		var r01, r02 []string
		for n := 0; n < 4; n++ {
			r01 = append(r01, nodeName(0, 1, n))
			r02 = append(r02, nodeName(0, 2, n))
		}
		fillerPG, fillers := fillerPods(r01, "6", "1Gi")
		_, fillers2 := fillerPods(r02, "4", "1Gi")
		for i, pod := range fillers2 {
			pod.Name = fmt.Sprintf("filler-b-%03d", i)
			pod.UID = "uid-" + pod.UID + "-b"
		}
		pg, pods := hardJob("job", 2, "8", "1Gi", 1, nil)
		pods = append(append(pods, fillers...), fillers2...)
		tc := &uthelper.TestCommonStruct{Name: "explain", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG}, Pods: pods, Queues: probeQueue()}
		topo.apply(tc)
		action := modeRankedFit.action()
		res := runProbe(t, tc, probeTiers(), nil, action, nil)
		if got := res.count("job-"); got != 0 {
			t.Fatalf("expected the job to be unschedulable, placed %d tasks", got)
		}
		for _, record := range action.trace.Records {
			t.Logf("%s", record)
		}
		checkTraceInvariants(t, action.trace)
	})

	t.Run("best ranked rack fails verification", func(t *testing.T) {
		// rack i carries i running 8-CPU pods; r04 has the best bin-packing score but is tainted.
		topo := buildProbeTopo(1, 6, 4, "16", "32Gi", func(_, rack, _ int, n *v1.Node) {
			if rack == 4 {
				n.Spec.Taints = []v1.Taint{{Key: "maintenance", Value: "true", Effect: v1.TaintEffectNoSchedule}}
			}
		})
		var occupied []string
		for r := 0; r <= 4; r++ {
			for n := 0; n < r; n++ {
				occupied = append(occupied, nodeName(0, r, n))
			}
		}
		fillerPG, fillers := fillerPods(occupied, "8", "16Gi")
		pg, pods := hardJob("job", 4, "8", "16Gi", 1, nil)
		tc := &uthelper.TestCommonStruct{Name: "trace", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG}, Pods: append(pods, fillers...), Queues: probeQueue()}
		topo.apply(tc)
		action := modeRankedFit.action()
		res := runProbe(t, tc, probeTiers(), nil, action, nil)
		for _, record := range action.trace.Records {
			t.Logf("%s", record)
		}
		checkTraceInvariants(t, action.trace)
		if used := res.racks("job-"); len(used) != 1 || used[0] != "z0-r03" {
			t.Errorf("job placed in %v, want z0-r03 (second in rank)", used)
		}
		t.Logf("counters: %s", res.counters)
	})
}

// Q10: the fit bound must stay an upper bound for pods whose init container
// requests more than the app containers. How such a pod is charged differs
// between code lines (InitResreq vs Resreq) and between configurations (the
// predicates plugin adds the Kubernetes NodeResourcesFit rule), so the test
// does not assume a capacity: it requires the bound never to place fewer pods
// than the exhaustive path, with and without the predicates plugin.
func TestProtoQ10FitBoundWithInitContainers(t *testing.T) {
	enabled := true
	noPredicates := []conf.Tier{{Plugins: []conf.PluginOption{
		{Name: gang.PluginName, EnabledJobOrder: &enabled, EnabledJobReady: &enabled, EnabledJobPipelined: &enabled,
			EnabledSubJobReady: &enabled, EnabledSubJobOrder: &enabled, EnabledSubJobPipelined: &enabled},
		{Name: networktopologyaware.PluginName, EnabledNodeOrder: &enabled, EnabledHyperNodeOrder: &enabled, EnabledHyperNodeGradient: &enabled},
		{Name: probePluginName, EnabledPredicate: &enabled, EnabledNodeOrder: &enabled, EnabledHyperNodeOrder: &enabled},
	}}}
	for _, cfg := range []struct {
		name  string
		tiers []conf.Tier
	}{{"with predicates", probeTiers()}, {"without predicates", noPredicates}} {
		for pods := 1; pods <= 12; pods++ {
			placed := map[string]int{}
			for _, mode := range []protoMode{modeExhaustiveDet, modeRankedFit} {
				// one rack of four 3-CPU nodes; a pod needs 2 CPU to start and 0.5 CPU afterwards
				topo := buildProbeTopo(1, 1, 4, "3", "16Gi", nil)
				pg, jobPods := hardJob("job", pods, "500m", "1Gi", 1, func(_ int, pod *v1.Pod) {
					pod.Spec.InitContainers = []v1.Container{{Resources: v1.ResourceRequirements{Requests: api.BuildResourceList("2", "1Gi")}}}
				})
				tc := &uthelper.TestCommonStruct{Name: "init", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: jobPods, Queues: probeQueue()}
				topo.apply(tc)
				placed[mode.name] = runProbe(t, tc, cfg.tiers, nil, mode.action(), nil).count("job-")
			}
			if placed[modeRankedFit.name] != placed[modeExhaustiveDet.name] {
				t.Errorf("%s, gang of %d: exhaustive placed %d, ranked+fitBound placed %d", cfg.name, pods,
					placed[modeExhaustiveDet.name], placed[modeRankedFit.name])
			}
			if pods == 12 || (pods > 1 && placed[modeExhaustiveDet.name] == 0) {
				t.Logf("%s: largest gang the exhaustive path places on four 3-CPU nodes (init 2 CPU, app 0.5 CPU) is below %d", cfg.name, pods)
				break
			}
		}
	}
}

// Q11: deterministic ties must not herd every job onto the same node.
func TestProtoQ11TieKeySpreads(t *testing.T) {
	run := func() map[string]string {
		topo := buildProbeTopo(1, 1, 8, "8", "16Gi", nil)
		var pgs []*schedulingv1.PodGroup
		var pods []*v1.Pod
		for j := 0; j < 20; j++ {
			name := fmt.Sprintf("solo%02d", j)
			pgs = append(pgs, util.BuildPodGroup(name, "c1", "q1", 1, nil, schedulingv1.PodGroupInqueue))
			pods = append(pods, util.BuildPod("c1", name+"-p0", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), name,
				map[string]string{"volcano.sh/task-spec": "worker"}, nil))
		}
		tc := &uthelper.TestCommonStruct{Name: "spread", PodGroups: pgs, Pods: pods, Queues: probeQueue()}
		topo.apply(tc)
		return runProbe(t, tc, probeTiers(), nil, modeExhaustiveDet.action(), nil).placed
	}
	first, second := run(), run()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("deterministic ties produced two different placements")
	}
	perNode := map[string]int{}
	for _, node := range first {
		perNode[node]++
	}
	t.Logf("20 single-pod jobs without topology, 8 identical nodes, no node scoring: pods per node = %v", perNode)
	if len(first) != 20 {
		t.Errorf("placed %d pods, want 20", len(first))
	}
	if len(perNode) < 4 {
		t.Errorf("pods landed on only %d of 8 nodes", len(perNode))
	}
}

// Q6: the paths on which ranking alone saves nothing.
func TestProtoQ6FailureAndUpperLayerPaths(t *testing.T) {
	t.Logf("| scenario | mode | task attempts | allocate | deallocate | placed |")
	t.Logf("|---|---|---|---|---|---|")
	type scenario struct {
		name  string
		build func() *uthelper.TestCommonStruct
		want  int
	}
	scenarios := []scenario{
		{
			// every rack has 16 idle CPU in total but only 4 per node: no rack can host one 8-CPU pod.
			name: "nothing fits: 16 fragmented racks, job 2x8cpu hard tier 1",
			build: func() *uthelper.TestCommonStruct {
				topo := buildProbeTopo(1, 16, 4, "8", "16Gi", nil)
				var all []string
				for _, node := range topo.nodes {
					all = append(all, node.Name)
				}
				fillerPG, fillers := fillerPods(all, "4", "1Gi")
				pg, pods := hardJob("job", 2, "8", "1Gi", 1, nil)
				tc := &uthelper.TestCommonStruct{Name: "fail", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG}, Pods: append(pods, fillers...), Queues: probeQueue()}
				topo.apply(tc)
				return tc
			},
			want: 0,
		},
		{
			// a rack hosts 2 pods, the job needs 4: every rack fails, the zone succeeds.
			name: "fits only one tier up: 2 zones x 8 racks, job 4x8cpu hard tier 2",
			build: func() *uthelper.TestCommonStruct {
				topo := buildProbeTopo(2, 8, 2, "8", "16Gi", nil)
				pg, pods := hardJob("job", 4, "8", "1Gi", 2, nil)
				tc := &uthelper.TestCommonStruct{Name: "upper", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: pods, Queues: probeQueue()}
				topo.apply(tc)
				return tc
			},
			want: 4,
		},
	}
	for _, sc := range scenarios {
		var base map[string]string
		for _, mode := range []protoMode{modeExhaustiveDet, modeRanked, modeRankedFit} {
			res := runProbe(t, sc.build(), probeTiers(), nil, mode.action(), nil)
			c := res.counters
			t.Logf("| %s | %s | %d | %d | %d | %d |", sc.name, mode.name, c.prePredicate.Load(), c.allocate.Load(), c.deallocate.Load(), res.count("job-"))
			if got := res.count("job-"); got != sc.want {
				t.Errorf("%s/%s: placed %d, want %d", sc.name, mode.name, got, sc.want)
			}
			if base == nil {
				base = res.placed
			} else if !reflect.DeepEqual(base, res.placed) {
				t.Errorf("%s/%s: placement differs from exhaustive", sc.name, mode.name)
			}
		}
	}
}

// ---- Q2: randomized differential test -------------------------------------

type fuzzScenario struct {
	tc      *uthelper.TestCommonStruct
	summary string
}

func genFuzzScenario(seed int64) *fuzzScenario { return genFuzzScenarioKinds(seed, 4) }

// genFuzzScenarioKinds draws job kinds from [0, kinds). Kind 4 is a plain
// soft-topology job: upstream converts it to hard at the cluster-top tier and
// sends it through allocateForJob; dev-1.14 keeps it on the flat path.
func genFuzzScenarioKinds(seed int64, kinds int) *fuzzScenario {
	rng := rand.New(rand.NewSource(seed))
	zones, racks, perRack := 1+rng.Intn(3), 2+rng.Intn(3), 2+rng.Intn(3)
	nodeCPU := map[string]int{}
	topo := buildProbeTopo(zones, racks, perRack, "16", "64Gi", func(_, _, _ int, n *v1.Node) {
		cpu := []int{8, 16}[rng.Intn(2)]
		nodeCPU[n.Name] = cpu
		alloc := api.BuildResourceList(fmt.Sprint(cpu), "64Gi", []api.ScalarResource{{Name: "pods", Value: "110"}}...)
		n.Status.Allocatable, n.Status.Capacity = alloc, alloc
		if rng.Intn(10) == 0 {
			n.Spec.Taints = []v1.Taint{{Key: "dedicated", Value: "other", Effect: v1.TaintEffectNoSchedule}}
		}
	})

	var pods []*v1.Pod
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	used := map[string]int{}
	for i, node := range topo.nodes {
		if rng.Intn(2) == 0 {
			continue
		}
		cpu := []int{2, 4, 6}[rng.Intn(3)]
		used[node.Name] = cpu
		pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning, api.BuildResourceList(fmt.Sprint(cpu), "1Gi"),
			"filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
	}

	maxTier := 2
	if zones > 1 {
		maxTier = 3
	}
	var summary []string
	jobs := 1 + rng.Intn(4)
	for j := 0; j < jobs; j++ {
		name := fmt.Sprintf("job%d", j)
		cpu := []int{2, 4, 8}[rng.Intn(3)]
		req := api.BuildResourceList(fmt.Sprint(cpu), "1Gi")
		switch kind := rng.Intn(kinds); kind {
		case 0, 1: // plain hard job
			tier := 1 + rng.Intn(maxTier)
			tasks := 1 + rng.Intn(6)
			minMember := tasks
			if tasks > 1 && rng.Intn(5) == 0 {
				minMember = tasks - 1
			}
			pgs = append(pgs, util.BuildPodGroupWithNetWorkTopologies(name, "c1", "", "q1", int32(minMember), nil, schedulingv1.PodGroupInqueue, "hard", tier))
			for i := 0; i < tasks; i++ {
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-p%02d", name, i), "", v1.PodPending, req, name,
					map[string]string{"volcano.sh/task-spec": "worker"}, nil))
			}
			summary = append(summary, fmt.Sprintf("%s: hard tier %d, %d x %dcpu, min %d", name, tier, tasks, cpu, minMember))
		case 2: // SubGroup job
			jobTier := 2 + rng.Intn(maxTier-1)
			groups, size := 2+rng.Intn(2), 1+rng.Intn(3)
			subMode := "hard"
			if rng.Intn(3) == 0 {
				subMode = ""
			}
			pgs = append(pgs, util.BuildPodGroupWithSubGroupPolicy(name, "c1", "", "q1", int32(groups*size), nil, schedulingv1.PodGroupInqueue, "hard", jobTier,
				[]schedulingv1.SubGroupPolicySpec{util.BuildSubGroupPolicyWithSubGroupSize("worker", []string{"volcano.sh/task-spec"}, subMode, 1, int32(size))}))
			for g := 0; g < groups; g++ {
				for i := 0; i < size; i++ {
					pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-g%d-p%d", name, g, i), "", v1.PodPending, req, name,
						map[string]string{"volcano.sh/task-spec": fmt.Sprintf("worker-%d", g)}, nil))
				}
			}
			summary = append(summary, fmt.Sprintf("%s: SubGroup job tier %d, %d groups x %d x %dcpu, subMode=%q", name, jobTier, groups, size, cpu, subMode))
		case 3: // partially running hard tier-2 job (exercises the task-number bonus)
			rack := topo.racks[rng.Intn(len(topo.racks))]
			running, pending := 1+rng.Intn(2), 1+rng.Intn(4)
			minMember := running + pending
			if rng.Intn(2) == 0 {
				minMember = running
			}
			pgs = append(pgs, util.BuildPodGroupWithNetWorkTopologies(name, "c1", rack, "q1", int32(minMember), nil, schedulingv1.PodGroupRunning, "hard", 2))
			placedRunning := 0
			for n := 0; n < perRack && placedRunning < running; n++ {
				node := rack + fmt.Sprintf("-n%d", n)
				if nodeCPU[node]-used[node] < 2 {
					continue
				}
				used[node] += 2
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-r%02d", name, placedRunning), node, v1.PodRunning, api.BuildResourceList("2", "1Gi"), name,
					map[string]string{"volcano.sh/task-spec": "worker"}, nil))
				placedRunning++
			}
			for i := 0; i < pending; i++ {
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-p%02d", name, i), "", v1.PodPending, req, name,
					map[string]string{"volcano.sh/task-spec": "worker"}, nil))
			}
			summary = append(summary, fmt.Sprintf("%s: running %d in %s + %d pending x %dcpu, hard tier 2, min %d", name, placedRunning, rack, pending, cpu, minMember))
		case 4: // plain soft job
			tier := 1 + rng.Intn(maxTier)
			tasks := 1 + rng.Intn(6)
			pgs = append(pgs, util.BuildPodGroupWithNetWorkTopologies(name, "c1", "", "q1", int32(tasks), nil, schedulingv1.PodGroupInqueue, "soft", tier))
			for i := 0; i < tasks; i++ {
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-p%02d", name, i), "", v1.PodPending, req, name,
					map[string]string{"volcano.sh/task-spec": "worker"}, nil))
			}
			summary = append(summary, fmt.Sprintf("%s: soft tier %d, %d x %dcpu", name, tier, tasks, cpu))
		}
	}

	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("fuzz-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return &fuzzScenario{tc: tc, summary: fmt.Sprintf("topo %dx%dx%d; %s", zones, racks, perRack, strings.Join(summary, " | "))}
}

func fuzzTiers() ([]conf.Tier, map[string]framework.PluginBuilder) {
	enabled := true
	return probeTiers(conf.PluginOption{Name: nodeorder.PluginName, EnabledNodeOrder: &enabled}),
		map[string]framework.PluginBuilder{nodeorder.PluginName: nodeorder.New}
}

func TestProtoQ2DifferentialRandom(t *testing.T) { runDifferential(t, 400, 4) }

// Q2b: same differential test with soft-topology jobs in the mix.
func TestProtoQ2bDifferentialRandomWithSoft(t *testing.T) { runDifferential(t, 300, 5) }

func runDifferential(t *testing.T, scenarios int64, kinds int) {
	tiers, extra := fuzzTiers()
	type agg struct{ allocate, deallocate, predicate, attempts int64 }
	totals := map[string]*agg{}
	mismatch := map[string]int{}
	placedJobs := 0
	for seed := int64(1); seed <= scenarios; seed++ {
		results := map[string]*probeResult{}
		traces := map[string]*DecisionTrace{}
		for _, mode := range []protoMode{modeExhaustiveDet, modeRanked, modeRankedFit} {
			scenario := genFuzzScenarioKinds(seed, kinds)
			action := mode.action()
			res := runProbe(t, scenario.tc, tiers, extra, action, nil)
			results[mode.name], traces[mode.name] = res, action.trace
			if totals[mode.name] == nil {
				totals[mode.name] = &agg{}
			}
			totals[mode.name].allocate += res.counters.allocate.Load()
			totals[mode.name].deallocate += res.counters.deallocate.Load()
			totals[mode.name].predicate += res.counters.predicate.Load()
			totals[mode.name].attempts += res.counters.prePredicate.Load()
			if mode.rank {
				checkTraceInvariants(t, action.trace)
			}
		}
		base := results[modeExhaustiveDet.name]
		if len(base.placed) > 0 {
			placedJobs++
		}
		for _, mode := range []protoMode{modeRanked, modeRankedFit} {
			if reflect.DeepEqual(base.placed, results[mode.name].placed) {
				continue
			}
			mismatch[mode.name]++
			if mismatch[mode.name] <= 3 {
				t.Logf("MISMATCH seed=%d mode=%s\n  scenario: %s\n  exhaustive: %v\n  %s: %v", seed, mode.name,
					genFuzzScenarioKinds(seed, kinds).summary, base.placed, mode.name, results[mode.name].placed)
				for _, record := range traces[mode.name].Records {
					t.Logf("    %s", record)
				}
			}
		}
	}
	t.Logf("%d random scenarios (%d placed at least one pod)", scenarios, placedJobs)
	for _, mode := range []protoMode{modeExhaustiveDet, modeRanked, modeRankedFit} {
		a := totals[mode.name]
		t.Logf("  %-20s taskAttempts=%d allocate=%d deallocate=%d predicate=%d placement mismatches vs exhaustive=%d", mode.name, a.attempts, a.allocate, a.deallocate, a.predicate, mismatch[mode.name])
	}
	for name, n := range mismatch {
		if n > 0 {
			t.Errorf("%s: %d of %d scenarios placed pods differently from the exhaustive path", name, n, scenarios)
		}
	}
}
