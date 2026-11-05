> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Proposal And Verification Notes

## Delivery Plan

Document the present implementation of `network-topology-aware` and
`group-topology-affinity`, focusing on their independent session callbacks and
the framework/allocate path that composes their results. Use Mermaid to show:

1. the end-to-end hard-constraint, resource-filter, dry-run, and commit flow;
2. the hard topology-affinity candidate decision tree; and
3. the temporary placement state used by the hard SubGroup search.

No implementation code changes are required, so the proposal-first gate for
implementation code does not apply.

## Verification Cases

1. **Implementation fidelity**
   - Scope: source-to-document review.
   - Expected result: every described callback, candidate transformation, and
     allocation behavior has a corresponding source location in the guide.

2. **Diagram and documentation validity**
   - Scope: static documentation verification.
   - Commands: extract Mermaid blocks and verify their delimiters; run
     `git diff --check`; verify local Markdown links.
   - Expected result: three balanced Mermaid diagrams, no whitespace errors,
     and all referenced local files exist.

## Results

Delivered `volcano/docs/design/hypernode-plugins-implementation.md` and linked
it from `docs/design/group-topology-affinity.md`.

- Source-to-document review covered the two plugins, `Session` candidate
  intersection, common min-resource filtering, dry-run allocation, placement
  restoration, and hard SubGroup backtracking.
- The guide contains three Mermaid diagrams: end-to-end composition, hard
  topology-affinity evaluation, and temporary search/commit state.
- Mermaid fences were balanced, local documentation and source links were
  present, and `git diff --check` passed for the tracked design-document link.

No scheduler/API/CRD implementation code changed. Builds, images, and Kind
verification are therefore not required for this documentation-only delivery.
