/*
Round-5 probe for issue 004: how the weights of the two topology plugins
combine when a domain is chosen. Needs probes/common, probes/history,
probes/prototype, probes/prototype2. Runs on both baselines.
*/

package allocate

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	grouptopologyaffinity "volcano.sh/volcano/pkg/scheduler/plugins/group-topology-affinity"
	networktopologyaware "volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// weightTiers is the probe configuration with explicit plugin weights. With
// cpuOnly the memory term of the bin-packing score is switched off so that
// the expected score depends on CPU only; otherwise the resource weights keep
// their defaults (cpu 1, memory 1).
func weightTiers(ntaWeight, gtaWeight int, cpuOnly bool) ([]conf.Tier, map[string]framework.PluginBuilder) {
	tiers, builders := historyTiers()
	for i := range tiers[0].Plugins {
		switch tiers[0].Plugins[i].Name {
		case networktopologyaware.PluginName:
			tiers[0].Plugins[i].Arguments = map[string]interface{}{"weight": ntaWeight}
			if cpuOnly {
				tiers[0].Plugins[i].Arguments["hypernode.binpack.memory"] = 0
			}
		case grouptopologyaffinity.PluginName:
			tiers[0].Plugins[i].Arguments = map[string]interface{}{"weight": gtaWeight}
		}
	}
	return tiers, builders
}

// weightScenario: two racks of two 8-CPU nodes. A peer PodGroup uses 4 CPU in
// rack r00. The Job (2 pods x 2 CPU, one rack) prefers not to share a rack
// with the peer, with the given term weight.
//
//	bin-packing   r00: (4+4)/16 = 0.50   r01: (0+4)/16 = 0.25
//	anti-affinity r00: 1 - term/100      r01: 1
func weightScenario(termWeight int32) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 2, "8", "64Gi", nil)
	peer := util.BuildPodGroup("peer", "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
	peer.Labels = map[string]string{groupLabel: groupValue}
	pods := []*v1.Pod{util.BuildPod("c1", "peer-0", nodeName(0, 0, 0), v1.PodRunning, api.BuildResourceList("4", "1Gi"), "peer",
		map[string]string{"volcano.sh/task-spec": "peer"}, nil)}
	job, jobPods := hardJob("job", 2, "2", "1Gi", 1, nil)
	job.Labels = map[string]string{groupLabel: groupValue}
	tier := int32(1)
	job.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
		Preferred: []schedulingv1.PodGroupAffinityTerm{{
			Weight:           termWeight,
			PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{groupLabel: groupValue}},
			TopologyTier:     &tier,
		}},
	}}
	tc := &uthelper.TestCommonStruct{Name: "weights", PodGroups: []*schedulingv1.PodGroup{peer, job}, Pods: append(pods, jobPods...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

func chosenRack(res *probeResult) string {
	racks := res.racks("job-")
	if len(racks) == 0 {
		return "-"
	}
	return strings.Join(racks, ",")
}

// WT1: the rack with the peer wins while the bin-packing advantage outweighs
// the anti-affinity penalty: 25*w_nta > w_gta*term (memory weight 0).
func TestWeightsWT1PluginWeightsDecideTheDomain(t *testing.T) {
	weightTable(t, true, 0.50, 0.25, []weightCase{
		{1, 1, 10}, {1, 1, 30}, {1, 1, 100}, {4, 1, 50}, {4, 2, 100}, {10, 1, 100}, {1, 10, 10}, {4, 1, 100},
	})
}

// WT2: the same scenario with the default resource weights (cpu 1, memory 1).
// Memory is nearly unused (r00: 3Gi of 128Gi, r01: 2Gi of 128Gi), so it
// halves the bin-packing term: 12.9*w_nta > w_gta*term.
func TestWeightsWT2DefaultResourceWeights(t *testing.T) {
	weightTable(t, false, (0.50+3.0/128)/2, (0.25+2.0/128)/2, []weightCase{
		{1, 1, 10}, {1, 1, 30}, {4, 1, 50}, {4, 1, 60}, {4, 1, 100}, {10, 1, 100},
	})
}

type weightCase struct {
	nta, gta int
	term     int32
}

func weightTable(t *testing.T, cpuOnly bool, b00, b01 float64, cases []weightCase) {
	t.Logf("| w_nta | w_gta | term weight | score r00 (with peer) | score r01 | expected | current flow | ranked flow | terms recorded for r00 / r01 |")
	t.Logf("|---|---|---|---|---|---|---|---|---|")
	for _, c := range cases {
		r00 := float64(c.nta)*100*b00 + float64(c.gta)*float64(100-c.term)
		r01 := float64(c.nta)*100*b01 + float64(c.gta)*100
		expected := "z0-r00"
		switch {
		case r01 > r00:
			expected = "z0-r01"
		case r01 == r00:
			expected = "tie"
		}
		tiers, builders := weightTiers(c.nta, c.gta, cpuOnly)
		current := runProbe(t, weightScenario(c.term), tiers, builders, modeExhaustiveDet.action(), nil)
		tiers, builders = weightTiers(c.nta, c.gta, cpuOnly)
		action := modeRankedFit.action()
		ranked := runProbe(t, weightScenario(c.term), tiers, builders, action, nil)
		terms := map[string]string{}
		for _, record := range action.trace.Records {
			for _, candidate := range record.Candidates {
				names := make([]string, 0, len(candidate.Terms))
				for name := range candidate.Terms {
					names = append(names, name)
				}
				sort.Strings(names)
				var parts []string
				for _, name := range names {
					parts = append(parts, fmt.Sprintf("%s=%.1f", name, candidate.Terms[name]))
				}
				terms[candidate.HyperNode] = fmt.Sprintf("%.1f (%s)", candidate.Score, strings.Join(parts, " + "))
				want := map[string]float64{"z0-r00": r00, "z0-r01": r01}[candidate.HyperNode]
				if diff := candidate.Score - want; diff > 1e-6 || diff < -1e-6 {
					t.Errorf("w_nta=%d w_gta=%d term=%d: %s scored %.4f, the formula gives %.4f", c.nta, c.gta, c.term, candidate.HyperNode, candidate.Score, want)
				}
			}
		}
		got, gotRanked := chosenRack(current), chosenRack(ranked)
		t.Logf("| %d | %d | %d | %.1f | %.1f | %s | %s | %s | %s / %s |", c.nta, c.gta, c.term, r00, r01, expected, got, gotRanked, terms["z0-r00"], terms["z0-r01"])
		if expected != "tie" && (got != expected || gotRanked != expected) {
			t.Errorf("w_nta=%d w_gta=%d term=%d: expected %s, current flow chose %s, ranked flow chose %s", c.nta, c.gta, c.term, expected, got, gotRanked)
		}
		if got != gotRanked {
			t.Errorf("w_nta=%d w_gta=%d term=%d: current flow chose %s, ranked flow chose %s", c.nta, c.gta, c.term, got, gotRanked)
		}
	}
}
