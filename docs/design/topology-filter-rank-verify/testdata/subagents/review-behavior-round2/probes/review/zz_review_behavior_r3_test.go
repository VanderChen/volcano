package allocate

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

// rvShape describes how compact a placement is: the tier of the smallest
// domain that contains every pod (1 rack, 2 zone, 3 several zones), then the
// number of zones, racks and nodes used.
type rvShape struct{ tier, zones, racks, nodes int }

func rvShapeOf(placement map[string]string) rvShape {
	zones, racks, nodes := sets.New[string](), sets.New[string](), sets.New[string]()
	for _, node := range placement {
		zones.Insert(zoneOfNode(node))
		racks.Insert(rackOfNode(node))
		nodes.Insert(node)
	}
	shape := rvShape{zones: zones.Len(), racks: racks.Len(), nodes: nodes.Len()}
	switch {
	case racks.Len() <= 1:
		shape.tier = 1
	case zones.Len() <= 1:
		shape.tier = 2
	default:
		shape.tier = 3
	}
	return shape
}

func (s rvShape) String() string {
	return fmt.Sprintf("tier%d/%dz/%dr/%dn", s.tier, s.zones, s.racks, s.nodes)
}

// cmp: -1 = a more compact than b, 0 = same, +1 = a less compact.
func (s rvShape) cmp(o rvShape) int {
	for _, pair := range [][2]int{{s.tier, o.tier}, {s.zones, o.zones}, {s.racks, o.racks}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// RB9 (dimension 5, S11): confirm the dev-1.14 R3 numbers and characterize
// every placement that differs between today's search and options a / b.
func TestReviewRB9ForestVersusRegion(t *testing.T) {
	const scenarios = 600
	configs := []rvCfg{rvA, rvB, rvB.repair(8)}
	type tally struct {
		scheduled, gained, lost, fewer, moved, illegal int
		moreCompact, sameShape, lessCompact        int
		widerTier                                  int
	}
	tallies := make([]tally, len(configs))
	baseScheduled := 0
	var details []string
	for seed := int64(1); seed <= scenarios; seed++ {
		scenario := genSingleJobScenario(seed)
		base := rvRun(t, scenario.tc, rvOffDet)
		basePlacement := jobPlacement(base.res)
		if len(basePlacement) > 0 {
			baseScheduled++
		}
		for i, cfg := range configs {
			scenario := genSingleJobScenario(seed)
			out := rvRun(t, scenario.tc, cfg)
			placement := jobPlacement(out.res)
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
			case len(basePlacement) > 0 && len(placement) < len(basePlacement):
				tallies[i].fewer++
			case len(basePlacement) == 0 && len(placement) > 0:
				tallies[i].gained++
			case len(basePlacement) > 0 && !reflect.DeepEqual(basePlacement, placement):
				tallies[i].moved++
				before, after := rvShapeOf(basePlacement), rvShapeOf(placement)
				verdict := "same shape"
				switch after.cmp(before) {
				case -1:
					tallies[i].moreCompact++
					verdict = "more compact"
				case 0:
					tallies[i].sameShape++
				case 1:
					tallies[i].lessCompact++
					verdict = "LESS COMPACT"
				}
				if after.tier > before.tier {
					tallies[i].widerTier++
				}
				if cfg.name == rvB.name {
					details = append(details, fmt.Sprintf("seed %3d: today %s -> region %s (%s) [%s]", seed, before, after, verdict, scenario.summary))
				}
			}
		}
	}
	t.Logf("%d single-job scenarios; today's search (exhaustive + deterministic ties) schedules %d", scenarios, baseScheduled)
	t.Logf("| configuration | scheduled | newly scheduled | lost | fewer pods | placed differently | more compact | same shape | less compact | wider tier | illegal |")
	t.Logf("|---|---|---|---|---|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d |", cfg.name, tl.scheduled, tl.gained, tl.lost, tl.fewer, tl.moved,
			tl.moreCompact, tl.sameShape, tl.lessCompact, tl.widerTier, tl.illegal)
	}
	sort.Strings(details)
	t.Logf("placements that differ under option b:\n%s", strings.Join(details, "\n"))
}
