/*
Scratch probes of the round-2 behavior and regression analyst for issue 004.
Test-only. Needs probes/common, history, prototype, prototype2, prototype2-dev114
and the round-2 prototype patch (dev-1.14).
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
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// rvCfg is one switch combination.
type rvCfg struct {
	name     string
	mode     protoMode
	scope    bool // framework.ProbeScopeUnion
	fallback bool // framework.ProbeScopeFallback (dev-1.14: forest first)
	budget   int  // Action.repairBudget
}

var (
	rvOff    = rvCfg{name: "off", mode: modeExhaustive}
	rvOffDet = rvCfg{name: "off+detTies", mode: modeExhaustiveDet}
	rvA      = rvCfg{name: "S7a forest-first", mode: modeExhaustiveDet, scope: true, fallback: true}
	rvB      = rvCfg{name: "S7b region-replaces-forest", mode: modeExhaustiveDet, scope: true}
)

func (c rvCfg) repair(n int) rvCfg {
	c.budget = n
	c.name += fmt.Sprintf("+repair(%d)", n)
	return c
}

type rvOut struct {
	res       *probeResult
	action    *Action
	jobs      map[api.JobID]*api.JobInfo
	ahnAtOpen map[api.JobID]string
}

func rvRun(t testing.TB, tc *uthelper.TestCommonStruct, cfg rvCfg) *rvOut {
	t.Helper()
	previous := framework.ProbeScopeFallback
	framework.ProbeScopeFallback = cfg.fallback
	defer func() { framework.ProbeScopeFallback = previous }()
	out := &rvOut{jobs: map[api.JobID]*api.JobInfo{}, ahnAtOpen: map[api.JobID]string{}}
	out.res, out.action = runRound2(t, tc, cfg.mode, round2{scope: cfg.scope, budget: cfg.budget}, nil, func(ssn *framework.Session) {
		for id, job := range ssn.Jobs {
			out.jobs[id] = job
			out.ahnAtOpen[id] = job.AllocatedHyperNode
		}
	})
	return out
}

// rvJobState reports what the scheduler recorded for a Job after the session closed.
func rvJobState(job *api.JobInfo) string {
	if job == nil {
		return "<nil>"
	}
	var subs []string
	for id, subJob := range job.SubJobs {
		if subJob.AllocatedHyperNode != "" {
			subs = append(subs, fmt.Sprintf("%s=%s", id, subJob.AllocatedHyperNode))
		}
	}
	sort.Strings(subs)
	annotation := "<no podgroup>"
	if job.PodGroup != nil {
		annotation = job.PodGroup.GetAnnotations()[api.JobAllocatedHyperNode]
	}
	return fmt.Sprintf("job.AllocatedHyperNode=%q annotation=%q subJobs=[%s]", job.AllocatedHyperNode, annotation, strings.Join(subs, " "))
}

func rvPlaced(res *probeResult, prefix string) string {
	var cells []string
	for pod, node := range res.placed {
		if strings.HasPrefix(pod, prefix) {
			cells = append(cells, pod+"@"+node)
		}
	}
	sort.Strings(cells)
	return fmt.Sprintf("%d [%s]", len(cells), strings.Join(cells, " "))
}

func rvTaskErrors(res *probeResult, job api.JobID) string {
	var cells []string
	for task, msg := range res.nodesFitErrors[job] {
		cells = append(cells, task+": "+msg)
	}
	sort.Strings(cells)
	if len(cells) == 0 {
		return "(none)"
	}
	return strings.Join(cells, " || ")
}

func rvTarget(tc *uthelper.TestCommonStruct, pgName, podPrefix string) (*schedulingv1.PodGroup, []*v1.Pod) {
	var target *schedulingv1.PodGroup
	for _, pg := range tc.PodGroups {
		if pg.Name == pgName {
			target = pg
		}
	}
	var pods []*v1.Pod
	for _, pod := range tc.Pods {
		if strings.HasPrefix(pod.Name, podPrefix) {
			pods = append(pods, pod)
		}
	}
	return target, pods
}

func rvSetRunning(pod *v1.Pod, node string) {
	pod.Spec.NodeName = node
	pod.Status.Phase = v1.PodRunning
}

// rvAddPeer adds a running PodGroup that matches the anti-affinity selector.
func rvAddPeer(tc *uthelper.TestCommonStruct, name, node, cards string) {
	pg := util.BuildPodGroup(name, "c1", "q1", 1, nil, schedulingv1.PodGroupRunning)
	pg.Labels = map[string]string{groupLabel: groupValue}
	tc.PodGroups = append(tc.PodGroups, pg)
	tc.Pods = append(tc.Pods, ascendPod(name, name+"-0", node, v1.PodRunning, cards, map[string]string{"volcano.sh/task-spec": "anchor"}))
}

// rvLeak lists target pods newly placed in the given zone.
func rvLeak(res *probeResult, prefix, zone string) []string {
	var leaked []string
	for pod, node := range res.placed {
		if strings.HasPrefix(pod, prefix) && strings.HasPrefix(node, zone+"-") {
			leaked = append(leaked, pod+"@"+node)
		}
	}
	sort.Strings(leaked)
	return leaked
}

// rvContention is the issue-017 shape with knobs: rack a has freeA free CPUs,
// rack b has freeB; one flexible SubJob is handled first, followed by `pinned`
// SubJobs that can only use rack a. Every pod asks for one CPU.
func rvContention(freeA, freeB, pinned int) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG := util.BuildPodGroup("filler", "c1", "q1", 0, nil, schedulingv1.PodGroupRunning)
	pods := []*v1.Pod{}
	for rack, free := range []int{freeA, freeB} {
		if free < 4 {
			pods = append(pods, util.BuildPod("c1", fmt.Sprintf("filler-%d", rack), nodeName(0, rack, 0), v1.PodRunning,
				api.BuildResourceList(fmt.Sprint(4-free), "1Gi"), "filler", map[string]string{"volcano.sh/task-spec": "filler"}, nil))
		}
	}
	size, minGroups, tier := int32(1), int32(1+pinned), 1
	pg := util.BuildPodGroup("job", "c1", "q1", int32(1+pinned), nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{{
		Name: "worker", MatchLabelKeys: []string{"idx"}, SubGroupSize: &size, MinSubGroups: &minGroups,
		NetworkTopology: &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier},
	}}
	pods = append(pods, util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil))
	for i := 0; i < pinned; i++ {
		pods = append(pods, util.BuildPod("c1", fmt.Sprintf("job-pinned%d", i), "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
			map[string]string{"volcano.sh/task-spec": "pinned", "idx": fmt.Sprint(i + 1)}, map[string]string{"only": "a"}))
	}
	tc := &uthelper.TestCommonStruct{Name: "rv-contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG}, Pods: pods, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// rvCondition returns the message of the last Unschedulable PodGroup condition
// written by the gang plugin at session close ("" when none was written).
func rvCondition(job *api.JobInfo) string {
	if job == nil || job.PodGroup == nil {
		return ""
	}
	msg := ""
	for _, cond := range job.PodGroup.Status.Conditions {
		if string(cond.Type) == "Unschedulable" {
			msg = cond.Reason + ": " + cond.Message
		}
	}
	return msg
}

func apiJobID(id string) api.JobID { return api.JobID(id) }
