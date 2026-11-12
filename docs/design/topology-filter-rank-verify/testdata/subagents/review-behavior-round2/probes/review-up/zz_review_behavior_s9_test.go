package allocate

import (
	"testing"
)

// U6 (S9): with the same budget, does retrying before widening the layer lose
// Jobs that the failure-path repair alone schedules?
func TestReviewUpPerLayerVersusFinalRepair(t *testing.T) {
	type cfg struct {
		name     string
		budget   int
		order    string
		perLayer bool
	}
	configs := []cfg{
		{"S8 budget 2, reserve", 2, "reserve", false},
		{"S9 budget 2, per layer", 2, "", true},
		{"S9 budget 2, per layer + reserve", 2, "reserve", true},
		{"S8 budget 4, reserve", 4, "reserve", false},
		{"S9 budget 4, per layer", 4, "", true},
		{"S9 budget 4, per layer + reserve", 4, "reserve", true},
		{"S8 budget 8, reserve", 8, "reserve", false},
		{"S9 budget 8, per layer", 8, "", true},
	}
	for _, gen := range []struct {
		name      string
		scenarios int64
		build     func(seed int64) *singleJobScenario
	}{
		{"R5 generator (contention)", 1000, genContentionScenario},
		{"R4 generator (single job, mixed sizes)", 1200, func(seed int64) *singleJobScenario {
			scenario := genSingleJobScenario(seed)
			mixSizes(scenario, seed)
			return scenario
		}},
	} {
		scheduled := make([][]bool, len(configs))
		base := make([]bool, gen.scenarios+1)
		for i := range scheduled {
			scheduled[i] = make([]bool, gen.scenarios+1)
		}
		for seed := int64(1); seed <= gen.scenarios; seed++ {
			res, _ := runRound2(t, gen.build(seed).tc, modeExhaustiveDet, round2{}, nil, nil)
			base[seed] = len(jobPlacement(res)) > 0
			for i, c := range configs {
				c := c
				res, _ := runRound2(t, gen.build(seed).tc, modeExhaustiveDet, round2{scope: true, budget: c.budget},
					func(a *Action) { a.repairPerLayer, a.repairOrder = c.perLayer, c.order }, nil)
				scheduled[i][seed] = len(jobPlacement(res)) > 0
			}
		}
		t.Logf("### %s, %d scenarios", gen.name, gen.scenarios)
		t.Logf("| configuration | scheduled | lost vs today | lost vs S8 with the same budget | gained vs S8 with the same budget |")
		t.Logf("|---|---|---|---|---|")
		reference := map[int]int{}
		for i, c := range configs {
			if !c.perLayer {
				reference[c.budget] = i
			}
		}
		for i, c := range configs {
			count, lostBase, lostS8, gainedS8 := 0, 0, 0, 0
			ref := reference[c.budget]
			for seed := int64(1); seed <= gen.scenarios; seed++ {
				if scheduled[i][seed] {
					count++
				}
				if base[seed] && !scheduled[i][seed] {
					lostBase++
				}
				if scheduled[ref][seed] && !scheduled[i][seed] {
					lostS8++
				}
				if !scheduled[ref][seed] && scheduled[i][seed] {
					gainedS8++
				}
			}
			t.Logf("| %s | %d | %d | %d | %d |", c.name, count, lostBase, lostS8, gainedS8)
		}
	}
}
