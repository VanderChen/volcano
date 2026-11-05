> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 011：Topology-aware Allocate 两阶段重构

## 状态

- 类型：Bug / Scheduler architecture refactor
- 阶段：Feasibility analysis and proposal
- 状态：IP
- 创建日期：2026-07-28
- 源码基线：`ec7141e5d`
- 基线分支：`fix/008-hypernode-job-soft-affinity-across-subjobs`
- 工作分支：`fix/011-topology-two-phase-allocation`

## 背景

Bug 009 证明当前 `allocateForJob` 将一个 Job gradient layer 中的 HyperNode
逐个解释为互斥的完整 Job 容器，无法表达 required PodGroup anti-affinity 过滤后
的精确联合可行域，例如 `{B,C}`。

011 作为该问题的另一种修复方向，不复用 009 的局部 multi-root domain 补丁，而是
评估并设计 allocate 主逻辑的两阶段重构：

1. hard feasibility 阶段：综合所有 required/hard topology 规则和资源约束，在当前
   HyperNode 拓扑上构造完整、精确的可选子树或候选森林；
2. soft optimization 阶段：在 hard 阶段保留下来的完整可行集合中，对 preferred/
   soft topology 规则进行打分和选择。

## 必须覆盖的语义层次

- Job / PodGroup 级 network topology；
- SubJob / SubGroupPolicy 级 network topology；
- required/preferred PodGroup anti-affinity；
- required/preferred SubGroup affinity 和 anti-affinity；
- `network-topology-aware` 与 `group-topology-affinity` plugin 的组合；
- gang、SubGroup minMember/minSubGroups、资源、predicate 和现有 dry-run/Recorder
  状态；
- hard 规则只能缩小集合，soft 规则不得提前删除 hard-feasible 候选；
- 不得用候选集合的 LCA 代替精确集合，从而重新引入已被 required 规则排除的分支。

## 当前任务范围

本阶段只做：

- 现有代码和数据流分析；
- 两阶段模型的可行性判断；
- 数据结构、plugin contract、allocate 流程、迁移步骤和验证计划；
- 带流程图的 proposal。

在用户明确批准 proposal 和验证用例前，不修改 Volcano 实现代码。

## 可行性结论

方向可行，但“完整可选子树”必须落成精确 candidate forest/allowed mask；`{B,C}`
不能用共同父级 H 代替。运行时保持两阶段：

1. Phase 1 先编译 hard forest、max-LCA envelope 和 SubGroup relation graph，再搜索
   第一个通过 resource/predicate/gang 验证的 hard-feasible incumbent；
2. Phase 2 沿剩余 frontier 流式优化完整 hard-valid solution，soft 不参与 hard
   candidate 删除，也不物化全部 solution。

进一步性能分析确认：朴素 `D^S` 全枚举不可用于生产。最终设计必须保留 independent
SubJob fast path、只搜索 hard/soft dependency component、只保存一个 incumbent，
并区分 `HardInconclusive` 与可安全提交的 `SoftTruncated`。生产搜索预算需在 focused
benchmark 后确定。

详细数据模型、plugin contract、流程图、交付拆分和验证门禁见
`PROPOSAL_COMMIT.md`。
