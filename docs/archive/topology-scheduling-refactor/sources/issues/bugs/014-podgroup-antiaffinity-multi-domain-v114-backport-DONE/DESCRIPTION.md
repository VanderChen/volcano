> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 014：v1.14 native soft-topology 基线缺少 PodGroup anti-affinity 多域联合分配

## 状态

- 类型：Bug
- 状态：DONE
- 创建日期：2026-07-29
- 源码基线分支：`fix/013-native-soft-topology-v1.14-backport`
- 源码基线提交：`b42c9f4262`
- 修复分支：`fix/014-podgroup-antiaffinity-multi-domain-v114-backport`
- 关联历史修复：Bug 009，提交 `473c6538b`
- 修复提交：`8a71ca532`

## 问题

Bug 013 修复了 v1.14 加 `#5206` 和 topology-affinity 能力后的 native
soft-topology 行为，并让 topology gradient callback 能区分：

- `Applied/Constrain`：插件有 hard 约束，候选参与交集；
- `Abstain`：插件只有 soft 语义，不得缩小候选集。

但这个修改只保证 required PodGroup anti-affinity 过滤后，framework 能保留同一
gradient layer 中的全部合法成员，例如 `{B,C}`。当前 allocate 主逻辑仍把 B、C
逐个当作完整 Job 的互斥候选：

```text
for each gradient layer:
  for each HyperNode in layer:
    start one Job dry-run rooted at this HyperNode
```

因此调度器实际搜索：

```text
Job entirely in B
OR
Job entirely in C
```

而没有搜索：

```text
Job in the exact union B+C
```

生产等价场景中，A 已被 required PodGroup anti-affinity 排除；B、C 各有两个
tier-1 节点，每节点有 8 张卡。四个 SubJob 各有一个 Pod，每 Pod 请求 6 张卡，
并要求 SubJob hard network topology 不超过 tier-1。B 或 C 单独只能容纳两个
Pod，只有精确联合候选域 `B+C` 可以容纳四个 Pod，所以 Job 仍会 Pending。

## 代码证据

基于 `b42c9f4262`：

1. `group-topology-affinity` 的 Job callback 对 required PodGroup
   anti-affinity 返回 `HyperNodeGradientConstrain(result)`，所以 `{B,C}`
   可以作为同一层的两个合法成员传到 framework。
2. `pkg/scheduler/actions/allocate/allocate.go` 的 `allocateForJob` 仍在每个
   gradient layer 内逐个遍历 `hyperNode`，每次只把这个单根传给
   `allocateForSubJob`。
3. `allocateForSubJob` 调用 `HyperNodeGradientForSubJobFn(subJob,
   hyperNodeForJob)` 时只有该单根的子树可见，不能让四个 SubJob 在同一事务中
   分别使用 B 和 C。
4. Bug 009 的修复提交 `473c6538b` 不在 013 提交祖先中。

## 与 Bug 009 的关系

Bug 009 已在旧的 008 基线上实现 allocate-private 的多根候选域，但 013 是另一条
生产兼容分支，明确未包含 009。014 不直接移植 009 的完整 multi-root domain。

013 已移除 soft-to-hard conversion：

- soft NTA 能真实 `Abstain`；
- GTA required 的 `{B,C}` 不再被 soft callback 缩小；
- B、C 可以一次进入现有 SubJob hard intersection 和 soft scoring。

因此 014 V2 使用更小的 candidate-forest 方案：以 `LCA(B,C)` 作为 SubJob callback
的遍历 envelope，再将结果与 `subtree(B) ∪ subtree(C)` 精确求交。envelope 不作为
合法 candidate 或最终 placement。

不采用直接 cherry-pick `473c6538b`：

- 009 基线早于 `#5206` 的 v1.14 backport；
- 009 的 soft helper 读取 `job.PodGroup.Spec.NetworkTopology`；
- 013 要求统一读取转换后的 `job.NetworkTopology`；
- 013 的 framework callback 已改为 `HyperNodeGradientResult`
  (`Applied/Abstain`)；
- 013 已增加 mixed topology matrix，必须保留这些行为；
- 009 的通用 hard-domain 分区、multi-root gradient/stats merge 和
  solution-level soft filter，在 native soft framework 下不再是本生产修复的
  必要条件。

## 范围

计划修改：

- `pkg/scheduler/actions/allocate/allocate.go`
- `pkg/scheduler/actions/allocate/podgroup_antiaffinity_forest_test.go`
- 必要时扩充同包现有 placement test，但不改变测试语义
- 本 issue 下的 Kind 验证清单和记录

原则上不修改：

- scheduler framework callback 契约
- `group-topology-affinity` 插件
- `network-topology-aware` 插件
- scheduler API、CRD、controller、webhook
- allocate recorder 的公共接口

详细设计和预先约定的验证用例见 `PROPOSAL_COMMIT.md`。在用户批准 proposal 和
验证用例前，不修改实现代码。
