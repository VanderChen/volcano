> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Feature 006: HyperNode Plugins Implementation Guide

## Status

DONE

## Request

Create a future-developer-oriented design guide for the two HyperNode-aware
scheduler plugins, with particular focus on the topology-affinity execution
path. The guide must explain ownership, hard/soft processing, framework
composition, allocation search, state updates, diagnostics, and relevant tests.

## Scope

- Add a design document under `volcano/docs/design/`.
- Use Mermaid diagrams where they clarify control flow and temporary placement
  state.
- Link the existing topology-affinity design to the implementation guide.

## Out of Scope

- Scheduler/API/CRD behavior changes.
- Image builds or Kind verification for a documentation-only delivery.
