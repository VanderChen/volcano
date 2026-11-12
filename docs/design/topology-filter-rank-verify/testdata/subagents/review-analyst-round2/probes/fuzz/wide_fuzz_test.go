/*
Round-2 proposal-analyst fuzz for issue 004 (review only). It widens the
scenario space of probes/prototype2 (R3) with constraint combinations that the
proposal's generators never produce:

  - topologies without a real common root (zones are top-level HyperNodes);
  - one or two required PodGroup anti-affinity terms at tier 1, 2 or 3, with
    one or two anchor PodGroups and different peer selectors;
  - Job network topology hard tier 1/2/3 and soft;
  - one to three SubGroup policies, up to three SubGroups each;
  - elastic gangs (minMember below the number of pods, MinSubGroups below the
    number of SubGroups);
  - a second phase in which the Job placed by the current search is running
    and is scaled up (AllocatedHyperNode already set).

The legality check below is written from the declared constraints and shares
no code with the scheduler or with the proposal's checker.
*/

package allocate

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	topologyv1alpha1 "volcano.sh/apis/pkg/apis/topology/v1alpha1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

type wPod struct {
	name   string
	cpu    int
	pinned bool
	policy string // "" = no SubGroup
	idx    string
	node   string // non-empty: running before the session
	group  string
}

type wPolicy struct {
	name                               string
	cpu, size, groups, minGroups, tier int
	pinned                             bool
}

type wTerm struct {
	tier int
	team bool // true: selects only anchor PodGroups labelled team=x
}

type wModel struct {
	zones, racks, perRack int
	hasRoot               bool
	nodeCPU               map[string]int
	pooledRack            map[string]bool
	fillers, anchors      []wPod
	anchorTeam            map[string]bool
	terms                 []wTerm
	jobMode               string
	jobTier               int
	policies              []wPolicy
	target                []wPod
	minMember             int
	running               bool
}

func (m *wModel) elastic() bool {
	if m.minMember < len(m.target) {
		return true
	}
	for _, p := range m.policies {
		if p.minGroups < p.groups {
			return true
		}
	}
	return false
}

func (m *wModel) summary() string {
	parts := []string{fmt.Sprintf("topo %dx%dx%d root=%t", m.zones, m.racks, m.perRack, m.hasRoot)}
	for _, a := range m.anchors {
		parts = append(parts, fmt.Sprintf("%s(team=%t)@%s", a.name, m.anchorTeam[a.group], a.node))
	}
	for _, term := range m.terms {
		parts = append(parts, fmt.Sprintf("PAA tier %d team=%t", term.tier, term.team))
	}
	if m.jobMode != "" {
		parts = append(parts, fmt.Sprintf("job %s tier %d", m.jobMode, m.jobTier))
	}
	for _, p := range m.policies {
		parts = append(parts, fmt.Sprintf("%s: %d(min %d) groups x %d x %dcpu tier %d pinned=%t", p.name, p.groups, p.minGroups, p.size, p.cpu, p.tier, p.pinned))
	}
	if len(m.policies) == 0 {
		parts = append(parts, fmt.Sprintf("plain %d x %dcpu", len(m.target), m.target[0].cpu))
	}
	running := 0
	for _, p := range m.target {
		if p.node != "" {
			running++
		}
	}
	parts = append(parts, fmt.Sprintf("minMember %d of %d pods (%d running)", m.minMember, len(m.target), running))
	return strings.Join(parts, "; ")
}

func genWideModel(seed int64) *wModel {
	rng := rand.New(rand.NewSource(seed*2654435761 + 97))
	m := &wModel{zones: 2 + rng.Intn(2), racks: 2 + rng.Intn(2), perRack: 1 + rng.Intn(2), hasRoot: rng.Intn(3) != 0,
		nodeCPU: map[string]int{}, pooledRack: map[string]bool{}, anchorTeam: map[string]bool{}}
	var nodes []string
	used := map[string]int{}
	for z := 0; z < m.zones; z++ {
		for r := 0; r < m.racks; r++ {
			m.pooledRack[rackName(z, r)] = rng.Intn(5) < 2
			for n := 0; n < m.perRack; n++ {
				name := nodeName(z, r, n)
				nodes = append(nodes, name)
				m.nodeCPU[name] = []int{8, 16}[rng.Intn(2)]
			}
		}
	}
	for i, node := range nodes {
		if rng.Intn(2) == 0 {
			cpu := []int{2, 4, 6}[rng.Intn(3)]
			m.fillers = append(m.fillers, wPod{name: fmt.Sprintf("filler-%03d", i), cpu: cpu, node: node, group: "filler"})
			used[node] += cpu
		}
	}
	nAnchors := []int{0, 0, 0, 1, 1, 1, 1, 1, 2, 2}[rng.Intn(10)]
	for a := 0; a < nAnchors; a++ {
		pg := fmt.Sprintf("anchor%d", a)
		m.anchorTeam[pg] = a == 1 || rng.Intn(3) == 0
		for p := 0; p < 1+rng.Intn(2); p++ {
			for try := 0; try < 20; try++ {
				node := nodes[rng.Intn(len(nodes))]
				if used[node]+1 <= m.nodeCPU[node] {
					m.anchors = append(m.anchors, wPod{name: fmt.Sprintf("%s-%d", pg, p), cpu: 1, node: node, group: pg})
					used[node]++
					break
				}
			}
		}
	}
	if len(m.anchors) > 0 && rng.Intn(10) < 7 {
		tiers := []int{1, 2, 2, 2}
		if m.hasRoot {
			tiers = []int{1, 2, 2, 2, 3}
		}
		m.terms = append(m.terms, wTerm{tier: tiers[rng.Intn(len(tiers))]})
		if rng.Intn(10) < 3 {
			m.terms = append(m.terms, wTerm{tier: tiers[rng.Intn(len(tiers))], team: true})
		}
	}
	switch k := rng.Intn(20); {
	case k < 8:
	case k < 10:
		m.jobMode, m.jobTier = "hard", 1
	case k < 14:
		m.jobMode, m.jobTier = "hard", 2
	case k < 17:
		m.jobMode, m.jobTier = "hard", 2
		if m.hasRoot {
			m.jobTier = 3
		}
	default:
		m.jobMode, m.jobTier = "soft", 2
	}
	if rng.Intn(4) < 3 {
		perm := rng.Perm(16)
		next, total := 0, 0
		for i, n := 0, 1+rng.Intn(3); i < n; i++ {
			p := wPolicy{name: fmt.Sprintf("p%d", i), cpu: []int{2, 4, 6, 8}[rng.Intn(4)], size: 1 + rng.Intn(2), groups: 1 + rng.Intn(3),
				tier: []int{1, 1, 2}[rng.Intn(3)], pinned: rng.Intn(10) < 3}
			if total+p.size*p.groups > 12 {
				p.groups = 1
			}
			p.minGroups = p.groups
			if p.groups >= 2 && rng.Intn(10) < 3 {
				p.minGroups = p.groups - 1
			}
			m.policies = append(m.policies, p)
			for g := 0; g < p.groups; g++ {
				idx := perm[next]
				next++
				for k := 0; k < p.size; k++ {
					m.target = append(m.target, wPod{name: fmt.Sprintf("job-%s-g%d-p%d", p.name, idx, k), cpu: p.cpu, pinned: p.pinned,
						policy: p.name, idx: fmt.Sprint(idx), group: "job"})
				}
			}
			m.minMember += p.minGroups * p.size
			total += p.size * p.groups
		}
	} else {
		tasks, cpu := 2+rng.Intn(4), []int{2, 4, 8}[rng.Intn(3)]
		for i := 0; i < tasks; i++ {
			m.target = append(m.target, wPod{name: fmt.Sprintf("job-plain-p%d", i), cpu: cpu, group: "job"})
		}
		m.minMember = tasks
		if rng.Intn(10) < 3 {
			m.minMember = tasks - 1
		}
	}
	return m
}

// scaleUp returns the next-cycle scenario: the pods placed by `placed` are
// running and the Job gets additional pending pods.
func (m *wModel) scaleUp(placed map[string]string, seed int64) *wModel {
	rng := rand.New(rand.NewSource(seed*40503 + 11))
	next := *m
	next.running = true
	next.target = append([]wPod(nil), m.target...)
	next.policies = append([]wPolicy(nil), m.policies...)
	for i := range next.target {
		if node, ok := placed[next.target[i].name]; ok {
			next.target[i].node = node
		}
	}
	if len(next.policies) > 0 {
		pi := rng.Intn(len(next.policies))
		p := next.policies[pi]
		idx := 20 + rng.Intn(20)
		for k := 0; k < p.size; k++ {
			next.target = append(next.target, wPod{name: fmt.Sprintf("job-%s-g%d-p%d", p.name, idx, k), cpu: p.cpu, pinned: p.pinned,
				policy: p.name, idx: fmt.Sprint(idx), group: "job"})
		}
		next.policies[pi].groups++
	} else {
		for i, extra := 0, 1+rng.Intn(2); i < extra; i++ {
			next.target = append(next.target, wPod{name: fmt.Sprintf("job-plain-x%d", i), cpu: m.target[0].cpu, group: "job"})
		}
	}
	return &next
}

func (m *wModel) build() *uthelper.TestCommonStruct {
	topo := buildProbeTopo(m.zones, m.racks, m.perRack, "8", "64Gi", nil)
	if !m.hasRoot {
		delete(topo.hyperMap, "dc")
		delete(topo.setByTier, 3)
		delete(topo.realNodes, "dc")
		for z := 0; z < m.zones; z++ {
			zone := fmt.Sprintf("z%d", z)
			var members []api.MemberConfig
			for r := 0; r < m.racks; r++ {
				members = append(members, api.MemberConfig{Name: rackName(z, r), Type: topologyv1alpha1.MemberTypeHyperNode, Selector: "exact"})
			}
			topo.hyperMap[zone] = api.NewHyperNodeInfo(api.BuildHyperNode(zone, 2, members))
		}
	}
	for _, node := range topo.nodes {
		alloc := api.BuildResourceList(fmt.Sprint(m.nodeCPU[node.Name]), "64Gi", []api.ScalarResource{{Name: "pods", Value: "110"}}...)
		node.Status.Allocatable, node.Status.Capacity = alloc, alloc
		if m.pooledRack[rackOfNode(node.Name)] {
			node.Labels["pool"] = "x"
		}
	}
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	for _, f := range m.fillers {
		pods = append(pods, util.BuildPod("c1", f.name, f.node, v1.PodRunning, api.BuildResourceList(fmt.Sprint(f.cpu), "1Gi"), "filler",
			map[string]string{"volcano.sh/task-spec": "filler"}, nil))
	}
	seen := map[string]bool{}
	for _, a := range m.anchors {
		if !seen[a.group] {
			seen[a.group] = true
			pg := util.BuildPodGroup(a.group, "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
			pg.Labels = map[string]string{groupLabel: groupValue}
			if m.anchorTeam[a.group] {
				pg.Labels["team"] = "x"
			}
			pgs = append(pgs, pg)
		}
		pods = append(pods, util.BuildPod("c1", a.name, a.node, v1.PodRunning, api.BuildResourceList(fmt.Sprint(a.cpu), "1Gi"), a.group,
			map[string]string{"volcano.sh/task-spec": "anchor"}, nil))
	}
	phase := schedulingv1.PodGroupInqueue
	if m.running {
		phase = schedulingv1.PodGroupRunning
	}
	target := util.BuildPodGroup("job", "c1", "q1", int32(m.minMember), nil, phase)
	target.Labels = map[string]string{groupLabel: groupValue}
	if len(m.terms) > 0 {
		paa := &schedulingv1.PodGroupAntiAffinity{}
		for _, term := range m.terms {
			tier := int32(term.tier)
			selector := map[string]string{groupLabel: groupValue}
			if term.team {
				selector = map[string]string{"team": "x"}
			}
			paa.Required = append(paa.Required, schedulingv1.PodGroupAffinityTerm{
				PodGroupSelector: &metav1.LabelSelector{MatchLabels: selector}, TopologyTier: &tier,
			})
		}
		target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: paa}
	}
	if m.jobMode != "" {
		tier := m.jobTier
		target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.NetworkTopologyMode(m.jobMode), HighestTierAllowed: &tier}
	}
	for _, p := range m.policies {
		target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy,
			subGroupPolicySpec(sgPolicy{name: p.name, cpu: p.cpu, size: p.size, groups: p.minGroups, tier: p.tier, pinned: p.pinned}))
	}
	for _, p := range m.target {
		labels := map[string]string{"volcano.sh/task-spec": "worker"}
		if p.policy != "" {
			labels = map[string]string{"volcano.sh/task-spec": p.policy, "role": p.policy, "idx": p.idx}
		}
		var selector map[string]string
		if p.pinned {
			selector = map[string]string{"pool": "x"}
		}
		podPhase := v1.PodPending
		if p.node != "" {
			podPhase = v1.PodRunning
		}
		pods = append(pods, util.BuildPod("c1", p.name, p.node, podPhase, api.BuildResourceList(fmt.Sprint(p.cpu), "1Gi"), "job", labels, selector))
	}
	pgs = append(pgs, target)
	tc := &uthelper.TestCommonStruct{Name: "wide", PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

func (m *wModel) domain(node string, tier int) string {
	switch tier {
	case 1:
		return rackOfNode(node)
	case 2:
		return zoneOfNode(node)
	case 3:
		if m.hasRoot {
			return "dc"
		}
	}
	return ""
}

// check verifies the pods placed in this session against the declared
// constraints. illegal: a hard constraint is violated. incomplete: pods were
// placed although the gang minimum is not met or a SubGroup is partly placed.
func (m *wModel) check(placed map[string]string) (illegal, incomplete string) {
	free := map[string]int{}
	for node, cpu := range m.nodeCPU {
		free[node] = cpu
	}
	for _, p := range m.fillers {
		free[p.node] -= p.cpu
	}
	for _, p := range m.anchors {
		free[p.node] -= p.cpu
	}
	byName := map[string]wPod{}
	final := map[string]string{}
	for _, p := range m.target {
		byName[p.name] = p
		if p.node != "" {
			final[p.name] = p.node
			free[p.node] -= p.cpu
		}
	}
	names := make([]string, 0, len(placed))
	for name := range placed {
		names = append(names, name)
	}
	sort.Strings(names)
	newly := 0
	for _, name := range names {
		node := placed[name]
		p, ok := byName[name]
		if !ok {
			continue
		}
		if p.node != "" {
			return fmt.Sprintf("%s was running on %s and is reported as newly placed on %s", name, p.node, node), ""
		}
		if _, known := m.nodeCPU[node]; !known {
			return fmt.Sprintf("%s on unknown node %s", name, node), ""
		}
		newly++
		final[name] = node
		free[node] -= p.cpu
		if p.pinned && !m.pooledRack[rackOfNode(node)] {
			return fmt.Sprintf("%s on %s violates its nodeSelector", name, node), ""
		}
		for _, term := range m.terms {
			d := m.domain(node, term.tier)
			if d == "" {
				return fmt.Sprintf("%s on %s: no domain at anti-affinity tier %d", name, node, term.tier), ""
			}
			for _, a := range m.anchors {
				if term.team && !m.anchorTeam[a.group] {
					continue
				}
				if d == m.domain(a.node, term.tier) {
					return fmt.Sprintf("%s on %s violates required PodGroup anti-affinity tier %d against %s on %s", name, node, term.tier, a.name, a.node), ""
				}
			}
		}
	}
	for node, left := range free {
		if left < 0 {
			return fmt.Sprintf("node %s over-committed by %d CPU", node, -left), ""
		}
	}
	policies := map[string]wPolicy{}
	for _, p := range m.policies {
		policies[p.name] = p
	}
	groupDomain := map[string]string{}
	groupCount := map[string]int{}
	jobDomain := ""
	finalNames := make([]string, 0, len(final))
	for name := range final {
		finalNames = append(finalNames, name)
	}
	sort.Strings(finalNames)
	for _, name := range finalNames {
		node, p := final[name], byName[name]
		if p.policy != "" {
			key := p.policy + "/" + p.idx
			d := m.domain(node, policies[p.policy].tier)
			if seen, ok := groupDomain[key]; ok && seen != d {
				return fmt.Sprintf("SubGroup %s spans %s and %s at tier %d", key, seen, d, policies[p.policy].tier), ""
			}
			groupDomain[key] = d
			groupCount[key]++
		}
		if m.jobMode == "hard" {
			d := m.domain(node, m.jobTier)
			if jobDomain != "" && jobDomain != d {
				return fmt.Sprintf("job spans %s and %s at hard tier %d", jobDomain, d, m.jobTier), ""
			}
			jobDomain = d
		}
	}
	if newly == 0 {
		return "", ""
	}
	if len(final) < m.minMember {
		return "", fmt.Sprintf("%d pods placed or running, minMember %d", len(final), m.minMember)
	}
	complete := map[string]int{}
	for key, count := range groupCount {
		policy := strings.SplitN(key, "/", 2)[0]
		if count == policies[policy].size {
			complete[policy]++
		} else {
			return "", fmt.Sprintf("SubGroup %s partly placed (%d of %d)", key, count, policies[policy].size)
		}
	}
	for _, p := range m.policies {
		if complete[p.name] < p.minGroups {
			return "", fmt.Sprintf("policy %s: %d complete SubGroups, MinSubGroups %d", p.name, complete[p.name], p.minGroups)
		}
	}
	return "", ""
}

type wConfig struct {
	name  string
	mode  protoMode
	r     round2
	order string
}

type wTally struct{ scheduled, gained, lost, moved, fewer, more, illegal, incomplete int }

func (m *wModel) run(t *testing.T, c wConfig) map[string]string {
	res, _ := runRound2(t, m.build(), c.mode, c.r, func(a *Action) { a.repairOrder = c.order }, nil)
	return jobPlacement(res)
}

// compare classifies a placement against the placement of the current search.
func compareToCurrent(current, now map[string]string) string {
	switch {
	case len(current) == 0 && len(now) == 0:
		return "none"
	case len(current) == 0:
		return "gained"
	case len(now) == 0:
		return "lost"
	}
	common, same := 0, 0
	for pod, node := range current {
		if other, ok := now[pod]; ok {
			common++
			if other == node {
				same++
			}
		}
	}
	switch {
	case same == len(current) && len(now) == len(current):
		return "same"
	case same == len(current) && same == common:
		return "more"
	case same == len(now) && same == common:
		return "fewer"
	}
	return "moved"
}

// B1: monotonicity over the wider scenario space. A Job the current search
// schedules must keep its placement; new mechanisms may only add pods; every
// placement must be legal.
func TestReviewB1WideMonotonicFuzz(t *testing.T) {
	const scenarios = 2000
	current := wConfig{"current search (exhaustive + deterministic ties)", modeExhaustiveDet, round2{}, ""}
	configs := []wConfig{
		{"ranked + fitBound (round 1)", modeRankedFit, round2{}, ""},
		{"ranked + region", modeRankedFit, round2{scope: true}, ""},
		{"ranked + region + repair(4, reserve)", modeRankedFit, round2{scope: true, budget: 4}, "reserve"},
		{"exhaustive + region + repair(4, reserve)", modeExhaustiveDet, round2{scope: true, budget: 4}, "reserve"},
		{"ranked + region + repair(8)", modeRankedFit, round2{scope: true, budget: 8}, ""},
	}
	tallies := [2][]wTally{make([]wTally, len(configs)), make([]wTally, len(configs))}
	currentTally := [2]wTally{}
	phaseCount := [2]int{}
	coverage := map[string]int{}
	examples := map[string]int{}
	note := func(kind, format string, args ...interface{}) {
		examples[kind]++
		if examples[kind] <= 4 {
			t.Logf("%s: %s", kind, fmt.Sprintf(format, args...))
		}
	}
	evaluate := func(phase int, seed int64, m *wModel) map[string]string {
		phaseCount[phase]++
		base := m.run(t, current)
		if illegal, incomplete := m.check(base); illegal != "" {
			currentTally[phase].illegal++
			note("current search illegal", "phase %d seed %d: %s (%s) %v", phase+1, seed, illegal, m.summary(), base)
		} else if incomplete != "" {
			currentTally[phase].incomplete++
			note("current search incomplete", "phase %d seed %d: %s (%s) %v", phase+1, seed, incomplete, m.summary(), base)
		}
		if len(base) > 0 {
			currentTally[phase].scheduled++
		}
		for i, c := range configs {
			now := m.run(t, c)
			tl := &tallies[phase][i]
			if len(now) > 0 {
				tl.scheduled++
			}
			if illegal, incomplete := m.check(now); illegal != "" {
				tl.illegal++
				note("ILLEGAL", "phase %d seed %d %s: %s (%s) %v", phase+1, seed, c.name, illegal, m.summary(), now)
			} else if incomplete != "" {
				tl.incomplete++
				note("incomplete", "phase %d seed %d %s: %s (%s) %v", phase+1, seed, c.name, incomplete, m.summary(), now)
			}
			switch compareToCurrent(base, now) {
			case "gained":
				tl.gained++
			case "lost":
				tl.lost++
				note("LOST", "phase %d seed %d %s (%s)\n  today: %v", phase+1, seed, c.name, m.summary(), base)
			case "moved":
				tl.moved++
				note("MOVED", "phase %d seed %d %s (%s)\n  today: %v\n  now:   %v", phase+1, seed, c.name, m.summary(), base, now)
			case "fewer":
				tl.fewer++
				note("FEWER", "phase %d seed %d %s (%s)\n  today: %v\n  now:   %v", phase+1, seed, c.name, m.summary(), base, now)
			case "more":
				tl.more++
				note("more pods", "phase %d seed %d %s (%s)\n  today: %v\n  now:   %v", phase+1, seed, c.name, m.summary(), base, now)
			}
		}
		return base
	}
	for seed := int64(1); seed <= scenarios; seed++ {
		m := genWideModel(seed)
		if !m.hasRoot {
			coverage["no real root"]++
		}
		if len(m.terms) > 0 {
			coverage["required PAA"]++
		}
		if len(m.terms) > 1 {
			coverage["two PAA terms"]++
		}
		for _, term := range m.terms {
			coverage[fmt.Sprintf("PAA term tier %d", term.tier)]++
		}
		if m.jobMode != "" {
			coverage[fmt.Sprintf("job %s tier %d", m.jobMode, m.jobTier)]++
		}
		if m.elastic() {
			coverage["elastic gang"]++
		}
		if len(m.policies) == 3 {
			coverage["three SubGroup policies"]++
		}
		base := evaluate(0, seed, m)
		if len(base) > 0 && seed%2 == 0 {
			evaluate(1, seed, m.scaleUp(base, seed))
		}
	}
	keys := make([]string, 0, len(coverage))
	for key := range coverage {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t.Logf("coverage: %s = %d", key, coverage[key])
	}
	for phase, title := range []string{"phase 1: pending Job", "phase 2: the Job placed by the current search is running and scaled up"} {
		t.Logf("%s: %d scenarios; current search places pods in %d (illegal %d, incomplete %d)", title, phaseCount[phase],
			currentTally[phase].scheduled, currentTally[phase].illegal, currentTally[phase].incomplete)
		t.Logf("| configuration | places pods | newly | lost | moved | fewer pods | more pods | illegal | incomplete |")
		t.Logf("|---|---|---|---|---|---|---|---|---|")
		for i, c := range configs {
			tl := tallies[phase][i]
			t.Logf("| %s | %d | %d | %d | %d | %d | %d | %d | %d |", c.name, tl.scheduled, tl.gained, tl.lost, tl.moved, tl.fewer, tl.more, tl.illegal, tl.incomplete)
		}
	}
	for kind, count := range examples {
		t.Logf("total %q events: %d", kind, count)
	}
}

// B2: the reported bug on the topology as the user describes it: three tier-2
// domains with no real HyperNode above them. Then one more SubGroup is added
// to the running Job.
func TestReviewB2ReportedBugWithoutRealRoot(t *testing.T) {
	model := func() *wModel {
		m := &wModel{zones: 3, racks: 3, perRack: 1, hasRoot: false, nodeCPU: map[string]int{}, pooledRack: map[string]bool{},
			anchorTeam: map[string]bool{}, terms: []wTerm{{tier: 2}}, minMember: 4}
		for z := 0; z < 3; z++ {
			for r := 0; r < 3; r++ {
				m.nodeCPU[nodeName(z, r, 0)] = 8
			}
		}
		m.anchors = []wPod{{name: "anchor0-0", cpu: 1, node: nodeName(0, 0, 0), group: "anchor0"}}
		for i, role := range []string{"prefill", "decode"} {
			m.policies = append(m.policies, wPolicy{name: role, cpu: 6, size: 1, groups: 2, minGroups: 2, tier: 1})
			for g := 0; g < 2; g++ {
				idx := i*2 + g
				m.target = append(m.target, wPod{name: fmt.Sprintf("job-%s-g%d-p0", role, idx), cpu: 6, policy: role, idx: fmt.Sprint(idx), group: "job"})
			}
		}
		return m
	}
	for _, c := range []wConfig{
		{"current search", modeExhaustiveDet, round2{}, ""},
		{"ranked + region + repair(4, reserve)", modeRankedFit, round2{scope: true, budget: 4}, "reserve"},
	} {
		m := model()
		previous := framework.ProbeScopeUnion
		framework.ProbeScopeUnion = c.r.scope
		action := c.mode.action()
		action.repairBudget, action.repairOrder = c.r.budget, c.order
		tiers, extra := historyTiers()
		var allocated string
		res := raRun(t, m.build(), tiers, extra, action, func(ssn *framework.Session) {
			if job := ssn.Jobs["c1/job"]; job != nil {
				allocated = job.AllocatedHyperNode
			}
		})
		framework.ProbeScopeUnion = previous
		placed := jobPlacement(res)
		illegal, incomplete := m.check(placed)
		t.Logf("%s: placed %d/4 %v; job.AllocatedHyperNode=%q; illegal=%q incomplete=%q; %s", c.name, len(placed), placed, allocated, illegal, incomplete,
			shortFitError(res.fitError["c1/job"]))
		for _, scope := range action.trace.Scopes {
			t.Logf("    usable region of %s: %v", scope.Job, scope.Roots)
		}
		if len(placed) != 4 {
			continue
		}
		next := m.scaleUp(placed, 1)
		for _, c2 := range []wConfig{
			{"current search", modeExhaustiveDet, round2{}, ""},
			{"ranked + region + repair(4, reserve)", modeRankedFit, round2{scope: true, budget: 4}, "reserve"},
		} {
			now := next.run(t, c2)
			illegal, incomplete := next.check(now)
			t.Logf("    scale-up by one SubGroup, %s: newly placed %v; illegal=%q incomplete=%q (%s)", c2.name, now, illegal, incomplete, next.summary())
		}
	}
}
