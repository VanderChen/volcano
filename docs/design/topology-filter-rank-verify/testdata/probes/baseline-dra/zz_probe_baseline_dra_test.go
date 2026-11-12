/*
Baseline probe for issue 004: does replaying the winner
with RecoverOperations work for a Reserve-style plugin (DRA) when the winner
is not the last candidate that was dry-run?
*/

package allocate

import (
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/utils/ptr"

	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/plugins/gang"
	networktopologyaware "volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware"
	"volcano.sh/volcano/pkg/scheduler/plugins/predicates"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

// draReplayResult is the outcome of one draReplayScenario run.
type draReplayResult struct {
	placed   map[string]string
	bindErr  error
	fitErr   string
	counters *probeCounters
}

// draReplayScenario: two racks with one node each, every node offers one GPU
// through DRA, the Job (hard tier 1) has one pod with a ResourceClaim. The
// rack that carries a running pod has the higher bin-packing score and is
// expected to win. Racks are dry-run in name order: z0-r00, then z0-r01.
func draReplayScenario(t *testing.T, action *Action, busyRack int) draReplayResult {
	t.Helper()
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.DynamicResourceAllocation, true)

	enabled := true
	tiers := []conf.Tier{{Plugins: []conf.PluginOption{
		{Name: gang.PluginName, EnabledJobOrder: &enabled, EnabledJobReady: &enabled, EnabledJobPipelined: &enabled,
			EnabledSubJobReady: &enabled, EnabledSubJobOrder: &enabled, EnabledSubJobPipelined: &enabled},
		{Name: predicates.PluginName, EnabledPredicate: &enabled,
			Arguments: framework.Arguments{predicates.DynamicResourceAllocationEnable: enabled}},
		{Name: networktopologyaware.PluginName, EnabledNodeOrder: &enabled, EnabledHyperNodeOrder: &enabled, EnabledHyperNodeGradient: &enabled},
		{Name: probePluginName, EnabledPredicate: &enabled, EnabledNodeOrder: &enabled, EnabledHyperNodeOrder: &enabled},
	}}}

	{
		topo := buildProbeTopo(1, 2, 1, "8", "16Gi", nil)
		fillerPG, fillers := fillerPods([]string{nodeName(0, busyRack, 0)}, "2", "1Gi")
		pg := util.BuildPodGroupWithNetWorkTopologies("job", "c1", "", "q1", 1, nil, schedulingv1.PodGroupInqueue, "hard", 1)
		pod := util.BuildPodWithResourceClaim("c1", "job-p00", "", v1.PodPending, api.BuildResourceList("4", "8Gi"), "job",
			map[string]string{"volcano.sh/task-spec": "worker"}, nil,
			[]v1.ResourceClaim{{Name: "gpu", Request: "gpu"}}, []v1.PodResourceClaim{{Name: "gpu", ResourceClaimName: ptr.To("claim1")}})

		tc := &uthelper.TestCommonStruct{
			Name:      "dra-replay",
			PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
			Pods:      append([]*v1.Pod{pod}, fillers...),
			Queues:    probeQueue(),
			ResourceClaims: []*resourcev1.ResourceClaim{util.BuildResourceClaim("c1", "claim1",
				[]resourcev1.DeviceRequest{util.BuildDeviceRequest("gpu", "gpu.example.com", nil, nil, nil)}, nil, nil)},
			DeviceClasses: []*resourcev1.DeviceClass{util.BuildDeviceClass("gpu.example.com",
				[]resourcev1.DeviceSelector{{CEL: &resourcev1.CELDeviceSelector{Expression: `device.driver == 'gpu.example.com'`}}}, nil)},
			ExpectBindsNum: 1,
			ExpectBindMap:  map[string]string{"c1/job-p00": nodeName(0, busyRack, 0)},
		}
		for _, node := range topo.nodes {
			tc.ResourceSlices = append(tc.ResourceSlices, util.BuildResourceSlice("slice-"+node.Name, "gpu.example.com", node.Name,
				resourcev1.ResourcePool{Name: "pool-" + node.Name, Generation: 1, ResourceSliceCount: 1},
				[]resourcev1.Device{util.BuildDevice("gpu-1", nil, nil)}))
		}
		topo.apply(tc)

		res := draReplayResult{counters: &probeCounters{}, placed: map[string]string{}}
		tc.Plugins = probePluginBuilders(res.counters, nil)
		ssn := tc.RegisterSession(tiers, nil)
		defer tc.Close()
		tc.Run([]framework.Action{action})
		job := ssn.Jobs["c1/job"]
		for _, task := range job.Tasks {
			if task.Status != api.Pending && task.NodeName != "" {
				res.placed[task.Name] = fmt.Sprintf("%s(%s)", task.NodeName, task.Status)
			}
		}
		res.bindErr, res.fitErr = tc.CheckBind(0), job.FitError()
		return res
	}
}

var draReplayCases = []struct {
	name     string
	busyRack int
}{
	{"winner z0-r00 is dry-run first, z0-r01 last", 0},
	{"winner z0-r01 is dry-run last", 1},
}

// P6: unmodified allocate action.
func TestProbeP6ReplayWithDRA(t *testing.T) {
	for _, tc := range draReplayCases {
		res := draReplayScenario(t, New(), tc.busyRack)
		t.Logf("%-45s placed=%v bindCheck=%v counters: %s", tc.name, res.placed, res.bindErr, res.counters)
		if res.bindErr != nil {
			t.Logf("    fitError: %s", res.fitErr)
		}
	}
}
