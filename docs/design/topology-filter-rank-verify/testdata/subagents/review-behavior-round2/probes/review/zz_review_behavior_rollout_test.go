package allocate

import (
	"fmt"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	"volcano.sh/volcano/pkg/scheduler/uthelper"
)

// RB1c: the PodGroup condition the gang plugin writes after failed repair attempts.
func TestReviewRB1cConditionAfterFailedRepair(t *testing.T) {
	for _, budget := range []int{0, 1, 4} {
		cfg := rvOffDet
		if budget > 0 {
			cfg = rvOffDet.repair(budget)
		}
		out := rvRun(t, rvContention(1, 2, 2), cfg)
		msg := rvCondition(out.jobs["c1/job"])
		if i := strings.Index(msg, " HyperNode:"); i > 0 {
			msg = msg[:i]
		}
		t.Logf("budget=%d placed=%s condition=%q", budget, rvPlaced(out.res, "job-"), msg)
	}
}

// RB3 (dimension 4): what is recorded after the first session.
func TestReviewRB3RecordAfterPlacement(t *testing.T) {
	for _, idx := range []int{0, 1, 2, 3, 6} {
		v := bcVariants[idx]
		t.Logf("### %s", v.name)
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
			out := rvRun(t, buildBCScenario(v), cfg)
			job := out.jobs["c1/pg-myq"]
			t.Logf("  [%s] placed=%s", cfg.name, rvPlaced(out.res, "myq-"))
			t.Logf("      %s recorderDecision=%q", rvJobState(job), out.action.recorder.jobDecisions["c1/pg-myq"])
			if leaked := rvLeak(out.res, "myq-", "z0"); len(leaked) > 0 {
				t.Errorf("%s [%s]: leaked into A: %v", v.name, cfg.name, leaked)
			}
		}
	}
}

// RB4 (dimension 4): the next session / after a scheduler restart. Two pods of
// the target already run on B1 and C1 (as S7 would have placed them), two are pending.
func TestReviewRB4SecondSession(t *testing.T) {
	type sc struct {
		name  string
		build func() *uthelper.TestCommonStruct
	}
	partial := func(v bcVariant, first, second int, mutate func(tc *uthelper.TestCommonStruct)) func() *uthelper.TestCommonStruct {
		return func() *uthelper.TestCommonStruct {
			tc := buildBCScenario(v)
			_, pods := rvTarget(tc, "pg-myq", "myq-")
			rvSetRunning(pods[first], nodeName(1, 0, 0))
			rvSetRunning(pods[second], nodeName(2, 0, 0))
			if mutate != nil {
				mutate(tc)
			}
			return tc
		}
	}
	scenarios := []sc{
		{"HB2-like (no SubGroups): 2 running on B1,C1 + 2 pending, minMember 4", partial(bcVariants[1], 0, 1, nil)},
		{"HB1-like (prefill/decode): prefill-0 on B1, decode-2 on C1 + 2 pending", partial(bcVariants[0], 0, 2, nil)},
		{"HB4-like (job hard tier 3): 2 running on B1,C1 + 2 pending", partial(bcVariants[3], 0, 1, nil)},
		{"excluded set changed: peer now runs in B2; target elastic (minMember 2), 2 pending", partial(bcVariants[1], 0, 1, func(tc *uthelper.TestCommonStruct) {
			pg, _ := rvTarget(tc, "pg-myq", "myq-")
			pg.Spec.MinMember = 2
			rvAddPeer(tc, "pg-peer-b", nodeName(1, 1, 0), "1")
		})},
	}
	for _, s := range scenarios {
		t.Logf("### %s", s.name)
		for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
			out := rvRun(t, s.build(), cfg)
			job := out.jobs["c1/pg-myq"]
			t.Logf("  [%s] atOpen job.AllocatedHyperNode=%q; newly placed=%s", cfg.name, out.ahnAtOpen["c1/pg-myq"], rvPlaced(out.res, "myq-"))
			t.Logf("      after: %s", rvJobState(job))
			t.Logf("      FitError: %s", shortFitError(out.res.fitError["c1/pg-myq"]))
			for _, zone := range []string{"z0"} {
				if leaked := rvLeak(out.res, "myq-", zone); len(leaked) > 0 {
					t.Errorf("%s [%s]: leaked into %s: %v", s.name, cfg.name, zone, leaked)
				}
			}
			if strings.HasPrefix(s.name, "excluded set changed") {
				if leaked := rvLeak(out.res, "myq-", "z1"); len(leaked) > 0 {
					t.Errorf("%s [%s]: new pods entered B after B became excluded: %v", s.name, cfg.name, leaked)
				}
			}
		}
	}
}

// RB5 (dimension 4): the target spans B and C (placed by S7). Can the peer in A
// that carries the same required anti-affinity still add a pod in A?
func TestReviewRB5PeerScaleUp(t *testing.T) {
	build := func() *uthelper.TestCommonStruct {
		tc := buildBCScenario(bcVariants[1])
		target, pods := rvTarget(tc, "pg-myq", "myq-")
		for i, node := range []string{nodeName(1, 0, 0), nodeName(1, 1, 0), nodeName(2, 0, 0), nodeName(2, 1, 0)} {
			rvSetRunning(pods[i], node)
		}
		anchor, _ := rvTarget(tc, "pg-myq-2", "anchor-")
		anchor.Spec.TopologyAffinity = target.Spec.TopologyAffinity.DeepCopy()
		tc.Pods = append(tc.Pods, ascendPod("pg-myq-2", "anchor-1", "", v1.PodPending, "1", map[string]string{"volcano.sh/task-spec": "anchor"}))
		return tc
	}
	for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
		out := rvRun(t, build(), cfg)
		t.Logf("[%s] target atOpen AllocatedHyperNode=%q; peer newly placed=%s; peer FitError=%s", cfg.name,
			out.ahnAtOpen["c1/pg-myq"], rvPlaced(out.res, "anchor-"), shortFitError(out.res.fitError["c1/pg-myq-2"]))
		if got := out.res.count("anchor-"); got != 1 {
			t.Errorf("[%s] peer placed %d pods, want 1", cfg.name, got)
		}
	}
	_ = fmt.Sprint
}
