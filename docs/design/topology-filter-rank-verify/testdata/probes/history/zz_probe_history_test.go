/*
Historical-bug probes for issue 004. Test-only, see common/zz_probe_harness_test.go.

HB (issues 009/014/018): required PodGroup anti-affinity leaves several
allowed tier-2 domains (B and C). The job needs both, but each allowed domain
is tried as a container for the whole job.

HC (issue 017): SubJobs are placed one after another and an earlier SubJob
takes the only domain a later SubJob can use.
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
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

const (
	ascend     = "huawei.com/ascend-1980"
	groupLabel = "topology.volcano.sh/group"
	groupValue = "llama-70b-prod"
)

func historyTiers() ([]conf.Tier, map[string]framework.PluginBuilder) {
	enabled := true
	return probeTiers(conf.PluginOption{
			Name:                     grouptopologyaffinity.PluginName,
			EnabledHyperNodeGradient: &enabled,
			EnabledHyperNodeOrder:    &enabled,
		}),
		map[string]framework.PluginBuilder{grouptopologyaffinity.PluginName: grouptopologyaffinity.New}
}

func ascendPod(group, name, node string, phase v1.PodPhase, cards string, labels map[string]string) *v1.Pod {
	return util.BuildPod("c1", name, node, phase,
		api.BuildResourceList("1", "1Gi", []api.ScalarResource{{Name: ascend, Value: cards}}...), group, labels, nil)
}

// bcVariant describes the target PodGroup of the B+C scenario.
type bcVariant struct {
	name string
	// what the general model predicts: true = a legal placement exists
	feasible bool
	pods     int
	// job-level network topology ("" = none)
	jobMode string
	jobTier int
	// SubGroup layout: "prefill-decode" = the reported bug (two policies, size 1,
	// hard tier 1); "pairs" = one policy, SubGroups of two pods, hard tier 2;
	// "" = no SubGroup policy.
	subGroups string
	noPAA     bool
}

// buildBCScenario: tier-2 domains A, B, C (z0, z1, z2); each has two tier-1
// domains with one node of 8 cards. PodGroup pg-myq-2 runs in A.
func buildBCScenario(v bcVariant) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(3, 2, 1, "32", "64Gi", func(_, _, _ int, n *v1.Node) {
		alloc := api.BuildResourceList("32", "64Gi", []api.ScalarResource{{Name: ascend, Value: "8"}, {Name: "pods", Value: "110"}}...)
		n.Status.Allocatable, n.Status.Capacity = alloc, alloc
	})

	anchor := util.BuildPodGroup("pg-myq-2", "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
	anchor.Labels = map[string]string{groupLabel: groupValue}
	pods := []*v1.Pod{ascendPod("pg-myq-2", "anchor-0", nodeName(0, 0, 0), v1.PodRunning, "1", map[string]string{"volcano.sh/task-spec": "anchor"})}

	target := util.BuildPodGroup("pg-myq", "c1", "q1", int32(v.pods), nil, schedulingv1.PodGroupInqueue)
	target.Labels = map[string]string{groupLabel: groupValue}
	if v.jobMode != "" {
		tier := v.jobTier
		target.Spec.NetworkTopology = &schedulingv1.NetworkTopologySpec{
			Mode: schedulingv1.NetworkTopologyMode(v.jobMode), HighestTierAllowed: &tier,
		}
	}
	if !v.noPAA {
		tierTwo := int32(2)
		target.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{
			PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
				Required: []schedulingv1.PodGroupAffinityTerm{{
					PodGroupSelector: &metav1.LabelSelector{MatchLabels: map[string]string{groupLabel: groupValue}},
					TopologyTier:     &tierTwo,
				}},
			},
		}
	}
	policy := func(name string, size, minGroups int32, tier int) schedulingv1.SubGroupPolicySpec {
		return schedulingv1.SubGroupPolicySpec{
			Name:           name,
			LabelSelector:  &metav1.LabelSelector{MatchLabels: map[string]string{"role": name}},
			MatchLabelKeys: []string{"idx"},
			SubGroupSize:   &size,
			MinSubGroups:   &minGroups,
			NetworkTopology: &schedulingv1.NetworkTopologySpec{
				Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier,
			},
		}
	}
	switch v.subGroups {
	case "prefill-decode":
		half := int32(v.pods / 2)
		target.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{policy("prefill", 1, half, 1), policy("decode", 1, int32(v.pods)-half, 1)}
		for i := 0; i < v.pods; i++ {
			role := "prefill"
			if i >= int(half) {
				role = "decode"
			}
			pods = append(pods, ascendPod("pg-myq", fmt.Sprintf("myq-%s-%d", role, i), "", v1.PodPending, "6",
				map[string]string{"volcano.sh/task-spec": role, "role": role, "idx": fmt.Sprint(i)}))
		}
	case "pairs":
		target.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{policy("worker", 2, int32(v.pods/2), 2)}
		for i := 0; i < v.pods; i++ {
			pods = append(pods, ascendPod("pg-myq", fmt.Sprintf("myq-worker-%d", i), "", v1.PodPending, "6",
				map[string]string{"volcano.sh/task-spec": "worker", "role": "worker", "idx": fmt.Sprint(i / 2)}))
		}
	default:
		for i := 0; i < v.pods; i++ {
			pods = append(pods, ascendPod("pg-myq", fmt.Sprintf("myq-worker-%d", i), "", v1.PodPending, "6",
				map[string]string{"volcano.sh/task-spec": "worker"}))
		}
	}

	tc := &uthelper.TestCommonStruct{Name: v.name, PodGroups: []*schedulingv1.PodGroup{anchor, target}, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

var bcVariants = []bcVariant{
	{name: "HB1 reported bug: PAA tier 2 + prefill/decode SubGroups hard tier 1, 4 pods", feasible: true, pods: 4, subGroups: "prefill-decode"},
	{name: "HB2 PAA tier 2, no SubGroup policy, 4 pods", feasible: true, pods: 4},
	{name: "HB3 job hard tier 3 + PAA tier 2 + prefill/decode SubGroups, 4 pods", feasible: true, pods: 4, jobMode: "hard", jobTier: 3, subGroups: "prefill-decode"},
	{name: "HB4 job hard tier 3 + PAA tier 2, no SubGroup policy, 4 pods", feasible: true, pods: 4, jobMode: "hard", jobTier: 3},
	{name: "HB5 PAA tier 2 + SubGroups of 2 pods hard tier 2, 4 pods", feasible: true, pods: 4, subGroups: "pairs"},
	{name: "HB6 job soft tier 2 + PAA tier 2 + prefill/decode SubGroups, 4 pods", feasible: true, pods: 4, jobMode: "soft", jobTier: 2, subGroups: "prefill-decode"},
	{name: "control: 2 pods fit in B alone", feasible: true, pods: 2, subGroups: "prefill-decode"},
	{name: "control: no PAA, 4 pods", feasible: true, pods: 4, subGroups: "prefill-decode", noPAA: true},
	{name: "control: job hard tier 2 + PAA, 4 pods need two tier-2 domains", feasible: false, pods: 4, jobMode: "hard", jobTier: 2, subGroups: "prefill-decode"},
	{name: "control: PAA, 5 pods but only 4 nodes outside A", feasible: false, pods: 5},
}

// bcOutcome runs one variant and reports how many target pods were placed.
func bcOutcome(t *testing.T, v bcVariant, action *Action) (placed int, nodes []string, res *probeResult) {
	t.Helper()
	tiers, extra := historyTiers()
	res = runProbe(t, buildBCScenario(v), tiers, extra, action, nil)
	for pod, node := range res.placed {
		if !strings.HasPrefix(pod, "myq-") {
			continue
		}
		placed++
		nodes = append(nodes, node)
		if !v.noPAA && strings.HasPrefix(node, "z0-") {
			t.Errorf("%s: pod %s leaked into excluded domain A (%s)", v.name, pod, node)
		}
	}
	sort.Strings(nodes)
	return placed, nodes, res
}

func shortFitError(msg string) string {
	if i := strings.Index(msg, "HyperNode:"); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}

// HB: unmodified allocate action.
func TestProbeHBAllowedUnion(t *testing.T) {
	t.Logf("| variant | legal placement exists | placed | nodes | status |")
	t.Logf("|---|---|---|---|---|")
	for _, v := range bcVariants {
		placed, nodes, res := bcOutcome(t, v, nil)
		verdict := "ok"
		switch {
		case v.feasible && placed != v.pods:
			verdict = "FALSE NEGATIVE"
		case !v.feasible && placed != 0:
			verdict = "ILLEGAL PLACEMENT"
		}
		t.Logf("| %s | %t | %d/%d | %s | %s; %s |", v.name, v.feasible, placed, v.pods, strings.Join(nodes, " "), verdict, shortFitError(res.fitError["c1/pg-myq"]))
		if !v.feasible && placed != 0 {
			t.Errorf("%s: placed %d pods although no legal placement exists", v.name, placed)
		}
	}
}

// contentionScenario is the example of issue 017: tier-1 domain a has one free
// CPU, domain b is idle; the flexible SubJob can use both, the pinned SubJob
// only domain a. flexibleFirst controls which SubJob the scheduler handles
// first (SubJobs are ordered by their match index).
func contentionScenario(flexibleFirst bool) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0)}, "3", "1Gi")
	size, minGroups, tier := int32(1), int32(2), 1
	pg := util.BuildPodGroup("job", "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{{
		Name: "worker", MatchLabelKeys: []string{"idx"}, SubGroupSize: &size, MinSubGroups: &minGroups,
		NetworkTopology: &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier},
	}}
	flexibleIdx, pinnedIdx := "0", "1"
	if !flexibleFirst {
		flexibleIdx, pinnedIdx = "1", "0"
	}
	flexible := util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": flexibleIdx}, nil)
	pinned := util.BuildPod("c1", "job-pinned", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": pinnedIdx}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// HC: unmodified allocate action.
func TestProbeHCCrossSubJobContention(t *testing.T) {
	for _, flexibleFirst := range []bool{true, false} {
		res := runProbe(t, contentionScenario(flexibleFirst), probeTiers(), nil, nil, nil)
		t.Logf("flexible SubJob first=%-5t placed %d/2 %v; %s", flexibleFirst, res.count("job-"), res.placed, shortFitError(res.fitError["c1/job"]))
	}
}
