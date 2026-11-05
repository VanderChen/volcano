> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 008：Job 级软网络亲和未跨 SubJob 生效

## 约束

- 初始分析基线为 `8fcc0012`（`feat(scheduler): support SubGroup topology affinity`），
  并已形成提交 `3e6b5c950`、`ec7141e5d`。
- 2026-07-28 根据 reviewer 意见重新评估：`release-1.15` 已包含
  `1733c8853 feat(network-topology-aware): Network-Topology-Aware scheduling soft mode
  under job/subjob (#5206)`，它会在 Session 初始化时把 scheduler 内部的 soft topology
  转换成 cluster-top tier 的 hard topology。旧实现依赖转换后的
  `JobInfo.IsSoftTopologyMode()` 和 `HighestTierAllowed`，与该逻辑不兼容。
- 新基线调整为最新的 `origin/release-1.15`：
  `bdea8d965551f75ba2ba603da7d4af225d9dacf9`。新分支为
  `fix/008-job-soft-topology-across-subjobs-release-1.15`，工作树为
  `volcano-008-r115/`。
- 旧方案及其审批作为历史记录保留，但不再作为新分支的实施依据。新方案和新验证用例
  需要重新获得明确审批后才能修改实现代码。
- 2026-07-28 再次根据 reviewer 意见调整方向：不再保留
  `convertSoftToHardTopology`，而是在集成 `release-1.15` 与 SubGroup topology
  affinity 功能时原生区分 hard 约束和 soft 偏好。上一版“保留转换并重排 gradient”
  的方案已废弃；工作树中的未完成实现已清理，当前分支仍为干净基线。

## 问题

集群中有 16 个空闲节点。拓扑有两个彼此独立的三层 HyperNode 分支；每个 tier-3
分支包含两个 tier-2 HyperNode，每个 tier-2 下有两个 tier-1 HyperNode，每个 tier-1
HyperNode 包含两个节点。每个 tier-3 分支因此包含 8 个节点；两个真实 tier-3 根的
公共祖先仅是调度 Session 自动加入的虚拟 cluster-top。

提交一个 4 副本的 Job。任务按 `partitionPolicy` 分为两个、每组两个 Pod 的 SubJob：
下面将原始文本中的 `vlalphal` 按显然的 API 版本 `v1alpha1` 解释，并根据
`replicas: 4`、`partitionSize: 2` 推断 `totalPartitions: 2`。回归清单会显式设置
`minAvailable: 4`，以便稳定验证四个 Pod 的整体绑定；这不改变要验证的拓扑关系。

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: test
spec:
  schedulerName: volcano
  minAvailable: 4
  networkTopology:
    mode: soft
    highestTierAllowed: 2
  tasks:
    - name: ps
      replicas: 4
      minAvailable: 4
      partitionPolicy:
        totalPartitions: 2
        minPartitions: 2
        partitionSize: 2
        networkTopology:
          mode: soft
          highestTierAllowed: 1
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: pause
              image: registry.k8s.io/pause:3.10
```

期望：每个两 Pod SubJob 的节点 LCA 都是 tier-1，四个 Pod 合并后的节点 LCA 是同一个
tier-2 HyperNode。

实际：每个 SubJob 内部仍在同一个 tier-1 HyperNode，但两个 SubJob 分别落在不同的
tier-3 分支，四个 Pod 的 LCA 升到 tier-3 以上。

## 初步根因

`allocateForJob` 在一次 Job dry-run 中按优先队列逐个调用 `allocateForSubJob`。第一个
SubJob 成功后会更新 `job.AllocatedHyperNode`，但后续空 SubJob 的 HyperNode 选择只使用
自身任务和自身放置状态打分，并不读取这个 Job 级锚点。因此所有候选分支资源相同时，
后续 SubJob 会在并列候选中随机选择，可能跨出第一个 SubJob 所在的 tier-2 分支。

## 2026-07-27 回归：用户原始清单

用户提供的 `user-job.yaml` 与上面的受控回归清单存在两个关键差异：

- 未设置 `partitionPolicy.minPartitions`。默认值为 `0`；默认 mutate webhook 会把
  Task `minAvailable` 设为 `0 * partitionSize = 0`，并使 Job `minAvailable` 也为 `0`。
  两个 partition 仍会拆为两个 SubJob，但第一个 SubJob 完成后 Job 已满足 ready 条件，
  第二个 SubJob 会在下一次 `allocateForJob` 中调度。
- 未设置任何 `resources.requests` 或 `limits`。若命名空间没有 LimitRange 注入默认
  request，Pod 是 BestEffort，`allocate` action 会跳过它们，随后由 `backfill` 对全量
  NodeList 逐 Pod 绑定；这条路径不会调用本任务已提交的
  `preferJobSoftTopologyCandidates`。

因此需要分别验证：原始 BestEffort 行为，以及带请求（或实际被 LimitRange 注入请求）时
的跨轮 SubJob 行为。后者暴露了本次初始修复未覆盖的 outer Job candidate 选择边界。
