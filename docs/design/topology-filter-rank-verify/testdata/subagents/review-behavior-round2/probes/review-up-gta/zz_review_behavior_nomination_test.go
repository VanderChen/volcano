package grouptopologyaffinity

// Scratch probe of the round-2 behavior analyst (issue 004): is the unchanged
// nomination fast path sound once the usable-region composition is on, and
// why does TestNominationFastPathMatchesPluginGradients fail?

import (
	"fmt"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	scheduling "volcano.sh/apis/pkg/apis/scheduling"
	"volcano.sh/volcano/pkg/scheduler/api"
	"volcano.sh/volcano/pkg/scheduler/cache"
	"volcano.sh/volcano/pkg/scheduler/conf"
	"volcano.sh/volcano/pkg/scheduler/framework"
	networktopologyaware "volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware"
	"volcano.sh/volcano/pkg/scheduler/util"
)

func TestReviewNominationSoundness(t *testing.T) {
	for _, union := range []bool{false, true} {
		previous := framework.ProbeScopeUnion
		framework.ProbeScopeUnion = union
		counts := map[string]int{}
		examples := map[string]string{}
		total := 0
		for _, networkEnabled := range []bool{false, true} {
			for _, hardJob := range []bool{false, true} {
				for _, hardSubJob := range []bool{false, true} {
					for _, placed := range []bool{false, true} {
						sc := cache.NewCustomMockSchedulerCache("nomination-test", util.NewFakeBinder(0), util.NewFakeEvictor(0), &util.FakeStatusUpdater{}, nil, nil)
						ssn := framework.OpenSession(sc, nil, nil)
						ssn.HyperNodes = buildRackUnderSupernodeTree()
						ssn.HyperNodesSetByTier = rackSupernodeSetByTier()
						ssn.HyperNodesTiers = []int{1, 2, 3}
						ssn.HyperNodeTierNameMap = defaultTierNameMap()
						ssn.RealNodesSet = map[string]sets.Set[string]{
							"root": sets.New("node-a", "node-b"), "sn-a": sets.New("node-a"), "sn-b": sets.New("node-b"),
							"cab-a": sets.New("node-a"), "cab-b": sets.New("node-b"),
						}
						for _, name := range []string{"node-a", "node-b"} {
							ssn.Nodes[name] = api.NewNodeInfo(util.BuildNode(name, api.BuildResourceList("4", "8Gi"), nil))
						}
						job := jobWithTopologyAffinity([]scheduling.PodGroupAffinityTerm{{TopologyTierName: "supernode", PodGroupSelector: &metav1.LabelSelector{}}}, nil)
						subJob := &api.SubJobInfo{Job: job.UID, UID: "sub"}
						if hardJob {
							job.NetworkTopology = &scheduling.NetworkTopologySpec{Mode: scheduling.HardNetworkTopologyMode, HighestTierAllowed: ptr.To(2)}
						}
						if hardSubJob {
							subJob.NetworkTopology = &scheduling.NetworkTopologySpec{Mode: scheduling.HardNetworkTopologyMode, HighestTierAllowed: ptr.To(1)}
						}
						if placed {
							job.AllocatedHyperNode = "sn-b"
							subJob.AllocatedHyperNode = "cab-b"
						}
						ssn.Jobs = map[api.JobID]*api.JobInfo{job.UID: job, "peer": otherJobWithTaskOnNode("peer", "node-a", "prod")}
						ssn.Tiers = []conf.Tier{{Plugins: []conf.PluginOption{
							{Name: PluginName, EnabledHyperNodeGradient: ptr.To(true)},
							{Name: networktopologyaware.PluginName, EnabledHyperNodeGradient: ptr.To(networkEnabled)},
						}}}
						plugin := New(framework.Arguments{})
						plugin.OnSessionOpen(ssn)
						if networkEnabled {
							networktopologyaware.New(framework.Arguments{}).OnSessionOpen(ssn)
						}
						for _, sub := range []*api.SubJobInfo{nil, subJob} {
							for _, rootName := range []string{"root", "sn-a", "sn-b"} {
								root := ssn.HyperNodes[rootName]
								var gradients [][]*api.HyperNodeInfo
								var stats *api.HyperNodeGradientStats
								if sub == nil {
									gradients, stats = ssn.HyperNodeGradientForJobFn(job, root, api.PurposeAllocate)
								} else {
									gradients, stats = ssn.HyperNodeGradientForSubJobFn(sub, root, api.PurposeAllocate)
								}
								var region sets.Set[string]
								if stats != nil && stats.ScopeHyperNodes != nil {
									region = sets.New[string]()
									for name := range stats.ScopeHyperNodes {
										region = region.Union(ssn.RealNodesSet[name])
									}
								}
								names := make([]string, 0, len(ssn.RealNodesSet))
								for name := range ssn.RealNodesSet {
									names = append(names, name)
								}
								sort.Strings(names)
								for _, name := range names {
									nodes := ssn.RealNodesSet[name]
									total++
									oracleOld, oracleNew := false, false
									for _, layer := range gradients {
										for _, candidate := range layer {
											if ssn.RealNodesSet[candidate.Name].IsSuperset(nodes) {
												oracleOld = true
												if region == nil || region.IsSuperset(nodes) {
													oracleNew = true
												}
											}
										}
									}
									fast := ssn.HyperNodeContainsNomination(job, sub, root, name)
									where := fmt.Sprintf("network=%t job=%t subJob=%t placed=%t root=%s pinned=%s sub=%t", networkEnabled, hardJob, hardSubJob, placed, rootName, name, sub != nil)
									record := func(kind string) {
										counts[kind]++
										if examples[kind] == "" {
											examples[kind] = where
										}
									}
									if fast && nodes.Has("node-a") {
										record("UNSOUND: fast path accepts a nomination that contains the excluded node")
									}
									if fast != oracleOld {
										record(fmt.Sprintf("differs from the committed test oracle (fast=%t full=%t)", fast, oracleOld))
										if nodes.Has("node-a") {
											record("  ... of which: pinned contains the excluded node, oracle counts the partly excluded container")
										} else {
											record("  ... of which: pinned lies inside the region")
										}
									}
									if fast != oracleNew {
										record(fmt.Sprintf("differs from container-intersect-region semantics (fast=%t legal=%t)", fast, oracleNew))
									}
								}
							}
						}
						plugin.OnSessionClose(ssn)
						framework.CloseSession(ssn)
					}
				}
			}
		}
		framework.ProbeScopeUnion = previous
		t.Logf("### ProbeScopeUnion=%t: %d (root, pinned, level) combinations", union, total)
		kinds := make([]string, 0, len(counts))
		for kind := range counts {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		if len(kinds) == 0 {
			t.Logf("  fast path agrees with both oracles everywhere")
		}
		for _, kind := range kinds {
			t.Logf("  %4d  %s   e.g. %s", counts[kind], kind, examples[kind])
		}
	}
}
