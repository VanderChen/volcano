/*
Copyright 2026 The Volcano Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package allocate

// PROBE (issue 004, round 5). The hand-built scenarios of
// relations-dev114 (RA1..RA7) on the UNMODIFIED dev-1.14 code: no prototype
// patch, the stock allocate action with its random tie-break. The scenario
// parameters are copied from relScenarios() in relations-dev114; this file
// only needs the helpers of common/ and history/.

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

type rbPolicy struct {
	name   string
	cpu    int
	size   int
	groups int
	tier   int
	pinned bool
}

type rbSpec struct {
	name                  string
	zones, racks, perRack int
	fillers               map[string]int
	pooledRacks           []string
	policies              []rbPolicy
	affinity, anti        []schedulingv1.SubGroupAffinityTerm
	feasible              bool
}

func rbTerm(tier int32, groups ...string) schedulingv1.SubGroupAffinityTerm {
	return schedulingv1.SubGroupAffinityTerm{SubGroups: groups, TopologyTier: &tier}
}

func rbBuild(spec rbSpec) *uthelper.TestCommonStruct {
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
	idx, total := 0, 0
	for _, p := range spec.policies {
		size, groups, tier := int32(p.size), int32(p.groups), p.tier
		target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy, schedulingv1.SubGroupPolicySpec{
			Name:           p.name,
			LabelSelector:  &metav1.LabelSelector{MatchLabels: map[string]string{"role": p.name}},
			MatchLabelKeys: []string{"idx"},
			SubGroupSize:   &size,
			MinSubGroups:   &groups,
			NetworkTopology: &schedulingv1.NetworkTopologySpec{
				Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier,
			},
		})
		for g := 0; g < p.groups; g++ {
			for i := 0; i < p.size; i++ {
				var selector map[string]string
				if p.pinned {
					selector = map[string]string{"pool": "x"}
				}
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("job-%s-g%d-p%d", p.name, idx, i), "", v1.PodPending,
					api.BuildResourceList(fmt.Sprint(p.cpu), "1Gi"), "job",
					map[string]string{"volcano.sh/task-spec": p.name, "role": p.name, "idx": fmt.Sprint(idx)}, selector))
				total++
			}
			idx++
		}
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
	target.Spec.MinMember = int32(total)
	pgs = append(pgs, target)
	tc := &uthelper.TestCommonStruct{Name: spec.name, PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

func rbScenarios() []rbSpec {
	one := func(name string, cpu, size, tier int) rbPolicy {
		return rbPolicy{name: name, cpu: cpu, size: size, groups: 1, tier: tier}
	}
	pinned := func(p rbPolicy) rbPolicy { p.pinned = true; return p }
	return []rbSpec{
		{name: "RA1 anti-affinity at tier 1; SubGroup a must span two racks", zones: 1, racks: 3, perRack: 1,
			policies: []rbPolicy{one("a", 6, 2, 2), one("b", 6, 1, 1)}, anti: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "a", "b")}, feasible: true},
		{name: "RA1c control: RA1 without the term", zones: 1, racks: 3, perRack: 1,
			policies: []rbPolicy{one("a", 6, 2, 2), one("b", 6, 1, 1)}, feasible: true},
		{name: "RA2 anti-affinity at tier 1; flexible SubGroup first", zones: 1, racks: 2, perRack: 2,
			fillers: map[string]int{"z0-r00-n0": 2}, pooledRacks: []string{"z0-r00"},
			policies: []rbPolicy{one("x", 6, 1, 1), pinned(one("y", 6, 1, 1))}, anti: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "x", "y")}, feasible: true},
		{name: "RA2c control: RA2 with the pinned SubGroup first", zones: 1, racks: 2, perRack: 2,
			fillers: map[string]int{"z0-r00-n0": 2}, pooledRacks: []string{"z0-r00"},
			policies: []rbPolicy{pinned(one("y", 6, 1, 1)), one("x", 6, 1, 1)}, anti: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "x", "y")}, feasible: true},
		{name: "RA3 affinity at tier 1 for p,q; third SubGroup r", zones: 1, racks: 2, perRack: 2,
			fillers:  map[string]int{"z0-r00-n0": 6},
			policies: []rbPolicy{one("p", 6, 1, 1), one("q", 6, 1, 1), one("r", 6, 1, 1)}, affinity: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "p", "q")}, feasible: true},
		{name: "RA3c control: RA3 with r first", zones: 1, racks: 2, perRack: 2,
			fillers:  map[string]int{"z0-r00-n0": 6},
			policies: []rbPolicy{one("r", 6, 1, 1), one("p", 6, 1, 1), one("q", 6, 1, 1)}, affinity: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "p", "q")}, feasible: true},
		{name: "RA4 anti-affinity at tier 1 among three replicas of one policy", zones: 1, racks: 3, perRack: 1,
			policies: []rbPolicy{{name: "w", cpu: 6, size: 1, groups: 3, tier: 1}}, anti: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "w")}, feasible: true},
		{name: "RA4n control: three replicas, two racks", zones: 1, racks: 2, perRack: 2,
			policies: []rbPolicy{{name: "w", cpu: 6, size: 1, groups: 3, tier: 1}}, anti: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "w")}, feasible: false},
		{name: "RA6 affinity at tier 2 for p,q; anti-affinity at tier 1 for r,p; r first", zones: 1, racks: 2, perRack: 1,
			policies: []rbPolicy{one("r", 4, 1, 1), one("p", 4, 2, 1), one("q", 4, 1, 1)},
			affinity: []schedulingv1.SubGroupAffinityTerm{rbTerm(2, "p", "q")}, anti: []schedulingv1.SubGroupAffinityTerm{rbTerm(1, "r", "p")}, feasible: true},
		{name: "RA7 control: anti-affinity on a tier that does not exist", zones: 1, racks: 3, perRack: 1,
			policies: []rbPolicy{one("a", 6, 2, 2), one("b", 6, 1, 1)}, anti: []schedulingv1.SubGroupAffinityTerm{rbTerm(0, "a", "b")}, feasible: false},
		{name: "RA5 affinity at tier 2 for p,q; each limited to a rack", zones: 2, racks: 2, perRack: 2,
			fillers:  map[string]int{"z0-r00-n0": 6, "z0-r00-n1": 6, "z0-r01-n0": 2, "z0-r01-n1": 6},
			policies: []rbPolicy{one("p", 6, 1, 1), one("q", 6, 1, 1)}, affinity: []schedulingv1.SubGroupAffinityTerm{rbTerm(2, "p", "q")}, feasible: true},
	}
}

// RB1: the stock action, 20 sessions per scenario because ties are random.
func TestProbeRB1RequiredSubGroupTermsUnmodified(t *testing.T) {
	const sessions = 20
	t.Logf("| scenario | legal placement exists | pods | sessions that scheduled the Job (of %d) | pods placed per session |", sessions)
	t.Logf("|---|---|---|---|---|")
	for _, spec := range rbScenarios() {
		pods, scheduled := 0, 0
		for _, p := range spec.policies {
			pods += p.size * p.groups
		}
		counts := map[int]int{}
		for i := 0; i < sessions; i++ {
			tiers, extra := historyTiers()
			res := runProbe(t, rbBuild(spec), tiers, extra, New(), nil)
			placed := res.count("job-")
			counts[placed]++
			if placed == pods {
				scheduled++
			}
		}
		keys := make([]int, 0, len(counts))
		for placed := range counts {
			keys = append(keys, placed)
		}
		sort.Ints(keys)
		var parts []string
		for _, placed := range keys {
			parts = append(parts, fmt.Sprintf("%d pods x%d", placed, counts[placed]))
		}
		t.Logf("| %s | %t | %d | %d | %s |", spec.name, spec.feasible, pods, scheduled, strings.Join(parts, ", "))
		if !spec.feasible && scheduled > 0 {
			t.Errorf("%s: scheduled although no legal placement exists", spec.name)
		}
	}
}
