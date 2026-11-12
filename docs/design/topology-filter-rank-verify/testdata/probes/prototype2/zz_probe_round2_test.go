/*
Round-2 prototype probes for issue 004: usable region (scope) and
failure-path repair. Needs probes/common, probes/history, probes/prototype
and the round-2 prototype patch.
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// round2 describes which round-2 mechanisms are on.
type round2 struct {
	scope  bool
	budget int
}

func (r round2) String() string {
	return fmt.Sprintf("scope=%t repairBudget=%d", r.scope, r.budget)
}

// runRound2 runs one scenario with the given mode and round-2 switches.
func runRound2(t testing.TB, tc *uthelper.TestCommonStruct, mode protoMode, r round2, mutate func(*Action), before func(*framework.Session)) (*probeResult, *Action) {
	t.Helper()
	previous := framework.ProbeScopeUnion
	framework.ProbeScopeUnion = r.scope
	defer func() { framework.ProbeScopeUnion = previous }()
	action := mode.action()
	action.repairBudget = r.budget
	if mutate != nil {
		mutate(action)
	}
	tiers, extra := historyTiers()
	return runProbe(t, tc, tiers, extra, action, before), action
}

// R1: the reported bug and its variants with the usable-region composition.
func TestProto2R1ScopeUnion(t *testing.T) {
	t.Logf("| variant | legal placement exists | exhaustive + scope | ranked + scope | nodes | task attempts: ranked without scope -> ranked + scope |")
	t.Logf("|---|---|---|---|---|---|")
	for _, v := range bcVariants {
		var cells []string
		var nodes []string
		var attempts int64
		for _, mode := range []protoMode{modeExhaustiveDet, modeRankedFit} {
			res, action := runRound2(t, buildBCScenario(v), mode, round2{scope: true}, nil, nil)
			placed := 0
			nodes = nodes[:0]
			for pod, node := range res.placed {
				if !strings.HasPrefix(pod, "myq-") {
					continue
				}
				placed++
				nodes = append(nodes, node)
				if !v.noPAA && strings.HasPrefix(node, "z0-") {
					t.Errorf("%s/%s: pod %s leaked into excluded domain A (%s)", v.name, mode.name, pod, node)
				}
			}
			sort.Strings(nodes)
			cells = append(cells, fmt.Sprintf("%d/%d", placed, v.pods))
			attempts = res.counters.prePredicate.Load()
			if v.feasible && placed != v.pods {
				t.Errorf("%s/%s: placed %d of %d although a legal placement exists; %s", v.name, mode.name, placed, v.pods, res.fitError["c1/pg-myq"])
			}
			if !v.feasible && placed != 0 {
				t.Errorf("%s/%s: placed %d pods although no legal placement exists", v.name, mode.name, placed)
			}
			if strings.HasPrefix(v.name, "HB1") && mode.rank {
				for _, scope := range action.trace.Scopes {
					t.Logf("    usable region of %s: %v", scope.Job, scope.Roots)
				}
				for _, record := range action.trace.Records {
					t.Logf("    %s", record)
				}
			}
		}
		without, _ := runRound2(t, buildBCScenario(v), modeRankedFit, round2{}, nil, nil)
		before := without.counters.prePredicate.Load()
		// A Job without a legal placement must not cost more dry-runs just
		// because a partly excluded container looks large enough.
		if !v.feasible && attempts > before {
			t.Errorf("%s: %d task attempts with the usable region, %d without, for a Job that has no legal placement", v.name, attempts, before)
		}
		t.Logf("| %s | %t | %s | %s | %s | %d -> %d |", v.name, v.feasible, cells[0], cells[1], strings.Join(nodes, " "), before, attempts)
	}
}

// R2: the contention example of issue 017 with failure-path repair.
func TestProto2R2ContentionRepair(t *testing.T) {
	for _, mode := range []protoMode{modeExhaustiveDet, modeRankedFit} {
		for _, budget := range []int{0, 4} {
			res, action := runRound2(t, contentionScenario(true), mode, round2{budget: budget}, nil, nil)
			t.Logf("%-20s repairBudget=%d placed %d/2 %v", mode.name, budget, res.count("job-"), res.placed)
			for _, repair := range action.trace.Repairs {
				t.Logf("    %s", repair)
			}
			want := 0
			if budget > 0 {
				want = 2
			}
			if got := res.count("job-"); got != want {
				t.Errorf("%s budget=%d: placed %d, want %d", mode.name, budget, got, want)
			}
		}
	}
}

// ---- single-job scenarios with an independent legality check --------------

type sgPolicy struct {
	name   string
	cpu    int
	size   int
	groups int
	tier   int
	pinned bool
}

func subGroupPolicySpec(p sgPolicy) schedulingv1.SubGroupPolicySpec {
	size, groups, tier := int32(p.size), int32(p.groups), p.tier
	return schedulingv1.SubGroupPolicySpec{
		Name:           p.name,
		LabelSelector:  &metav1.LabelSelector{MatchLabels: map[string]string{"role": p.name}},
		MatchLabelKeys: []string{"idx"},
		SubGroupSize:   &size,
		MinSubGroups:   &groups,
		NetworkTopology: &schedulingv1.NetworkTopologySpec{
			Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier,
		},
	}
}

// subGroupPods builds the pods of the given policies. order supplies the
// match index of each SubGroup, which decides the order SubJobs are handled in.
func subGroupPods(job string, policies []sgPolicy, order func() int) []*v1.Pod {
	var pods []*v1.Pod
	for _, p := range policies {
		for g := 0; g < p.groups; g++ {
			idx := order()
			for i := 0; i < p.size; i++ {
				var selector map[string]string
				if p.pinned {
					selector = map[string]string{"pool": "x"}
				}
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-%s-g%d-p%d", job, p.name, idx, i), "", v1.PodPending,
					api.BuildResourceList(fmt.Sprint(p.cpu), "1Gi"), job,
					map[string]string{"volcano.sh/task-spec": p.name, "role": p.name, "idx": fmt.Sprint(idx)}, selector))
			}
		}
	}
	return pods
}

type singleJobScenario struct {
	tc      *uthelper.TestCommonStruct
	summary string
	pods    int
}

func zoneOfNode(node string) string { return strings.SplitN(node, "-", 2)[0] }

func genSingleJobScenario(seed int64) *singleJobScenario {
	rng := rand.New(rand.NewSource(seed))
	zones, racks, perRack := 2+rng.Intn(2), 2+rng.Intn(2), 1+rng.Intn(2)
	topo := buildProbeTopo(zones, racks, perRack, "8", "64Gi", nil)
	pooled := map[string]bool{}
	for _, rack := range topo.racks {
		pooled[rack] = rng.Intn(5) < 2
	}
	for _, node := range topo.nodes {
		cpu := []int{8, 16}[rng.Intn(2)]
		alloc := api.BuildResourceList(fmt.Sprint(cpu), "64Gi", []api.ScalarResource{{Name: "pods", Value: "110"}}...)
		node.Status.Allocatable, node.Status.Capacity = alloc, alloc
		if pooled[rackOfNode(node.Name)] {
			node.Labels["pool"] = "x"
		}
	}

	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	for i, node := range topo.nodes {
		if rng.Intn(2) == 0 {
			continue
		}
		cpu := []int{2, 4, 6}[rng.Intn(3)]
		pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning, api.BuildResourceList(fmt.Sprint(cpu), "1Gi"),
			"filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
	}

	var summary []string
	hasAnchor := rng.Intn(2) == 0
	if hasAnchor {
		anchorZone := rng.Intn(zones)
		anchor := util.BuildPodGroup("anchor", "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
		anchor.Labels = map[string]string{groupLabel: groupValue}
		pgs = append(pgs, anchor)
		pods = append(pods, util.BuildPod("c1", "anchor-0", nodeName(anchorZone, rng.Intn(racks), 0), v1.PodRunning, api.BuildResourceList("1", "1Gi"),
			"anchor", map[string]string{"volcano.sh/task-spec": "anchor"}, nil))
		summary = append(summary, fmt.Sprintf("anchor in z%d", anchorZone))
	}

	target := util.BuildPodGroup("job", "c1", "q1", 0, nil, schedulingv1.PodGroupInqueue)
	target.Labels = map[string]string{groupLabel: groupValue}
	if hasAnchor && rng.Intn(5) < 3 {
		tierTwo := int32(2)
		target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
			Required: []schedulingv1.PodGroupAffinityTerm{{
				PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{groupLabel: groupValue}},
				TopologyTier:     &tierTwo,
			}},
		}}
		summary = append(summary, "required PAA tier 2")
	}
	jobTier := []int{0, 0, 2, 3}[rng.Intn(4)]
	if jobTier > 0 {
		tier := jobTier
		target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier}
		summary = append(summary, fmt.Sprintf("job hard tier %d", jobTier))
	}

	var jobPods []*v1.Pod
	if rng.Intn(4) < 3 {
		perm := rng.Perm(8)
		next := 0
		var policies []sgPolicy
		for i := 0; i < 2; i++ {
			p := sgPolicy{
				name: fmt.Sprintf("p%d", i), cpu: []int{2, 4, 6, 8}[rng.Intn(4)], size: 1 + rng.Intn(2), groups: 1 + rng.Intn(2),
				tier: []int{1, 1, 2}[rng.Intn(3)], pinned: rng.Intn(10) < 3,
			}
			policies = append(policies, p)
			target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy, subGroupPolicySpec(p))
			summary = append(summary, fmt.Sprintf("%s: %d groups x %d x %dcpu tier %d pinned=%t", p.name, p.groups, p.size, p.cpu, p.tier, p.pinned))
		}
		jobPods = subGroupPods("job", policies, func() int { next++; return perm[next-1] })
	} else {
		tasks, cpu := 2+rng.Intn(4), []int{2, 4, 8}[rng.Intn(3)]
		for i := 0; i < tasks; i++ {
			jobPods = append(jobPods, util.BuildPod("c1", fmt.Sprintf("job-plain-p%d", i), "", v1.PodPending, api.BuildResourceList(fmt.Sprint(cpu), "1Gi"),
				"job", map[string]string{"volcano.sh/task-spec": "worker"}, nil))
		}
		summary = append(summary, fmt.Sprintf("plain %d x %dcpu", tasks, cpu))
	}
	target.Spec.MinMember = int32(len(jobPods))
	pgs = append(pgs, target)
	pods = append(pods, jobPods...)

	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("single-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return &singleJobScenario{tc: tc, pods: len(jobPods),
		summary: fmt.Sprintf("topo %dx%dx%d; %s", zones, racks, perRack, strings.Join(summary, "; "))}
}

// illegalPlacement checks a placement of PodGroup "job" against the declared
// constraints without using scheduler code. It returns "" when legal.
func illegalPlacement(tc *uthelper.TestCommonStruct, placed map[string]string) string {
	return illegalPlacementJobs(tc, placed, "job")
}

// illegalPlacementJobs checks the placement of the named pending PodGroups:
// node capacity jointly, every other constraint per PodGroup.
func illegalPlacementJobs(tc *uthelper.TestCommonStruct, placed map[string]string, jobs ...string) string {
	isTarget := map[string]bool{}
	for _, job := range jobs {
		isTarget[job] = true
	}
	nodes := map[string]*v1.Node{}
	free := map[string]int64{}
	for _, node := range tc.Nodes {
		nodes[node.Name] = node
		free[node.Name] = node.Status.Allocatable.Cpu().MilliValue()
	}
	targets := map[string]*schedulingv1.PodGroup{}
	labelled := map[string]bool{}
	for _, pg := range tc.PodGroups {
		if isTarget[pg.Name] {
			targets[pg.Name] = pg
		} else if pg.Labels[groupLabel] == groupValue {
			labelled[pg.Name] = true
		}
	}
	excludedZones := map[string]bool{}
	podsByName := map[string]*v1.Pod{}
	for _, pod := range tc.Pods {
		podsByName[pod.Name] = pod
		group := pod.Annotations[schedulingv1.KubeGroupNameAnnotationKey]
		// The scheduler writes the chosen node back into the pod object, so
		// the pods of the target Jobs are always taken from `placed`.
		if pod.Spec.NodeName == "" || isTarget[group] {
			continue
		}
		free[pod.Spec.NodeName] -= pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
		if labelled[group] {
			excludedZones[zoneOfNode(pod.Spec.NodeName)] = true
		}
	}
	domain := func(node string, tier int) string {
		switch tier {
		case 1:
			return rackOfNode(node)
		case 2:
			return zoneOfNode(node)
		}
		return "dc"
	}
	groupDomain := map[string]string{}
	jobDomain := map[string]string{}
	for podName, node := range placed {
		pod := podsByName[podName]
		if pod == nil {
			continue
		}
		job := pod.Annotations[schedulingv1.KubeGroupNameAnnotationKey]
		target := targets[job]
		if target == nil {
			continue
		}
		if nodes[node] == nil {
			return fmt.Sprintf("%s on unknown node %s", podName, node)
		}
		free[node] -= pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
		for key, value := range pod.Spec.NodeSelector {
			if nodes[node].Labels[key] != value {
				return fmt.Sprintf("%s on %s violates nodeSelector %s=%s", podName, node, key, value)
			}
		}
		hasPAA := target.Spec.TopologyAffinity != nil && target.Spec.TopologyAffinity.PodGroupAntiAffinity != nil
		if hasPAA && excludedZones[zoneOfNode(node)] {
			return fmt.Sprintf("%s on %s violates required PodGroup anti-affinity", podName, node)
		}
		for _, policy := range target.Spec.SubGroupPolicy {
			if policy.Name != pod.Labels["role"] {
				continue
			}
			tier := *policy.NetworkTopology.HighestTierAllowed
			key := job + "/" + pod.Labels["role"] + "/" + pod.Labels["idx"]
			if seen, ok := groupDomain[key]; ok && seen != domain(node, tier) {
				return fmt.Sprintf("SubGroup %s spans %s and %s at tier %d", key, seen, domain(node, tier), tier)
			}
			groupDomain[key] = domain(node, tier)
		}
		if nt := target.Spec.NetworkTopology; nt != nil && nt.Mode == schedulingv1.HardNetworkTopologyMode {
			d := domain(node, *nt.HighestTierAllowed)
			if seen, ok := jobDomain[job]; ok && seen != d {
				return fmt.Sprintf("job %s spans %s and %s at tier %d", job, seen, d, *nt.HighestTierAllowed)
			}
			jobDomain[job] = d
		}
	}
	for node, left := range free {
		if left < 0 {
			return fmt.Sprintf("node %s over-committed by %dm CPU", node, -left)
		}
	}
	return ""
}

func jobPlacement(res *probeResult) map[string]string {
	placement := map[string]string{}
	for pod, node := range res.placed {
		if strings.HasPrefix(pod, "job-") {
			placement[pod] = node
		}
	}
	return placement
}

// R3: one pending Job per scenario. A Job the current search schedules must
// keep exactly its placement; the new mechanisms may only add Jobs, and every
// placement must be legal.
func TestProto2R3SingleJobMonotonic(t *testing.T) {
	const scenarios = 600
	type tally struct{ scheduled, lost, moved, gained, illegal int }
	configs := []struct {
		name string
		mode protoMode
		r    round2
	}{
		{"ranked (round 1)", modeRankedFit, round2{}},
		{"ranked + scope", modeRankedFit, round2{scope: true}},
		{"ranked + scope + repair(8)", modeRankedFit, round2{scope: true, budget: 8}},
		{"exhaustive + scope + repair(8)", modeExhaustiveDet, round2{scope: true, budget: 8}},
	}
	tallies := make([]tally, len(configs))
	baseScheduled := 0
	for seed := int64(1); seed <= scenarios; seed++ {
		scenario := genSingleJobScenario(seed)
		base, _ := runRound2(t, scenario.tc, modeExhaustiveDet, round2{}, nil, nil)
		basePlacement := jobPlacement(base)
		if msg := illegalPlacement(scenario.tc, basePlacement); msg != "" {
			t.Errorf("seed %d current search: illegal placement: %s (%s)", seed, msg, scenario.summary)
		}
		if len(basePlacement) > 0 {
			baseScheduled++
		}
		for i, cfg := range configs {
			scenario := genSingleJobScenario(seed)
			res, action := runRound2(t, scenario.tc, cfg.mode, cfg.r, nil, nil)
			placement := jobPlacement(res)
			if cfg.mode.rank {
				checkTraceInvariants(t, action.trace)
			}
			if msg := illegalPlacement(scenario.tc, placement); msg != "" {
				tallies[i].illegal++
				t.Errorf("seed %d %s: illegal placement: %s (%s) %v", seed, cfg.name, msg, scenario.summary, placement)
			}
			if len(placement) > 0 {
				tallies[i].scheduled++
			}
			switch {
			case len(basePlacement) > 0 && len(placement) == 0:
				tallies[i].lost++
				t.Errorf("seed %d %s: schedulable today, not scheduled now (%s)", seed, cfg.name, scenario.summary)
			case len(basePlacement) > 0 && !reflect.DeepEqual(basePlacement, placement):
				tallies[i].moved++
				if tallies[i].moved <= 2 {
					t.Logf("seed %d %s: placement differs from today (%s)\n  today: %v\n  now:   %v", seed, cfg.name, scenario.summary, basePlacement, placement)
				}
			case len(basePlacement) == 0 && len(placement) > 0:
				tallies[i].gained++
				if tallies[i].gained <= 2 && cfg.r.budget > 0 {
					t.Logf("seed %d %s: newly scheduled (%s) %v", seed, cfg.name, scenario.summary, placement)
					for _, repair := range action.trace.Repairs {
						t.Logf("    %s", repair)
					}
				}
			}
		}
	}
	t.Logf("%d single-job scenarios; current search (exhaustive + deterministic ties) schedules %d", scenarios, baseScheduled)
	t.Logf("| configuration | scheduled | newly scheduled | lost | placed differently | illegal |")
	t.Logf("|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %d | %d | %d |", cfg.name, tl.scheduled, tl.gained, tl.lost, tl.moved, tl.illegal)
		if tl.moved > 0 {
			t.Errorf("%s: %d jobs schedulable today were placed differently", cfg.name, tl.moved)
		}
	}
}
