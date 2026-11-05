> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Proposal And Verification Notes

## Initial Analysis

`group-topology-affinity` is the other scheduler plugin that registers
HyperNode callbacks:

- `AddHyperNodeGradientForJobFn`
- `AddHyperNodeGradientForSubJobFn`
- `AddHyperNodeOrderFn`

The plugin evaluates subgroup topology affinity and anti-affinity using current
peer subjob placement. This makes it sensitive to whether the allocate action
actually simulates all relevant subjobs before selecting and committing a
solution.

## Suspected Root Cause

The hard subgroup topology path in `allocateForJobInHyperNode` delegates to
`searchSubJobAllocations`. That search currently exits immediately when
`ssn.JobReady(job)` is true. With `minMember=0` and `minSubGroups=0`, a job may
be considered ready before any subgroup topology placement has been simulated.

Expected behavior for subgroup topology affinity is that the scheduler should
still allocate the relevant subjobs to evaluate and satisfy the subgroup
topology terms.

## Proposed Direction

Scheduler-only:

- Identify jobs whose subgroup topology affinity requires multi-subjob
  simulation.
- Avoid early termination of hard subgroup search merely because ordinary
  gang readiness is already true when target subgroup topology work remains.
- Reuse existing backtracking/search logic where possible.
- Keep API/controller semantics unchanged.

## Verification Plan

1. Add focused unit tests in scheduler allocate tests.
2. Run focused scheduler tests.
3. Build scheduler-only image.
4. Load it into local Kind.
5. Run a minimal PodGroup workload with subgroup topology affinity and
   `minMember=0`, `minSubGroups=0`.
6. Confirm the workload schedules and satisfies topology placement.

## Implementation Summary

Changed scheduler allocate logic only:

- Hard subgroup topology affinity:
  - `searchSubJobAllocations` no longer exits only because ordinary
    `JobReady`/`JobPipelined` is true while hard subgroup topology target
    subjobs remain in the worksheet.
  - This preserves existing hard subgroup backtracking and lets it choose a
    feasible peer-subgroup HyperNode layout.
- Preferred subgroup topology affinity:
  - `allocateForJobInHyperNode` counts subjobs referenced by preferred
    subgroup topology terms.
  - The normal subjob loop continues after ordinary `JobReady` until those
    target subjobs have participated in the dry-run, allowing peer-subgroup
    HyperNode scoring to take effect.

No API, controller, CRD, or user-facing PodGroup semantics were changed.

## Unit Test Results

Initial reproduction before the fix:

```bash
go test ./pkg/scheduler/actions/allocate -run TestAllocateWithHardSubGroupTopologyAffinityWhenMinMemberIsZero -count=1
```

Result: failed with no Bind request.

After the fix:

```bash
go test ./pkg/scheduler/actions/allocate -run 'TestAllocateWithHardSubGroupTopologyAffinityWhenMinMemberIsZero|TestAllocateWithPreferredSubGroupTopologyAffinityContinuesAfterJobReady' -count=1
go test ./pkg/scheduler/actions/allocate -run 'TestAllocateWithCompositeGroupTopologyAffinity|TestAllocateWithHardSubGroupTopologyAffinityWhenMinMemberIsZero|TestAllocateWithPreferredSubGroupTopologyAffinityContinuesAfterJobReady|TestAllocateWithNetWorkTopologies|TestAllocateWithSoftNetworkTopologyAcrossSubJobs' -count=1
go test ./pkg/scheduler/plugins/group-topology-affinity -count=1
go test ./pkg/scheduler/actions/allocate -count=1
```

Results:

- New hard and preferred subgroup topology tests passed.
- Focused topology allocate regression tests passed.
- `group-topology-affinity` plugin tests passed.
- Full `pkg/scheduler/actions/allocate` package passed on rerun.

## Local Image Build

Built the scheduler binary and scheduler-only local image:

```bash
make vc-scheduler
docker build --platform linux/arm64 -t volcanosh/vc-scheduler:dev-002-topo-affinity -f ../dockerfile.scheduler.local .
kind load docker-image volcanosh/vc-scheduler:dev-002-topo-affinity --name kind
kubectl set image deployment/volcano-scheduler volcano-scheduler=volcanosh/vc-scheduler:dev-002-topo-affinity -n volcano-system
kubectl rollout status deployment/volcano-scheduler -n volcano-system --timeout=120s
```

Notes:

- `make vc-scheduler` returned success and produced
  `_output/bin/vc-scheduler` as an arm64 static binary.
- Go emitted a non-fatal stat-cache warning for the host module cache.
- Scheduler deployment rolled out with
  `volcanosh/vc-scheduler:dev-002-topo-affinity`.

## Kind Verification

Cluster:

- Kind cluster: `kind`
- Nodes: 4 total, all arm64
- HyperNode topology:
  - `tier1`: one HyperNode per Kubernetes node
  - `tier2`: `kind-tier2-pair-a` and `kind-tier2-pair-b`
  - `tier3`: `kind-tier3-all`

Validation manifest:

- `issues/bugs/002-topology-affinity-subjob-global-search-IP/kind-topo-affinity-validation.yaml`

Temporary labels:

```bash
kubectl label node kind-worker2 topo-affinity-test/role-a=true --overwrite
kubectl label node kind-worker3 topo-affinity-test/role-b=true --overwrite
kubectl label node kind-worker topo-affinity-test/role-b=true --overwrite
```

Applied workload:

```bash
kubectl apply -f issues/bugs/002-topology-affinity-subjob-global-search-IP/kind-topo-affinity-validation.yaml
kubectl wait pod/topo-affinity-hard-a pod/topo-affinity-hard-b pod/topo-affinity-preferred-a pod/topo-affinity-preferred-b --for=condition=PodScheduled --timeout=120s
kubectl get pod topo-affinity-hard-a topo-affinity-hard-b topo-affinity-preferred-a topo-affinity-preferred-b -o wide
kubectl get pg topo-affinity-hard-zero-min topo-affinity-preferred-after-ready -o wide
```

Observed placement:

```text
topo-affinity-hard-a        kind-worker2
topo-affinity-hard-b        kind-worker3
topo-affinity-preferred-a   kind-worker2
topo-affinity-preferred-b   kind-worker3
```

Both hard and preferred cases placed peer subgroups inside `kind-tier2-pair-b`
as expected. PodGroups reached `Running`.

Cleanup:

```bash
kubectl delete -f issues/bugs/002-topology-affinity-subjob-global-search-IP/kind-topo-affinity-validation.yaml --ignore-not-found=true
kubectl label node kind-worker2 topo-affinity-test/role-a-
kubectl label node kind-worker3 topo-affinity-test/role-b-
kubectl label node kind-worker topo-affinity-test/role-b-
```

Cleanup verification:

- No validation pod or PodGroup remained.
- Temporary node labels were removed.

## Commit

```text
6d2abfe071b43ed5113bd78c6c15fe103c091363
```
