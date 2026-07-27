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
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"volcano.sh/apis/pkg/apis/scheduling"
	schedulingv1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
	topologyv1alpha1 "volcano.sh/apis/pkg/apis/topology/v1alpha1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/plugins/gang"
	"volcano.sh/volcano/pkg/scheduler/plugins/predicates"
	"volcano.sh/volcano/pkg/scheduler/uthelper"
	"volcano.sh/volcano/pkg/scheduler/util"
)

const (
	podGroupAntiAffinityDomainTestPlugin = "podgroup-antiaffinity-domain-test"
	testAscendResource                   = v1.ResourceName("huawei.com/ascend-1980")
)

type podGroupAntiAffinityDomainTestPluginImpl struct{}

func (*podGroupAntiAffinityDomainTestPluginImpl) Name() string {
	return podGroupAntiAffinityDomainTestPlugin
}

func (*podGroupAntiAffinityDomainTestPluginImpl) OnSessionOpen(ssn *framework.Session) {
	// This is the exact allocate input produced after required PodGroup
	// anti-affinity removes occupied tier-2 domain A and its coarser ancestors.
	ssn.AddHyperNodeGradientForJobFn(podGroupAntiAffinityDomainTestPlugin, func(_ *api.JobInfo, _ *api.HyperNodeInfo) [][]*api.HyperNodeInfo {
		return [][]*api.HyperNodeInfo{{ssn.HyperNodes["b-t2"], ssn.HyperNodes["c-t2"]}}
	})
	ssn.AddHyperNodeGradientForSubJobFn(podGroupAntiAffinityDomainTestPlugin, func(_ *api.SubJobInfo, root *api.HyperNodeInfo) [][]*api.HyperNodeInfo {
		switch root.Name {
		case "b-t2":
			return [][]*api.HyperNodeInfo{{ssn.HyperNodes["b-t1-0"], ssn.HyperNodes["b-t1-1"]}}
		case "c-t2":
			return [][]*api.HyperNodeInfo{{ssn.HyperNodes["c-t1-0"], ssn.HyperNodes["c-t1-1"]}}
		default:
			return nil
		}
	})
	ssn.AddHyperNodeOrderFn(podGroupAntiAffinityDomainTestPlugin, func(_ *api.SubJobInfo, candidates map[string][]*api.NodeInfo) (map[string]float64, error) {
		baseScores := map[string]float64{
			// Domain A would win if an LCA expansion accidentally exposed it.
			"a-t1-0": 10000,
			"a-t1-1": 9000,
			"b-t1-0": 400,
			"b-t1-1": 300,
			"c-t1-0": 200,
			"c-t1-1": 100,
		}
		scores := make(map[string]float64, len(candidates))
		for name := range candidates {
			scores[name] = baseScores[name]
		}
		return scores, nil
	})
}

func (*podGroupAntiAffinityDomainTestPluginImpl) OnSessionClose(_ *framework.Session) {}

func TestAllocatePodGroupAntiAffinityAcrossCandidateDomains(t *testing.T) {
	runPodGroupAntiAffinityAcrossCandidateDomains(t, false)
}

func TestPodGroupAntiAffinityCandidateDomainDoesNotReintroduceExcludedBranch(t *testing.T) {
	runPodGroupAntiAffinityAcrossCandidateDomains(t, false)
}

func TestPodGroupAntiAffinityDomainDryRunIsolation(t *testing.T) {
	runPodGroupAntiAffinityAcrossCandidateDomains(t, true)
}

func runPodGroupAntiAffinityAcrossCandidateDomains(t *testing.T, hardJobTierTwo bool) {
	t.Helper()
	tier1, tier2, tier3, hyperNodes, realNodes, nodes := buildPodGroupAntiAffinityDomainTopology()

	trueValue := true
	tiers := []conf.Tier{{Plugins: []conf.PluginOption{
		{
			Name:                gang.PluginName,
			EnabledJobOrder:     &trueValue,
			EnabledJobReady:     &trueValue,
			EnabledJobPipelined: &trueValue,
			EnabledJobStarving:  &trueValue,
			EnabledSubJobReady:  &trueValue,
			EnabledSubJobOrder:  &trueValue,
		},
		{
			Name:             predicates.PluginName,
			EnabledPredicate: &trueValue,
		},
		{
			Name:                     podGroupAntiAffinityDomainTestPlugin,
			EnabledHyperNodeGradient: &trueValue,
			EnabledHyperNodeOrder:    &trueValue,
		},
	}}}

	jobTopologyMode := ""
	jobHighestTier := 0
	expectedBinds := 4
	if hardJobTierTwo {
		jobTopologyMode = "hard"
		jobHighestTier = 2
		expectedBinds = 0
	}
	pg := util.BuildPodGroupWithSubGroupPolicy(
		"target", "default", "", "q1", 4, nil, schedulingv1.PodGroupInqueue, jobTopologyMode, jobHighestTier,
		[]schedulingv1.SubGroupPolicySpec{
			util.BuildSubGroupPolicyWithMinSubGroups("worker", []string{"volcano.sh/shard-id"}, "hard", 1, 1, 4),
		},
	)
	tierTwo := int32(2)
	pg.Spec.TopologyAffinity = &schedulingv1.TopologyAffinitySpec{
		PodGroupAntiAffinity: &schedulingv1.PodGroupAntiAffinity{
			Required: []schedulingv1.PodGroupAffinityTerm{{
				PodGroupSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"topology.volcano.sh/group": "llama-70b-prod"},
				},
				TopologyTier: &tierTwo,
			}},
		},
	}

	pods := make([]*v1.Pod, 0, 4)
	for shard := 0; shard < 4; shard++ {
		pods = append(pods, util.BuildPod(
			"default",
			fmt.Sprintf("target-%d", shard),
			"",
			v1.PodPending,
			api.BuildResourceList("100m", "128Mi", []api.ScalarResource{{Name: string(testAscendResource), Value: "6"}}...),
			"target",
			map[string]string{
				"volcano.sh/task-spec": "worker",
				"volcano.sh/shard-id":  fmt.Sprintf("%d", shard),
			},
			nil,
		))
	}

	test := uthelper.TestCommonStruct{
		Name: "required PodGroup anti-affinity allows four SubJobs to use B plus C",
		Plugins: map[string]framework.PluginBuilder{
			gang.PluginName:       gang.New,
			predicates.PluginName: predicates.New,
			podGroupAntiAffinityDomainTestPlugin: func(_ framework.Arguments) framework.Plugin {
				return &podGroupAntiAffinityDomainTestPluginImpl{}
			},
		},
		PodGroups:           []*schedulingv1.PodGroup{pg},
		Pods:                pods,
		Nodes:               nodes,
		HyperNodesSetByTier: map[int]sets.Set[string]{1: tier1, 2: tier2, 3: tier3},
		HyperNodesMap:       hyperNodes,
		HyperNodes:          realNodes,
		Queues:              []*schedulingv1.Queue{util.BuildQueue("q1", 1, nil)},
		ExpectBindsNum:      expectedBinds,
		MinimalBindCheck:    true,
	}

	ssn := test.RegisterSession(tiers, nil)
	defer test.Close()
	action := New()
	test.Run([]framework.Action{action})
	if err := test.CheckAll(0); err != nil {
		t.Fatal(err)
	}

	job := ssn.Jobs[api.JobID("default/target")]
	if job == nil {
		t.Fatal("scheduled job default/target not found")
	}
	if hardJobTierTwo {
		for _, task := range job.Tasks {
			if task.NodeName != "" || task.Status != api.Pending {
				t.Fatalf("failed domain dry-run leaked task %s placement/status: node=%q status=%s",
					task.UID, task.NodeName, task.Status)
			}
		}
		if job.AllocatedHyperNode != "" {
			t.Fatalf("failed domain dry-run leaked job AllocatedHyperNode %q", job.AllocatedHyperNode)
		}
		for _, subJob := range job.SubJobs {
			if subJob.AllocatedHyperNode != "" {
				t.Fatalf("failed domain dry-run leaked subJob %s placement %q", subJob.UID, subJob.AllocatedHyperNode)
			}
		}
		if decision := action.recorder.jobDecisions[job.UID]; decision != "" {
			t.Fatalf("failed domain dry-run recorded job decision %q", decision)
		}
		return
	}

	if job.AllocatedHyperNode != "shared-t3" {
		t.Fatalf("job AllocatedHyperNode = %q, want shared-t3", job.AllocatedHyperNode)
	}

	gotNodes := sets.New[string]()
	for _, task := range job.Tasks {
		if task.NodeName == "" {
			t.Fatalf("task %s was not allocated", task.UID)
		}
		if task.NodeName == "a-node-0" || task.NodeName == "a-node-1" {
			t.Fatalf("task %s was allocated to excluded domain A node %s", task.UID, task.NodeName)
		}
		gotNodes.Insert(task.NodeName)
	}
	wantNodes := sets.New[string]("b-node-0", "b-node-1", "c-node-0", "c-node-1")
	if !gotNodes.Equal(wantNodes) {
		t.Fatalf("allocated nodes = %v, want exact B+C leaves %v", gotNodes.UnsortedList(), wantNodes.UnsortedList())
	}
	for _, subJob := range job.SubJobs {
		placement := ssn.HyperNodes[subJob.AllocatedHyperNode]
		if placement == nil || placement.Tier() != 1 {
			t.Fatalf("subJob %s AllocatedHyperNode = %q, want a tier-1 leaf", subJob.UID, subJob.AllocatedHyperNode)
		}
	}
	if decision := action.recorder.jobDecisions[job.UID]; decision != "shared-t3" {
		t.Fatalf("recorded job decision = %q, want actual placement shared-t3", decision)
	}
	for decisionKey := range action.recorder.subJobDecisions[job.UID] {
		if decisionKey != "shared-t3" {
			t.Fatalf("recorded subJob decision key = %q, want actual placement shared-t3", decisionKey)
		}
	}
}

func TestBuildPodGroupAntiAffinityCandidateDomainsWithHardJobTopology(t *testing.T) {
	hyperNodes := api.HyperNodeInfoMap{
		"shared-t3": newPlacementTestHyperNode("shared-t3", 3, ""),
		"b-t2":      newPlacementTestHyperNode("b-t2", 2, "shared-t3"),
		"c-t2":      newPlacementTestHyperNode("c-t2", 2, "shared-t3"),
	}
	alloc := &Action{session: &framework.Session{HyperNodes: hyperNodes}}
	candidates := []*api.HyperNodeInfo{hyperNodes["b-t2"], hyperNodes["c-t2"]}

	newJob := func(hardTier *int) *api.JobInfo {
		spec := scheduling.PodGroupSpec{
			TopologyAffinity: &scheduling.TopologyAffinitySpec{
				PodGroupAntiAffinity: &scheduling.PodGroupAntiAffinity{
					Required: []scheduling.PodGroupAffinityTerm{{PodGroupSelector: &metav1.LabelSelector{}}},
				},
			},
		}
		if hardTier != nil {
			spec.NetworkTopology = &scheduling.NetworkTopologySpec{
				Mode:               scheduling.HardNetworkTopologyMode,
				HighestTierAllowed: hardTier,
			}
		}
		return &api.JobInfo{
			UID:      "default/target",
			PodGroup: &api.PodGroup{PodGroup: scheduling.PodGroup{Spec: spec}},
		}
	}

	tierTwo := 2
	tierThree := 3
	tests := []struct {
		name     string
		hardTier *int
		want     [][]string
	}{
		{name: "no hard Job topology unions siblings", want: [][]string{{"b-t2", "c-t2"}}},
		{name: "hard tier2 keeps siblings separate", hardTier: &tierTwo, want: [][]string{{"b-t2"}, {"c-t2"}}},
		{name: "hard tier3 permits siblings under common container", hardTier: &tierThree, want: [][]string{{"b-t2", "c-t2"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domains := alloc.buildJobCandidateDomains(newJob(tt.hardTier), candidates)
			if len(domains) != len(tt.want) {
				t.Fatalf("domain count = %d, want %d: %#v", len(domains), len(tt.want), domains)
			}
			for index, wantRoots := range tt.want {
				gotRoots := candidateDomainRootNames(domains[index])
				if fmt.Sprint(gotRoots) != fmt.Sprint(wantRoots) {
					t.Fatalf("domain %d roots = %v, want %v", index, gotRoots, wantRoots)
				}
			}
		})
	}
}

func TestFilterJobCandidateDomainsByMinResource(t *testing.T) {
	buildNodeInfo := func(name string) *api.NodeInfo {
		return api.NewNodeInfo(util.BuildNode(
			name,
			api.BuildResourceList("1", "1Gi", []api.ScalarResource{{Name: string(testAscendResource), Value: "8"}}...),
			nil,
		))
	}
	hyperNodes := api.HyperNodeInfoMap{
		"b-t2": newPlacementTestHyperNode("b-t2", 2, "shared-t3"),
		"c-t2": newPlacementTestHyperNode("c-t2", 2, "shared-t3"),
		"d-t2": newPlacementTestHyperNode("d-t2", 2, "shared-t3"),
	}
	ssn := &framework.Session{
		Nodes: map[string]*api.NodeInfo{
			"b0": buildNodeInfo("b0"),
			"b1": buildNodeInfo("b1"),
			"c0": buildNodeInfo("c0"),
			"c1": buildNodeInfo("c1"),
		},
		RealNodesSet: map[string]sets.Set[string]{
			"b-t2": sets.New[string]("b0", "b1"),
			"c-t2": sets.New[string]("c0", "c1"),
			"d-t2": sets.New[string]("b0", "b1"), // exact overlap with B
		},
	}
	minResource := api.NewResource(v1.ResourceList{
		testAscendResource: resource.MustParse("24"),
	})

	union := newJobCandidateDomain([]*api.HyperNodeInfo{hyperNodes["b-t2"], hyperNodes["c-t2"]})
	filtered, stats := FilterJobCandidateDomainsByMinResource(
		ssn, [][]*jobCandidateDomain{{union}}, minResource, "",
	)
	if len(filtered) != 1 || len(filtered[0]) != 1 {
		t.Fatalf("B+C domain should satisfy 24 cards, got %#v, stats=%#v", filtered, stats)
	}

	singletons := [][]*jobCandidateDomain{{
		newJobCandidateDomain([]*api.HyperNodeInfo{hyperNodes["b-t2"]}),
		newJobCandidateDomain([]*api.HyperNodeInfo{hyperNodes["c-t2"]}),
	}}
	filtered, _ = FilterJobCandidateDomainsByMinResource(ssn, singletons, minResource, "")
	if len(filtered) != 0 {
		t.Fatalf("individual B and C domains should each fail 24 cards, got %#v", filtered)
	}

	overlap := newJobCandidateDomain([]*api.HyperNodeInfo{hyperNodes["b-t2"], hyperNodes["d-t2"]})
	filtered, _ = FilterJobCandidateDomainsByMinResource(
		ssn, [][]*jobCandidateDomain{{overlap}}, minResource, "",
	)
	if len(filtered) != 0 {
		t.Fatalf("overlapping roots must not double-count nodes, got %#v", filtered)
	}
}

func TestJobCandidateDomainDecisionHyperNode(t *testing.T) {
	hyperNodes := api.HyperNodeInfoMap{
		"shared-t3": newPlacementTestHyperNode("shared-t3", 3, ""),
		"b-t2":      newPlacementTestHyperNode("b-t2", 2, "shared-t3"),
		"c-t2":      newPlacementTestHyperNode("c-t2", 2, "shared-t3"),
	}
	ssn := &framework.Session{HyperNodes: hyperNodes}

	singleton := newJobCandidateDomain([]*api.HyperNodeInfo{hyperNodes["b-t2"]})
	if got := jobCandidateDomainDecisionHyperNode(ssn, singleton, "b-t1-0"); got != "b-t2" {
		t.Fatalf("singleton decision = %q, want legacy outer root b-t2", got)
	}

	union := newJobCandidateDomain([]*api.HyperNodeInfo{hyperNodes["b-t2"], hyperNodes["c-t2"]})
	if got := jobCandidateDomainDecisionHyperNode(ssn, union, "shared-t3"); got != "shared-t3" {
		t.Fatalf("multi-root decision from actual placement = %q, want shared-t3", got)
	}
	if got := jobCandidateDomainDecisionHyperNode(ssn, union, ""); got != "shared-t3" {
		t.Fatalf("multi-root LCA fallback = %q, want shared-t3", got)
	}
}

func buildPodGroupAntiAffinityDomainTopology() (
	sets.Set[string],
	sets.Set[string],
	sets.Set[string],
	map[string]*api.HyperNodeInfo,
	map[string]sets.Set[string],
	[]*v1.Node,
) {
	tier1 := sets.New[string]()
	tier2 := sets.New[string]()
	tier3 := sets.New[string]("shared-t3")
	hyperNodes := make(map[string]*api.HyperNodeInfo)
	realNodes := make(map[string]sets.Set[string])
	nodes := make([]*v1.Node, 0, 6)
	rootMembers := make([]api.MemberConfig, 0, 3)
	allNodes := sets.New[string]()

	for _, branch := range []string{"a", "b", "c"} {
		tier2Name := branch + "-t2"
		tier2Members := make([]api.MemberConfig, 0, 2)
		branchNodes := sets.New[string]()
		for leaf := 0; leaf < 2; leaf++ {
			tier1Name := fmt.Sprintf("%s-t1-%d", branch, leaf)
			nodeName := fmt.Sprintf("%s-node-%d", branch, leaf)
			nodes = append(nodes, util.BuildNode(
				nodeName,
				api.BuildResourceList("1", "1Gi", []api.ScalarResource{{Name: string(testAscendResource), Value: "8"}, {Name: "pods", Value: "10"}}...),
				nil,
			))
			hyperNodes[tier1Name] = api.NewHyperNodeInfo(api.BuildHyperNode(tier1Name, 1, []api.MemberConfig{{
				Name:     nodeName,
				Type:     topologyv1alpha1.MemberTypeNode,
				Selector: "exact",
			}}))
			realNodes[tier1Name] = sets.New[string](nodeName)
			tier1.Insert(tier1Name)
			branchNodes.Insert(nodeName)
			allNodes.Insert(nodeName)
			tier2Members = append(tier2Members, api.MemberConfig{
				Name:     tier1Name,
				Type:     topologyv1alpha1.MemberTypeHyperNode,
				Selector: "exact",
			})
		}
		hyperNodes[tier2Name] = api.NewHyperNodeInfo(api.BuildHyperNode(tier2Name, 2, tier2Members))
		realNodes[tier2Name] = branchNodes
		tier2.Insert(tier2Name)
		rootMembers = append(rootMembers, api.MemberConfig{
			Name:     tier2Name,
			Type:     topologyv1alpha1.MemberTypeHyperNode,
			Selector: "exact",
		})
	}
	hyperNodes["shared-t3"] = api.NewHyperNodeInfo(api.BuildHyperNode("shared-t3", 3, rootMembers))
	realNodes["shared-t3"] = allNodes
	return tier1, tier2, tier3, hyperNodes, realNodes, nodes
}
