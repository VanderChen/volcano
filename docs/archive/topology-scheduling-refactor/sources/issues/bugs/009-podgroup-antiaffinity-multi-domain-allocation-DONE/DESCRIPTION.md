> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 009：PodGroupAntiAffinity 剩余兄弟域无法联合分配

## 状态

- 类型：Bug
- 阶段：Implemented and verified
- 状态：DONE
- 调查日期：2026-07-27
- 完成日期：2026-07-28
- 源码基线：`ec7141e5d`（`fix/008-hypernode-job-soft-affinity-across-subjobs`）
- 修复分支：`fix/009-podgroup-antiaffinity-multi-domain-allocation`
- 实现提交：`473c6538b`

用户在 2026-07-28 明确要求基于 008 分支创建 009 分支并修复，视为批准既有
proposal 与验证用例。实现、聚焦回归和真实 Kind 验证均已完成。

## 生产现象

HyperNode 拓扑中有三个 tier-2
`volcano.sh/hyperinstance-group`，记作 A、B、C。每个 tier-2 下有两个 tier-1
`volcano.sh/hypernode`，每个 tier-1 对应一个 Kubernetes Node；每个 Node 有
`huawei.com/ascend-1980: 8`。

另一个已运行 PodGroup `pg-myq-2` 与目标 PodGroup 使用相同标签：

```yaml
topology.volcano.sh/group: llama-70b-prod
```

`pg-myq-2` 已位于 A。目标 PodGroup `pg-myq` 声明：

- `minMember: 4`；
- required PodGroup anti-affinity 位于
  `volcano.sh/hyperinstance-group`；
- `prefill` 和 `decode` 各要求两个 SubGroup；
- 每个 SubGroup 大小为 1；
- 每个 SubGroup 使用 hard network topology，最高层级为
  `volcano.sh/hypernode`；
- 每个 Pod 请求 `huawei.com/ascend-1980: 6`。

预期放置是四个单 Pod SubJob 分别使用 B、C 下的四个 tier-1 节点。每个 Pod
只需要在自己的 tier-1 内满足 hard network topology；PodGroup anti-affinity
只禁止使用 A，并不要求目标 PodGroup 整体收敛在 B 或 C 中的某一个。

实际 PodGroup 保持 `Inqueue/Pending`。状态显示两个任务可调度、两个任务不可调度，
典型错误为：

```text
2/4 tasks in gang unschedulable
Pending: 2 Schedulable, 2 Unschedulable
subJob ... 0/6 hyperNodes available
```

## 已确认根因

`group-topology-affinity` 对 required PodGroup anti-affinity 做 Job 级过滤后：

- A 因已有匹配 PodGroup 而被排除；
- B、C 保留；
- B、C 的共同父级 H 因在 tier-2 没有唯一 ancestor 而被排除。

因此 Job gradient 中保留的是同层候选 `[B, C]`。framework 对插件结果做的是
HyperNode 名称集合交集，不会产生一个表示 `B ∪ C` 的对象。

`allocateForJob` 随后逐个处理该层中的 HyperNode：

```go
for _, hyperNode := range hyperNodes {
    allocateForSubJob(..., hyperNode, ...)
}
```

所以一次 Job dry-run 的搜索根只能是 B 或 C：

- 根为 B 时，四个 SubJob 只能看到 B1、B2；
- 根为 C 时，四个 SubJob 只能看到 C1、C2；
- 每个节点只能容纳一个请求 6 卡的 Pod，因此任一单根最多放置两个 Pod；
- scheduler 从未在一次 Job transaction 中尝试 `B ∪ C`。

这里的 `[B, C]` 被 allocate 解释为“B 或 C 两个完整 Job 容器”，而 required
PodGroup anti-affinity 的正确含义是“B、C 都是允许目标 PodGroup 使用的域”。

## 引入历史

- `allocateForJob` 的单 HyperNode 外层 dry-run 结构早于本问题涉及的 topology
  affinity 功能。
- `d42fb0de1` 引入 required PodGroup anti-affinity 后，会删除已占用域 A 及无法
  映射到唯一 term-tier ancestor 的共同父级 H，因此首次形成了当前无法表达
  `B ∪ C` 的具体失败路径。
- `8fcc0012c` 引入 SubGroup topology affinity/anti-affinity，但没有改变上述
  required PodGroup anti-affinity 过滤或 allocate 的单根事务模型。它不是本问题
  的引入提交。

## 影响

- 一个 PodGroup 在满足 required PodGroup anti-affinity 后需要跨多个未占用兄弟域
  才能满足 gang/SubGroup 最小成员数时，会被错误判定为不可调度。
- 资源状态可能只展示最后一次单根 dry-run 的失败，例如某一个 tier-1 的
  `minResource`，不能据此推断 B、C 联合资源不足。
- preferred SubGroup affinity/anti-affinity 只能在当前单根暴露的候选中评分，
  不能补回另一个兄弟域。

## 范围边界

本任务聚焦于：

- required PodGroup anti-affinity 产生多个合法兄弟域；
- Job 含多个可独立放置的 SubJob；
- 单个 SubJob 仍按自己的 hard/soft topology 规则选择一个合法 HyperNode；
- 一次 Job gang transaction 需要让不同 SubJob 使用不同的合法兄弟域。

本任务不承诺：

- 让同一个不可拆分 SubJob 的任务跨多个候选根；
- 为 required SubGroup affinity/anti-affinity 引入全局组合搜索或回溯；
- 求解所有 topology 插件之间的全局最优布局；
- 修改 PodGroup API、CRD、controller 或 HyperNode API。
