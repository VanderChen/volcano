/*
Round-2 prototype probes for issue 004, dev-1.14 only: the candidate forest
stays the normal path and the usable region is searched only after it fails.
Needs probes/common, probes/history, probes/prototype, probes/prototype2.
*/

package allocate

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"volcano.sh/volcano/pkg/scheduler/framework"
)

func withScopeFallback(run func()) {
	previous := framework.ProbeScopeFallback
	framework.ProbeScopeFallback = true
	defer func() { framework.ProbeScopeFallback = previous }()
	run()
}

// R1f: the reported bug and its variants with forest first, region on failure.
func TestProto2R1fForestFirst(t *testing.T) {
	withScopeFallback(func() {
		t.Logf("| variant | legal placement exists | today | forest first, region on failure | region searched |")
		t.Logf("|---|---|---|---|---|")
		for _, v := range bcVariants {
			today, _ := runRound2(t, buildBCScenario(v), modeExhaustiveDet, round2{}, nil, nil)
			res, action := runRound2(t, buildBCScenario(v), modeRankedFit, round2{scope: true}, nil, nil)
			placed := res.count("myq-")
			for pod, node := range res.placed {
				if strings.HasPrefix(pod, "myq-") && !v.noPAA && strings.HasPrefix(node, "z0-") {
					t.Errorf("%s: pod %s leaked into excluded domain A (%s)", v.name, pod, node)
				}
			}
			if v.feasible && placed != v.pods {
				t.Errorf("%s: placed %d of %d although a legal placement exists", v.name, placed, v.pods)
			}
			if !v.feasible && placed != 0 {
				t.Errorf("%s: placed %d pods although no legal placement exists", v.name, placed)
			}
			t.Logf("| %s | %t | %d/%d | %d/%d | %t |", v.name, v.feasible, today.count("myq-"), v.pods, placed, v.pods, len(action.trace.ScopeFallbacks) > 0)
		}
	})
}

// R3f: same check as R3 with forest first, region on failure.
func TestProto2R3fForestFirstMonotonic(t *testing.T) {
	const scenarios = 600
	type tally struct{ scheduled, lost, moved, gained, illegal, fallbacks int }
	configs := []struct {
		name string
		mode protoMode
		r    round2
	}{
		{"ranked + forest first + region on failure", modeRankedFit, round2{scope: true}},
		{"ranked + forest first + region on failure + repair(8)", modeRankedFit, round2{scope: true, budget: 8}},
	}
	tallies := make([]tally, len(configs))
	baseScheduled := 0
	withScopeFallback(func() {
		for seed := int64(1); seed <= scenarios; seed++ {
			scenario := genSingleJobScenario(seed)
			base, _ := runRound2(t, scenario.tc, modeExhaustiveDet, round2{}, nil, nil)
			basePlacement := jobPlacement(base)
			if len(basePlacement) > 0 {
				baseScheduled++
			}
			for i, cfg := range configs {
				scenario := genSingleJobScenario(seed)
				res, action := runRound2(t, scenario.tc, cfg.mode, cfg.r, nil, nil)
				placement := jobPlacement(res)
				tallies[i].fallbacks += len(action.trace.ScopeFallbacks)
				if msg := illegalPlacement(scenario.tc, placement); msg != "" {
					tallies[i].illegal++
					t.Errorf("seed %d %s: illegal placement: %s (%s)", seed, cfg.name, msg, scenario.summary)
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
					t.Errorf("seed %d %s: placement differs from today (%s)\n  today: %v\n  now:   %v", seed, cfg.name, scenario.summary, basePlacement, placement)
				case len(basePlacement) == 0 && len(placement) > 0:
					tallies[i].gained++
				}
			}
		}
	})
	t.Logf("%d single-job scenarios; current search (exhaustive + deterministic ties) schedules %d", scenarios, baseScheduled)
	t.Logf("| configuration | scheduled | newly scheduled | lost | placed differently | illegal | region searched after forest failed |")
	t.Logf("|---|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %d | %d | %d | %s |", cfg.name, tl.scheduled, tl.gained, tl.lost, tl.moved, tl.illegal, fmt.Sprint(tl.fallbacks))
	}
}
