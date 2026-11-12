/*
Prototype probes for issue 004 that need the upstream-master prototype only:
the optional node-preference rank term and wall-clock benchmarks.
*/

package allocate

import (
	"fmt"
	"reflect"
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

// Q5: scenario of P4. With the node-preference term the domain that holds the
// preferred nodes can win; the weights decide, and the trace shows the terms.
func TestProtoQ5NodePreferenceTerm(t *testing.T) {
	enabled := true
	nodeOrderOpt := conf.PluginOption{Name: nodeorder.PluginName, EnabledNodeOrder: &enabled,
		Arguments: map[string]interface{}{"nodeaffinity.weight": 2, "leastrequested.weight": 0, "balancedresource.weight": 0}}
	extra := map[string]framework.PluginBuilder{nodeorder.PluginName: nodeorder.New}
	labelRack1 := func(_, rack, _ int, n *v1.Node) {
		if rack == 1 {
			n.Labels["pref"] = "yes"
		}
	}
	withAffinity := func(_ int, pod *v1.Pod) { pod.Spec.Affinity = preferNodes("pref", "yes", 100) }

	run := func(fill bool, weight float64) (string, string) {
		topo := buildProbeTopo(1, 2, 2, "16", "32Gi", labelRack1)
		pg, pods := hardJob("job", 2, "8", "16Gi", 1, withAffinity)
		pgs := []*schedulingv1.PodGroup{pg}
		if fill {
			fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0), nodeName(0, 0, 1)}, "8", "16Gi")
			pgs = append(pgs, fillerPG)
			pods = append(pods, fillers...)
		}
		tc := &uthelper.TestCommonStruct{Name: "pref", PodGroups: pgs, Pods: pods, Queues: probeQueue()}
		topo.apply(tc)
		action := modeRanked.action()
		action.nodePreferenceWeight = weight
		res := runProbe(t, tc, probeTiers(nodeOrderOpt), extra, action, nil)
		checkTraceInvariants(t, action.trace)
		jobRecord := ""
		for _, record := range action.trace.Records {
			if strings.HasPrefix(record.Scope, "job ") {
				var rows []string
				for _, c := range record.Candidates {
					rows = append(rows, fmt.Sprintf("#%d %s total=%.1f terms=%v %s", c.Rank, c.HyperNode, c.Score, c.Terms, c.Verdict))
				}
				jobRecord = strings.Join(rows, "; ")
			}
		}
		return strings.Join(res.racks("job-"), "+"), jobRecord
	}

	for _, tc := range []struct {
		name   string
		fill   bool
		weight float64
		want   string
	}{
		{"identical racks, term off", false, 0, ""}, // a tie: decided by the tie key
		{"identical racks, term on", false, 1, "z0-r01"},
		{"z0-r00 half used (binpack 100 vs 50), term off", true, 0, "z0-r00"},
		{"z0-r00 half used (binpack 100 vs 50), term weight 1", true, 1, "z0-r01"},
		{"z0-r00 half used (binpack 100 vs 50), term weight 0.1", true, 0.1, "z0-r00"},
	} {
		got, record := run(tc.fill, tc.weight)
		t.Logf("%-55s -> %s | %s", tc.name, got, record)
		if tc.want != "" && got != tc.want {
			t.Errorf("%s: placed in %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Q7: SubGroup Jobs keep the exhaustive job-level loop because their job-level
// score is only known after the dry-run. How often does verifying only the
// top K candidates of a pre-rank estimate change the result?
func TestProtoQ7SubGroupTopK(t *testing.T) {
	const scenarios = 400
	tiers, extra := fuzzTiers()
	t.Logf("| job-level search for SubGroup jobs | scenarios with a different placement | scenarios placing fewer pods | task attempts | allocate events |")
	t.Logf("|---|---|---|---|---|")
	base := map[int64]map[string]string{}
	for _, k := range []int{0, 1, 2, 3} {
		different, fewer := 0, 0
		var attempts, allocs int64
		for seed := int64(1); seed <= scenarios; seed++ {
			action := modeRanked.action()
			action.subGroupJobTopK = k
			res := runProbe(t, genFuzzScenario(seed).tc, tiers, extra, action, nil)
			attempts += res.counters.prePredicate.Load()
			allocs += res.counters.allocate.Load()
			if k == 0 {
				base[seed] = res.placed
				continue
			}
			if !reflect.DeepEqual(base[seed], res.placed) {
				different++
			}
			if len(res.placed) < len(base[seed]) {
				fewer++
			}
		}
		name := "exhaustive (today's rule)"
		if k > 0 {
			name = fmt.Sprintf("estimate, verify top %d", k)
		}
		t.Logf("| %s | %d / %d | %d | %d | %d |", name, different, scenarios, fewer, attempts, allocs)
	}
}

func benchPlainJobs(jobs int) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(4, 16, 16, "32", "128Gi", nil)
	var pgs []*schedulingv1.PodGroup
	var pods []*v1.Pod
	for j := 0; j < jobs; j++ {
		pg, jobPods := hardJob(fmt.Sprintf("job%03d", j), 8, "4", "8Gi", 1, nil)
		pgs = append(pgs, pg)
		pods = append(pods, jobPods...)
	}
	tc := &uthelper.TestCommonStruct{Name: "bench-plain", PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

func benchSubGroupJobs(jobs int) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(4, 16, 16, "32", "128Gi", nil)
	var pgs []*schedulingv1.PodGroup
	var pods []*v1.Pod
	for j := 0; j < jobs; j++ {
		name := fmt.Sprintf("job%03d", j)
		pgs = append(pgs, util.BuildPodGroupWithSubGroupPolicy(name, "c1", "", "q1", 16, nil, schedulingv1.PodGroupInqueue, "hard", 2,
			[]schedulingv1.SubGroupPolicySpec{util.BuildSubGroupPolicyWithSubGroupSize("worker", []string{"volcano.sh/task-spec"}, "hard", 1, 4)}))
		for g := 0; g < 4; g++ {
			for i := 0; i < 4; i++ {
				pods = append(pods, util.BuildPod("c1", fmt.Sprintf("%s-g%d-p%d", name, g, i), "", v1.PodPending, api.BuildResourceList("4", "8Gi"), name,
					map[string]string{"volcano.sh/task-spec": fmt.Sprintf("worker-%d", g)}, nil))
			}
		}
	}
	tc := &uthelper.TestCommonStruct{Name: "bench-subgroup", PodGroups: pgs, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// BenchmarkProtoAllocate times one allocate execution on 1024 nodes
// (4 zones x 16 racks x 16 nodes). Session open/close is excluded.
func BenchmarkProtoAllocate(b *testing.B) {
	scenarios := []struct {
		name  string
		build func() *uthelper.TestCommonStruct
		pods  int
	}{
		{"plain-20jobs-x8", func() *uthelper.TestCommonStruct { return benchPlainJobs(20) }, 160},
		{"subgroup-8jobs-x16", func() *uthelper.TestCommonStruct { return benchSubGroupJobs(8) }, 128},
	}
	for _, scenario := range scenarios {
		for _, mode := range []protoMode{modeExhaustive, modeRanked, modeRankedFit, {name: "ranked+top1", rank: true}, {name: "ranked+top3", rank: true}} {
			b.Run(scenario.name+"/"+mode.name, func(b *testing.B) {
				var counters probeCounters
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					tc := scenario.build()
					c := &probeCounters{}
					tc.Plugins = probePluginBuilders(c, nil)
					ssn := tc.RegisterSession(probeTiers(), nil)
					action := mode.action()
					switch mode.name {
					case "ranked+top1":
						action.subGroupJobTopK = 1
					case "ranked+top3":
						action.subGroupJobTopK = 3
					}
					b.StartTimer()
					tc.Run([]framework.Action{action})
					b.StopTimer()
					placed := 0
					for _, job := range ssn.Jobs {
						placed += len(job.TaskStatusIndex[api.Binding]) + len(job.TaskStatusIndex[api.Allocated])
					}
					if placed != scenario.pods {
						b.Fatalf("placed %d pods, want %d", placed, scenario.pods)
					}
					counters.allocate.Add(c.allocate.Load())
					counters.deallocate.Add(c.deallocate.Load())
					counters.predicate.Add(c.predicate.Load())
					tc.Close()
				}
				b.ReportMetric(float64(counters.allocate.Load())/float64(b.N), "allocEvents/op")
				b.ReportMetric(float64(counters.deallocate.Load())/float64(b.N), "deallocEvents/op")
				b.ReportMetric(float64(counters.predicate.Load())/float64(b.N), "predicates/op")
			})
		}
	}
}
