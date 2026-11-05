> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 002: Topology Affinity SubJob Global Search

## Background

After fixing job/subjob soft network topology scoring, inspect whether other
HyperNode topology plugins have the same "subjobs are allocated sequentially
without enough whole-job consideration" issue.

The primary target is `group-topology-affinity`, especially:

- hard `TopologyAffinity.SubGroupAffinity`
- hard `TopologyAffinity.SubGroupAntiAffinity`
- preferred `TopologyAffinity.SubGroupAffinity`
- preferred `TopologyAffinity.SubGroupAntiAffinity`

## Suspected Problem

`group-topology-affinity` evaluates subgroup topology rules using peer subjob
placement state. However the allocate action can stop early when normal
`JobReady` becomes true, and the hard subgroup backtracking path also returns
immediately when `JobReady` is already true.

For jobs whose `minMember` and `minSubGroups` are zero, this can hide required
subgroup topology constraints or skip subgroup placement entirely.
For preferred subgroup topology terms, the same early stop can prevent peer
subgroups from participating in HyperNode scoring after the ordinary gang
readiness threshold has already been reached.

## Scope

- Keep the fix scheduler-only.
- Do not change Volcano Job, PodGroup, or user API semantics.
- Add focused unit/regression coverage.
- Verify with the local Kind cluster after building a scheduler-only image.
