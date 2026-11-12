package allocate

import (
	"sort"
	"strings"
	"testing"
)

// RB10d: the one elastic scenario in which option b places fewer pods than today.
func TestReviewRB10dElasticSeed195(t *testing.T) {
	for _, variant := range []string{"with PAA", "PAA removed (general path today)"} {
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
			scenario, class, anchorZone, _ := genReviewScenario(195)
			if variant != "with PAA" {
				target, _ := rvTarget(scenario.tc, "job", "job-")
				target.Spec.TopologyAffinity = nil
			}
			var free []string
			for _, node := range scenario.tc.Nodes {
				used := int64(0)
				for _, pod := range scenario.tc.Pods {
					if pod.Spec.NodeName == node.Name {
						used += pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
					}
				}
				free = append(free, node.Name+"="+strings.TrimSuffix(node.Status.Allocatable.Cpu().String(), "")+"-"+itoa(used/1000))
			}
			sort.Strings(free)
			out := rvRun(t, scenario.tc, cfg)
			job := out.jobs["c1/job"]
			t.Logf("%s [%s] class=%s anchorZone=%s minMember=%d", variant, cfg.name, class, anchorZone, job.MinAvailable)
			t.Logf("    nodes (allocatable-used cpu): %v", free)
			t.Logf("    placed=%s", rvPlaced(out.res, "job-"))
			t.Logf("    %s", rvJobState(job))
			t.Logf("    FitError: %s", out.res.fitError["c1/job"])
			t.Logf("    NodesFitErrors: %s", rvTaskErrors(out.res, "c1/job"))
		}
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}
