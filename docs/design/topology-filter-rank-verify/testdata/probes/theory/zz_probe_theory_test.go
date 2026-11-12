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

import (
	"fmt"
	"math/rand"
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

// genHomogeneousScenario builds one Job of the class for which THEORY.md
// claims the search is exact: every task has the same request, every SubGroup
// has the same size, SubGroup tier limits may differ, the Job may carry a
// Job-level hard tier and a required PodGroup anti-affinity term.
// order decides the match index of the SubGroups and therefore the order the
// scheduler handles them in: "narrow" = narrowest limit first, "wide" =
// widest limit first, "random" = shuffled.
// hard makes the scenarios tighter: there is always a peer PodGroup, the Job
// usually carries the anti-affinity term, nodes are fuller, and the Job always
// has SubGroups with both tier limits.
func genHomogeneousScenario(seed int64, order string, hard bool) *singleJobScenario {
	rng := rand.New(rand.NewSource(seed*7919 + 3))
	if hard {
		rng = rand.New(rand.NewSource(seed*104729 + 11))
	}
	zones, racks, perRack := 2+rng.Intn(2), 2+rng.Intn(2), 1+rng.Intn(2)
	topo := buildProbeTopo(zones, racks, perRack, "8", "64Gi", nil)
	for _, node := range topo.nodes {
		cpu := []int{8, 16}[rng.Intn(2)]
		alloc := api.BuildResourceList(fmt.Sprint(cpu), "64Gi", []api.ScalarResource{{Name: "pods", Value: "110"}}...)
		node.Status.Allocatable, node.Status.Capacity = alloc, alloc
	}
	pgs := []*schedulingv1.PodGroup{util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)}
	var pods []*v1.Pod
	for i, node := range topo.nodes {
		if rng.Intn(10) < 4 && !(hard && rng.Intn(2) == 0) {
			continue
		}
		cpu := []int{2, 4, 6}[rng.Intn(3)]
		pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%03d", i), node.Name, v1.PodRunning, api.BuildResourceList(fmt.Sprint(cpu), "1Gi"),
			"filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
	}

	var summary []string
	hasAnchor := rng.Intn(2) == 0 || hard
	if hasAnchor {
		anchorZone := rng.Intn(zones)
		anchor := util.BuildPodGroup("anchor", "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
		anchor.Labels = map[string]string{groupLabel: groupValue}
		pgs = append(pgs, anchor)
		pods = append(pods, util.BuildPod("c1", "anchor-0", nodeName(anchorZone, rng.Intn(racks), 0), v1.PodRunning, api.BuildResourceList("1", "1Gi"),
			"anchor", map[string]string{"volcano.sh/task-spec": "anchor"}, nil))
		summary = append(summary, fmt.Sprintf("anchor in z%d", anchorZone))
	}
	target := util.BuildPodGroup("job", "c1", "q1", 0, nil, schedulingv1.PodGroupInqueue)
	target.Labels = map[string]string{groupLabel: groupValue}
	if hasAnchor && (rng.Intn(5) < 3 || hard && rng.Intn(2) == 0) {
		tierTwo := int32(2)
		target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
			Required: []schedulingv1.PodGroupAffinityTerm{{
				PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{groupLabel: groupValue}},
				TopologyTier:     &tierTwo,
			}},
		}}
		summary = append(summary, "required PAA tier 2")
	}
	if jobTier := []int{0, 0, 2, 3}[rng.Intn(4)]; jobTier > 0 {
		tier := jobTier
		target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier}
		summary = append(summary, fmt.Sprintf("job hard tier %d", jobTier))
	}

	cpu := []int{2, 4, 8}[rng.Intn(3)]
	var jobPods []*v1.Pod
	if rng.Intn(4) < 3 || hard {
		size := 1 + rng.Intn(2)
		var policies []sgPolicy
		count := 1 + rng.Intn(3)
		if hard && count < 2 {
			count = 2
		}
		for i := 0; i < count; i++ {
			p := sgPolicy{name: fmt.Sprintf("p%d", i), cpu: cpu, size: size, groups: 1 + rng.Intn(2), tier: []int{1, 1, 2}[rng.Intn(3)]}
			if hard && i < 2 {
				p.tier = 1 + i
			}
			policies = append(policies, p)
			target.Spec.SubGroupPolicy = append(target.Spec.SubGroupPolicy, subGroupPolicySpec(p))
			summary = append(summary, fmt.Sprintf("%s: %d groups x %d x %dcpu tier %d", p.name, p.groups, p.size, p.cpu, p.tier))
		}
		perm := rng.Perm(16)
		switch order {
		case "narrow":
			sort.SliceStable(policies, func(i, j int) bool { return policies[i].tier < policies[j].tier })
			sort.Ints(perm)
		case "wide":
			sort.SliceStable(policies, func(i, j int) bool { return policies[i].tier > policies[j].tier })
			sort.Ints(perm)
		}
		next := 0
		jobPods = subGroupPods("job", policies, func() int { next++; return perm[next-1] })
	} else {
		tasks := 2 + rng.Intn(4)
		for i := 0; i < tasks; i++ {
			jobPods = append(jobPods, util.BuildPod("c1", fmt.Sprintf("job-plain-p%d", i), "", v1.PodPending, api.BuildResourceList(fmt.Sprint(cpu), "1Gi"),
				"job", map[string]string{"volcano.sh/task-spec": "worker"}, nil))
		}
		summary = append(summary, fmt.Sprintf("plain %d x %dcpu", tasks, cpu))
	}
	target.Spec.MinMember = int32(len(jobPods))
	pgs = append(pgs, target)
	pods = append(pods, jobPods...)
	tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("homogeneous-%d", seed), PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return &singleJobScenario{tc: tc, pods: len(jobPods),
		summary: fmt.Sprintf("topo %dx%dx%d; %s", zones, racks, perRack, strings.Join(summary, "; "))}
}

// TH1 checks Theorems 4 and 5 of THEORY.md against the scheduler itself.
// Theorem 4: with the usable region and SubGroups handled narrowest limit
// first, the normal search schedules the Job iff a legal placement exists, in
// the narrowest possible tier. Theorem 5: from any SubGroup order, the retry
// reaches the same answer when the budget is large enough.
func TestTheoryTH1HomogeneousClassIsExact(t *testing.T) { theoryExactness(t, 2000, false) }

// TH2 is TH1 on the tighter family, where most feasible scenarios need the
// usable region, a particular SubGroup order, or both.
func TestTheoryTH2HomogeneousClassHardFamily(t *testing.T) { theoryExactness(t, 2000, true) }

func theoryExactness(t *testing.T, scenarios int64, hard bool) {
	type config struct {
		name   string
		mode   protoMode
		r      round2
		order  string
		exact  bool // assert: scheduled iff the oracle finds a placement
		tierOK bool // assert: Jobs with a Job-level hard tier land in the narrowest tier
	}
	configs := []config{
		{name: "current search, narrowest limit first", mode: modeExhaustiveDet, order: "narrow"},
		{name: "ranked, no region, narrowest limit first", mode: modeRankedFit, order: "narrow"},
		{name: "ranked + region, narrowest limit first, no retry (Thm 4)", mode: modeRankedFit, r: round2{scope: true}, order: "narrow", exact: true, tierOK: true},
		{name: "ranked + region, widest limit first, no retry", mode: modeRankedFit, r: round2{scope: true}, order: "wide"},
		{name: "ranked + region, widest limit first, retry 4", mode: modeRankedFit, r: round2{scope: true, budget: 4}, order: "wide"},
		{name: "ranked + region, widest limit first, retry 64 (Thm 5)", mode: modeRankedFit, r: round2{scope: true, budget: 64}, order: "wide", exact: true},
		{name: "ranked + region, random order, no retry", mode: modeRankedFit, r: round2{scope: true}, order: "random"},
		{name: "ranked + region, random order, retry 4", mode: modeRankedFit, r: round2{scope: true, budget: 4}, order: "random"},
		{name: "ranked + region, random order, retry 64 (Thm 5)", mode: modeRankedFit, r: round2{scope: true, budget: 64}, order: "random", exact: true},
	}
	type tally struct{ scheduled, missed, wider, retries, mostRetries int }
	tallies := make([]tally, len(configs))
	feasible, mixedLimits, hardFeasible := 0, 0, 0
	for seed := int64(1); seed <= scenarios; seed++ {
		reference := genHomogeneousScenario(seed, "narrow", hard)
		problem := extractOracleProblem(reference.tc)
		best := problem.minTier()
		if best > 0 {
			feasible++
			if problem.jobTier > 0 {
				hardFeasible++
			}
			tiers := map[int]bool{}
			for _, pod := range problem.pods {
				if pod.group != "" {
					tiers[pod.tier] = true
				}
			}
			if len(tiers) > 1 {
				mixedLimits++
			}
		}
		for i, cfg := range configs {
			scenario := genHomogeneousScenario(seed, cfg.order, hard)
			res, action := runRound2(t, scenario.tc, cfg.mode, cfg.r, func(a *Action) { a.repairOrder = "reserve" }, nil)
			placement := jobPlacement(res)
			if msg := illegalPlacement(scenario.tc, placement); msg != "" {
				t.Errorf("seed %d %s: illegal placement: %s (%s)", seed, cfg.name, msg, scenario.summary)
			}
			tl := &tallies[i]
			tl.retries += len(action.trace.Repairs)
			if len(action.trace.Repairs) > tl.mostRetries {
				tl.mostRetries = len(action.trace.Repairs)
			}
			switch {
			case len(placement) > 0 && best == 0:
				t.Errorf("seed %d %s: scheduled although the oracle finds no legal placement (%s)", seed, cfg.name, scenario.summary)
			case len(placement) > 0:
				tl.scheduled++
				if problem.jobTier > 0 && placementTier(placement) > best {
					tl.wider++
					if cfg.tierOK {
						t.Errorf("seed %d %s: tier %d, narrowest possible %d (%s)", seed, cfg.name, placementTier(placement), best, scenario.summary)
					}
				}
			case best > 0:
				tl.missed++
				if cfg.exact {
					t.Errorf("seed %d %s: a legal placement exists but the Job is not scheduled (%s): %s", seed, cfg.name, scenario.summary,
						shortFitError(res.fitError["c1/job"]))
				}
			}
		}
	}
	t.Logf("%d scenarios of the homogeneous class (hard family: %t); oracle: %d have a legal placement (%d with SubGroups of different tier limits, %d with a Job-level hard tier)",
		scenarios, hard, feasible, mixedLimits, hardFeasible)
	t.Logf("| configuration | scheduled | missed | wider tier than needed | retries | most retries for one Job |")
	t.Logf("|---|---|---|---|---|---|")
	for i, cfg := range configs {
		tl := tallies[i]
		t.Logf("| %s | %d | %d | %d | %d | %d |", cfg.name, tl.scheduled, tl.missed, tl.wider, tl.retries, tl.mostRetries)
	}
}
