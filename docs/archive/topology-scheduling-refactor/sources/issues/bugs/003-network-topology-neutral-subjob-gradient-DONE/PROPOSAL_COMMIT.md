> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Proposal And Verification Notes

## Current Workspace State

- Volcano source repository: `volcano/`
- Current scheduler image in Kind: `volcanosh/vc-scheduler:dev-002-topo-affinity`
- Current `volcano/` git status:

```text
?? example/topology-affinity/
```

The untracked `example/topology-affinity/` test manifests are left untouched.

## Root Cause

Case 3 is feasible on the standard 4-node topology, but it fails when
`network-topology-aware` and `group-topology-affinity` both contribute SubJob
HyperNode gradients.

The critical interaction is:

1. `group-topology-affinity` expands the SubJob candidate set under the current
   job HyperNode, then filters candidates by hard subgroup terms. For Case 3,
   the valid candidates are `tier1` HyperNodes.
2. `network-topology-aware` sees a real SubJob but no SubJob-level
   `networkTopology`, so it returns only the caller-provided HyperNode:

   ```go
   return [][]*api.HyperNodeInfo{{hyperNode}}
   ```

3. The scheduler framework intersects all enabled plugin gradients with AND
   semantics.

When the job candidate is `tier3`, this becomes:

```text
network-topology-aware:    {kind-tier3-all}
group-topology-affinity:   {kind-tier1-control-plane, kind-tier1-worker, kind-tier1-worker2, kind-tier1-worker3}
intersection:              empty
```

That is why the failure summary shows `networkTopology` excluding the `tier1`
candidates even though the workload did not declare SubJob network topology.

## Proposed Fix

Change `network-topology-aware` SubJob gradient behavior only for the neutral
case:

- Keep the existing fast path for jobs without real SubJob policy:

  ```go
  if job, found := ssn.Jobs[subJob.Job]; found && !job.ContainsSubJobPolicy() {
      return [][]*api.HyperNodeInfo{{hyperNode}}
  }
  ```

- Keep hard SubJob network topology behavior unchanged.
- Keep soft SubJob network topology behavior unchanged.
- When the job has real SubJobs and topology-affinity rules, but this SubJob has
  no `networkTopology`, return a neutral full-subtree gradient instead of the
  single current HyperNode.

Suggested logic:

```go
if job, found := ssn.Jobs[subJob.Job]; found {
    if !job.ContainsSubJobPolicy() {
        return [][]*api.HyperNodeInfo{{hyperNode}}
    }
    if !subJob.WithNetworkTopology() && job.WithTopologyAffinity() {
        result, err := nta.hyperNodeGradientFn(
            ssn,
            hyperNode,
            maxHyperNodeTier(ssn.HyperNodesSetByTier),
            subJob.AllocatedHyperNode,
        )
        if err != nil {
            ...
            return emptyHyperNodeGradients
        }
        return result
    }
}
```

Rationale:

- A SubJob with no network topology should not constrain another topology
  plugin's descendant candidates.
- Using the full subtree makes `network-topology-aware` neutral in the framework
  gradient intersection.
- Restricting this to `job.WithTopologyAffinity()` avoids broadening ordinary
  partition-only jobs that do not need topology-affinity-driven subgroup search.
- Existing bug 001 soft network topology paths remain unchanged because
  SubJobs with soft network topology still enter `SoftTopologyPreferredTier()`.
- Existing bug 002 subgroup search paths remain unchanged because this fix only
  changes what candidates the network plugin contributes before the existing
  global search evaluates them.

## Proposed Tests

### New regression test for Bug 003

Add an allocate test under `pkg/scheduler/actions/allocate/allocate_test.go`:

- Build the standard 4-node, 3-tier HyperNode topology.
- Enable both plugins with `enabledHyperNodeGradient: true`:
  - `network-topology-aware`
  - `group-topology-affinity`
- Create a PodGroup equivalent to Case 3:
  - four one-pod SubJobs
  - hard `subGroupAffinity` at `tier3`
  - hard `subGroupAntiAffinity` at `tier1`
  - no job/subjob `networkTopology`
- Assert all four tasks bind.
- Assert the four tasks occupy four distinct `tier1` HyperNodes.
- Assert the final placement remains under one `tier3`.

### Plugin-level test

Add a focused test under
`pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go`:

- SubJob has no `networkTopology`.
- Job has `TopologyAffinity`.
- The returned SubJob gradient includes descendants under the provided root,
  not only the root itself.

### Regression tests for Bug 001

Run existing soft topology tests:

```bash
go test ./pkg/scheduler/actions/allocate -run 'TestSelectBestHyperNodeForJobPrefersSoftJobPlacement|TestAllocateWithPartitionPolicySoftNetworkTopologyPacksJobIntoPreferredTier' -count=1
go test ./pkg/scheduler/plugins/network-topology-aware -run 'TestSoftHyperNodeGradientFnPrefersConfiguredTier' -count=1
```

These ensure soft job/subjob network topology scoring still works.

### Regression tests for Bug 002

Run existing subgroup global-search tests:

```bash
go test ./pkg/scheduler/actions/allocate -run 'TestAllocateWithCompositeGroupTopologyAffinity|TestAllocateWithHardSubGroupTopologyAffinityWhenMinMemberIsZero|TestAllocateWithPreferredSubGroupTopologyAffinityContinuesAfterJobReady' -count=1
go test ./pkg/scheduler/plugins/group-topology-affinity -count=1
```

These ensure hard/preferred subgroup topology search behavior still works,
including `minMember=0` / `minSubGroups=0` coverage.

## Kind Verification Plan

After implementation:

1. Build a scheduler-only image:

   ```bash
   make vc-scheduler
   docker build --platform linux/arm64 -t volcanosh/vc-scheduler:dev-003-topo-neutral-subjob -f ../dockerfile.scheduler.local .
   kind load docker-image volcanosh/vc-scheduler:dev-003-topo-neutral-subjob --name kind
   kubectl set image deployment/volcano-scheduler -n volcano-system \
     volcano-scheduler=volcanosh/vc-scheduler:dev-003-topo-neutral-subjob
   kubectl rollout status deployment/volcano-scheduler -n volcano-system --timeout=120s
   ```

2. Keep the standard scheduler config with both plugins enabled:

   ```yaml
   - name: network-topology-aware
     enabledHyperNodeGradient: true
     enabledHyperNodeOrder: true
   - name: group-topology-affinity
     enabledHyperNodeGradient: true
     enabledHyperNodeOrder: true
   ```

3. Run the topology-affinity cases from `example/topology-affinity/cases`.

4. Required Kind result:

   - Case 3 must pass with both plugins enabled.
   - Case 1, 2, 4, 5, 6 must keep passing.
   - `default` namespace is cleaned afterward.

## Implementation Status

Implemented after user approval.

Changed files:

- `pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go`
  - Added neutral SubJob gradient behavior for jobs that declare
    topology-affinity while the specific SubJob has no `networkTopology`.
  - Jobs without real SubJob policy keep the previous single-HyperNode fast
    path.
  - Hard and soft SubJob `networkTopology` paths are unchanged.
- `pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go`
  - Added `TestSubJobNeutralGradientWithTopologyAffinityReturnsFullSubtree`.
- `pkg/scheduler/actions/allocate/allocate_test.go`
  - Added
    `TestAllocateWithHardSubGroupTopologyAffinityAndNeutralNetworkGradient`.

No API, CRD, controller, or admission changes were made.

## Unit And Package Verification

Formatting:

```bash
gofmt -w pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go \
  pkg/scheduler/actions/allocate/allocate_test.go
git diff --check
```

Focused Bug 003 tests:

```bash
go test ./pkg/scheduler/plugins/network-topology-aware \
  -run 'TestSubJobNeutralGradientWithTopologyAffinityReturnsFullSubtree|TestSoftHyperNodeGradientFnPrefersConfiguredTier' \
  -count=1
go test ./pkg/scheduler/actions/allocate \
  -run 'TestAllocateWithHardSubGroupTopologyAffinityAndNeutralNetworkGradient' \
  -count=1
```

Bug 001 / Bug 002 regression tests:

```bash
go test ./pkg/scheduler/actions/allocate \
  -run 'TestSelectBestHyperNodeForJobPrefersSoftJobPlacement|TestAllocateWithPartitionPolicySoftNetworkTopologyPacksJobIntoPreferredTier|TestAllocateWithCompositeGroupTopologyAffinity|TestAllocateWithHardSubGroupTopologyAffinityWhenMinMemberIsZero|TestAllocateWithPreferredSubGroupTopologyAffinityContinuesAfterJobReady' \
  -count=1
go test ./pkg/scheduler/plugins/network-topology-aware \
  -run 'TestSoftHyperNodeGradientFnPrefersConfiguredTier|TestSubJobNeutralGradientWithTopologyAffinityReturnsFullSubtree' \
  -count=1
go test ./pkg/scheduler/plugins/group-topology-affinity -count=1
```

Related package gate:

```bash
go test ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/api
```

All commands passed. `make vc-scheduler` also passed before image build; it
printed a non-fatal Go stat-cache permission warning in the local environment.

## Image And Kind Deployment

Built and loaded scheduler image:

```bash
make vc-scheduler
docker build --platform linux/arm64 \
  -t volcanosh/vc-scheduler:dev-003-topo-neutral-subjob \
  -f ../dockerfile.scheduler.local .
kind load docker-image volcanosh/vc-scheduler:dev-003-topo-neutral-subjob --name kind
kubectl set image deployment/volcano-scheduler -n volcano-system \
  volcano-scheduler=volcanosh/vc-scheduler:dev-003-topo-neutral-subjob
kubectl rollout status deployment/volcano-scheduler -n volcano-system --timeout=180s
```

Kind scheduler deployment reached `Ready 1/1` with image
`volcanosh/vc-scheduler:dev-003-topo-neutral-subjob`.

The verification cluster used the standard 4-node topology:

```text
kind-tier3-all
  -> kind-tier2-pair-a
       -> kind-tier1-kind-control-plane -> kind-control-plane
       -> kind-tier1-kind-worker        -> kind-worker
  -> kind-tier2-pair-b
       -> kind-tier1-kind-worker2       -> kind-worker2
       -> kind-tier1-kind-worker3       -> kind-worker3
```

Scheduler config kept both topology plugins enabled:

```yaml
- name: network-topology-aware
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
- name: group-topology-affinity
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
```

## Kind Verification Results

All six topology-affinity scenarios passed with the fixed scheduler image.

Case 1: hard PodGroup anti-affinity

- Result: pass.
- PodGroups reached `Running 2/2`.
- Group A placed on `kind-control-plane`; Group B placed on `kind-worker2`.

Case 2: soft PodGroup anti-affinity

- Result: pass.
- PodGroups reached `Running 2/2`.
- Group A placed on `kind-control-plane`; Group B placed on `kind-worker2`.

Case 3: hard SubGroup affinity plus hard SubGroup anti-affinity

- Result: pass.
- PodGroup `ta-deploy-sg-hard` reached `Running 4/4`.
- All four Deployments became available.
- Observed placement:

```text
decode-0    kind-control-plane
decode-1    kind-worker2
prefill-0   kind-worker
prefill-1   kind-worker3
```

The four one-pod SubJobs used four different `tier1` HyperNodes under the same
`tier3`, which validates the fixed interaction between `network-topology-aware`
and `group-topology-affinity`.

Case 4: soft SubGroup affinity plus soft SubGroup anti-affinity

- Result: pass.
- PodGroup reached `Running 4/4`.
- All four Pods ran successfully. The soft preference allowed co-location on
  `kind-control-plane`.

Case 5: `network-topology-aware` plus `group-topology-affinity`

- Result: pass.
- PodGroup reached `Running 4/4`.
- Observed placement:

```text
decode-0    kind-control-plane
decode-1    kind-worker
prefill-0   kind-control-plane
prefill-1   kind-worker
```

All Pods stayed in `kind-tier2-pair-a`, and same-role shards were split across
two `tier1` HyperNodes.

Case 6: PodGroup anti-affinity plus SubGroup rules

- Result: pass.
- Anchor PodGroup reached `Running 2/2`; combo PodGroup reached `Running 2/2`.
- Anchor Pods placed on `kind-control-plane`.
- Combo Pods placed on `kind-worker2`, satisfying hard PodGroup
  anti-affinity and hard SubGroup tier2 affinity.

After each case, workload resources were cleaned up. Final default namespace
check returned no PodGroup, Pod, or Deployment resources.

## Final Notes

- No commit was created.
- Existing untracked manifests under `example/topology-affinity/` were left
  untouched.
