package allocate

import (
	"testing"

	v1 "k8s.io/api/core/v1"

	"volcano.sh/volcano/pkg/scheduler/uthelper"
)

// RB1 (dimension 3, S8): what the user sees when repair runs and fails.
func TestReviewRB1StatusAfterFailedRepair(t *testing.T) {
	scenarios := []struct {
		name  string
		build func() *uthelper.TestCommonStruct
	}{
		{"017 as archived (a:1 free, b idle, 1 pinned) - repair can succeed", func() *uthelper.TestCommonStruct { return contentionScenario(true) }},
		{"a full, b idle, 1 pinned - infeasible", func() *uthelper.TestCommonStruct { return rvContention(0, 4, 1) }},
		{"a full, b idle, 2 pinned - infeasible", func() *uthelper.TestCommonStruct { return rvContention(0, 4, 2) }},
		{"a:1 free, b:2 free, 2 pinned - infeasible, gate passes", func() *uthelper.TestCommonStruct { return rvContention(1, 2, 2) }},
	}
	for _, sc := range scenarios {
		t.Logf("### %s", sc.name)
		for _, budget := range []int{0, 1, 4} {
			cfg := rvOffDet
			if budget > 0 {
				cfg = rvOffDet.repair(budget)
			}
			out := rvRun(t, sc.build(), cfg)
			t.Logf("  budget=%d placed=%s", budget, rvPlaced(out.res, "job-"))
			t.Logf("    FitError      : %s", out.res.fitError["c1/job"])
			t.Logf("    JobFitErrors  : %q", out.res.jobFitErrors["c1/job"])
			t.Logf("    NodesFitErrors: %s", rvTaskErrors(out.res, "c1/job"))
			t.Logf("    counters      : %s", out.res.counters)
			for _, repair := range out.action.trace.Repairs {
				t.Logf("    repair        : %s", repair)
			}
			t.Logf("    budgetExhausted(trace only): %v", out.action.trace.RepairBudgetExhausted)
		}
	}
}

// RB2 (dimension 3, S7): messages when the region helps, is empty, or misses every container.
func TestReviewRB2StatusRegion(t *testing.T) {
	scenarios := []struct {
		name  string
		build func() *uthelper.TestCommonStruct
		cfgs  []rvCfg
	}{
		{"(i) HB2 becomes schedulable through the region", func() *uthelper.TestCommonStruct { return buildBCScenario(bcVariants[1]) }, []rvCfg{rvOffDet, rvA, rvB}},
		{"(iii) control: job hard tier 2 + PAA, needs two tier-2 domains", func() *uthelper.TestCommonStruct { return buildBCScenario(bcVariants[8]) }, []rvCfg{rvOffDet, rvB, rvB.repair(4)}},
		{"(iii) control: PAA, 5 pods, 4 nodes outside A", func() *uthelper.TestCommonStruct { return buildBCScenario(bcVariants[9]) }, []rvCfg{rvOffDet, rvA, rvB, rvB.repair(4)}},
		{"(iv-a) region empty: peers in A, B and C", func() *uthelper.TestCommonStruct {
			tc := buildBCScenario(bcVariants[1])
			rvAddPeer(tc, "pg-peer-b", nodeName(1, 0, 0), "1")
			rvAddPeer(tc, "pg-peer-c", nodeName(2, 0, 0), "1")
			return tc
		}, []rvCfg{rvOffDet, rvA, rvB}},
		{"(iv-b) region misses the container: job hard tier 2 already runs in A, peer in A", func() *uthelper.TestCommonStruct {
			tc := buildBCScenario(bcVariant{name: "disjoint", pods: 2, jobMode: "hard", jobTier: 2})
			_, pods := rvTarget(tc, "pg-myq", "myq-")
			rvSetRunning(pods[0], nodeName(0, 1, 0))
			return tc
		}, []rvCfg{rvOffDet, rvB}},
	}
	for _, sc := range scenarios {
		t.Logf("### %s", sc.name)
		for _, cfg := range sc.cfgs {
			out := rvRun(t, sc.build(), cfg)
			t.Logf("  [%s] placed=%s", cfg.name, rvPlaced(out.res, "myq-"))
			t.Logf("    FitError      : %s", out.res.fitError["c1/pg-myq"])
			t.Logf("    NodesFitErrors: %s", rvTaskErrors(out.res, "c1/pg-myq"))
			t.Logf("    counters      : %s", out.res.counters)
			if leaked := rvLeak(out.res, "myq-", "z0"); len(leaked) > 0 {
				t.Errorf("%s [%s]: pods placed in excluded domain A: %v", sc.name, cfg.name, leaked)
			}
			for _, pod := range sc.build().Pods {
				_ = pod.Status.Phase == v1.PodPending
			}
		}
	}
}
