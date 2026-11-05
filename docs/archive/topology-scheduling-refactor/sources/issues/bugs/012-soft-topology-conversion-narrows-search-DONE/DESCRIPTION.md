> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 012：Soft Topology 被转换为 Hard 后搜索范围与模式语义错误

## 状态

- 类型：Bug / Scheduler
- 阶段：完成
- 状态：DONE
- 创建日期：2026-07-28
- 上游基线：`origin/release-1.15`
- 源码基线：`a31ab3dd3`
- 基线分支：`fix/008-hypernode-job-soft-affinity-across-subjobs`
- 工作分支：`fix/012-remove-soft-to-hard-topology-conversion`

## 背景

release-1.15 为了让当时尚未完整处理的 Job/SubJob soft network topology 复用
hard HyperNode gradient，引入了 `convertSoftToHardTopology`：

- Job soft 被转换为 hard，并将允许 tier 提升到 cluster-top；
- SubJob soft 被转换为 hard，并继承转换后的 Job 有效 tier。

Bug 008 后续提交 `cc84f7065` 和 `a31ab3dd3` 已经在现有 first-feasible
allocate 流程中增加了 Job soft 跨 SubJob、跨外层候选的贪心亲和选择。但 Session
打开时的转换会擦除 scheduler 内部的 soft mode，使这些逻辑无法启用；SubJob soft
还可能因 hard tier 语义提前丢失本应可用的 fallback 候选。

## 目标

1. 移除 scheduler 内部的 soft-to-hard 转换，Job/SubJob soft mode 在整个
   Session 中保持不变。
2. 只保留 `highestTierName` 到 `highestTierAllowed` 的内部翻译。
3. 纯 soft Job/SubJob 能进入 HyperNode allocate 路径。
4. 只有真实 hard/required topology 规则参与 HyperNode 候选过滤。
5. soft topology 使用现有 first-feasible gradient 和 Bug 008 的锚点贪心逻辑：
   preferred 域有可行候选时优先，preferred 域不可行时保留完整 fallback。
6. 使用真实 Kind 集群验证 preferred 和 fallback 两条路径。

## 非目标

- 不移植 `c41125816` 的完整 hard-feasible layout 构建和全局 soft solution
  scoring。
- 不保证 soft topology 的全局最优布局。
- 不实现 PodGroup/SubGroup dependency graph、Beam search 或通用组合回溯。
- 不在本 Bug 中移植 `473c6538b` 的 multi-root Job candidate domain；该提交属于
  Bug 009。012 只保证未来与精确 candidate domain 组合时，soft 不会再次被当作
  hard 过滤。
- 不改变 BestEffort/backfill 的现有边界；Kind workload 必须包含非零 request，
  走 allocate action。
- 不修改 API、CRD、controller 或 webhook。

## 完成条件

- [x] 用户批准 `PROPOSAL_COMMIT.md` 中的方案和验证用例；
- [x] 聚焦单测、相关 package 测试通过；
- [x] 本地 scheduler 镜像构建成功；
- [x] 不超过 5 个 Kubernetes Node 的真实 Kind preferred/fallback 验证通过；
- [x] 验证命令、Pod placement、LCA、日志、镜像和最终提交记录在
  `PROPOSAL_COMMIT.md`；
- [x] 完成后将任务目录从 `IP` 改为 `DONE`。
