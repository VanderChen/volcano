> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 013：在 v1.14 生产拓扑能力上回移原生 Soft Topology

## 状态

- 类型：Bug / Scheduler / Production Backport
- 阶段：Implemented and verified
- 状态：DONE
- 创建日期：2026-07-29
- 工作分支：`fix/013-native-soft-topology-v1.14-backport`
- 分支基线：`b8af4d766`
- 生产基线描述：Volcano v1.14 + `1733c8853`（#5206）+ PodGroup/SubGroup
  topology affinity 能力

## 分支构造

本分支按生产线重新构造，不从当前 release-1.15 的 012 分支直接回移：

1. 以 Bug 009 的父提交 `ec7141e5d` 为起点；
2. 不包含 `473c6538b fix(scheduler): allocate jobs across anti-affinity domains`；
3. 回移 `1733c8853 feat(network-topology-aware): Network-Topology-Aware
   scheduling soft mode under job/subjob (#5206)`；
4. 因 #5206 依赖其 master 基线中的 `863336447` 行为，冲突解决同时补齐：
   - 导出 `SubJobInfo.NetworkTopology` 内部副本；
   - 在 Session 打开时调用 `adjustNetworkTopologySpec()`；
   - 删除 allocate 中旧的 `updateJobTier`，避免修改 PodGroup 原始对象。

回移后的生产基线提交为：

```text
b8af4d766 feat(network-topology-aware): Network-Topology-Aware scheduling soft mode under job/subjob (#5206)
```

## 问题

#5206 通过在 Session 打开时把 Job/SubJob soft network topology 转换为 hard，
使纯 soft workload 能复用旧版 `ContainsHardTopology()` 和 HyperNode allocate 路径。

该转换与 Bug 008 的原生 soft 选择逻辑冲突：

- scheduler 内部 `IsSoftTopologyMode()` 变为 false；
- `preferJobSoftTopologyCandidates` 不执行；
- `preferJobSoftTopologyScoreCandidates` 不执行；
- SubJob soft 被施加 hard tier 边界，丢失域外 fallback；
- NTA soft gradient 与 GTA required gradient 无法表达“soft plugin 对 hard 候选无意见”。

## 基线复现

命令：

```bash
go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/actions/allocate -count=1
```

结果：

- `pkg/scheduler/api`：PASS；
- `pkg/scheduler/framework`：PASS；
- `pkg/scheduler/actions/allocate`：FAIL；
- 失败用例：
  - `TestAllocateJobSoftAffinityAcrossSubJobs`；
  - `TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates`；
  - `TestPreferJobSoftTopologyCandidates`；
  - `TestPreferJobSoftTopologyScoreCandidates`。

失败现象为 Job LCA 上升到 `<cluster-top-hypernode>`，或 soft helper 保留全部候选而
没有选择已建立的 preferred domain。该结果证明 #5206 已成功生效，同时也固定了
013 需要修复的生产回归。

## 目标

1. 保留 #5206 引入的 Job/SubJob topology 独立副本；
2. Session 只翻译 `highestTierName`，不改写 hard/soft mode；
3. 纯 soft Job/SubJob 仍进入 HyperNode allocate；
4. NTA soft 和 GTA preferred-only 不参与 hard candidate intersection；
5. 保留 Bug 008 的跨 SubJob、跨 allocate 调用 soft preferred/fallback；
6. hard NTA、required PodGroup/SubGroup topology 语义不被放宽；
7. 以实际 v1.14 生产代码路径完成单测、构建和 Kind 验证。

## 非目标

- 不包含或重新实现 `473c6538b` 的 B+C multi-root candidate domain；
- 不实现 Bug 011 的两阶段完整候选树和全局 soft scoring；
- 不把 release-1.15 的 `SearchPurpose`、eviction gradient 或其他 scheduler
  基础设施整体回移到 v1.14；
- 不修改 API、CRD、controller 或 webhook；
- 不改变用户提交的 PodGroup API 对象。

## 完成条件

- [x] Proposal 和验证用例获得用户明确批准；
- [x] 实现生产版 Constrain/Abstain contract；
- [x] 基线失败的四组 soft 测试全部通过；
- [x] hard/required topology 回归测试通过；
- [x] scheduler 构建与相关 package 测试通过；
- [x] 使用 5 个 Node 的 Kind 完成 preferred/fallback 验证；
- [x] 验证命令、结果、镜像和提交记录写入 `PROPOSAL_COMMIT.md`；
- [x] 完成后将任务目录改为 `DONE`。
