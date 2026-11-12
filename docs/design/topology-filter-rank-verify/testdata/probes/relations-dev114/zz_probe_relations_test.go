/*
Round-5 probes for issue 004, dev-1.14 only: constraints that relate the
SubGroups of one PodGroup (required and preferred SubGroup affinity and
anti-affinity). Needs probes/common, probes/history, probes/prototype,
probes/prototype2.
*/

package allocate

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/plugins/binpack"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// relSpec describes one hand-built scenario. SubGroups are handled in the
// order the policies are listed (lower match index first).
type relSpec struct {
	name                  string
	zones, racks, perRack int
	fillers               map[string]int // node -> CPU already used
	pooledRacks           []string       // racks labelled pool=x
	policies              []sgPolicy
	affinity, anti        []schedulingv1.SubGroupAffinityTerm // required
	feasible              bool
	note                  string
}

func sgTerm(tier int32, groups ...string) schedulingv1.SubGroupAffinityTerm {
	return schedulingv1.SubGroupAffinityTerm{SubGroups: groups, TopologyTier: &tier}
}

func sgPreferred(tier, weight int32, groups ...string) schedulingv1.SubGroupAffinityTerm {
	return schedulingv1.SubGroupAffinityTerm{SubGroups: groups, TopologyTier: &tier, Weight: weight}
}

func buildRelScenario(spec relSpec) *uthelper.TestCommonStruct {
	pooled := map[string]bool{}
	for _, rack := range spec.pooledRacks {
		pooled[rack] = true
	}
	topo := buildProbeTopo(spec.zones, spec.racks, spec.perRack, "8", "64Gi", func(zone, rack, _ int, n *v1.Node) {
		if pooled[rackName(zone, rack)] {
			n.Labels["pool"] = "x"
		}
	})
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	nodes := make([]string, 0, len(spec.fillers))
	for node := range spec.fillers {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for i, node := range nodes {
		pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node, v1.PodRunning,
			api.BuildResourceList(fmt.Sprint(spec.fillers[node]), "1Gi"), "filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
	}
	target := util.BuildPodGroup("job", "c1", "q1", 0, nil, schedulingv1.PodGroupInqueue)
	for _, p := range spec.policies {
		target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy, subGroupPolicySpec(p))
	}
	if len(spec.affinity)+len(spec.anti) > 0 {
		target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{}
		if len(spec.affinity) > 0 {
			target.Spec.TopologyAffinity.SubGroupAffinity = &schedulingv1.SubGroupAffinity{Required: spec.affinity}
		}
		if len(spec.anti) > 0 {
			target.Spec.TopologyAffinity.SubGroupAntiAffinity = &schedulingv1.SubGroupAntiAffinity{Required: spec.anti}
		}
	}
	next := 0
	jobPods := subGroupPods("job", spec.policies, func() int { next++; return next - 1 })
	target.Spec.MinMember = int32(len(jobPods))
	pgs = append(pgs, target)
	pods = append(pods, jobPods...)
	tc := &uthelper.TestCommonStruct{Name: spec.name, PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// subGroupOf returns policy and SubGroup index of a pod built by subGroupPods.
func subGroupOf(pod string) (policy, group string) {
	parts := strings.Split(pod, "-") // job-<policy>-g<idx>-p<i>
	return parts[1], parts[1] + "-" + parts[2]
}

func domainAtTier(node string, tier int32) string {
	switch tier {
	case 1:
		return rackOfNode(node)
	case 2:
		return zoneOfNode(node)
	}
	return "dc"
}

// relationViolation checks a placement against the required SubGroup terms by
// their definition, without scheduler code. It returns "" when legal.
// strict adds today's reading: a SubGroup named by a required anti-affinity
// term must itself stay inside one domain of the term's tier.
func relationViolation(spec relSpec, placement map[string]string, strict bool) string {
	policyOf, nodesOf := map[string]string{}, map[string][]string{}
	for pod, node := range placement {
		policy, group := subGroupOf(pod)
		policyOf[group] = policy
		nodesOf[group] = append(nodesOf[group], node)
	}
	domains := func(group string, tier int32) map[string]bool {
		out := map[string]bool{}
		for _, node := range nodesOf[group] {
			out[domainAtTier(node, tier)] = true
		}
		return out
	}
	in := func(term schedulingv1.SubGroupAffinityTerm, policy string) bool {
		for _, name := range term.SubGroups {
			if name == policy {
				return true
			}
		}
		return false
	}
	for _, term := range spec.affinity {
		union := map[string]bool{}
		for group, policy := range policyOf {
			if in(term, policy) {
				for domain := range domains(group, *term.TopologyTier) {
					union[domain] = true
				}
			}
		}
		if len(union) > 1 {
			return fmt.Sprintf("affinity term %v spans %d domains", term.SubGroups, len(union))
		}
	}
	for _, term := range spec.anti {
		if *term.TopologyTier < 1 || *term.TopologyTier > 2 {
			// The probe topology has racks (tier 1) and zones (tier 2). A term
			// on a tier that does not exist cannot be satisfied.
			for _, policy := range policyOf {
				if in(term, policy) {
					return fmt.Sprintf("anti-affinity term %v names tier %d, which does not exist", term.SubGroups, *term.TopologyTier)
				}
			}
		}
		for g1, p1 := range policyOf {
			if strict && in(term, p1) && len(domains(g1, *term.TopologyTier)) > 1 {
				return fmt.Sprintf("anti-affinity term %v: %s spans several domains (not allowed today)", term.SubGroups, g1)
			}
			for g2, p2 := range policyOf {
				if g1 >= g2 || !in(term, p1) || !in(term, p2) {
					continue
				}
				if (len(term.SubGroups) == 1) != (p1 == p2) {
					continue // one name: peers share the policy; several names: peers differ
				}
				other := domains(g2, *term.TopologyTier)
				for domain := range domains(g1, *term.TopologyTier) {
					if other[domain] {
						return fmt.Sprintf("anti-affinity term %v: %s and %s share %s", term.SubGroups, g1, g2, domain)
					}
				}
			}
		}
	}
	return ""
}

func relScenarios() []relSpec {
	one := func(name string, cpu, size, tier int) sgPolicy {
		return sgPolicy{name: name, cpu: cpu, size: size, groups: 1, tier: tier}
	}
	pinned := func(p sgPolicy) sgPolicy { p.pinned = true; return p }
	return []relSpec{
		{name: "RA1 anti-affinity at tier 1; SubGroup a must span two racks", zones: 1, racks: 3, perRack: 1,
			policies: []sgPolicy{one("a", 6, 2, 2), one("b", 6, 1, 1)}, anti: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "a", "b")},
			feasible: true, note: "b in one rack, a in the other two"},
		{name: "RA1c control: RA1 without the term", zones: 1, racks: 3, perRack: 1,
			policies: []sgPolicy{one("a", 6, 2, 2), one("b", 6, 1, 1)}, feasible: true},
		{name: "RA2 anti-affinity at tier 1; flexible SubGroup first", zones: 1, racks: 2, perRack: 2,
			fillers: map[string]int{"z0-r00-n0": 2}, pooledRacks: []string{"z0-r00"},
			policies: []sgPolicy{one("x", 6, 1, 1), pinned(one("y", 6, 1, 1))}, anti: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "x", "y")},
			feasible: true, note: "y can only use r00; x takes it first because it is the fullest rack"},
		{name: "RA2c control: RA2 with the pinned SubGroup first", zones: 1, racks: 2, perRack: 2,
			fillers: map[string]int{"z0-r00-n0": 2}, pooledRacks: []string{"z0-r00"},
			policies: []sgPolicy{pinned(one("y", 6, 1, 1)), one("x", 6, 1, 1)}, anti: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "x", "y")},
			feasible: true},
		{name: "RA3 affinity at tier 1 for p,q; third SubGroup r", zones: 1, racks: 2, perRack: 2,
			fillers:  map[string]int{"z0-r00-n0": 6},
			policies: []sgPolicy{one("p", 6, 1, 1), one("q", 6, 1, 1), one("r", 6, 1, 1)}, affinity: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "p", "q")},
			feasible: true, note: "r00 has one free node and is the fullest; p,q fit only in r01"},
		{name: "RA3c control: RA3 with r first", zones: 1, racks: 2, perRack: 2,
			fillers:  map[string]int{"z0-r00-n0": 6},
			policies: []sgPolicy{one("r", 6, 1, 1), one("p", 6, 1, 1), one("q", 6, 1, 1)}, affinity: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "p", "q")},
			feasible: true},
		{name: "RA4 anti-affinity at tier 1 among three replicas of one policy", zones: 1, racks: 3, perRack: 1,
			policies: []sgPolicy{{name: "w", cpu: 6, size: 1, groups: 3, tier: 1}}, anti: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "w")},
			feasible: true},
		{name: "RA4n control: three replicas, two racks", zones: 1, racks: 2, perRack: 2,
			policies: []sgPolicy{{name: "w", cpu: 6, size: 1, groups: 3, tier: 1}}, anti: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "w")},
			feasible: false},
		{name: "RA6 affinity at tier 2 for p,q; anti-affinity at tier 1 for r,p; r first", zones: 1, racks: 2, perRack: 1,
			policies: []sgPolicy{one("r", 4, 1, 1), one("p", 4, 2, 1), one("q", 4, 1, 1)},
			affinity: []schedulingv1.SubGroupAffinityTerm{sgTerm(2, "p", "q")}, anti: []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "r", "p")},
			feasible: true, note: "p fills one rack; q shares the other rack with r. A group filter that counts only what p may use would drop p's rack"},
		{name: "RA7 control: anti-affinity on a tier that does not exist", zones: 1, racks: 3, perRack: 1,
			policies: []sgPolicy{one("a", 6, 2, 2), one("b", 6, 1, 1)}, anti: []schedulingv1.SubGroupAffinityTerm{sgTerm(0, "a", "b")},
			feasible: false, note: "today every candidate is rejected; the relaxed reading must not turn the term off"},
		{name: "RA5 affinity at tier 2 for p,q; each limited to a rack", zones: 2, racks: 2, perRack: 2,
			fillers:  map[string]int{"z0-r00-n0": 6, "z0-r00-n1": 6, "z0-r01-n0": 2, "z0-r01-n1": 6},
			policies: []sgPolicy{one("p", 6, 1, 1), one("q", 6, 1, 1)}, affinity: []schedulingv1.SubGroupAffinityTerm{sgTerm(2, "p", "q")},
			feasible: true, note: "zone z0 has one free slot in its fullest rack; both fit only in z1"},
	}
}

type relConfig struct {
	name      string
	mode      protoMode
	r         round2
	relations bool // round-5 prototype: SubGroup anti-affinity as a region, group-aware first choice for affinity
	reuse     int  // "reuse before opening": 0 off, 1 among equally scored candidates, 2 before the score
	fallback  bool // search with today's reading first, the relaxed reading only when that finds nothing
}

func relConfigs() []relConfig {
	return []relConfig{
		{"today", modeExhaustiveDet, round2{}, false, 0, false},
		{"ranked", modeRankedFit, round2{}, false, 0, false},
		{"ranked + retry 4", modeRankedFit, round2{budget: 4}, false, 0, false},
		{"today + relations", modeExhaustiveDet, round2{}, true, 0, false},
		{"ranked + relations", modeRankedFit, round2{}, true, 0, false},
		{"ranked + relations + retry 4", modeRankedFit, round2{budget: 4}, true, 0, false},
		{"ranked, today's reading first, then relations + reuse among ties + retry 4", modeRankedFit, round2{budget: 4}, true, 1, true},
	}
}

func runRel(t testing.TB, tc *uthelper.TestCommonStruct, cfg relConfig) *probeResult {
	previous := framework.ProbeSubJobRelations
	framework.ProbeSubJobRelations = cfg.relations
	relationReuse, relationFallback = cfg.reuse, cfg.fallback
	defer func() { framework.ProbeSubJobRelations, relationReuse, relationFallback = previous, 0, false }()
	res, _ := runRound2(t, tc, cfg.mode, cfg.r, func(a *Action) { a.repairOrder = "reserve" }, nil)
	return res
}

// RL1: hand-built scenarios with required SubGroup affinity and anti-affinity.
func TestRelRL1RequiredSubGroupTerms(t *testing.T) {
	configs := relConfigs()
	header := "| scenario | legal placement exists |"
	rule := "|---|---|"
	for _, cfg := range configs {
		header += " " + cfg.name + " |"
		rule += "---|"
	}
	t.Log(header)
	t.Log(rule)
	for _, spec := range relScenarios() {
		row := fmt.Sprintf("| %s | %t |", spec.name, spec.feasible)
		total := 0
		for _, cfg := range configs {
			res := runRel(t, buildRelScenario(spec), cfg)
			placement := jobPlacement(res)
			total = 0
			for _, p := range spec.policies {
				total += p.size * p.groups
			}
			if msg := relationViolation(spec, placement, false); msg != "" {
				t.Errorf("%s / %s: illegal placement: %s %v", spec.name, cfg.name, msg, placement)
			}
			if !spec.feasible && len(placement) > 0 {
				t.Errorf("%s / %s: placed %d pods although no legal placement exists", spec.name, cfg.name, len(placement))
			}
			row += fmt.Sprintf(" %d/%d |", len(placement), total)
		}
		t.Log(row)
		if spec.note != "" {
			t.Logf("    (%s)", spec.note)
		}
	}
}

// genPreferredScenario builds a random Job whose SubGroups carry preferred
// SubGroup affinity and anti-affinity terms.
func genPreferredScenario(seed int64) *uthelper.TestCommonStruct {
	rng := rand.New(rand.NewSource(seed*9973 + 5))
	zones, racks, perRack := 2, 2+rng.Intn(2), 1+rng.Intn(2)
	topo := buildProbeTopo(zones, racks, perRack, "8", "64Gi", nil)
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	for i, node := range topo.nodes {
		if rng.Intn(10) < 5 {
			pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning,
				api.BuildResourceList(fmt.Sprint([]int{2, 4, 6}[rng.Intn(3)]), "1Gi"), "filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
		}
	}
	target := util.BuildPodGroup("job", "c1", "q1", 0, nil, schedulingv1.PodGroupInqueue)
	var policies []sgPolicy
	names := []string{"p0", "p1", "p2"}[:2+rng.Intn(2)]
	for _, name := range names {
		p := sgPolicy{name: name, cpu: []int{2, 4}[rng.Intn(2)], size: 1 + rng.Intn(2), groups: 1 + rng.Intn(2), tier: []int{1, 1, 2}[rng.Intn(3)]}
		policies = append(policies, p)
		target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy, subGroupPolicySpec(p))
	}
	spec := &schedulingv1.TopologyAffinitySpec{}
	weight := func() int32 { return int32(10 + 10*rng.Intn(10)) }
	tier := func() int32 { return int32(1 + rng.Intn(2)) }
	if rng.Intn(2) == 0 {
		spec.SubGroupAffinity = &schedulingv1.SubGroupAffinity{Preferred: []schedulingv1.SubGroupAffinityTerm{sgPreferred(tier(), weight(), names...)}}
	}
	if spec.SubGroupAffinity == nil || rng.Intn(2) == 0 {
		groups := names
		if rng.Intn(2) == 0 {
			groups = names[:1]
		}
		spec.SubGroupAntiAffinity = &schedulingv1.SubGroupAntiAffinity{Preferred: []schedulingv1.SubGroupAffinityTerm{sgPreferred(tier(), weight(), groups...)}}
	}
	target.Spec.TopologyAffinity = spec
	perm := rng.Perm(12)
	next := 0
	jobPods := subGroupPods("job", policies, func() int { next++; return perm[next-1] })
	target.Spec.MinMember = int32(len(jobPods))
	pgs = append(pgs, target)
	pods = append(pods, jobPods...)
	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("preferred-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// RL2: preferred SubGroup terms. The score of a candidate depends on where
// the peers were placed earlier in the same trial. Ranking before the dry-run
// must still select what the current flow selects.
func TestRelRL2PreferredTermsDifferential(t *testing.T) {
	const scenarios = 600
	scheduledToday, scheduledRanked, lost, gained, moved := 0, 0, 0, 0, 0
	var attemptsToday, attemptsRanked int64
	for seed := int64(1); seed <= scenarios; seed++ {
		today, _ := runRound2(t, genPreferredScenario(seed), modeExhaustiveDet, round2{}, nil, nil)
		ranked, _ := runRound2(t, genPreferredScenario(seed), modeRankedFit, round2{}, nil, nil)
		a, b := jobPlacement(today), jobPlacement(ranked)
		attemptsToday += today.counters.prePredicate.Load()
		attemptsRanked += ranked.counters.prePredicate.Load()
		if len(a) > 0 {
			scheduledToday++
		}
		if len(b) > 0 {
			scheduledRanked++
		}
		switch {
		case len(a) > 0 && len(b) == 0:
			lost++
		case len(a) == 0 && len(b) > 0:
			gained++
		case len(a) > 0 && fmt.Sprint(a) != fmt.Sprint(b):
			moved++
		}
	}
	t.Logf("%d scenarios with preferred SubGroup terms: scheduled today %d, ranked %d; lost %d, gained %d, placed differently %d; task attempts %d -> %d",
		scenarios, scheduledToday, scheduledRanked, lost, gained, moved, attemptsToday, attemptsRanked)
	if lost > 0 {
		t.Errorf("ranked search lost %d Jobs the current search schedules", lost)
	}
}

// relOracle decides by exhaustive search whether the scenario has a placement
// that satisfies capacity, node selectors, each SubGroup's own tier limit and
// the required SubGroup terms. It uses no scheduler code.
func relOracle(spec relSpec, strict bool) bool {
	type node struct {
		name   string
		free   int
		pooled bool
	}
	pooled := map[string]bool{}
	for _, rack := range spec.pooledRacks {
		pooled[rack] = true
	}
	var nodes []node
	for z := 0; z < spec.zones; z++ {
		for r := 0; r < spec.racks; r++ {
			for n := 0; n < spec.perRack; n++ {
				name := nodeName(z, r, n)
				nodes = append(nodes, node{name, 8 - spec.fillers[name], pooled[rackName(z, r)]})
			}
		}
	}
	type pod struct {
		name, group string
		cpu, tier   int
		pinned      bool
	}
	var pods []pod
	next := 0
	for _, p := range spec.policies {
		for g := 0; g < p.groups; g++ {
			for i := 0; i < p.size; i++ {
				pods = append(pods, pod{fmt.Sprintf("job-%s-g%d-p%d", p.name, next, i), fmt.Sprintf("%s-g%d", p.name, next), p.cpu, p.tier, p.pinned})
			}
			next++
		}
	}
	placement := map[string]string{}
	groupDomain := map[string]string{}
	var place func(i int) bool
	place = func(i int) bool {
		if i == len(pods) {
			return relationViolation(spec, placement, strict) == ""
		}
		p := pods[i]
		tried := map[string]bool{}
		for n := range nodes {
			if nodes[n].free < p.cpu || (p.pinned && !nodes[n].pooled) {
				continue
			}
			domain := domainAtTier(nodes[n].name, int32(p.tier))
			fixed, has := groupDomain[p.group]
			if has && fixed != domain {
				continue
			}
			signature := fmt.Sprint(nodes[n].free, rackOfNode(nodes[n].name), nodes[n].pooled)
			if tried[signature] {
				continue
			}
			tried[signature] = true
			nodes[n].free -= p.cpu
			placement[p.name] = nodes[n].name
			if !has {
				groupDomain[p.group] = domain
			}
			ok := place(i + 1)
			nodes[n].free += p.cpu
			delete(placement, p.name)
			if !has {
				delete(groupDomain, p.group)
			}
			if ok {
				return true
			}
		}
		return false
	}
	return place(0)
}

// genRelScenario builds a random Job with required SubGroup terms. The match
// index of a SubGroup, and with it the processing order, is random.
// relWebhookFix applies the admission rule of dev-1.14: when a required
// affinity term and a required anti-affinity term share a policy, the
// affinity tier may not be lower than the anti-affinity tier.
func relWebhookFix(spec *relSpec) {
	for _, affinity := range spec.affinity {
		for i, anti := range spec.anti {
			if relTermsShareAPolicy(affinity, anti) && *affinity.TopologyTier < *anti.TopologyTier {
				spec.anti[i] = sgTerm(*affinity.TopologyTier, anti.SubGroups...)
				relWebhookAdjusted++
			}
		}
	}
}

// relWebhookAdjusted counts the terms relWebhookFix changed.
var relWebhookAdjusted int

func relTermsShareAPolicy(a, b schedulingv1.SubGroupAffinityTerm) bool {
	for _, x := range a.SubGroups {
		for _, y := range b.SubGroups {
			if x == y {
				return true
			}
		}
	}
	return false
}

// relAdmissionError repeats the checks of the dev-1.14 PodGroup webhook that
// concern required SubGroup terms. It returns "" for an admissible spec.
func relAdmissionError(spec relSpec) string {
	known := map[string]sgPolicy{}
	for _, p := range spec.policies {
		known[p.name] = p
	}
	for _, terms := range [][]schedulingv1.SubGroupAffinityTerm{spec.affinity, spec.anti} {
		for _, term := range terms {
			seen := map[string]bool{}
			for _, name := range term.SubGroups {
				if _, ok := known[name]; !ok || seen[name] {
					return fmt.Sprintf("term %v names an unknown or repeated policy", term.SubGroups)
				}
				seen[name] = true
			}
		}
	}
	for _, affinity := range spec.affinity {
		if len(affinity.SubGroups) < 2 {
			return "affinity term with fewer than two policies"
		}
		for _, anti := range spec.anti {
			if relTermsShareAPolicy(affinity, anti) && *affinity.TopologyTier < *anti.TopologyTier {
				return fmt.Sprintf("affinity %v at tier %d below anti-affinity %v at tier %d", affinity.SubGroups, *affinity.TopologyTier, anti.SubGroups, *anti.TopologyTier)
			}
		}
	}
	// A single-policy anti-affinity term needs MatchLabelKeys or at least two
	// SubGroups; subGroupPolicySpec sets MatchLabelKeys on every policy.
	return ""
}

// RL0: every generated scenario of RL3 and RL4 would pass admission.
func TestRelRL0GeneratedScenariosAreAdmissible(t *testing.T) {
	relWebhookAdjusted = 0
	for seed := int64(1); seed <= 1200; seed++ {
		if msg := relAdmissionError(genRelScenario(seed)); msg != "" {
			t.Errorf("mixed seed %d: %s", seed, msg)
		}
	}
	adjusted := relWebhookAdjusted
	for seed := int64(1); seed <= 600; seed++ {
		if msg := relAdmissionError(genSpanScenario(seed)); msg != "" {
			t.Errorf("span seed %d: %s", seed, msg)
		}
	}
	t.Logf("1200 mixed and 600 spanning scenarios pass the admission checks; %d mixed scenarios had the anti-affinity tier lowered to the affinity tier to do so", adjusted)
}

func genRelScenario(seed int64) (spec relSpec) {
	rng := rand.New(rand.NewSource(seed*6151 + 29))
	spec = relSpec{name: fmt.Sprintf("rel-%d", seed), zones: 1 + rng.Intn(2), racks: 2 + rng.Intn(2), perRack: 1 + rng.Intn(2), fillers: map[string]int{}}
	for z := 0; z < spec.zones; z++ {
		for r := 0; r < spec.racks; r++ {
			if rng.Intn(5) < 2 {
				spec.pooledRacks = append(spec.pooledRacks, rackName(z, r))
			}
			for n := 0; n < spec.perRack; n++ {
				if rng.Intn(3) == 0 {
					spec.fillers[nodeName(z, r, n)] = []int{2, 4, 6}[rng.Intn(3)]
				}
			}
		}
	}
	names := []string{"p0", "p1", "p2"}[:2+rng.Intn(2)]
	pods := 0
	for _, name := range names {
		p := sgPolicy{name: name, cpu: []int{4, 6}[rng.Intn(2)], size: 1 + rng.Intn(2), groups: 1 + rng.Intn(2),
			tier: []int{1, 1, 2}[rng.Intn(3)], pinned: rng.Intn(5) == 0}
		if pods+p.size*p.groups > 6 {
			p.size, p.groups = 1, 1
		}
		pods += p.size * p.groups
		spec.policies = append(spec.policies, p)
	}
	rng.Shuffle(len(spec.policies), func(i, j int) { spec.policies[i], spec.policies[j] = spec.policies[j], spec.policies[i] })
	tier := func() int32 { return int32(1 + rng.Intn(2)) }
	if rng.Intn(5) < 2 {
		spec.affinity = append(spec.affinity, sgTerm(tier(), names[0], names[1]))
	}
	defer func() { relWebhookFix(&spec) }()
	if len(spec.affinity) == 0 || rng.Intn(2) == 0 {
		switch rng.Intn(3) {
		case 0:
			spec.anti = append(spec.anti, sgTerm(tier(), names[rng.Intn(len(names))]))
		case 1:
			spec.anti = append(spec.anti, sgTerm(tier(), names...))
		default:
			spec.anti = append(spec.anti, sgTerm(tier(), names[len(names)-1], names[0]))
		}
	}
	return spec
}

// genSpanScenario builds Jobs in which a SubGroup named by a required
// anti-affinity term at rack level is itself allowed a whole zone, so that it
// may have to span racks: the case the usable-region reading changes.
func genSpanScenario(seed int64) (spec relSpec) {
	rng := rand.New(rand.NewSource(seed*7717 + 41))
	spec = relSpec{name: fmt.Sprintf("span-%d", seed), zones: 1 + rng.Intn(2), racks: 3, perRack: 1 + rng.Intn(2), fillers: map[string]int{}}
	for z := 0; z < spec.zones; z++ {
		for r := 0; r < spec.racks; r++ {
			for n := 0; n < spec.perRack; n++ {
				if rng.Intn(3) == 0 {
					spec.fillers[nodeName(z, r, n)] = []int{2, 4, 6}[rng.Intn(3)]
				}
			}
		}
	}
	cpu := []int{4, 6}[rng.Intn(2)]
	if rng.Intn(3) == 0 {
		// One policy, two replicas that may each span racks but not share one.
		spec.policies = []sgPolicy{{name: "a", cpu: cpu, size: 2, groups: 2, tier: 2}}
		spec.anti = []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "a")}
	} else {
		wide := sgPolicy{name: "a", cpu: cpu, size: 2 + rng.Intn(2), groups: 1, tier: 2}
		narrow := sgPolicy{name: "b", cpu: cpu, size: 1, groups: 1 + rng.Intn(2), tier: 1}
		spec.policies = []sgPolicy{wide, narrow}
		if rng.Intn(2) == 0 {
			spec.policies = []sgPolicy{narrow, wide}
		}
		spec.anti = []schedulingv1.SubGroupAffinityTerm{sgTerm(1, "a", "b")}
	}
	if rng.Intn(3) == 0 {
		spec.policies = append(spec.policies, sgPolicy{name: "c", cpu: cpu, size: 1, groups: 1, tier: 1})
	}
	return spec
}

// RL3: random Jobs with required SubGroup terms against the exhaustive oracle.
func TestRelRL3RequiredTermsOracle(t *testing.T) {
	relOracleGap(t, "mixed required terms", 1200, genRelScenario)
}

// RL4: the same comparison on Jobs built so that the usable-region reading
// of SubGroup anti-affinity matters.
func TestRelRL4SpanningSubGroupOracle(t *testing.T) {
	relOracleGap(t, "anti-affinity with a SubGroup that may span racks", 600, genSpanScenario)
}

func relOracleGap(t *testing.T, family string, scenarios int64, gen func(int64) relSpec) {
	configs := append(relConfigs()[:6],
		relConfig{"ranked + relations + retry 16", modeRankedFit, round2{budget: 16}, true, 0, false},
		relConfig{"ranked + relations + reuse first", modeRankedFit, round2{}, true, 2, false},
		relConfig{"ranked + relations + reuse among ties + retry 16", modeRankedFit, round2{budget: 16}, true, 1, false},
		relConfig{"ranked + relations + reuse first + retry 16", modeRankedFit, round2{budget: 16}, true, 2, false},
		relConfig{"ranked, today's reading first, then relations", modeRankedFit, round2{}, true, 0, true},
		relConfig{"ranked, today's reading first, then relations + reuse among ties + retry 4", modeRankedFit, round2{budget: 4}, true, 1, true},
		relConfig{"ranked, today's reading first, then relations + reuse among ties + retry 16", modeRankedFit, round2{budget: 16}, true, 1, true})
	// attemptsHit: task attempts in scenarios the first configuration schedules; attemptsMiss: in the others.
	type tally struct {
		scheduled, missed, lost, moved int
		attemptsHit, attemptsMiss      int64
	}
	tallies := make([]tally, len(configs))
	feasible, feasibleToday, withAffinity, withAnti := 0, 0, 0, 0
	var missedExamples, movedExamples []string
	for seed := int64(1); seed <= scenarios; seed++ {
		spec := gen(seed)
		ok := relOracle(spec, false)
		if ok {
			feasible++
			if relOracle(spec, true) {
				feasibleToday++
			}
			if len(spec.affinity) > 0 {
				withAffinity++
			}
			if len(spec.anti) > 0 {
				withAnti++
			}
		}
		var today map[string]string
		for i, cfg := range configs {
			res := runRel(t, buildRelScenario(spec), cfg)
			placement := jobPlacement(res)
			if msg := relationViolation(spec, placement, false); msg != "" {
				t.Errorf("seed %d %s: illegal placement: %s", seed, cfg.name, msg)
			}
			if len(placement) > 0 && !ok {
				t.Errorf("seed %d %s: scheduled although the oracle finds no legal placement %v", seed, cfg.name, placement)
			}
			if i == 0 {
				today = placement
			}
			tl := &tallies[i]
			if len(today) > 0 {
				tl.attemptsHit += res.counters.prePredicate.Load()
			} else {
				tl.attemptsMiss += res.counters.prePredicate.Load()
			}
			switch {
			case len(placement) > 0:
				tl.scheduled++
				if len(today) > 0 && fmt.Sprint(today) != fmt.Sprint(placement) {
					tl.moved++
					if len(movedExamples) < 3 {
						movedExamples = append(movedExamples, fmt.Sprintf("seed %d, %s: topo %dx%dx%d fillers %v policies %+v affinity %v anti %v; today %v; now %v", seed, cfg.name,
							spec.zones, spec.racks, spec.perRack, spec.fillers, spec.policies, termNames(spec.affinity), termNames(spec.anti), today, placement))
					}
				}
			case ok:
				tl.missed++
				if i == len(configs)-1 && len(missedExamples) < 6 {
					missedExamples = append(missedExamples, fmt.Sprintf("seed %d: topo %dx%dx%d fillers %v policies %+v affinity %v anti %v", seed,
						spec.zones, spec.racks, spec.perRack, spec.fillers, spec.policies, termNames(spec.affinity), termNames(spec.anti)))
				}
			}
			if len(today) > 0 && len(placement) == 0 {
				tl.lost++
				t.Errorf("seed %d %s: scheduled today, not scheduled now", seed, cfg.name)
			}
		}
	}
	t.Logf("%s: %d scenarios; oracle: %d have a legal placement, %d of them also under today's reading (a SubGroup of an anti-affinity term stays in one domain of the term's tier); %d with an affinity term, %d with an anti-affinity term",
		family, scenarios, feasible, feasibleToday, withAffinity, withAnti)
	t.Logf("| configuration | scheduled | missed | lost vs today | placed differently vs today | task attempts where today schedules | task attempts where today does not |")
	t.Logf("|---|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %d | %d | %d | %d |", cfg.name, tl.scheduled, tl.missed, tl.lost, tl.moved, tl.attemptsHit, tl.attemptsMiss)
		if cfg.fallback && tl.moved != 0 {
			t.Errorf("%s: %d Jobs that schedule today were placed differently", cfg.name, tl.moved)
		}
	}
	for _, example := range missedExamples {
		t.Logf("still missed with every mechanism on: %s", example)
	}
	for _, example := range movedExamples {
		t.Logf("placed differently from today: %s", example)
	}
}

// RL5: the scenarios RL3 and RL4 still miss with every mechanism on, run again
// with the binpack plugin scoring nodes. Which nodes a SubGroup takes inside
// its domain decides which domains are left to its peers, so a node-level
// plugin can change whether the Job schedules.
func TestRelRL5NodeScoringOnRemainingMisses(t *testing.T) {
	cases := []struct {
		family string
		seed   int64
	}{
		{"mixed", 19}, {"span", 151}, {"span", 479},
	}
	t.Logf("| scenario | ranked + relations + retry 16 | the same with binpack scoring nodes |")
	t.Logf("|---|---|---|")
	for _, c := range cases {
		spec := genRelScenario(c.seed)
		if c.family == "span" {
			spec = genSpanScenario(c.seed)
		}
		if !relOracle(spec, false) {
			t.Fatalf("%s seed %d: the oracle finds no legal placement", c.family, c.seed)
		}
		var cells []string
		for _, withBinpack := range []bool{false, true} {
			previous := framework.ProbeSubJobRelations
			framework.ProbeSubJobRelations = true
			action := modeRankedFit.action()
			action.repairBudget, action.repairOrder = 16, "reserve"
			tiers, extra := historyTiers()
			if withBinpack {
				enabled := true
				tiers[0].Plugins = append(tiers[0].Plugins, conf.PluginOption{Name: binpack.PluginName, EnabledNodeOrder: &enabled})
				extra[binpack.PluginName] = binpack.New
			}
			placement := jobPlacement(runProbe(t, buildRelScenario(spec), tiers, extra, action, nil))
			framework.ProbeSubJobRelations = previous
			if msg := relationViolation(spec, placement, false); msg != "" {
				t.Errorf("%s seed %d binpack=%t: illegal placement: %s", c.family, c.seed, withBinpack, msg)
			}
			cell := "not scheduled"
			if len(placement) > 0 {
				cell = fmt.Sprintf("scheduled, %d pods", len(placement))
			}
			cells = append(cells, cell)
		}
		t.Logf("| %s seed %d | %s | %s |", c.family, c.seed, cells[0], cells[1])
	}
}

// RL6: follow-up questions of the second review.
//   - which Jobs the relaxed reading places differently when applied directly;
//   - whether both positions of "reuse before opening" schedule the same Jobs;
//   - what "today's reading first" keeps when the reference is the flow that
//     already has the failure-path retry.
func TestRelRL6FollowUpMixed(t *testing.T) {
	relFollowUp(t, "mixed required terms", 1200, genRelScenario)
}

func TestRelRL6FollowUpSpanning(t *testing.T) {
	relFollowUp(t, "anti-affinity with a SubGroup that may span racks", 600, genSpanScenario)
}

// relShape summarises a placement: zones the Job uses, and SubGroups that
// use more than one rack.
func relShape(placement map[string]string) (zones, spanning int) {
	zoneSet, racks := map[string]bool{}, map[string]map[string]bool{}
	for pod, node := range placement {
		zoneSet[zoneOfNode(node)] = true
		_, group := subGroupOf(pod)
		if racks[group] == nil {
			racks[group] = map[string]bool{}
		}
		racks[group][rackOfNode(node)] = true
	}
	for _, used := range racks {
		if len(used) > 1 {
			spanning++
		}
	}
	return len(zoneSet), spanning
}

func relFollowUp(t *testing.T, family string, scenarios int64, gen func(int64) relSpec) {
	type run struct {
		cfg        relConfig
		firstRetry bool // the first search of "today's reading first" keeps the retry
	}
	const (
		iToday = iota
		iRetry
		iDirect
		iReuseTies
		iReuseFirst
		iFallback
		iFallbackRetry
	)
	runs := []run{
		iToday:         {relConfig{"today", modeExhaustiveDet, round2{}, false, 0, false}, false},
		iRetry:         {relConfig{"ranked + retry 4", modeRankedFit, round2{budget: 4}, false, 0, false}, false},
		iDirect:        {relConfig{"ranked + relations", modeRankedFit, round2{}, true, 0, false}, false},
		iReuseTies:     {relConfig{"ranked + relations + reuse among ties + retry 16", modeRankedFit, round2{budget: 16}, true, 1, false}, false},
		iReuseFirst:    {relConfig{"ranked + relations + reuse first + retry 16", modeRankedFit, round2{budget: 16}, true, 2, false}, false},
		iFallback:      {relConfig{"today's reading first without retry, then relations + reuse among ties + retry 4", modeRankedFit, round2{budget: 4}, true, 1, true}, false},
		iFallbackRetry: {relConfig{"today's reading first with retry 4, then relations + reuse among ties + retry 4", modeRankedFit, round2{budget: 4}, true, 1, true}, true},
	}
	placed := make([]map[int64]string, len(runs))
	for i := range placed {
		placed[i] = map[int64]string{}
	}
	attempts := make([][2]int64, len(runs)) // [0]: scenarios today schedules, [1]: the others
	t.Logf("%s, %d scenarios. Jobs that the relaxed reading, applied directly, places differently from today:", family, scenarios)
	for seed := int64(1); seed <= scenarios; seed++ {
		spec := gen(seed)
		ok := relOracle(spec, false)
		var today map[string]string
		for i, r := range runs {
			relationFallbackRetry = r.firstRetry
			res := runRel(t, buildRelScenario(spec), r.cfg)
			relationFallbackRetry = false
			placement := jobPlacement(res)
			if msg := relationViolation(spec, placement, false); msg != "" {
				t.Errorf("seed %d %s: illegal placement: %s", seed, r.cfg.name, msg)
			}
			if len(placement) > 0 && !ok {
				t.Errorf("seed %d %s: scheduled although the oracle finds no legal placement", seed, r.cfg.name)
			}
			if i == iToday {
				today = placement
			}
			if len(today) > 0 {
				attempts[i][0] += res.counters.prePredicate.Load()
			} else {
				attempts[i][1] += res.counters.prePredicate.Load()
			}
			if len(placement) == 0 {
				continue
			}
			placed[i][seed] = fmt.Sprint(placement)
			if i == iDirect && len(today) > 0 && fmt.Sprint(today) != fmt.Sprint(placement) {
				z0, s0 := relShape(today)
				z1, s1 := relShape(placement)
				t.Logf("  seed %d: zones used %d -> %d; SubGroups on more than one rack %d -> %d; affinity %v anti %v; today %v; now %v",
					seed, z0, z1, s0, s1, termNames(spec.affinity), termNames(spec.anti), today, placement)
			}
		}
	}
	t.Logf("| reference | configuration | scheduled by reference | scheduled by configuration | lost | placed differently | gained |")
	t.Logf("|---|---|---|---|---|---|---|")
	compare := func(ref, got int, mustKeep bool) {
		lost, moved, gained := 0, 0, 0
		for seed, want := range placed[ref] {
			switch have, ok := placed[got][seed]; {
			case !ok:
				lost++
			case have != want:
				moved++
			}
		}
		for seed := range placed[got] {
			if _, ok := placed[ref][seed]; !ok {
				gained++
			}
		}
		t.Logf("| %s | %s | %d | %d | %d | %d | %d |", runs[ref].cfg.name, runs[got].cfg.name, len(placed[ref]), len(placed[got]), lost, moved, gained)
		if mustKeep && (lost != 0 || moved != 0) {
			t.Errorf("%s against %s: lost %d, placed differently %d", runs[got].cfg.name, runs[ref].cfg.name, lost, moved)
		}
	}
	compare(iToday, iDirect, false)
	compare(iReuseTies, iReuseFirst, false)
	compare(iToday, iFallback, true)
	compare(iToday, iFallbackRetry, true)
	compare(iRetry, iFallback, false)
	compare(iRetry, iFallbackRetry, true)
	t.Logf("| configuration | task attempts where today schedules | task attempts where today does not |")
	t.Logf("|---|---|---|")
	for _, i := range []int{iToday, iRetry, iFallback, iFallbackRetry} {
		t.Logf("| %s | %d | %d |", runs[i].cfg.name, attempts[i][0], attempts[i][1])
	}
}

func termNames(terms []schedulingv1.SubGroupAffinityTerm) []string {
	var out []string
	for _, term := range terms {
		out = append(out, fmt.Sprintf("tier%d%v", *term.TopologyTier, term.SubGroups))
	}
	return out
}
