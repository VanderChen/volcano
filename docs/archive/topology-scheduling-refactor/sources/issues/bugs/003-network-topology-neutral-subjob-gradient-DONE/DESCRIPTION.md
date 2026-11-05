> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 003: network-topology-aware blocks group-topology-affinity subgroup search

## Status

DONE

## Reported Scenario

Topology-affinity Case 3 fails when both topology plugins are enabled:

- `network-topology-aware`
- `group-topology-affinity`

Case 3 creates one PodGroup with four one-pod SubJobs:

- `prefill/p0`
- `prefill/p1`
- `decode/d0`
- `decode/d1`

The PodGroup declares hard subgroup topology affinity:

- `prefill` and `decode` must share `tier3`.

It also declares hard subgroup topology anti-affinity:

- `prefill` shards must not share `tier1`.
- `decode` shards must not share `tier1`.
- `prefill` and `decode` must not share `tier1`.

On the standard 4-node Kind topology, this has a valid placement: place each
one-pod SubJob on a different `tier1` HyperNode under the same `tier3`.

## Actual Result

With both topology plugins enabled and scheduler image
`volcanosh/vc-scheduler:dev-002-topo-affinity`, Case 3 stays unscheduled:

- PodGroup: `Inqueue`
- all four Pods: `Pending`
- wait for all four Deployments: timeout

Representative scheduler logs:

```text
HyperNode screening for job, job=default/ta-deploy-sg-hard, fitError=8/8 hyperNodes available
No hyperNode gradient for subJob ... decode-d1 ...
subJob ... 0/8 hyperNodes available ...
tier1 0/4 (4 podGroupAntiAffinity, 3 networkTopology)
Try to allocate resource for job in hyperNode fail ... reason=no allocatable solution
```

## Expected Result

Case 3 should schedule successfully:

- all four Pods become `Running`
- PodGroup becomes `Running`
- the four SubJobs occupy four distinct `tier1` HyperNodes under `kind-tier3-all`

## Validation Performed

Temporarily disabling `network-topology-aware` and keeping only
`group-topology-affinity` makes Case 3 pass. Observed placement:

```text
decode-0    kind-control-plane
decode-1    kind-worker
prefill-0   kind-worker2
prefill-1   kind-worker3
```

This proves the workload is feasible and the failure is caused by plugin
interaction, not by an impossible topology request.

## Relationship To Previous Bugs

This fix must not regress:

- Bug 001: `001-hypernode-soft-affinity-job-subjob-scoring-DONE`
  - soft job/subjob network topology scoring must keep preferring configured
    tiers and preserving fallback behavior.
- Bug 002: `002-topology-affinity-subjob-global-search-DONE`
  - hard subgroup topology search must still continue when subgroup topology
    targets remain.
  - preferred subgroup topology terms must still participate after ordinary
    gang readiness.
  - `minMember=0` / `minSubGroups=0` coverage must keep working.

## Scope

- Scheduler-only.
- No API, CRD, controller, or admission behavior change.
- Keep `network-topology-aware` hard and soft network topology semantics intact.
- Keep `group-topology-affinity` hard/preferred subgroup semantics intact.
