/*
Round-2 proposal-analyst probe for issue 004 (review only): how a static
"most constrained SubJob first" order compares with the blocked-first retry.

The scheduler handles SubJobs in the order of their match index. Relabelling
the SubGroups of a scenario therefore emulates another fixed order without
touching the prototype.
*/

package allocate

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	"sort"
	"strconv"
	"testing"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/util"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
)

// mostConstrainedFirst relabels the SubGroups of PodGroup "job": SubGroups
// with a nodeSelector first, then tier-1 before tier-2 SubGroups, then the
// larger total request first; the original order breaks ties.
func mostConstrainedFirst(tc *uthelper.TestCommonStruct) {
	type group struct {
		key    string
		pinned bool
		tier   int
		cpu    int64
		old    int
	}
	tiers := map[string]int{}
	for _, pg := range tc.PodGroups {
		if pg.Name == "job" {
			for _, policy := range pg.Spec.SubGroupPolicy {
				tiers[policy.Name] = *policy.NetworkTopology.HighestTierAllowed
			}
		}
	}
	groups := map[string]*group{}
	for _, pod := range tc.Pods {
		if pod.Annotations[schedulingv1.KubeGroupNameAnnotationKey] != "job" || pod.Labels["role"] == "" {
			continue
		}
		key := pod.Labels["role"] + "/" + pod.Labels["idx"]
		g := groups[key]
		if g == nil {
			old, _ := strconv.Atoi(pod.Labels["idx"])
			g = &group{key: key, pinned: pod.Spec.NodeSelector["pool"] == "x", tier: tiers[pod.Labels["role"]], old: old}
			groups[key] = g
		}
		g.cpu += pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
	}
	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.pinned != b.pinned {
			return a.pinned
		}
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.cpu != b.cpu {
			return a.cpu > b.cpu
		}
		return a.old < b.old
	})
	renamed := map[string]string{}
	for i, g := range ordered {
		renamed[g.key] = fmt.Sprint(i)
	}
	for _, pod := range tc.Pods {
		if pod.Annotations[schedulingv1.KubeGroupNameAnnotationKey] != "job" || pod.Labels["role"] == "" {
			continue
		}
		pod.Labels["idx"] = renamed[pod.Labels["role"]+"/"+pod.Labels["idx"]]
	}
}

// A7: on the proposal's R4 and R5 scenarios, compare
//   - the blocked-first retry (budget 4, one attempt reserved), and
//   - a static most-constrained-first order used (a) only as a second pass
//     after the normal search failed, (b) as the normal order.
func TestReviewA7StaticMostConstrainedFirst(t *testing.T) {
	type outcome struct {
		placement map[string]string
		attempts  int64
	}
	run := func(scenario *singleJobScenario, r round2, order string) outcome {
		res, _ := runRound2(t, scenario.tc, modeRankedFit, r, func(a *Action) { a.repairOrder = order }, nil)
		placement := jobPlacement(res)
		if msg := illegalPlacement(scenario.tc, placement); msg != "" {
			t.Errorf("illegal placement: %s (%s)", msg, scenario.summary)
		}
		return outcome{placement, res.counters.prePredicate.Load()}
	}
	report := func(title string, scenarios int, scope bool, gen func(seed int64) *singleJobScenario) {
		var feasible, baseOK, swoOK, mcfPassOK, mcfNormalOK, mcfNormalMoved, mcfNormalLost, both int
		var baseFailAttempts, swoFailAttempts, mcfPassFailAttempts int64
		for seed := int64(1); seed <= int64(scenarios); seed++ {
			best := extractOracleProblem(gen(seed).tc).minTier()
			if best > 0 {
				feasible++
			}
			base := run(gen(seed), round2{scope: scope}, "")
			swo := run(gen(seed), round2{scope: scope, budget: 4}, "reserve")
			reordered := gen(seed)
			mostConstrainedFirst(reordered.tc)
			mcf := run(reordered, round2{scope: scope}, "")
			if len(mcf.placement) > 0 && best == 0 {
				t.Errorf("%s seed %d: most-constrained-first scheduled a Job the oracle calls infeasible", title, seed)
			}
			if len(base.placement) > 0 {
				baseOK++
				switch {
				case len(mcf.placement) == 0:
					mcfNormalLost++
				case fmt.Sprint(mcf.placement) != fmt.Sprint(base.placement):
					mcfNormalMoved++
				}
			} else {
				baseFailAttempts += base.attempts
				swoFailAttempts += swo.attempts
				mcfPassFailAttempts += base.attempts + mcf.attempts
			}
			if len(swo.placement) > 0 {
				swoOK++
			}
			if len(mcf.placement) > 0 {
				mcfNormalOK++
			}
			if len(base.placement) > 0 || len(mcf.placement) > 0 {
				mcfPassOK++
			}
			if len(base.placement) == 0 && len(swo.placement) > 0 && len(mcf.placement) > 0 {
				both++
			}
		}
		t.Logf("%s: %d scenarios, %d with a legal placement (oracle)", title, scenarios, feasible)
		t.Logf("| search (all: ranked + fitBound%s) | scheduled | missed | task attempts when the normal search fails |", map[bool]string{true: " + region", false: ""}[scope])
		t.Logf("|---|---|---|---|")
		t.Logf("| normal search only | %d | %d | %d |", baseOK, feasible-baseOK, baseFailAttempts)
		t.Logf("| + blocked-first retry (4, reserve) | %d | %d | %d |", swoOK, feasible-swoOK, swoFailAttempts)
		t.Logf("| + one more full pass in most-constrained-first order | %d | %d | %d |", mcfPassOK, feasible-mcfPassOK, mcfPassFailAttempts)
		t.Logf("| most-constrained-first as the normal order (behavior change) | %d | %d | - |", mcfNormalOK, feasible-mcfNormalOK)
		t.Logf("most-constrained-first as the normal order: of %d Jobs the normal search schedules, %d are placed differently and %d are lost; %d Jobs are rescued by both fallbacks",
			baseOK, mcfNormalMoved, mcfNormalLost, both)
	}
	report("R5 scenarios", 1000, false, genContentionScenario)
	report("R4 scenarios", 1200, true, func(seed int64) *singleJobScenario {
		scenario := genSingleJobScenario(seed)
		mixSizes(scenario, seed)
		return scenario
	})
}

// A8: how many repair attempts repeat, in the virtual cluster-top HyperNode,
// an attempt already made with the same SubJob order in the real root "dc"
// (same node set), and where repair succeeds.
func TestReviewA8DuplicateContainerAttempts(t *testing.T) {
	type cfg struct {
		budget int
		order  string
	}
	for _, c := range []cfg{{4, ""}, {4, "reserve"}, {8, ""}, {8, "reserve"}} {
		total, inTop, duplicates, okInTop, okElsewhere, jobsWithRepair := 0, 0, 0, 0, 0, 0
		for seed := int64(1); seed <= 1000; seed++ {
			scenario := genContentionScenario(seed)
			_, action := runRound2(t, scenario.tc, modeRankedFit, round2{budget: c.budget}, func(a *Action) { a.repairOrder = c.order }, nil)
			if len(action.trace.Repairs) > 0 {
				jobsWithRepair++
			}
			seenInDC := map[string]bool{}
			for _, repair := range action.trace.Repairs {
				total++
				key := fmt.Sprint(repair.First)
				switch repair.Container {
				case "dc":
					seenInDC[key] = true
				case "<cluster-top-hypernode>":
					inTop++
					if seenInDC[key] {
						duplicates++
					}
				}
				if repair.Success {
					if repair.Container == "<cluster-top-hypernode>" {
						okInTop++
					} else {
						okElsewhere++
					}
				}
			}
		}
		t.Logf("R5, budget %d order %q: %d jobs entered repair; %d attempts; %d in <cluster-top-hypernode>, of which %d repeat an order already tried in dc; successes: %d in <cluster-top-hypernode>, %d elsewhere",
			c.budget, c.order, jobsWithRepair, total, inTop, duplicates, okInTop, okElsewhere)
	}
}

// A9: task-level contention with a node tie. One rack, two identical nodes
// with 4 CPU. The small task (2 CPU, handled first) fits either node; the big
// task (4 CPU) is pinned to node 0. Which node the small task takes is a tie.
func nodeTieScenario(job string, hardTier int) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 1, 2, "4", "16Gi", func(_, _, node int, n *v1.Node) {
		if node == 0 {
			n.Labels["only"] = "a"
		}
	})
	var pg *schedulingv1.PodGroup
	if hardTier > 0 {
		pg = util.BuildPodGroupWithNetWorkTopologies(job, "c1", "", "q1", 2, nil, schedulingv1.PodGroupInqueue, "hard", hardTier)
	} else {
		pg = util.BuildPodGroup(job, "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	}
	small := util.BuildPod("c1", job+"-a-small", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "small"}, nil)
	big := util.BuildPod("c1", job+"-b-big", "", v1.PodPending, api.BuildResourceList("4", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "big"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "node-tie", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: []*v1.Pod{small, big}, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

func TestReviewA9NodeTieIsAnImplicitRestart(t *testing.T) {
	const names, repeats = 16, 10
	type cfg struct {
		name   string
		action func() *Action
	}
	configs := []cfg{
		{"unmodified (random ties)", unmodifiedAction},
		{"exhaustive + deterministic ties", func() *Action { return modeExhaustiveDet.action() }},
		{"ranked + fitBound (round 1)", func() *Action { return modeRankedFit.action() }},
		{"ranked + fitBound + repair(8)", func() *Action { a := modeRankedFit.action(); a.repairBudget = 8; return a }},
	}
	for _, hardTier := range []int{0, 1} {
		t.Logf("job network topology: hard tier %d (0 = none)", hardTier)
		t.Logf("| configuration | job names always scheduled | never scheduled | sometimes | sessions scheduled / total |")
		t.Logf("|---|---|---|---|---|")
		for _, c := range configs {
			always, never, sometimes, ok := 0, 0, 0, 0
			for j := 0; j < names; j++ {
				job := fmt.Sprintf("job%02d", j)
				succ := 0
				for rep := 0; rep < repeats; rep++ {
					res := runProbe(t, nodeTieScenario(job, hardTier), probeTiers(), nil, c.action(), nil)
					switch got := res.count(job + "-"); got {
					case 2:
						succ++
					case 0:
					default:
						t.Errorf("%s %s: partial gang placed (%d/2) %v", c.name, job, got, res.placed)
					}
				}
				ok += succ
				switch succ {
				case repeats:
					always++
				case 0:
					never++
				default:
					sometimes++
				}
			}
			t.Logf("| %s | %d | %d | %d | %d/%d |", c.name, always, never, sometimes, ok, names*repeats)
		}
	}
}
