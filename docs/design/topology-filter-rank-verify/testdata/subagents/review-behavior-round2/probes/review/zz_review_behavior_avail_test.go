package allocate

import (
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// rvAddSmallJob adds a second pending PodGroup with the same required
// anti-affinity: `pods` pods of 6 cards each.
func rvAddSmallJob(tc *uthelper.TestCommonStruct, name string, pods int) {
	target, _ := rvTarget(tc, "pg-myq", "myq-")
	pg := util.BuildPodGroup(name, "c1", "q1", int32(pods), nil, schedulingv1.PodGroupInqueue)
	pg.Labels = map[string]string{groupLabel: groupValue}
	pg.Spec.TopologyAffinity = target.Spec.TopologyAffinity.DeepCopy()
	tc.PodGroups = append(tc.PodGroups, pg)
	for i := 0; i < pods; i++ {
		tc.Pods = append(tc.Pods, ascendPod(name, fmt.Sprintf("small-%d", i), "", v1.PodPending, "6", map[string]string{"volcano.sh/task-spec": "worker"}))
	}
}

// RB6 (dimension 5): two pending Jobs that exclude each other in one session.
// J1 needs B and C together (unschedulable today), J2 fits in one domain.
func TestReviewRB6MutualAntiAffinityInOneSession(t *testing.T) {
	for _, small := range []string{"pg-small", "pg-a-small"} {
		t.Logf("### J1=pg-myq (4 pods, HB2), J2=%s (2 pods)", small)
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
			tc := buildBCScenario(bcVariants[1])
			rvAddSmallJob(tc, small, 2)
			out := rvRun(t, tc, cfg)
			t.Logf("  [%s] J1 placed=%s | J2 placed=%s", cfg.name, rvPlaced(out.res, "myq-"), rvPlaced(out.res, "small-"))
			t.Logf("      J2 FitError: %s", shortFitError(out.res.fitError[apiJobID("c1/"+small)]))
			for _, prefix := range []string{"myq-", "small-"} {
				if leaked := rvLeak(out.res, prefix, "z0"); len(leaked) > 0 {
					t.Errorf("[%s] leaked into A: %v", cfg.name, leaked)
				}
			}
		}
	}
}

// RB7 (dimension 5): classes the random generator does not cover.
func TestReviewRB7UncoveredClasses(t *testing.T) {
	scenarios := []struct {
		name  string
		build func() *uthelper.TestCommonStruct
	}{
		{"elastic HB2: 4 pods, minMember 2", func() *uthelper.TestCommonStruct {
			tc := buildBCScenario(bcVariants[1])
			pg, _ := rvTarget(tc, "pg-myq", "myq-")
			pg.Spec.MinMember = 2
			return tc
		}},
		{"elastic HB1: 4 pods, minMember 2, minSubGroups 1 per policy", func() *uthelper.TestCommonStruct {
			tc := buildBCScenario(bcVariants[0])
			pg, _ := rvTarget(tc, "pg-myq", "myq-")
			pg.Spec.MinMember = 2
			one := int32(1)
			for i := range pg.Spec.SubGroupPolicy {
				pg.Spec.SubGroupPolicy[i].MinSubGroups = &one
			}
			return tc
		}},
		{"soft job tier 2 + PAA, no SubGroups, 4 pods", func() *uthelper.TestCommonStruct {
			return buildBCScenario(bcVariant{name: "soft4", pods: 4, jobMode: "soft", jobTier: 2})
		}},
		{"soft job tier 2 + PAA, no SubGroups, 2 pods", func() *uthelper.TestCommonStruct {
			return buildBCScenario(bcVariant{name: "soft2", pods: 2, jobMode: "soft", jobTier: 2})
		}},
		{"soft job tier 1 + PAA, no SubGroups, 2 pods", func() *uthelper.TestCommonStruct {
			return buildBCScenario(bcVariant{name: "soft1", pods: 2, jobMode: "soft", jobTier: 1})
		}},
		{"no real common root (dc removed): HB1", func() *uthelper.TestCommonStruct { return rvDropRoot(buildBCScenario(bcVariants[0])) }},
		{"no real common root (dc removed): HB2", func() *uthelper.TestCommonStruct { return rvDropRoot(buildBCScenario(bcVariants[1])) }},
	}
	for _, sc := range scenarios {
		t.Logf("### %s", sc.name)
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB, rvB.repair(4)} {
			out := rvRun(t, sc.build(), cfg)
			t.Logf("  [%s] placed=%s", cfg.name, rvPlaced(out.res, "myq-"))
			t.Logf("      %s | %s", rvJobState(out.jobs["c1/pg-myq"]), out.res.counters)
			if leaked := rvLeak(out.res, "myq-", "z0"); len(leaked) > 0 {
				t.Errorf("%s [%s]: leaked into A: %v", sc.name, cfg.name, leaked)
			}
		}
	}
}

// rvDropRoot removes the tier-3 HyperNode "dc" so that B and C have no real common parent.
func rvDropRoot(tc *uthelper.TestCommonStruct) *uthelper.TestCommonStruct {
	delete(tc.HyperNodesMap, "dc")
	delete(tc.HyperNodesSetByTier, 3)
	delete(tc.HyperNodes, "dc")
	for _, hn := range tc.HyperNodesMap {
		if hn.Parent == "dc" {
			hn.Parent = ""
		}
	}
	return tc
}

// RB8 (dimension 6): work done on the success path and on the failure path.
func TestReviewRB8Counters(t *testing.T) {
	t.Logf("| scenario | config | placed | taskAttempts | predicate | allocate | deallocate | hyperNodeOrder |")
	t.Logf("|---|---|---|---|---|---|---|---|")
	for _, idx := range []int{0, 4, 5, 6, 7, 8, 9} {
		v := bcVariants[idx]
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB, rvB.repair(4)} {
			out := rvRun(t, buildBCScenario(v), cfg)
			c := out.res.counters
			t.Logf("| %s | %s | %d/%d | %d | %d | %d | %d | %d |", v.name, cfg.name, out.res.count("myq-"), v.pods,
				c.prePredicate.Load(), c.predicate.Load(), c.allocate.Load(), c.deallocate.Load(), len(c.hyperNodeOrderCohorts))
		}
	}
}
