/*
Round-3 probes for issue 004 (upstream prototype only): what the rank key is
based on and what it costs.
Needs probes/common, probes/history, probes/prototype, probes/prototype2,
probes/prototype2-ext and the round-3 prototype patch (test-only rankPolicy).
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
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// rankStats summarises the decisions of one or more allocate runs.
type rankStats struct {
	decisions   int // decisions that selected a candidate
	candidates  int // candidates of those decisions
	firstWins   int // decisions won by the candidate ranked first
	verified    int // dry-runs spent in those decisions (failed + selected)
	pruned      int // candidates skipped by the resource bound
	failedOnly  int // decisions of a layer in which no candidate verified
	failedTries int // dry-runs spent in those failed layers
}

func (s *rankStats) add(trace *DecisionTrace) {
	for _, record := range trace.Records {
		if strings.HasPrefix(record.Scope, "job(estimate)") {
			continue
		}
		selected, failed, pruned := 0, 0, 0
		for _, c := range record.Candidates {
			switch c.Verdict {
			case VerdictSelected:
				selected++
			case VerdictFailedVerify:
				failed++
			case VerdictPruned:
				pruned++
			}
		}
		if selected == 0 {
			s.failedOnly++
			s.failedTries += failed
			continue
		}
		s.decisions++
		s.candidates += len(record.Candidates)
		s.verified += failed + 1
		s.pruned += pruned
		if record.Candidates[0].Verdict == VerdictSelected {
			s.firstWins++
		}
	}
}

func (s *rankStats) row() string {
	if s.decisions == 0 {
		return "0 | - | - | - | - | - | -"
	}
	return fmt.Sprintf("%d | %.1f | %.1f%% | %.2f | %.2f | %d | %d",
		s.decisions, float64(s.candidates)/float64(s.decisions), 100*float64(s.firstWins)/float64(s.decisions),
		float64(s.verified)/float64(s.decisions), float64(s.pruned)/float64(s.decisions), s.failedOnly, s.failedTries)
}

const rankStatsHeader = "decisions with a winner | candidates per decision | won by rank 1 | dry-runs per decision | pruned by resource bound per decision | layers without a winner | dry-runs in those layers"

// RK1: how often does the first-ranked candidate verify, and how many dry-runs
// does a decision cost?
func TestProto3RK1FirstHit(t *testing.T) {
	t.Logf("| workload | mode | %s |", rankStatsHeader)
	t.Logf("|---|---|---|---|---|---|---|---|---|")
	tiers, extra := fuzzTiers()
	for _, mode := range []protoMode{modeRanked, modeRankedFit} {
		stats := &rankStats{}
		for seed := int64(1); seed <= 400; seed++ {
			action := mode.action()
			runProbe(t, genFuzzScenarioKinds(seed, 5).tc, tiers, extra, action, nil)
			stats.add(action.trace)
		}
		t.Logf("| 400 mixed sessions (round-1 generator) | %s | %s |", mode.name, stats.row())
	}
	for _, mode := range []protoMode{modeRanked, modeRankedFit} {
		stats := &rankStats{}
		for seed := int64(1); seed <= 300; seed++ {
			scenario, _ := genStreamScenario(seed)
			_, action := runRound2(t, scenario.tc, mode, round2{}, nil, nil)
			stats.add(action.trace)
		}
		t.Logf("| 300 job streams (tight cluster) | %s | %s |", mode.name, stats.row())
	}
}

// genStreamScenario models jobs arriving one after another on a partly used
// cluster whose free capacity is smaller than the total demand: 2 zones x 6
// racks x 4 nodes x 8 CPU. Jobs are plain gangs with hard topology; most must
// fit into one rack, some into one zone. Jobs are handled in name order.
func genStreamScenario(seed int64) (*singleJobScenario, []string) {
	rng := rand.New(rand.NewSource(seed*32452843 + 11))
	topo := buildProbeTopo(2, 6, 4, "8", "64Gi", nil)
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	free := 0
	for i, node := range topo.nodes {
		used := 0
		if rng.Intn(10) < 4 {
			used = []int{2, 4, 6}[rng.Intn(3)]
			pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning, api.BuildResourceList(fmt.Sprint(used), "1Gi"),
				"filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
		}
		free += 8 - used
	}
	type shape struct {
		tasks, cpu, tier int
	}
	shapes := []shape{
		{2, 2, 1}, {3, 2, 1}, {2, 4, 1}, {4, 2, 1}, // small: 4-8 CPU
		{4, 4, 1}, {3, 6, 1}, {2, 8, 1}, // medium: 16-18 CPU
		{6, 4, 1}, {3, 8, 1}, {4, 8, 1}, // large: 24-32 CPU in one rack
		{8, 4, 2}, {6, 8, 2}, // one zone: 32-48 CPU
	}
	var names, summary []string
	demand := 0
	for j := 0; demand < free*12/10 && j < 60; j++ {
		s := shapes[rng.Intn(len(shapes))]
		name := fmt.Sprintf("s%02d", j)
		names = append(names, name)
		pgs = append(pgs, util.BuildPodGroupWithNetWorkTopologies(name, "c1", "", "q1", int32(s.tasks), nil, schedulingv1.PodGroupInqueue, "hard", s.tier))
		for i := 0; i < s.tasks; i++ {
			pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-p%d", name, i), "", v1.PodPending, api.BuildResourceList(fmt.Sprint(s.cpu), "1Gi"),
				name, map[string]string{"volcano.sh/task-spec": "worker"}, nil))
		}
		demand += s.tasks * s.cpu
		summary = append(summary, fmt.Sprintf("%s:%dx%d@t%d", name, s.tasks, s.cpu, s.tier))
	}
	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("stream-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return &singleJobScenario{tc: tc, pods: demand,
		summary: fmt.Sprintf("free %d CPU, demand %d CPU; %s", free, demand, strings.Join(summary, " "))}, names
}

type streamOutcome struct {
	jobs, cpu int
	attempts  int64
}

func runStream(t *testing.T, seed int64, mode protoMode, policy string) (streamOutcome, *DecisionTrace) {
	scenario, names := genStreamScenario(seed)
	cpuOf := map[string]int64{}
	for _, pod := range scenario.tc.Pods {
		cpuOf[pod.Name] = pod.Spec.Containers[0].Resources.Requests.Cpu().Value()
	}
	res, action := runRound2(t, scenario.tc, mode, round2{}, func(a *Action) { a.rankPolicy = policy }, nil)
	if msg := illegalPlacementJobs(scenario.tc, res.placed, names...); msg != "" {
		t.Errorf("seed %d policy %q: illegal placement: %s", seed, policy, msg)
	}
	out := streamOutcome{attempts: res.counters.prePredicate.Load()}
	scheduled := map[string]bool{}
	for pod := range res.placed {
		name := strings.SplitN(pod, "-", 2)[0]
		if strings.HasPrefix(name, "s") {
			scheduled[name] = true
			out.cpu += int(cpuOf[pod])
		}
	}
	out.jobs = len(scheduled)
	return out, action.trace
}

// RK2: what the order inside a tier layer is based on. The same job stream is
// scheduled with different orders; everything else is identical. "hindsight"
// is the best of 24 pseudo-random orders per stream, a lower bound of what an
// order chosen with knowledge of the whole stream could reach.
func TestProto3RK2RankBasis(t *testing.T) {
	const streams = 300
	const restarts = 24
	policies := []struct{ name, policy string }{
		{"score: fullest domain that still fits first (design)", ""},
		{"emptiest domain first", "loosest"},
		{"name order", "name"},
		{"pseudo-random order", "hash:0"},
	}
	type tally struct {
		jobs, cpu          int
		attempts           int64
		better, same, less int // sessions compared with the design order, by scheduled CPU
		stats              rankStats
	}
	tallies := make([]tally, len(policies))
	totalJobs, totalFree, totalDemand := 0, 0, 0
	hindsightJobs, hindsightCPU, hindsightBeatsDesign, designBeatsHindsight := 0, 0, 0, 0
	for seed := int64(1); seed <= streams; seed++ {
		scenario, names := genStreamScenario(seed)
		totalJobs += len(names)
		var free, demand int
		fmt.Sscanf(scenario.summary, "free %d CPU, demand %d CPU", &free, &demand)
		totalFree += free
		totalDemand += demand
		var design streamOutcome
		for i, p := range policies {
			out, trace := runStream(t, seed, modeRankedFit, p.policy)
			if i == 0 {
				design = out
			}
			tl := &tallies[i]
			tl.jobs += out.jobs
			tl.cpu += out.cpu
			tl.attempts += out.attempts
			tl.stats.add(trace)
			switch {
			case out.cpu > design.cpu:
				tl.better++
			case out.cpu < design.cpu:
				tl.less++
			default:
				tl.same++
			}
		}
		best := streamOutcome{}
		for r := 0; r < restarts; r++ {
			out, _ := runStream(t, seed, modeRankedFit, fmt.Sprintf("hash:%d", r+1))
			if out.cpu > best.cpu || (out.cpu == best.cpu && out.jobs > best.jobs) {
				best = out
			}
		}
		if design.cpu > best.cpu || (design.cpu == best.cpu && design.jobs > best.jobs) {
			designBeatsHindsight++
			best = design
		} else if best.cpu > design.cpu {
			hindsightBeatsDesign++
		}
		hindsightJobs += best.jobs
		hindsightCPU += best.cpu
	}
	t.Logf("%d job streams, %d jobs; free capacity %d CPU, demand %d CPU", streams, totalJobs, totalFree, totalDemand)
	t.Logf("| order inside a tier layer | jobs scheduled | CPU scheduled | share of free capacity | streams with more / same / less CPU than the design order | task attempts | won by rank 1 | dry-runs per decision |")
	t.Logf("|---|---|---|---|---|---|---|---|")
	for i, p := range policies {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %.1f%% | %d / %d / %d | %d | %.1f%% | %.2f |", p.name, tl.jobs, tl.cpu, 100*float64(tl.cpu)/float64(totalFree),
			tl.better, tl.same, tl.less, tl.attempts,
			100*float64(tl.stats.firstWins)/float64(max(1, tl.stats.decisions)), float64(tl.stats.verified)/float64(max(1, tl.stats.decisions)))
	}
	t.Logf("| hindsight: best of design order and %d pseudo-random orders per stream | %d | %d | %.1f%% | random order beat design in %d streams; design beat every random order in %d | - | - | - |",
		restarts, hindsightJobs, hindsightCPU, 100*float64(hindsightCPU)/float64(totalFree), hindsightBeatsDesign, designBeatsHindsight)
}

// RK3: SubGroup Jobs at Job level. Today the Job-level score is the sum of
// the scores of the domains the SubJobs end up in, known only after a dry-run
// of every candidate. "ranked" orders the candidates by the same score table
// before any dry-run and takes the first that verifies.
func TestProto3RK3SubGroupJobLevel(t *testing.T) {
	type cfg struct {
		name string
		mode protoMode
		topK int
		r    round2
	}
	configs := []cfg{
		{"exhaustive at Job level (today's rule)", modeRankedFit, 0, round2{}},
		{"ranked at Job level, first that verifies", modeRankedFit, 1, round2{}},
		{"ranked at Job level, best of first 3 that verify", modeRankedFit, 3, round2{}},
		{"ranked at Job level, first that verifies + region + retry(4)", modeRankedFit, 1, round2{scope: true, budget: 4}},
	}
	generators := []struct {
		name      string
		scenarios int64
		gen       func(int64) *singleJobScenario
	}{
		{"mixed single-job scenarios", 600, genSingleJobScenario},
		{"contention scenarios (three SubGroup policies)", 600, genContentionScenario},
	}
	for _, g := range generators {
		t.Logf("%s: %d", g.name, g.scenarios)
		t.Logf("| Job-level search | scheduled | lost vs today | newly scheduled | placed differently | wider tier than needed | task attempts |")
		t.Logf("|---|---|---|---|---|---|---|")
		type tally struct {
			scheduled, lost, gained, moved, wider int
			attempts                              int64
		}
		tallies := make([]tally, len(configs))
		for seed := int64(1); seed <= g.scenarios; seed++ {
			problem := extractOracleProblem(g.gen(seed).tc)
			best := problem.minTier()
			var today map[string]string
			for i, c := range configs {
				scenario := g.gen(seed)
				res, _ := runRound2(t, scenario.tc, c.mode, c.r, func(a *Action) {
					a.subGroupJobTopK = c.topK
					if c.r.budget > 0 {
						a.repairOrder = "reserve"
					}
				}, nil)
				placement := jobPlacement(res)
				if msg := illegalPlacement(scenario.tc, placement); msg != "" {
					t.Errorf("seed %d %s: illegal placement: %s (%s)", seed, c.name, msg, scenario.summary)
				}
				if i == 0 {
					today = placement
				}
				tl := &tallies[i]
				tl.attempts += res.counters.prePredicate.Load()
				if len(placement) > 0 {
					tl.scheduled++
					if best == 0 {
						t.Errorf("seed %d %s: scheduled although the oracle finds no legal placement", seed, c.name)
					}
					if problem.jobTier > 0 && placementTier(placement) > best {
						tl.wider++
					}
				}
				switch {
				case len(today) > 0 && len(placement) == 0:
					tl.lost++
					t.Errorf("seed %d %s: schedulable today, not scheduled now (%s)", seed, c.name, scenario.summary)
				case len(today) == 0 && len(placement) > 0:
					tl.gained++
				case len(today) > 0 && fmt.Sprint(sortedPlacement(today)) != fmt.Sprint(sortedPlacement(placement)):
					tl.moved++
				}
			}
		}
		for i, c := range configs {
			tl := tallies[i]
			t.Logf("| %s | %d | %d | %d | %d | %d | %d |", c.name, tl.scheduled, tl.lost, tl.gained, tl.moved, tl.wider, tl.attempts)
		}
	}
}

func sortedPlacement(placement map[string]string) []string {
	out := make([]string, 0, len(placement))
	for pod, node := range placement {
		out = append(out, pod+"="+node)
	}
	sort.Strings(out)
	return out
}

// genSubGroupStreamScenario: a stream of SubGroup Jobs on 4 zones x 4 racks x
// 4 nodes x 8 CPU. Every Job must fit into one zone (hard tier 2) or may span
// zones (hard tier 3); every SubGroup must fit into one rack.
func genSubGroupStreamScenario(seed int64) (*singleJobScenario, []string) {
	rng := rand.New(rand.NewSource(seed*49979687 + 3))
	topo := buildProbeTopo(4, 4, 4, "8", "64Gi", nil)
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	free := 0
	for i, node := range topo.nodes {
		used := 0
		if rng.Intn(10) < 4 {
			used = []int{2, 4, 6}[rng.Intn(3)]
			pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning, api.BuildResourceList(fmt.Sprint(used), "1Gi"),
				"filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
		}
		free += 8 - used
	}
	var names []string
	demand := 0
	for j := 0; demand < free*12/10 && j < 60; j++ {
		name := fmt.Sprintf("s%02d", j)
		names = append(names, name)
		target := util.BuildPodGroup(name, "c1", "q1", 0, nil, schedulingv1.PodGroupInqueue)
		tier := []int{2, 2, 2, 3}[rng.Intn(4)]
		target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier}
		var policies []sgPolicy
		for i := 0; i < 1+rng.Intn(2); i++ {
			p := sgPolicy{name: fmt.Sprintf("p%d", i), cpu: []int{2, 4, 8}[rng.Intn(3)], size: 1 + rng.Intn(3), groups: 1 + rng.Intn(3), tier: 1}
			policies = append(policies, p)
			target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy, subGroupPolicySpec(p))
			demand += p.cpu * p.size * p.groups
		}
		next := 0
		jobPods := subGroupPods(name, policies, func() int { next++; return next })
		target.Spec.MinMember = int32(len(jobPods))
		pgs = append(pgs, target)
		pods = append(pods, jobPods...)
	}
	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("sgstream-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return &singleJobScenario{tc: tc, pods: demand, summary: fmt.Sprintf("free %d CPU, demand %d CPU", free, demand)}, names
}

// RK4: does ranking SubGroup Jobs at Job level before the dry-run cost
// capacity over a whole stream, compared with today's exhaustive rule?
func TestProto3RK4SubGroupStream(t *testing.T) {
	const streams = 200
	configs := []struct {
		name string
		mode protoMode
		topK int
	}{
		{"today: every candidate dry-run, random ties", modeExhaustive, 0},
		{"exhaustive at Job level, deterministic ties", modeRankedFit, 0},
		{"ranked at Job level, first that verifies", modeRankedFit, 1},
		{"ranked at Job level, best of first 3 that verify", modeRankedFit, 3},
	}
	type tally struct {
		jobs, cpu          int
		attempts, allocate int64
		more, same, less   int
	}
	tallies := make([]tally, len(configs))
	totalJobs, totalFree := 0, 0
	for seed := int64(1); seed <= streams; seed++ {
		reference, names := genSubGroupStreamScenario(seed)
		totalJobs += len(names)
		var free, demand int
		fmt.Sscanf(reference.summary, "free %d CPU, demand %d CPU", &free, &demand)
		totalFree += free
		baseCPU := 0
		for i, c := range configs {
			scenario, names := genSubGroupStreamScenario(seed)
			cpuOf := map[string]int64{}
			for _, pod := range scenario.tc.Pods {
				cpuOf[pod.Name] = pod.Spec.Containers[0].Resources.Requests.Cpu().Value()
			}
			res, _ := runRound2(t, scenario.tc, c.mode, round2{}, func(a *Action) { a.subGroupJobTopK = c.topK }, nil)
			if msg := illegalPlacementJobs(scenario.tc, res.placed, names...); msg != "" {
				t.Errorf("seed %d %s: illegal placement: %s", seed, c.name, msg)
			}
			scheduled := map[string]bool{}
			cpu := 0
			for pod := range res.placed {
				if name := strings.SplitN(pod, "-", 2)[0]; strings.HasPrefix(name, "s") {
					scheduled[name] = true
					cpu += int(cpuOf[pod])
				}
			}
			if i == 1 {
				baseCPU = cpu
			}
			tl := &tallies[i]
			tl.jobs += len(scheduled)
			tl.cpu += cpu
			tl.attempts += res.counters.prePredicate.Load()
			tl.allocate += res.counters.allocate.Load()
			if i >= 2 {
				switch {
				case cpu > baseCPU:
					tl.more++
				case cpu < baseCPU:
					tl.less++
				default:
					tl.same++
				}
			}
		}
	}
	t.Logf("%d streams of SubGroup Jobs, %d jobs; free capacity %d CPU", streams, totalJobs, totalFree)
	t.Logf("| Job-level search | jobs scheduled | CPU scheduled | share of free capacity | streams with more / same / less CPU than exhaustive | task attempts | Allocate events |")
	t.Logf("|---|---|---|---|---|---|---|")
	for i, c := range configs {
		tl := tallies[i]
		compare := "-"
		if i >= 2 {
			compare = fmt.Sprintf("%d / %d / %d", tl.more, tl.same, tl.less)
		}
		t.Logf("| %s | %d | %d | %.1f%% | %s | %d | %d |", c.name, tl.jobs, tl.cpu, 100*float64(tl.cpu)/float64(totalFree), compare, tl.attempts, tl.allocate)
	}
}
