/*
Round-2 prototype probes for issue 004 (upstream prototype only): how far the
search is from a brute-force oracle, in schedulability and in tier.
Needs probes/common, probes/history, probes/prototype, probes/prototype2.
*/

package allocate

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// ---- brute-force oracle; uses no scheduler code ---------------------------

type oracleNode struct {
	name, rack, zone string
	free             int64
	pooled           bool
}

type oraclePod struct {
	cpu    int64
	pinned bool
	group  string // "" = no SubGroup
	tier   int    // SubGroup co-location tier
}

type oracleProblem struct {
	nodes   []oracleNode
	pods    []oraclePod
	jobTier int // 0 = no Job-level hard topology
}

func extractOracleProblem(tc *uthelper.TestCommonStruct) *oracleProblem {
	var target *schedulingv1.PodGroup
	labelled := map[string]bool{}
	for _, pg := range tc.PodGroups {
		if pg.Name == "job" {
			target = pg
		} else if pg.Labels[groupLabel] == groupValue {
			labelled[pg.Name] = true
		}
	}
	hasPAA := target.Spec.TopologyAffinity != nil && target.Spec.TopologyAffinity.PodGroupAntiAffinity != nil
	used := map[string]int64{}
	excluded := map[string]bool{}
	for _, pod := range tc.Pods {
		group := pod.Annotations[schedulingv1.KubeGroupNameAnnotationKey]
		if group == "job" || pod.Spec.NodeName == "" {
			continue
		}
		used[pod.Spec.NodeName] += pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
		if labelled[group] {
			excluded[zoneOfNode(pod.Spec.NodeName)] = true
		}
	}
	problem := &oracleProblem{}
	for _, node := range tc.Nodes {
		if hasPAA && excluded[zoneOfNode(node.Name)] {
			continue
		}
		problem.nodes = append(problem.nodes, oracleNode{
			name: node.Name, rack: rackOfNode(node.Name), zone: zoneOfNode(node.Name),
			free: node.Status.Allocatable.Cpu().MilliValue() - used[node.Name], pooled: node.Labels["pool"] == "x",
		})
	}
	sort.Slice(problem.nodes, func(i, j int) bool { return problem.nodes[i].name < problem.nodes[j].name })
	policyTier := map[string]int{}
	for _, policy := range target.Spec.SubGroupPolicy {
		policyTier[policy.Name] = *policy.NetworkTopology.HighestTierAllowed
	}
	for _, pod := range tc.Pods {
		if pod.Annotations[schedulingv1.KubeGroupNameAnnotationKey] != "job" {
			continue
		}
		p := oraclePod{cpu: pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue(), pinned: pod.Spec.NodeSelector["pool"] == "x"}
		if tier, found := policyTier[pod.Labels["role"]]; found {
			p.group, p.tier = pod.Labels["role"]+"/"+pod.Labels["idx"], tier
		}
		problem.pods = append(problem.pods, p)
	}
	sort.SliceStable(problem.pods, func(i, j int) bool {
		a, b := problem.pods[i], problem.pods[j]
		if a.group != b.group {
			return a.group > b.group
		}
		return a.cpu > b.cpu
	})
	if nt := target.Spec.NetworkTopology; nt != nil && nt.Mode == schedulingv1.HardNetworkTopologyMode {
		problem.jobTier = *nt.HighestTierAllowed
	}
	return problem
}

func domainOf(node oracleNode, tier int) string {
	switch tier {
	case 1:
		return node.rack
	case 2:
		return node.zone
	}
	return "dc"
}

// fits reports whether all pods can be placed on the given nodes.
func (p *oracleProblem) fits(nodes []oracleNode) bool {
	free := make([]int64, len(nodes))
	for i, node := range nodes {
		free[i] = node.free
	}
	groupDomain := map[string]string{}
	dead := map[string]bool{}
	var place func(i int) bool
	place = func(i int) bool {
		if i == len(p.pods) {
			return true
		}
		domains := make([]string, 0, len(groupDomain))
		for group, domain := range groupDomain {
			domains = append(domains, group+"="+domain)
		}
		sort.Strings(domains)
		key := fmt.Sprint(i, free, domains)
		if dead[key] {
			return false
		}
		pod := p.pods[i]
		tried := map[string]bool{}
		for n, node := range nodes {
			if free[n] < pod.cpu || (pod.pinned && !node.pooled) {
				continue
			}
			pinnedDomain, pinnedGroup := "", false
			if pod.group != "" {
				pinnedDomain, pinnedGroup = groupDomain[pod.group]
				if pinnedGroup && pinnedDomain != domainOf(node, pod.tier) {
					continue
				}
			}
			signature := fmt.Sprint(free[n], node.rack, node.pooled)
			if tried[signature] {
				continue
			}
			tried[signature] = true
			free[n] -= pod.cpu
			if pod.group != "" && !pinnedGroup {
				groupDomain[pod.group] = domainOf(node, pod.tier)
			}
			ok := place(i + 1)
			free[n] += pod.cpu
			if pod.group != "" && !pinnedGroup {
				delete(groupDomain, pod.group)
			}
			if ok {
				return true
			}
		}
		dead[key] = true
		return false
	}
	return place(0)
}

// minTier returns the lowest tier of a common ancestor in which a legal
// placement exists (1 rack, 2 zone, 3 whole cluster), or 0 when there is none.
func (p *oracleProblem) minTier() int {
	highest := 3
	if p.jobTier > 0 {
		highest = p.jobTier
	}
	for tier := 1; tier <= highest; tier++ {
		byDomain := map[string][]oracleNode{}
		for _, node := range p.nodes {
			byDomain[domainOf(node, tier)] = append(byDomain[domainOf(node, tier)], node)
		}
		names := make([]string, 0, len(byDomain))
		for name := range byDomain {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if p.fits(byDomain[name]) {
				return tier
			}
		}
	}
	return 0
}

func placementTier(placement map[string]string) int {
	racks, zones := map[string]bool{}, map[string]bool{}
	for _, node := range placement {
		racks[rackOfNode(node)] = true
		zones[zoneOfNode(node)] = true
	}
	switch {
	case len(racks) <= 1:
		return 1
	case len(zones) <= 1:
		return 2
	}
	return 3
}

// mixSizes gives the tasks of a Job without SubGroups different sizes, so that
// the order of tasks inside one unit matters.
func mixSizes(scenario *singleJobScenario, seed int64) bool {
	rng := rand.New(rand.NewSource(seed * 7919))
	if rng.Intn(2) == 0 {
		return false
	}
	mixed := false
	for _, pod := range scenario.tc.Pods {
		if !strings.HasPrefix(pod.Name, "job-plain-") {
			continue
		}
		cpu := resource.MustParse(fmt.Sprint([]int{2, 4, 6, 8}[rng.Intn(4)]))
		pod.Spec.Containers[0].Resources.Requests[v1.ResourceCPU] = cpu
		mixed = true
	}
	return mixed
}

type oracleConfig struct {
	name     string
	mode     protoMode
	r        round2
	perLayer bool
	order    string
}

// R4: distance to the oracle. "missed" = a legal placement exists but the Job
// stays pending; "wider" = scheduled in a wider tier than necessary (Jobs with
// Job-level hard topology only).
func TestProto2R4OracleGap(t *testing.T) {
	const scenarios = 1200
	configs := []oracleConfig{
		{name: "current search", mode: modeExhaustiveDet},
		{name: "ranked (round 1)", mode: modeRankedFit},
		{name: "ranked + region", mode: modeRankedFit, r: round2{scope: true}},
		{name: "ranked + region + repair(1)", mode: modeRankedFit, r: round2{scope: true, budget: 1}},
		{name: "ranked + region + repair(1, widest first)", mode: modeRankedFit, r: round2{scope: true, budget: 1}, order: "high"},
		{name: "ranked + region + repair(2)", mode: modeRankedFit, r: round2{scope: true, budget: 2}},
		{name: "ranked + region + repair(2, reserve)", mode: modeRankedFit, r: round2{scope: true, budget: 2}, order: "reserve"},
		{name: "ranked + region + repair(4)", mode: modeRankedFit, r: round2{scope: true, budget: 4}},
		{name: "ranked + region + repair(4, reserve)", mode: modeRankedFit, r: round2{scope: true, budget: 4}, order: "reserve"},
		{name: "ranked + region + repair(8)", mode: modeRankedFit, r: round2{scope: true, budget: 8}},
		{name: "ranked + region + repair(8, reserve)", mode: modeRankedFit, r: round2{scope: true, budget: 8}, order: "reserve"},
		{name: "ranked + region + repair(32)", mode: modeRankedFit, r: round2{scope: true, budget: 32}},
		{name: "ranked + region + repair(8, per layer)", mode: modeRankedFit, r: round2{scope: true, budget: 8}, perLayer: true},
	}
	type tally struct {
		scheduled, missed, missedMixed, wider, lostVsCurrent, movedVsCurrent int
		attemptsWhenCurrentSchedules, attemptsWhenCurrentFails               int64
		repairs, exhausted                                                   int
	}
	tallies := make([]tally, len(configs))
	feasible, hardFeasible := 0, 0
	var missedExamples []string
	for seed := int64(1); seed <= scenarios; seed++ {
		reference := genSingleJobScenario(seed)
		mixed := mixSizes(reference, seed)
		problem := extractOracleProblem(reference.tc)
		best := problem.minTier()
		if best > 0 {
			feasible++
			if problem.jobTier > 0 {
				hardFeasible++
			}
		}
		var current map[string]string
		for i, cfg := range configs {
			scenario := genSingleJobScenario(seed)
			mixSizes(scenario, seed)
			res, action := runRound2(t, scenario.tc, cfg.mode, cfg.r, func(a *Action) {
				a.repairPerLayer, a.repairOrder = cfg.perLayer, cfg.order
			}, nil)
			placement := jobPlacement(res)
			if msg := illegalPlacement(scenario.tc, placement); msg != "" {
				t.Errorf("seed %d %s: illegal placement: %s (%s)", seed, cfg.name, msg, scenario.summary)
			}
			if i == 0 {
				current = placement
			}
			tl := &tallies[i]
			tl.repairs += len(action.trace.Repairs)
			tl.exhausted += len(action.trace.RepairBudgetExhausted)
			if len(current) > 0 {
				tl.attemptsWhenCurrentSchedules += res.counters.prePredicate.Load()
			} else {
				tl.attemptsWhenCurrentFails += res.counters.prePredicate.Load()
			}
			switch {
			case len(placement) > 0 && best == 0:
				t.Errorf("seed %d %s: scheduled although the oracle finds no legal placement (%s) %v", seed, cfg.name, scenario.summary, placement)
			case len(placement) > 0:
				tl.scheduled++
				if problem.jobTier > 0 && placementTier(placement) > best {
					tl.wider++
				}
			case best > 0:
				tl.missed++
				if mixed {
					tl.missedMixed++
				}
				if cfg.r.budget == 32 && len(missedExamples) < 4 {
					missedExamples = append(missedExamples, fmt.Sprintf("seed %d (%s; mixed sizes=%t): %s", seed, scenario.summary, mixed, shortFitError(res.fitError["c1/job"])))
				}
			}
			if len(current) > 0 && len(placement) == 0 {
				tl.lostVsCurrent++
				t.Errorf("seed %d %s: schedulable today, not scheduled now (%s)", seed, cfg.name, scenario.summary)
			}
			if len(current) > 0 && len(placement) > 0 && fmt.Sprint(current) != fmt.Sprint(placement) {
				tl.movedVsCurrent++
			}
		}
	}
	t.Logf("%d single-job scenarios; oracle: %d have a legal placement (%d of them with Job-level hard topology)", scenarios, feasible, hardFeasible)
	t.Logf("| configuration | scheduled | missed (of them: mixed task sizes) | wider tier than needed | lost vs current | placed differently vs current | repair attempts | budget exhausted | task attempts, current schedules | task attempts, current fails |")
	t.Logf("|---|---|---|---|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d (%d) | %d | %d | %d | %d | %d | %d | %d |", cfg.name, tl.scheduled, tl.missed, tl.missedMixed, tl.wider,
			tl.lostVsCurrent, tl.movedVsCurrent, tl.repairs, tl.exhausted, tl.attemptsWhenCurrentSchedules, tl.attemptsWhenCurrentFails)
	}
	for _, example := range missedExamples {
		t.Logf("still missed with budget 32: %s", example)
	}
}

// genContentionScenario builds tight scenarios with three SubGroup policies
// that compete for the same racks: two zones, three to five racks each, most
// nodes partly used.
func genContentionScenario(seed int64) *singleJobScenario {
	rng := rand.New(rand.NewSource(seed*104729 + 17))
	zones, racks, perRack := 2, 3+rng.Intn(3), 1+rng.Intn(2)
	topo := buildProbeTopo(zones, racks, perRack, "8", "64Gi", nil)
	pooled := map[string]bool{}
	for _, rack := range topo.racks {
		pooled[rack] = rng.Intn(5) < 2
	}
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	for i, node := range topo.nodes {
		if pooled[rackOfNode(node.Name)] {
			node.Labels["pool"] = "x"
		}
		if rng.Intn(10) < 7 {
			cpu := []int{2, 4, 6}[rng.Intn(3)]
			pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning, api.BuildResourceList(fmt.Sprint(cpu), "1Gi"),
				"filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
		}
	}
	target := util.BuildPodGroup("job", "c1", "q1", 0, nil, schedulingv1.PodGroupInqueue)
	var summary []string
	if jobTier := []int{0, 0, 2, 3}[rng.Intn(4)]; jobTier > 0 {
		tier := jobTier
		target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier}
		summary = append(summary, fmt.Sprintf("job hard tier %d", jobTier))
	}
	perm := rng.Perm(12)
	next := 0
	var policies []sgPolicy
	for i := 0; i < 3; i++ {
		p := sgPolicy{
			name: fmt.Sprintf("p%d", i), cpu: []int{2, 4, 6, 8}[rng.Intn(4)], size: 1 + rng.Intn(2), groups: 1 + rng.Intn(2),
			tier: []int{1, 1, 2}[rng.Intn(3)], pinned: rng.Intn(10) < 3,
		}
		policies = append(policies, p)
		target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy, subGroupPolicySpec(p))
		summary = append(summary, fmt.Sprintf("%s: %d groups x %d x %dcpu tier %d pinned=%t", p.name, p.groups, p.size, p.cpu, p.tier, p.pinned))
	}
	jobPods := subGroupPods("job", policies, func() int { next++; return perm[next-1] })
	target.Spec.MinMember = int32(len(jobPods))
	pgs = append(pgs, target)
	pods = append(pods, jobPods...)
	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("contention-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return &singleJobScenario{tc: tc, pods: len(jobPods),
		summary: fmt.Sprintf("topo %dx%dx%d; %s", zones, racks, perRack, strings.Join(summary, "; "))}
}

// R5: the same comparison on scenarios built to make SubGroups compete.
func TestProto2R5ContentionOracleGap(t *testing.T) {
	const scenarios = 1000
	configs := []oracleConfig{
		{name: "current search", mode: modeExhaustiveDet},
		{name: "ranked (round 1)", mode: modeRankedFit},
		{name: "ranked + repair(1)", mode: modeRankedFit, r: round2{budget: 1}},
		{name: "ranked + repair(1, widest first)", mode: modeRankedFit, r: round2{budget: 1}, order: "high"},
		{name: "ranked + repair(2)", mode: modeRankedFit, r: round2{budget: 2}},
		{name: "ranked + repair(2, reserve)", mode: modeRankedFit, r: round2{budget: 2}, order: "reserve"},
		{name: "ranked + repair(4, reserve)", mode: modeRankedFit, r: round2{budget: 4}, order: "reserve"},
		{name: "ranked + repair(8, reserve)", mode: modeRankedFit, r: round2{budget: 8}, order: "reserve"},
		{name: "ranked + region", mode: modeRankedFit, r: round2{scope: true}},
		{name: "ranked + region + repair(4, reserve)", mode: modeRankedFit, r: round2{scope: true, budget: 4}, order: "reserve"},
		{name: "ranked + region + repair(8, reserve)", mode: modeRankedFit, r: round2{scope: true, budget: 8}, order: "reserve"},
		{name: "ranked + repair(64)", mode: modeRankedFit, r: round2{budget: 64}},
		{name: "ranked + repair(8, per layer)", mode: modeRankedFit, r: round2{budget: 8}, perLayer: true},
	}
	type tally struct {
		scheduled, missed, wider, lost, moved, repairs, exhausted int
		attemptsOK, attemptsFail                                  int64
	}
	tallies := make([]tally, len(configs))
	feasible := 0
	var examples []string
	for seed := int64(1); seed <= scenarios; seed++ {
		reference := genContentionScenario(seed)
		problem := extractOracleProblem(reference.tc)
		best := problem.minTier()
		if best > 0 {
			feasible++
		}
		var current map[string]string
		for i, cfg := range configs {
			scenario := genContentionScenario(seed)
			res, action := runRound2(t, scenario.tc, cfg.mode, cfg.r, func(a *Action) {
				a.repairPerLayer, a.repairOrder = cfg.perLayer, cfg.order
			}, nil)
			placement := jobPlacement(res)
			if msg := illegalPlacement(scenario.tc, placement); msg != "" {
				t.Errorf("seed %d %s: illegal placement: %s (%s)", seed, cfg.name, msg, scenario.summary)
			}
			if i == 0 {
				current = placement
			}
			tl := &tallies[i]
			tl.repairs += len(action.trace.Repairs)
			tl.exhausted += len(action.trace.RepairBudgetExhausted)
			if len(current) > 0 {
				tl.attemptsOK += res.counters.prePredicate.Load()
			} else {
				tl.attemptsFail += res.counters.prePredicate.Load()
			}
			switch {
			case len(placement) > 0 && best == 0:
				t.Errorf("seed %d %s: scheduled although the oracle finds no legal placement (%s) %v", seed, cfg.name, scenario.summary, placement)
			case len(placement) > 0:
				tl.scheduled++
				if problem.jobTier > 0 && placementTier(placement) > best {
					tl.wider++
				}
			case best > 0:
				tl.missed++
				if cfg.r.budget == 64 && len(examples) < 3 {
					examples = append(examples, fmt.Sprintf("seed %d (%s)", seed, scenario.summary))
					for _, repair := range action.trace.Repairs {
						examples = append(examples, "    "+repair.String())
					}
				}
			}
			if len(current) > 0 && len(placement) == 0 {
				tl.lost++
				t.Errorf("seed %d %s: schedulable today, not scheduled now (%s)", seed, cfg.name, scenario.summary)
			}
			if len(current) > 0 && len(placement) > 0 && fmt.Sprint(current) != fmt.Sprint(placement) {
				tl.moved++
			}
		}
	}
	t.Logf("%d contention scenarios; oracle: %d have a legal placement", scenarios, feasible)
	t.Logf("| configuration | scheduled | missed | wider tier than needed | lost vs current | placed differently vs current | repair attempts | budget exhausted | task attempts, current schedules | task attempts, current fails |")
	t.Logf("|---|---|---|---|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d |", cfg.name, tl.scheduled, tl.missed, tl.wider, tl.lost, tl.moved, tl.repairs, tl.exhausted, tl.attemptsOK, tl.attemptsFail)
	}
	for _, example := range examples {
		t.Logf("still missed with budget 64: %s", example)
	}
}

// genMultiJobScenario puts two or three pending Jobs of mixed kinds into one
// session. Jobs with required PodGroup anti-affinity avoid the running anchor
// PodGroup; the pending Jobs carry no group label and do not exclude each other.
func genMultiJobScenario(seed int64) (*singleJobScenario, []string) {
	rng := rand.New(rand.NewSource(seed*15485863 + 5))
	zones, racks, perRack := 2+rng.Intn(2), 2+rng.Intn(2), 1+rng.Intn(2)
	topo := buildProbeTopo(zones, racks, perRack, "8", "64Gi", nil)
	pooled := map[string]bool{}
	for _, rack := range topo.racks {
		pooled[rack] = rng.Intn(5) < 2
	}
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	for i, node := range topo.nodes {
		cpu := []int{8, 16}[rng.Intn(2)]
		alloc := api.BuildResourceList(fmt.Sprint(cpu), "64Gi", []api.ScalarResource{{Name: "pods", Value: "110"}}...)
		node.Status.Allocatable, node.Status.Capacity = alloc, alloc
		if pooled[rackOfNode(node.Name)] {
			node.Labels["pool"] = "x"
		}
		if rng.Intn(2) == 0 {
			used := []int{2, 4, 6}[rng.Intn(3)]
			pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning, api.BuildResourceList(fmt.Sprint(used), "1Gi"),
				"filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
		}
	}
	anchorZone := rng.Intn(zones)
	anchor := util.BuildPodGroup("anchor", "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
	anchor.Labels = map[string]string{groupLabel: groupValue}
	pgs = append(pgs, anchor)
	pods = append(pods, util.BuildPod("c1", "anchor-0", nodeName(anchorZone, rng.Intn(racks), 0), v1.PodRunning, api.BuildResourceList("1", "1Gi"),
		"anchor", map[string]string{"volcano.sh/task-spec": "anchor"}, nil))

	var names, summary []string
	total := 0
	for j := 0; j < 2+rng.Intn(2); j++ {
		name := fmt.Sprintf("j%c", 'a'+j)
		names = append(names, name)
		target := util.BuildPodGroup(name, "c1", "q1", 0, nil, schedulingv1.PodGroupInqueue)
		var parts []string
		if rng.Intn(2) == 0 {
			tierTwo := int32(2)
			target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
				Required: []schedulingv1.PodGroupAffinityTerm{{
					PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{groupLabel: groupValue}},
					TopologyTier:     &tierTwo,
				}},
			}}
			parts = append(parts, "PAA")
		}
		if jobTier := []int{0, 0, 2, 3}[rng.Intn(4)]; jobTier > 0 {
			tier := jobTier
			target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier}
			parts = append(parts, fmt.Sprintf("hard tier %d", jobTier))
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
				parts = append(parts, fmt.Sprintf("%s: %dx%dx%dcpu tier %d pinned=%t", p.name, p.groups, p.size, p.cpu, p.tier, p.pinned))
			}
			jobPods = subGroupPods(name, policies, func() int { next++; return perm[next-1] })
		} else {
			tasks, cpu := 2+rng.Intn(4), []int{2, 4, 8}[rng.Intn(3)]
			for i := 0; i < tasks; i++ {
				jobPods = append(jobPods, util.BuildPod("c1", fmt.Sprintf("%s-plain-p%d", name, i), "", v1.PodPending, api.BuildResourceList(fmt.Sprint(cpu), "1Gi"),
					name, map[string]string{"volcano.sh/task-spec": "worker"}, nil))
			}
			parts = append(parts, fmt.Sprintf("plain %d x %dcpu", tasks, cpu))
		}
		target.Spec.MinMember = int32(len(jobPods))
		pgs = append(pgs, target)
		pods = append(pods, jobPods...)
		total += len(jobPods)
		summary = append(summary, name+": "+strings.Join(parts, ", "))
	}
	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("multi-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return &singleJobScenario{tc: tc, pods: total,
		summary: fmt.Sprintf("topo %dx%dx%d; anchor in z%d; %s", zones, racks, perRack, anchorZone, strings.Join(summary, " | "))}, names
}

// R6: several pending Jobs in one session. Scheduling one more Job uses
// resources, so a later Job of the same session can lose its place. The probe
// counts how often that happens and what the session gains in total.
func TestProto2R6MultiJobSession(t *testing.T) {
	const scenarios = 600
	configs := []oracleConfig{
		{name: "current search", mode: modeExhaustiveDet},
		{name: "ranked (round 1)", mode: modeRankedFit},
		{name: "ranked + region", mode: modeRankedFit, r: round2{scope: true}},
		{name: "ranked + region + repair(4, reserve)", mode: modeRankedFit, r: round2{scope: true, budget: 4}, order: "reserve"},
	}
	type tally struct{ jobs, pods, sessionsMore, sessionsFewer, sessionsSwapped, jobsDisplaced, jobsGained int }
	tallies := make([]tally, len(configs))
	totalJobs := 0
	var examples []string
	for seed := int64(1); seed <= scenarios; seed++ {
		var current map[string]bool
		for i, cfg := range configs {
			scenario, names := genMultiJobScenario(seed)
			if i == 0 {
				totalJobs += len(names)
			}
			res, _ := runRound2(t, scenario.tc, cfg.mode, cfg.r, func(a *Action) { a.repairOrder = cfg.order }, nil)
			if msg := illegalPlacementJobs(scenario.tc, res.placed, names...); msg != "" {
				t.Errorf("seed %d %s: illegal placement: %s (%s)", seed, cfg.name, msg, scenario.summary)
			}
			scheduled := map[string]bool{}
			for pod := range res.placed {
				for _, name := range names {
					if strings.HasPrefix(pod, name+"-") {
						scheduled[name] = true
						tallies[i].pods++
					}
				}
			}
			tallies[i].jobs += len(scheduled)
			if i == 0 {
				current = scheduled
				continue
			}
			displaced, gained := 0, 0
			for _, name := range names {
				switch {
				case current[name] && !scheduled[name]:
					displaced++
				case !current[name] && scheduled[name]:
					gained++
				}
			}
			tallies[i].jobsDisplaced += displaced
			tallies[i].jobsGained += gained
			switch {
			case gained > displaced:
				tallies[i].sessionsMore++
			case gained < displaced:
				tallies[i].sessionsFewer++
			case displaced > 0:
				tallies[i].sessionsSwapped++
			}
			if displaced > 0 && len(examples) < 3 {
				examples = append(examples, fmt.Sprintf("seed %d %s: displaced %d, gained %d (%s)", seed, cfg.name, displaced, gained, scenario.summary))
			}
		}
	}
	t.Logf("%d sessions with %d pending Jobs in total", scenarios, totalJobs)
	t.Logf("| configuration | Jobs scheduled | pods scheduled | Jobs gained vs current | Jobs displaced vs current | sessions with more Jobs | sessions with fewer Jobs | sessions with a swap |")
	t.Logf("|---|---|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %d | %d | %d | %d | %d |", cfg.name, tl.jobs, tl.pods, tl.jobsGained, tl.jobsDisplaced, tl.sessionsMore, tl.sessionsFewer, tl.sessionsSwapped)
	}
	for _, example := range examples {
		t.Logf("displacement example: %s", example)
	}
}
