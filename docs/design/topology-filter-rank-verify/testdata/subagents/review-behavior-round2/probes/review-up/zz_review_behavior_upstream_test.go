/*
Upstream-master variant of the round-2 behavior review probes (scratch copy of
upstream + prototype). Needs probes/common, history, prototype, prototype2.
*/

package allocate

import (
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

func rvCondShort(job *api.JobInfo) string {
	msg := rvCondition(job)
	if i := strings.Index(msg, " HyperNode:"); i > 0 {
		msg = msg[:i]
	}
	return msg
}

// U1 (dimension 3, S8): status after failed repair attempts.
func TestReviewUpStatusAfterFailedRepair(t *testing.T) {
	scenarios := []struct {
		name  string
		build func() *uthelper.TestCommonStruct
	}{
		{"017 as archived", func() *uthelper.TestCommonStruct { return contentionScenario(true) }},
		{"a full, b idle, 2 pinned - infeasible", func() *uthelper.TestCommonStruct { return rvContention(0, 4, 2) }},
		{"a:1 free, b:2 free, 2 pinned - infeasible, gate passes", func() *uthelper.TestCommonStruct { return rvContention(1, 2, 2) }},
	}
	for _, sc := range scenarios {
		t.Logf("### %s", sc.name)
		for _, budget := range []int{0, 1, 4} {
			for _, order := range []string{"", "reserve"} {
				if budget == 0 && order != "" {
					continue
				}
				prev := framework.ProbeScopeUnion
				res, action := runRound2(t, sc.build(), modeExhaustiveDet, round2{budget: budget}, func(a *Action) { a.repairOrder = order }, nil)
				framework.ProbeScopeUnion = prev
				t.Logf("  budget=%d order=%q placed=%s", budget, order, rvPlaced(res, "job-"))
				t.Logf("    FitError      : %s", shortFitError(res.fitError["c1/job"]))
				t.Logf("    NodesFitErrors: %s", rvTaskErrors(res, "c1/job"))
				t.Logf("    counters      : %s", res.counters)
				for _, repair := range action.trace.Repairs {
					t.Logf("    repair        : %s", repair)
				}
				t.Logf("    budgetExhausted(trace only): %v", action.trace.RepairBudgetExhausted)
			}
		}
	}
	for _, budget := range []int{0, 1, 4} {
		cfg := rvOffDet
		if budget > 0 {
			cfg = rvOffDet.repair(budget)
		}
		out := rvRun(t, rvContention(1, 2, 2), cfg)
		t.Logf("condition budget=%d placed=%s: %q", budget, rvPlaced(out.res, "job-"), rvCondShort(out.jobs["c1/job"]))
	}
}

// U2 (dimensions 3, 4): region statuses and records.
func TestReviewUpRegion(t *testing.T) {
	for _, idx := range []int{0, 1, 3, 6, 8, 9} {
		v := bcVariants[idx]
		t.Logf("### %s", v.name)
		for _, cfg := range []rvCfg{rvOffDet, rvB, rvB.repair(4)} {
			out := rvRun(t, buildBCScenario(v), cfg)
			t.Logf("  [%s] placed=%s", cfg.name, rvPlaced(out.res, "myq-"))
			t.Logf("      %s recorderDecision=%q", rvJobState(out.jobs["c1/pg-myq"]), out.action.recorder.jobDecisions["c1/pg-myq"])
			t.Logf("      condition: %q", rvCondShort(out.jobs["c1/pg-myq"]))
			t.Logf("      FitError : %s", out.res.fitError["c1/pg-myq"])
			t.Logf("      counters : %s", out.res.counters)
			if leaked := rvLeak(out.res, "myq-", "z0"); len(leaked) > 0 {
				t.Errorf("%s [%s]: leaked into A: %v", v.name, cfg.name, leaked)
			}
		}
	}
	extra := []struct {
		name  string
		build func() *uthelper.TestCommonStruct
	}{
		{"(iv-a) region empty: peers in A, B and C", func() *uthelper.TestCommonStruct {
			tc := buildBCScenario(bcVariants[1])
			rvAddPeer(tc, "pg-peer-b", nodeName(1, 0, 0), "1")
			rvAddPeer(tc, "pg-peer-c", nodeName(2, 0, 0), "1")
			return tc
		}},
		{"(iv-b) region misses the container: job hard tier 2 already runs in A, peer in A", func() *uthelper.TestCommonStruct {
			tc := buildBCScenario(bcVariant{name: "disjoint", pods: 2, jobMode: "hard", jobTier: 2})
			_, pods := rvTarget(tc, "pg-myq", "myq-")
			rvSetRunning(pods[0], nodeName(0, 1, 0))
			return tc
		}},
		{"no real common root (dc removed): HB1", func() *uthelper.TestCommonStruct { return rvDropRootUp(buildBCScenario(bcVariants[0])) }},
		{"no real common root (dc removed): HB2", func() *uthelper.TestCommonStruct { return rvDropRootUp(buildBCScenario(bcVariants[1])) }},
	}
	for _, sc := range extra {
		t.Logf("### %s", sc.name)
		for _, cfg := range []rvCfg{rvOffDet, rvB} {
			out := rvRun(t, sc.build(), cfg)
			t.Logf("  [%s] placed=%s", cfg.name, rvPlaced(out.res, "myq-"))
			t.Logf("      %s", rvJobState(out.jobs["c1/pg-myq"]))
			t.Logf("      FitError : %s", out.res.fitError["c1/pg-myq"])
			if leaked := rvLeak(out.res, "myq-", "z0"); len(leaked) > 0 {
				t.Errorf("%s [%s]: leaked into A: %v", sc.name, cfg.name, leaked)
			}
		}
	}
}

func rvDropRootUp(tc *uthelper.TestCommonStruct) *uthelper.TestCommonStruct {
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

// U3 (dimension 4): next session with part of the Job already running across B and C.
func TestReviewUpSecondSession(t *testing.T) {
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
	scenarios := []struct {
		name  string
		build func() *uthelper.TestCommonStruct
	}{
		{"HB2-like: 2 running on B1,C1 + 2 pending", partial(bcVariants[1], 0, 1, nil)},
		{"HB1-like: prefill-0 on B1, decode-2 on C1 + 2 pending", partial(bcVariants[0], 0, 2, nil)},
		{"HB4-like (job hard tier 3): 2 running on B1,C1 + 2 pending", partial(bcVariants[3], 0, 1, nil)},
		{"excluded set changed: peer now in B2; target elastic (minMember 2)", partial(bcVariants[1], 0, 1, func(tc *uthelper.TestCommonStruct) {
			pg, _ := rvTarget(tc, "pg-myq", "myq-")
			pg.Spec.MinMember = 2
			rvAddPeer(tc, "pg-peer-b", nodeName(1, 1, 0), "1")
		})},
	}
	for _, s := range scenarios {
		t.Logf("### %s", s.name)
		for _, cfg := range []rvCfg{rvOffDet, rvB} {
			out := rvRun(t, s.build(), cfg)
			t.Logf("  [%s] atOpen job.AllocatedHyperNode=%q; newly placed=%s", cfg.name, out.ahnAtOpen["c1/pg-myq"], rvPlaced(out.res, "myq-"))
			t.Logf("      after: %s", rvJobState(out.jobs["c1/pg-myq"]))
			t.Logf("      FitError: %s", shortFitError(out.res.fitError["c1/pg-myq"]))
			if leaked := rvLeak(out.res, "myq-", "z0"); len(leaked) > 0 {
				t.Errorf("%s [%s]: leaked into A: %v", s.name, cfg.name, leaked)
			}
			if strings.HasPrefix(s.name, "excluded set changed") {
				if leaked := rvLeak(out.res, "myq-", "z1"); len(leaked) > 0 {
					t.Errorf("%s [%s]: new pods entered B after B became excluded: %v", s.name, cfg.name, leaked)
				}
			}
		}
	}
}

// U4 (dimension 5): two pending Jobs that exclude each other.
func TestReviewUpMutualAntiAffinity(t *testing.T) {
	for _, small := range []string{"pg-small", "pg-a-small"} {
		t.Logf("### J1=pg-myq (4 pods, HB1), J2=%s (2 pods)", small)
		for _, cfg := range []rvCfg{rvOffDet, rvB} {
			tc := buildBCScenario(bcVariants[0])
			target, _ := rvTarget(tc, "pg-myq", "myq-")
			pg := util.BuildPodGroup(small, "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
			pg.Labels = map[string]string{groupLabel: groupValue}
			pg.Spec.TopologyAffinity = target.Spec.TopologyAffinity.DeepCopy()
			tc.PodGroups = append(tc.PodGroups, pg)
			for i := 0; i < 2; i++ {
				tc.Pods = append(tc.Pods, ascendPod(small, fmt.Sprintf("small-%d", i), "", v1.PodPending, "6", map[string]string{"volcano.sh/task-spec": "worker"}))
			}
			out := rvRun(t, tc, cfg)
			t.Logf("  [%s] J1 placed=%s | J2 placed=%s", cfg.name, rvPlaced(out.res, "myq-"), rvPlaced(out.res, "small-"))
			t.Logf("      J2 condition: %q", rvCondShort(out.jobs[api.JobID("c1/"+small)]))
		}
	}
}

// U5 (dimension 6): cost of the composition as the number of HyperNodes grows.
func TestReviewUpScopeCostAtScale(t *testing.T) {
	t.Logf("| zones x racks x nodes | HyperNodes | nodes | Job gradient off | Job gradient on | SubJob gradient off | SubJob gradient on | setScope | whole session off | region on |")
	t.Logf("|---|---|---|---|---|---|---|---|---|---|")
	for _, shape := range [][3]int{{5, 20, 2}, {10, 50, 2}, {20, 100, 2}} {
		zones, racks, perRack := shape[0], shape[1], shape[2]
		build := func() *uthelper.TestCommonStruct {
			topo := buildProbeTopo(zones, racks, perRack, "8", "64Gi", nil)
			anchor := util.BuildPodGroup("anchor", "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
			anchor.Labels = map[string]string{groupLabel: groupValue}
			pods := []*v1.Pod{util.BuildPod("c1", "anchor-0", nodeName(0, 0, 0), v1.PodRunning, api.BuildResourceList("1", "1Gi"), "anchor",
				map[string]string{"volcano.sh/task-spec": "anchor"}, nil)}
			target := util.BuildPodGroup("job", "c1", "q1", 4, nil, schedulingv1.PodGroupInqueue)
			target.Labels = map[string]string{groupLabel: groupValue}
			tierTwo := int32(2)
			target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
				Required: []schedulingv1.PodGroupAffinityTerm{{
					PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{groupLabel: groupValue}}, TopologyTier: &tierTwo}}}}
			for i := 0; i < 4; i++ {
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("job-p%d", i), "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
					map[string]string{"volcano.sh/task-spec": "worker"}, nil))
			}
			tc := &uthelper.TestCommonStruct{Name: "scale", PodGroups: []*schedulingv1.PodGroup{anchor, target}, Pods: pods, Queues: probeQueue()}
			topo.apply(tc)
			return tc
		}
		var gradOff, gradOn, subOff, subOn, setScope time.Duration
		hyperNodes := 0
		tiers, extra := historyTiers()
		runProbe(t, build(), tiers, extra, nil, func(ssn *framework.Session) {
			job := ssn.Jobs["c1/job"]
			subJob := job.SubJobs[job.DefaultSubJobID()]
			root := ssn.HyperNodes[framework.ClusterTopHyperNode]
			hyperNodes = len(ssn.HyperNodes)
			previous := framework.ProbeScopeUnion
			defer func() { framework.ProbeScopeUnion = previous }()
			const rounds = 5
			var stats *api.HyperNodeGradientStats
			timeIt := func(fn func()) time.Duration {
				start := time.Now()
				for i := 0; i < rounds; i++ {
					fn()
				}
				return time.Since(start) / rounds
			}
			framework.ProbeScopeUnion = false
			gradOff = timeIt(func() { ssn.HyperNodeGradientForJobFn(job, root, api.PurposeAllocate) })
			subOff = timeIt(func() { ssn.HyperNodeGradientForSubJobFn(subJob, root, api.PurposeAllocate) })
			framework.ProbeScopeUnion = true
			gradOn = timeIt(func() { _, stats = ssn.HyperNodeGradientForJobFn(job, root, api.PurposeAllocate) })
			subOn = timeIt(func() { ssn.HyperNodeGradientForSubJobFn(subJob, root, api.PurposeAllocate) })
			action := New()
			action.session = ssn
			setScope = timeIt(func() { action.setScope(job, stats) })
		})
		var whole []string
		for _, cfg := range []rvCfg{rvOffDet, rvB} {
			start := time.Now()
			out := rvRun(t, build(), cfg)
			whole = append(whole, fmt.Sprintf("%v (placed %d, taskAttempts %d)", time.Since(start).Round(time.Millisecond), out.res.count("job-"), out.res.counters.prePredicate.Load()))
		}
		t.Logf("| %dx%dx%d | %d | %d | %v | %v | %v | %v | %v | %s | %s |", zones, racks, perRack, hyperNodes, zones*racks*perRack,
			gradOff.Round(time.Microsecond), gradOn.Round(time.Microsecond), subOff.Round(time.Microsecond), subOn.Round(time.Microsecond),
			setScope.Round(time.Microsecond), whole[0], whole[1])
	}
}
