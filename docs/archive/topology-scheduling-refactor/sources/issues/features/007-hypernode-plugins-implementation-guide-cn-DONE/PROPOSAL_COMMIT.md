> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Proposal And Verification Notes

## Delivery Plan

Translate the developer implementation guide faithfully rather than creating a
shorter user guide. Preserve the three Mermaid explanations of plugin
composition, hard topology-affinity filtering, and hard SubGroup search state.

No implementation code changes are required, so the proposal-first gate for
implementation code does not apply.

## Verification Cases

1. **English/Chinese parity**
   - Scope: documentation review.
   - Expected result: both documents cover the same ownership, callback,
     candidate, search, diagnostics, source-map, and test concepts.

2. **Diagram and link validity**
   - Scope: static documentation verification.
   - Commands: check Mermaid block counts and fences, `git diff --check`, and
     local-link checks.
   - Expected result: three Mermaid diagrams, balanced fences, and valid
     reciprocal/local links.

## Results

Delivered `volcano/docs/design/hypernode-plugins-implementation-cn.md` as the
Chinese counterpart of the developer implementation guide.

- The Chinese guide retains the English guide's ownership model, callback
  contract, hard/soft candidate handling, allocation search, diagnostics,
  source map, and focused test commands.
- Both language versions contain three Mermaid diagrams, with matching
  composition, hard affinity, and temporary search-state coverage.
- Reciprocal English/Chinese links and the design-document entry point were
  verified locally. Mermaid fences were balanced and `git diff --check` passed
  for tracked documentation changes.

No scheduler/API/CRD implementation code changed, so builds, images, and Kind
verification are not required for this documentation-only delivery.
