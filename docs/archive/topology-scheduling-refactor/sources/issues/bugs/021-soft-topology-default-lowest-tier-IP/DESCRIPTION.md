> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 021：Soft Topology 未指定最高 Tier 时丢失默认亲和语义

## 状态

- 类型：Bug / Scheduler
- 阶段：修订实现及 scheduler/Kind 验证完成；repository-wide unit gate 保留既有平台失败
- 状态：IP
- 创建日期：2026-08-05
- 生产基线分支：`dev-1.14`
- Bug 分支：`fix/021-soft-topology-default-tier`
- 最终提交：`ce76e556d fix(scheduler): normalize default soft topology tier`
- 源码基线：`71f1d3248 fix(scheduler): preserve preferred subgroup traversal order`

## 问题

`fix(scheduler): preserve native soft topology on v1.14` 保留了原生 soft
network topology，并沿用 Bug 008 的两个 Job 级候选偏好 helper：

- `preferJobSoftTopologyCandidates`：同一次 Job dry-run 内，在后续 SubJob
  选择前保持已经建立的 Job topology anchor；
- `preferJobSoftTopologyScoreCandidates`：跨 `allocateForJob` 调用，在外层
  HyperNode 候选选择前保持已经建立的 Job topology anchor。

原始 API 允许 `networkTopology.mode: soft` 不指定 `highestTierAllowed` 或
`highestTierName`。在调度器内部，其语义与显式 `highestTierAllowed: 1` 等价：从 tier1
开始尽量亲和，当前偏好没有可行解时仍允许 soft 回退。

当前两个 helper 都把 `job.NetworkTopology.HighestTierAllowed == nil` 放在提前返回
条件中。因此，soft Job 未指定最高 tier 时，两个保持逻辑完全不执行；可行的近端候选
可能被远端的既有插件分数击败，Job 的 LCA 会不必要地上升到更高 tier。

## 根因

Bug 008 最初只处理了显式 `highestTierAllowed` 的 soft Job，并把“无解析 tier”列为
不启用 helper 的保护条件。后来原生 soft topology 明确保留了 soft 下 tier 字段可选的
API 语义，但这两个 helper 的 gate 没有同步补齐缺省 tier 的自动搜索语义。

该问题不是 network-topology-aware hard gradient 的问题：soft 模式正确地 abstain。
根因是 Session 入口只翻译 `highestTierName`，没有把 scheduler 内部 Job/SubJob 的 soft
缺省值统一成 tier1，导致后续候选逻辑能观察到并区别处理本应等价的 `nil` 与 `1`。

## 期望行为

1. soft + 显式最高 tier：保持现有语义，优先选择 LCA tier 不高于显式 tier 的可行
   候选；该集合为空时保留所有可行候选，允许 soft 回退。
2. soft + 未指定最高 tier：Session 内部统一转换为 `highestTierAllowed: 1`，与用户显式
   指定 tier1 的所有下游行为完全一致。
3. 上述语义同时适用于 SubJob 级 statement 候选和 Job 外层 score 候选。
4. hard topology、非 topology Job、无 SubGroup policy、无有效 anchor、无 peer SubJob
   等非目标路径保持不变。
5. 只修改 scheduler 内部深拷贝，不修改或持久化用户提交的 PodGroup/Job API 对象。

## 范围

预计修改：

- `pkg/scheduler/framework/session.go`
- `pkg/scheduler/framework/session_test.go`
- `pkg/scheduler/actions/allocate/allocate.go`
- `pkg/scheduler/actions/allocate/placement_test.go`
- 必要时调整 `pkg/scheduler/actions/allocate/allocate_test.go` 中已有完整路径回归。

不修改：

- staging API、CRD、generated code；
- controller、webhook；
- network-topology-aware plugin 接口或 gradient contract；
- hard topology 与 group-topology-affinity 语义；
- 全局回溯、迁移或组合搜索。

## 完成条件

- [x] Proposal 和验证用例获得用户明确批准；
- [x] Session 内部 Job/SubJob soft nil 统一为 tier1，且原始 API 对象不变；
- [x] nil 与显式 tier1 的 focused 等价性及 scheduler 相关回归通过；
- [x] Kind 所需本地 scheduler/controller/webhook 镜像重建完成；
- [x] 真实 Kind workload 验证 nil/tier1 等价性及 soft fallback；
- [x] 修订后的命令、结果、镜像、日志和清理信息记录到 `PROPOSAL_COMMIT.md`；
- [ ] 所有约定验证通过后将任务目录改为 `DONE`。
