/*
Copyright 2025 The Volcano Authors.

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

	"k8s.io/apimachinery/pkg/util/sets"
	"volcano.sh/apis/pkg/apis/scheduling"
	topologyv1alpha1 "volcano.sh/apis/pkg/apis/topology/v1alpha1"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/framework"
	"volcano.sh/volcano/pkg/scheduler/util"
)

func TestCaptureRestoreHyperNodePlacement(t *testing.T) {
	job := &api.JobInfo{
		UID:                "job-1",
		AllocatedHyperNode: "",
		SubJobs: map[api.SubJobID]*api.SubJobInfo{
			"sub-1": {
				UID:                "sub-1",
				AllocatedHyperNode: "",
			},
		},
	}
	subJob := job.SubJobs["sub-1"]
	placement := captureHyperNodePlacement(job, subJob)

	job.AllocatedHyperNode = "sn-a"
	subJob.AllocatedHyperNode = "sn-a"
	restoreHyperNodePlacement(job, subJob, placement)

	if job.AllocatedHyperNode != "" {
		t.Fatalf("job AllocatedHyperNode = %q, want empty", job.AllocatedHyperNode)
	}
	if subJob.AllocatedHyperNode != "" {
		t.Fatalf("subJob AllocatedHyperNode = %q, want empty", subJob.AllocatedHyperNode)
	}
}

func TestUpdateJobAllocatedHyperNodeFromSubJob(t *testing.T) {
	hn := api.HyperNodeInfoMap{
		"root": newPlacementTestHyperNode("root", 3, ""),
		"sn-a": newPlacementTestHyperNode("sn-a", 2, "root"),
		"sn-b": newPlacementTestHyperNode("sn-b", 2, "root"),
	}
	ssn := &framework.Session{
		HyperNodes:                hn,
		HyperNodesReadyToSchedule: true,
		DirtyJobs:                 sets.New[api.JobID](),
	}
	job := &api.JobInfo{UID: "job-1", AllocatedHyperNode: "sn-a"}
	subJob := &api.SubJobInfo{UID: "sub-1"}

	updateJobAllocatedHyperNodeFromSubJob(ssn, job, subJob, "sn-b")
	if job.AllocatedHyperNode != "root" {
		t.Fatalf("job AllocatedHyperNode = %q, want root", job.AllocatedHyperNode)
	}
}

func TestSelectBestHyperNodeForJobPrefersSoftJobPlacement(t *testing.T) {
	tierTwo := 2
	alloc := &Action{
		session: &framework.Session{
			HyperNodes: api.HyperNodeInfoMap{
				"root": newPlacementTestHyperNode("root", 3, ""),
				"sn-a": newPlacementTestHyperNode("sn-a", 2, "root"),
				"sn-b": newPlacementTestHyperNode("sn-b", 2, "root"),
			},
		},
	}
	job := &api.JobInfo{
		UID: "job-1",
		PodGroup: &api.PodGroup{
			PodGroup: scheduling.PodGroup{
				Spec: scheduling.PodGroupSpec{
					NetworkTopology: &scheduling.NetworkTopologySpec{
						Mode:               scheduling.SoftNetworkTopologyMode,
						HighestTierAllowed: &tierTwo,
					},
				},
			},
		},
	}
	solutions := map[string]*jobAllocationSolution{
		"candidate-a": {
			score:              10,
			allocatedHyperNode: "sn-a",
		},
		"candidate-b": {
			score:              100,
			allocatedHyperNode: "root",
		},
	}

	best, err := alloc.selectBestHyperNodeForJob(solutions, job)
	if err != nil {
		t.Fatalf("selectBestHyperNodeForJob returned error: %v", err)
	}
	if best != "candidate-a" {
		t.Fatalf("best HyperNode = %q, want candidate-a", best)
	}
}

func TestSelectBestHyperNodeForJobUsesSoftScoreWithinPreferredTier(t *testing.T) {
	tierTwo := 2
	alloc := &Action{
		session: &framework.Session{
			HyperNodes: api.HyperNodeInfoMap{
				"sn-a": newPlacementTestHyperNode("sn-a", 2, "root"),
				"sn-b": newPlacementTestHyperNode("sn-b", 2, "root"),
			},
		},
	}
	job := &api.JobInfo{
		UID: "job-1",
		PodGroup: &api.PodGroup{PodGroup: scheduling.PodGroup{Spec: scheduling.PodGroupSpec{
			NetworkTopology: &scheduling.NetworkTopologySpec{
				Mode:               scheduling.SoftNetworkTopologyMode,
				HighestTierAllowed: &tierTwo,
			},
		}}},
	}
	solutions := map[string]*jobAllocationSolution{
		"sn-a": {score: 10, allocatedHyperNode: "sn-a", softTopologyAllocatedSubJobs: 2},
		"sn-b": {score: 100, allocatedHyperNode: "sn-b", softTopologyAllocatedSubJobs: 2},
	}

	best, err := alloc.selectBestHyperNodeForJob(solutions, job)
	if err != nil {
		t.Fatalf("selectBestHyperNodeForJob returned error: %v", err)
	}
	if best != "sn-b" {
		t.Fatalf("best HyperNode = %q, want sn-b with the higher merged soft score", best)
	}
}

func TestSelectBestHyperNodeForJobUsesSoftScoreAcrossInBoundLCATiers(t *testing.T) {
	tierThree := 3
	alloc := &Action{
		session: &framework.Session{
			HyperNodes: api.HyperNodeInfoMap{
				"tier1": newPlacementTestHyperNode("tier1", 1, "tier3"),
				"tier3": newPlacementTestHyperNode("tier3", 3, ""),
			},
		},
	}
	job := &api.JobInfo{
		UID: "job-1",
		PodGroup: &api.PodGroup{PodGroup: scheduling.PodGroup{Spec: scheduling.PodGroupSpec{
			NetworkTopology: &scheduling.NetworkTopologySpec{
				Mode:               scheduling.SoftNetworkTopologyMode,
				HighestTierAllowed: &tierThree,
			},
		}}},
	}
	solutions := map[string]*jobAllocationSolution{
		"compact": {score: 10, allocatedHyperNode: "tier1", softTopologyAllocatedSubJobs: 2},
		"spread":  {score: 100, allocatedHyperNode: "tier3", softTopologyAllocatedSubJobs: 2},
	}

	best, err := alloc.selectBestHyperNodeForJob(solutions, job)
	if err != nil {
		t.Fatalf("selectBestHyperNodeForJob returned error: %v", err)
	}
	if best != "spread" {
		t.Fatalf("best HyperNode = %q, want spread with the higher in-bound merged score", best)
	}
}

func TestSelectBestHyperNodeForJobUsesCompactnessOnSoftScoreTie(t *testing.T) {
	tierThree := 3
	alloc := &Action{
		session: &framework.Session{
			HyperNodes: api.HyperNodeInfoMap{
				"tier1": newPlacementTestHyperNode("tier1", 1, "tier3"),
				"tier3": newPlacementTestHyperNode("tier3", 3, ""),
			},
		},
	}
	job := &api.JobInfo{
		UID: "job-1",
		PodGroup: &api.PodGroup{PodGroup: scheduling.PodGroup{Spec: scheduling.PodGroupSpec{
			NetworkTopology: &scheduling.NetworkTopologySpec{
				Mode:               scheduling.SoftNetworkTopologyMode,
				HighestTierAllowed: &tierThree,
			},
		}}},
	}
	solutions := map[string]*jobAllocationSolution{
		"compact": {score: 100, allocatedHyperNode: "tier1", softTopologyAllocatedSubJobs: 2},
		"spread":  {score: 100, allocatedHyperNode: "tier3", softTopologyAllocatedSubJobs: 2},
	}

	best, err := alloc.selectBestHyperNodeForJob(solutions, job)
	if err != nil {
		t.Fatalf("selectBestHyperNodeForJob returned error: %v", err)
	}
	if best != "compact" {
		t.Fatalf("best HyperNode = %q, want compact on an in-bound merged-score tie", best)
	}
}

func TestSelectBestHyperNodeForJobIsDeterministicOnTie(t *testing.T) {
	hyperNodes := api.HyperNodeInfoMap{}
	solutions := map[string]*jobAllocationSolution{}
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("rack-%d", i)
		hyperNodes[name] = newPlacementTestHyperNode(name, 1, "root")
		solutions[name] = &jobAllocationSolution{score: 10, allocatedHyperNode: name}
	}
	alloc := &Action{
		session: &framework.Session{
			HyperNodes: hyperNodes,
		},
	}
	job := &api.JobInfo{UID: "job-1"}

	for i := 0; i < 100; i++ {
		best, err := alloc.selectBestHyperNodeForJob(solutions, job)
		if err != nil {
			t.Fatalf("selectBestHyperNodeForJob returned error: %v", err)
		}
		if best != "rack-0" {
			t.Fatalf("iteration %d selected %q, want deterministic rack-0", i, best)
		}
	}
}

func TestTopologySearchContextPrunesDominatedEquivalentStates(t *testing.T) {
	ctx := newTopologySearchContext()

	if !ctx.shouldExplore("same-state", 10) {
		t.Fatal("first state visit must be explored")
	}
	if ctx.shouldExplore("same-state", 9) {
		t.Fatal("lower-scored equivalent state must be pruned")
	}
	if ctx.shouldExplore("same-state", 10) {
		t.Fatal("equal-scored equivalent state must be pruned")
	}
	if !ctx.shouldExplore("same-state", 11) {
		t.Fatal("higher-scored equivalent state must remain explorable")
	}

	if ctx.exploredStates != 2 || ctx.prunedStates != 2 {
		t.Fatalf("search stats explored=%d pruned=%d, want 2 and 2", ctx.exploredStates, ctx.prunedStates)
	}
}

func TestEffectiveHyperNodeForOptionUsesDryRunPlacement(t *testing.T) {
	candidate := &subJobPlacementCandidate{hyperNode: "root", allocatedHyperNode: "rack-a"}
	if got := effectiveHyperNodeForSubJobPlacement(candidate); got != "rack-a" {
		t.Fatalf("effective HyperNode=%q, want dry-run placement rack-a", got)
	}
	candidate.allocatedHyperNode = ""
	if got := effectiveHyperNodeForSubJobPlacement(candidate); got != "root" {
		t.Fatalf("effective HyperNode=%q, want search fallback root", got)
	}
}

func TestBuildTopologyWorkingSetUsesRequiredSubJobsOnly(t *testing.T) {
	less := func(left, right interface{}) bool {
		return left.(*api.SubJobInfo).UID < right.(*api.SubJobInfo).UID
	}
	queue := util.NewPriorityQueue(less)
	for _, id := range []api.SubJobID{"sub-3", "sub-1", "sub-2", "sub-0"} {
		queue.Push(&api.SubJobInfo{UID: id})
	}
	worksheet := &JobWorksheet{
		subJobs:         queue,
		requiredSubJobs: sets.New[api.SubJobID]("sub-2"),
	}

	workingSet := buildTopologyWorkingSet(worksheet)
	if !workingSet.Equal(sets.New[api.SubJobID]("sub-2")) {
		t.Fatalf("working set = %v, want only required sub-2", workingSet)
	}

	worksheet.requiredSubJobs = sets.New[api.SubJobID]()
	workingSet = buildTopologyWorkingSet(worksheet)
	if !workingSet.Equal(sets.New[api.SubJobID]("sub-0")) {
		t.Fatalf("zero-minimum working set = %v, want one deterministic progress SubJob sub-0", workingSet)
	}
}

func TestFilterGradientsByMinResourceTierStats(t *testing.T) {
	nodeInfo := api.NewNodeInfo(util.BuildNode(
		"node-a",
		api.BuildResourceList("4", "8Gi", []api.ScalarResource{{Name: "pods", Value: "110"}}...),
		nil,
	))

	ssn := &framework.Session{
		Nodes: map[string]*api.NodeInfo{"node-a": nodeInfo},
		RealNodesSet: map[string]sets.Set[string]{
			"sn-a": sets.New("node-a"),
			"sn-b": sets.New("node-a"),
		},
		HyperNodes: api.HyperNodeInfoMap{
			"sn-a": newPlacementTestHyperNode("sn-a", 2, "root"),
			"sn-b": newPlacementTestHyperNode("sn-b", 2, "root"),
		},
		HyperNodeTierNameMap: api.HyperNodeTierNameMap{"supernode": 2},
	}

	gradients := [][]*api.HyperNodeInfo{
		{ssn.HyperNodes["sn-a"], ssn.HyperNodes["sn-b"]},
	}
	minResource := &api.Resource{MilliCPU: 20000, Memory: 40 * 1024 * 1024 * 1024}

	filtered, stats := FilterGradientsByMinResource(ssn, gradients, minResource, "")
	if len(filtered) != 0 {
		t.Fatalf("expected empty filtered gradients, got %#v", filtered)
	}
	if stats == nil {
		t.Fatal("expected resource filter stats")
	}
	if stats.ExcludedByTier[2] != 2 {
		t.Fatalf("expected 2 resource exclusions at supernode tier, got %#v", stats.ExcludedByTier)
	}
	if stats.FinalByTier[2] != 0 {
		t.Fatalf("expected 0 final hyperNodes, got %#v", stats.FinalByTier)
	}
}

func newPlacementTestHyperNode(name string, tier int, parent string) *api.HyperNodeInfo {
	hn := &topologyv1alpha1.HyperNode{}
	hn.Name = name
	hn.Spec.Tier = tier
	hni := api.NewHyperNodeInfo(hn)
	hni.Parent = parent
	return hni
}
