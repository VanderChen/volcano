> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Proposal And Verification Notes

## Delivery Plan

Create one user guide that describes the relationship between existing
`networkTopology` and the new `topologyAffinity` API. It will cover:

- PodGroup-level cross-PodGroup anti-affinity.
- SubGroup-level affinity and anti-affinity inside one PodGroup.
- Hard (`required`) and soft (`preferred`) terms, tier selection, admission
  constraints, and current feature boundaries.
- A complete PodGroup API example plus the repository's runnable Kind example.

No implementation code changes are required, so the proposal-first gate for
implementation code does not apply.

## Verification Cases

1. **Guide completeness**
   - Purpose: ensure a user can choose the correct layer and configure both
     supported topology-affinity levels.
   - Scope: documentation review.
   - Expected result: the guide includes prerequisites, PodGroup and SubGroup
     semantics, YAML, verification commands, troubleshooting, and limits.

2. **Repository integration**
   - Purpose: ensure the guide is reachable from the feature design and its
     links refer to repository paths that exist.
   - Scope: static documentation verification.
   - Commands: `git diff --check`; path and link inspection with `rg`.
   - Expected result: no whitespace errors and all referenced local paths are
     present.

## Results

The guide was delivered at
`volcano/docs/user-guide/how_to_use_topology_affinity.md`; the topology-affinity
design now links to it.

- `git diff --check -- docs/design/group-topology-affinity.md` passed.
- All seven fenced YAML blocks in the new guide parsed successfully with the
  host YAML parser.
- The guide's local links to the network-topology guide and the runnable
  `example/topology-affinity/README.md` were confirmed to exist.

No scheduler/API/CRD implementation code changed, so a build, image build, and
Kind run were not required for this documentation-only delivery. The guide
points users to the existing focused Kind validation case that covers combined
PodGroup and SubGroup rules.
