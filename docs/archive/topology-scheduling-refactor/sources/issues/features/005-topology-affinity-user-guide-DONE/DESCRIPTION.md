> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Feature 005: Topology Affinity User Guide

## Status

DONE

## Request

Deliver a user-facing guide for the `topologyAffinity` PodGroup feature. The
guide must explain the PodGroup and SubGroup layers, configuration
prerequisites, API semantics, examples, verification, and known scope limits.

## Scope

- Add a Chinese guide under `volcano/docs/user-guide/`.
- Link the design document to the delivered guide.
- Reuse existing topology-affinity examples as the runnable validation path.

## Out of Scope

- Scheduler, API, CRD, or controller behavior changes.
- Building images or running a Kind cluster for a documentation-only change.
