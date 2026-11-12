/*
Baseline characterization probes for issue 004. Test-only, see zz_probe_harness_test.go.
These tests describe what the unmodified allocate action does; they log
measurements and assert only facts that the proposal relies on.
*/

package allocate

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/plugins/nodeorder"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

func probeQueue() []*schedulingv1.Queue {
	return []*schedulingv1.Queue{util.BuildQueue("q1", 1, nil)}
}

// P1: how much work does one hard-topology job cost as the number of
// equally feasible candidate racks grows?
func TestProbeP1FanOut(t *testing.T) {
	const tasks, nodesPerRack = 4, 4
	t.Logf("| racks | predicate | nodeOrder | allocate | deallocate | hyperNodeOrder calls | allocate / task |")
	t.Logf("|---|---|---|---|---|---|---|")
	for _, racks := range []int{1, 2, 4, 8, 16, 32} {
		topo := buildProbeTopo(1, racks, nodesPerRack, "8", "16Gi", nil)
		pg, pods := hardJob("job", tasks, "8", "16Gi", 1, nil)
		tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("racks-%d", racks), PodGroups: []*schedulingv1.PodGroup{pg}, Pods: pods, Queues: probeQueue()}
		topo.apply(tc)
		res := runProbe(t, tc, probeTiers(), nil, nil, nil)
		if got := res.count("job-"); got != tasks {
			t.Fatalf("racks=%d: placed %d tasks, want %d", racks, got, tasks)
		}
		if used := res.racks("job-"); len(used) != 1 {
			t.Fatalf("racks=%d: job spans racks %v, want exactly one", racks, used)
		}
		c := res.counters
		t.Logf("| %d | %d | %d | %d | %d | %d | %.1f |", racks, c.predicate.Load(), c.nodeOrder.Load(), c.allocate.Load(),
			c.deallocate.Load(), len(c.hyperNodeOrderCohorts), float64(c.allocate.Load())/tasks)
	}
}

// P1c: job-level x subJob-level fan-out. Two SubGroups, each needs a whole
// rack; the job must stay within one zone.
func TestProbeP1cNestedFanOut(t *testing.T) {
	const nodesPerRack = 2
	t.Logf("| zones x racks | predicate | allocate | deallocate | hyperNodeOrder calls | allocate / task |")
	t.Logf("|---|---|---|---|---|---|")
	for _, shape := range [][2]int{{1, 2}, {2, 2}, {2, 4}, {4, 4}, {4, 8}} {
		zones, racks := shape[0], shape[1]
		topo := buildProbeTopo(zones, racks, nodesPerRack, "8", "16Gi", nil)
		pg := util.BuildPodGroupWithSubGroupPolicy("job", "c1", "", "q1", 4, nil, schedulingv1.PodGroupInqueue, "hard", 2,
			[]schedulingv1.SubGroupPolicySpec{util.BuildSubGroupPolicyWithSubGroupSize("worker", []string{"volcano.sh/task-spec"}, "hard", 1, 2)})
		var pods []*v1.Pod
		for g := 0; g < 2; g++ {
			for i := 0; i < 2; i++ {
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("job-g%d-p%d", g, i), "", v1.PodPending, api.BuildResourceList("8", "16Gi"), "job",
					map[string]string{"volcano.sh/task-spec": fmt.Sprintf("worker-%d", g)}, nil))
			}
		}
		tc := &uthelper.TestCommonStruct{Name: fmt.Sprintf("%dx%d", zones, racks), PodGroups: []*schedulingv1.PodGroup{pg}, Pods: pods, Queues: probeQueue()}
		topo.apply(tc)
		res := runProbe(t, tc, probeTiers(), nil, nil, nil)
		if got := res.count("job-"); got != 4 {
			t.Fatalf("%dx%d: placed %d tasks, want 4 (fitErr=%q)", zones, racks, got, res.fitError["c1/job"])
		}
		c := res.counters
		t.Logf("| %dx%d | %d | %d | %d | %d | %.1f |", zones, racks, c.predicate.Load(), c.allocate.Load(), c.deallocate.Load(),
			len(c.hyperNodeOrderCohorts), float64(c.allocate.Load())/4)
	}
}

// P2: the HyperNode score is computed from session state in which none of the
// job's tasks is allocated, so the same ranking is available before any dry-run.
func TestProbeP2ScoreKnownBeforeDryRun(t *testing.T) {
	const racks, nodesPerRack, tasks = 6, 4, 4
	topo := buildProbeTopo(1, racks, nodesPerRack, "16", "32Gi", nil)
	// rack i (i<=4) carries i running 8-CPU pods, one per node; rack 5 is empty.
	var occupied []string
	for r := 0; r <= 4; r++ {
		for n := 0; n < r; n++ {
			occupied = append(occupied, nodeName(0, r, n))
		}
	}
	fillerPG, fillers := fillerPods(occupied, "8", "16Gi")
	pg, pods := hardJob("job", tasks, "8", "16Gi", 1, nil)
	tc := &uthelper.TestCommonStruct{Name: "pre-score", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG}, Pods: append(pods, fillers...), Queues: probeQueue()}
	topo.apply(tc)

	preScore := map[string]float64{}
	singleton := map[string]float64{}
	res := runProbe(t, tc, probeTiers(), nil, nil, func(ssn *framework.Session) {
		job := ssn.Jobs["c1/job"]
		subJob := job.SubJobs[job.DefaultSubJobID()]
		all := map[string][]*api.NodeInfo{}
		for _, rack := range topo.racks {
			all[rack] = ssn.RealNodesList[rack]
		}
		sum := func(scores map[string]map[string]float64, out map[string]float64) {
			for _, perPlugin := range scores {
				for hn, s := range perPlugin {
					out[hn] += s
				}
			}
		}
		scores, err := ssn.HyperNodeOrderMapFn(subJob, all)
		if err != nil {
			t.Fatal(err)
		}
		sum(scores, preScore)
		for _, rack := range topo.racks {
			one, err := ssn.HyperNodeOrderMapFn(subJob, map[string][]*api.NodeInfo{rack: ssn.RealNodesList[rack]})
			if err != nil {
				t.Fatal(err)
			}
			sum(one, singleton)
		}
	})

	best, bestScore := "", -1.0
	for _, rack := range topo.racks {
		t.Logf("rack=%s preScore(all-cohort)=%.2f preScore(singleton)=%.2f", rack, preScore[rack], singleton[rack])
		if preScore[rack] != singleton[rack] {
			t.Errorf("rack %s: score depends on cohort: all=%v singleton=%v", rack, preScore[rack], singleton[rack])
		}
		if preScore[rack] > bestScore {
			best, bestScore = rack, preScore[rack]
		}
	}
	chosen := res.racks("job-")
	t.Logf("chosen=%v argmax(preScore)=%s counters: %s", chosen, best, res.counters)
	if len(chosen) != 1 || chosen[0] != best {
		t.Errorf("chosen rack %v differs from argmax of the pre-dry-run score %s", chosen, best)
	}
	for i, allocated := range res.counters.allocatedWhenScored {
		if allocated != 0 {
			t.Errorf("HyperNodeOrderFn call %d ran with %d allocated tasks; score would depend on dry-run state", i, allocated)
		}
	}
	sizes := map[int]int{}
	for _, cohort := range res.counters.hyperNodeOrderCohorts {
		sizes[len(cohort)]++
	}
	t.Logf("HyperNodeOrderFn cohort sizes (size -> calls): %v", sizes)
}

// P3: identical candidates. Which rack wins, and is it the same every time?
func TestProbeP3TieBreakDeterminism(t *testing.T) {
	const runs = 40
	hist := map[string]int{}
	for i := 0; i < runs; i++ {
		topo := buildProbeTopo(1, 4, 2, "8", "16Gi", nil)
		pg, pods := hardJob("job", 2, "8", "16Gi", 1, nil)
		tc := &uthelper.TestCommonStruct{Name: "tie", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: pods, Queues: probeQueue()}
		topo.apply(tc)
		res := runProbe(t, tc, probeTiers(), nil, nil, nil)
		hist[strings.Join(res.racks("job-"), "+")]++
	}
	t.Logf("identical racks, %d sessions, chosen rack histogram: %v (distinct=%d)", runs, hist, len(hist))
}

func preferNodes(key, value string, weight int32) *v1.Affinity {
	return &v1.Affinity{NodeAffinity: &v1.NodeAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []v1.PreferredSchedulingTerm{{
			Weight: weight,
			Preference: v1.NodeSelectorTerm{MatchExpressions: []v1.NodeSelectorRequirement{
				{Key: key, Operator: v1.NodeSelectorOpIn, Values: []string{value}},
			}},
		}},
	}}
}

// P4: node-level preferences (nodeorder preferred NodeAffinity) versus the
// choice of domain for a hard-topology job.
func TestProbeP4NodePreferenceAcrossDomains(t *testing.T) {
	enabled := true
	nodeOrderOpt := conf.PluginOption{Name: nodeorder.PluginName, EnabledNodeOrder: &enabled,
		Arguments: map[string]interface{}{"nodeaffinity.weight": 100}}
	extra := map[string]framework.PluginBuilder{nodeorder.PluginName: nodeorder.New}
	labelRack1 := func(_, rack, _ int, n *v1.Node) {
		if rack == 1 {
			n.Labels["pref"] = "yes"
		}
	}
	withAffinity := func(_ int, pod *v1.Pod) { pod.Spec.Affinity = preferNodes("pref", "yes", 100) }

	const runs = 40
	run := func(name string, fill bool) map[string]int {
		hist := map[string]int{}
		for i := 0; i < runs; i++ {
			topo := buildProbeTopo(1, 2, 2, "16", "32Gi", labelRack1)
			pg, pods := hardJob("job", 2, "8", "16Gi", 1, withAffinity)
			pgs := []*schedulingv1.PodGroup{pg}
			if fill {
				// rack 0 is half used, so its domain bin-packing score is higher.
				fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0), nodeName(0, 0, 1)}, "8", "16Gi")
				pgs = append(pgs, fillerPG)
				pods = append(pods, fillers...)
			}
			tc := &uthelper.TestCommonStruct{Name: name, PodGroups: pgs, Pods: pods, Queues: probeQueue()}
			topo.apply(tc)
			res := runProbe(t, tc, probeTiers(nodeOrderOpt), extra, nil, nil)
			hist[strings.Join(res.racks("job-"), "+")]++
		}
		return hist
	}
	t.Logf("all preferred nodes are in z0-r01; nodeorder nodeaffinity.weight=100")
	t.Logf("(a) racks otherwise identical      -> chosen rack histogram over %d sessions: %v", runs, run("identical", false))
	t.Logf("(b) z0-r00 half used (binpack win) -> chosen rack histogram over %d sessions: %v", runs, run("binpack", true))
}

// P5: what does the user see when no rack can host the job, and the racks
// fail for different reasons?
func TestProbeP5FailureExplanation(t *testing.T) {
	// 3 racks x 4 nodes x 8 CPU; job = 2 pods x 8 CPU, hard tier 1.
	// r00: every node tainted NoSchedule            -> predicates reject
	// r01: 6 CPU used on every node (idle 8 total)  -> aggregated minResource rejects
	// r02: 4 CPU used on every node (idle 16 total) -> aggregate passes, no node fits 8 CPU
	topo := buildProbeTopo(1, 3, 4, "8", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Spec.Taints = []v1.Taint{{Key: "dedicated", Value: "other", Effect: v1.TaintEffectNoSchedule}}
		}
	})
	var r01, r02 []string
	for n := 0; n < 4; n++ {
		r01 = append(r01, nodeName(0, 1, n))
		r02 = append(r02, nodeName(0, 2, n))
	}
	fillerPG, fillers := fillerPods(r01, "6", "1Gi")
	_, fillers2 := fillerPods(r02, "4", "1Gi")
	for i, pod := range fillers2 {
		pod.Name = fmt.Sprintf("filler-b-%03d", i)
		pod.UID = "uid-" + pod.UID + "-b"
	}
	pg, pods := hardJob("job", 2, "8", "1Gi", 1, nil)
	// vcjob sets minResources; it enables the aggregated resource pre-filter.
	minResources := api.BuildResourceList("16", "2Gi")
	pg.Spec.MinResources = &minResources
	pods = append(append(pods, fillers...), fillers2...)
	tc := &uthelper.TestCommonStruct{Name: "explain", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG}, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	res := runProbe(t, tc, probeTiers(), nil, nil, nil)
	if got := res.count("job-"); got != 0 {
		t.Fatalf("expected the job to be unschedulable, placed %d tasks", got)
	}
	t.Logf("JobFitErrors: %s", res.jobFitErrors["c1/job"])
	t.Logf("FitError():   %s", res.fitError["c1/job"])
	names := make([]string, 0, len(res.nodesFitErrors["c1/job"]))
	for name := range res.nodesFitErrors["c1/job"] {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Logf("NodesFitErrors[%s]: %s", name, res.nodesFitErrors["c1/job"][name])
	}
	t.Logf("counters: %s", res.counters)
}
