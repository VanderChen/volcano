package allocate

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
)

// genReviewScenario derives three classes the round-2 generator does not
// produce from genSingleJobScenario: elastic gangs, soft Job topology, and a
// Job one pod of which already runs.
func genReviewScenario(seed int64) (scenario *singleJobScenario, class string, anchorZone string, hasPAA bool) {
	scenario = genSingleJobScenario(seed)
	tc := scenario.tc
	target, pods := rvTarget(tc, "job", "job-")
	hasPAA = target.Spec.TopologyAffinity != nil
	for _, pod := range tc.Pods {
		if pod.Name == "anchor-0" {
			anchorZone = zoneOfNode(pod.Spec.NodeName)
		}
	}
	switch seed % 3 {
	case 0:
		class = "elastic"
		target.Spec.MinMember = int32((len(pods) + 1) / 2)
		one := int32(1)
		for i := range target.Spec.SubGroupPolicy {
			target.Spec.SubGroupPolicy[i].MinSubGroups = &one
		}
	case 1:
		class = "soft-job"
		if nt := target.Spec.NetworkTopology; nt != nil {
			nt.Mode = schedulingv1.SoftNetworkTopologyMode
		} else {
			tier := 2
			target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.SoftNetworkTopologyMode, HighestTierAllowed: &tier}
		}
	default:
		class = "partly-running"
		free := map[string]int64{}
		nodes := map[string]*v1.Node{}
		for _, node := range tc.Nodes {
			free[node.Name] = node.Status.Allocatable.Cpu().MilliValue()
			nodes[node.Name] = node
		}
		for _, pod := range tc.Pods {
			if pod.Spec.NodeName != "" {
				free[pod.Spec.NodeName] -= pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
			}
		}
		pod := pods[0]
		need := pod.Spec.Containers[0].Resources.Requests.Cpu().MilliValue()
		placed := false
		for _, node := range tc.Nodes {
			if hasPAA && zoneOfNode(node.Name) == anchorZone {
				continue
			}
			ok := free[node.Name] >= need
			for key, value := range pod.Spec.NodeSelector {
				if node.Labels[key] != value {
					ok = false
				}
			}
			if ok {
				rvSetRunning(pod, node.Name)
				placed = true
				break
			}
		}
		if !placed {
			class = "unchanged"
		}
	}
	return scenario, class, anchorZone, hasPAA
}

func rvNewPods(tc *uthelper.TestCommonStruct, res *probeResult) map[string]string {
	return jobPlacement(res)
}

// RB10 (dimension 5): today's search against options a and b on the three classes.
func TestReviewRB10UncoveredClassesRandom(t *testing.T) {
	const scenarios = 450
	configs := []rvCfg{rvA, rvB, rvB.repair(8)}
	type tally struct{ scheduled, gained, lost, fewer, more, moved, illegal, leaked int }
	tallies := map[string][]tally{}
	baseScheduled := map[string]int{}
	total := map[string]int{}
	for seed := int64(1); seed <= scenarios; seed++ {
		scenario, class, anchorZone, hasPAA := genReviewScenario(seed)
		if tallies[class] == nil {
			tallies[class] = make([]tally, len(configs))
		}
		total[class]++
		base := rvRun(t, scenario.tc, rvOffDet)
		basePlacement := rvNewPods(scenario.tc, base.res)
		if len(basePlacement) > 0 {
			baseScheduled[class]++
		}
		for i, cfg := range configs {
			scenario, _, _, _ := genReviewScenario(seed)
			out := rvRun(t, scenario.tc, cfg)
			placement := rvNewPods(scenario.tc, out.res)
			tl := &tallies[class][i]
			if hasPAA && anchorZone != "" {
				for pod, node := range placement {
					if zoneOfNode(node) == anchorZone {
						tl.leaked++
						t.Errorf("seed %d %s %s: %s placed in excluded zone %s (%s)", seed, class, cfg.name, pod, node, scenario.summary)
					}
				}
			}
			if class == "elastic" || class == "soft-job" {
				if msg := illegalPlacement(scenario.tc, placement); msg != "" {
					tl.illegal++
					t.Errorf("seed %d %s %s: illegal placement: %s (%s)", seed, class, cfg.name, msg, scenario.summary)
				}
			}
			if len(placement) > 0 {
				tl.scheduled++
			}
			switch {
			case len(basePlacement) > 0 && len(placement) == 0:
				tl.lost++
				t.Logf("LOST seed %d %s %s (%s)\n  today: %v", seed, class, cfg.name, scenario.summary, basePlacement)
			case len(basePlacement) > 0 && len(placement) < len(basePlacement):
				tl.fewer++
				t.Logf("FEWER seed %d %s %s (%s)\n  today: %v\n  now:   %v", seed, class, cfg.name, scenario.summary, basePlacement, placement)
			case len(basePlacement) > 0 && len(placement) > len(basePlacement):
				tl.more++
			case len(basePlacement) == 0 && len(placement) > 0:
				tl.gained++
			case len(basePlacement) > 0 && !reflect.DeepEqual(basePlacement, placement):
				tl.moved++
				if tl.moved <= 2 && cfg.name == rvA.name {
					t.Logf("MOVED seed %d %s %s (%s)\n  today: %v\n  now:   %v", seed, class, cfg.name, scenario.summary, basePlacement, placement)
				}
			}
		}
	}
	t.Logf("| class | scenarios | today schedules | configuration | scheduled | newly scheduled | lost | fewer pods | more pods | placed differently | illegal | leaked |")
	t.Logf("|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, class := range []string{"elastic", "soft-job", "partly-running", "unchanged"} {
		for i, cfg := range configs {
			if tallies[class] == nil {
				continue
			}
			tl := tallies[class][i]
			t.Logf("| %s | %d | %d | %s | %d | %d | %d | %d | %d | %d | %d | %d |", class, total[class], baseScheduled[class], cfg.name,
				tl.scheduled, tl.gained, tl.lost, tl.fewer, tl.more, tl.moved, tl.illegal, tl.leaked)
		}
	}
	_ = fmt.Sprint
	_ = strings.Join
}
