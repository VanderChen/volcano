> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 019：preferred SubGroup 容器顺序在 native-soft abstain 后丢失

## 状态

- 类型：Bug
- 状态：DONE
- 创建日期：2026-08-03
- 源码分支：`fix/018-podgroup-antiaffinity-bc-production-containment`
- 当前基线：`f954233d2ba7d3c512f0e1d7c1b999c9e829d14e`
- 回归引入点：`18deb521904e15b426ee596cc14bf3af18ac0de9`

## 用户场景

拓扑包含四个 tier1 `volcano.sh/hypernode`：`a`、`b`、`c`、`d`。

- `a`、`b` 属于同一个 tier2 `volcano.sh/hyperinstance-group`；
- `c`、`d` 属于另一个 tier2 `volcano.sh/hyperinstance-group`；
- PodGroup 有两个 `prefill` shard 和两个 `decode` shard；
- preferred SubGroup affinity（weight 100）要求 `prefill` 与 `decode` 倾向同一 tier2；
- 两条 preferred single-policy SubGroup anti-affinity（weight 100）分别让两个
  `prefill` shard、两个 `decode` shard 倾向不同 tier1。

在节点资源对称、没有更强 HyperNode 分数竞争时，预期布局为：

```text
pair-ab 或 pair-cd
  第一个 tier1：1 prefill + 1 decode
  第二个 tier1：1 prefill + 1 decode
```

实际布局为四个 Pod（2 prefill + 2 decode）全部落在同一个 tier1（用户现场为
`a`）。

## 语义澄清

用户用例标题写的是“required 硬约束”，但所给 YAML 字段均为 `preferred`。因此它们
是软偏好，不是在所有资源/插件分数组合下都必须满足的硬约束。

本 bug 的判定依据不是“preferred 未被强制”，而是：当前调度路径在任何 peer-aware
打分发生前就把候选收窄到单个 tier1，导致 weight 100 的反亲和分数即使正确计算也
没有 sibling 可比较。在本用例的对称资源和无竞争 HyperNode 分数条件下，这会稳定
产生明显错误的紧凑布局。

## 根因

`18deb5219` 正确引入了 hard constraint 与 native-soft abstention 的区分，但同时
切断了 GTA 已有的 preferred Job-container 顺序：

1. `group-topology-affinity.OnSessionOpen` 的 Job gradient 回调从
   `hyperNodeGradientForJob` 改为 `hyperNodeConstraintForJob`；
2. 当前 PodGroup 只有 preferred terms，因此 `hyperNodeConstraintForJob` 返回
   `HyperNodeGradientAbstain()`；
3. framework 对全部 abstain 的结果返回固定的低 tier 优先 universe：
   `[tier1(a,b,c,d), tier2(ab,cd), tier3(root)]`；
4. allocate 接受第一层可行的单 tier1 Job candidate；
5. 后续 SubJob order callback 每次只收到一个候选，例如 `{a}`；
6. `scorePreferredSubGroupAntiAffinityTerms` 能把冲突的 `a` 从 100 降到 0，但
   `b` 不在候选集中，因此无法发生打散。

代码中 `prioritizePreferredSubGroupJobGradients` 仍会正确生成
`[tier2, tier3, tier1]`，但真实 session 回调不再调用它。现有测试直接调用该 helper
和 scorer，所以测试通过而集成行为失败。

## 现场配置相关性

`group-topology-affinity` 已同时启用 `enabledHyperNodeGradient` 与
`enabledHyperNodeOrder`。`network-topology-aware` 也启用相同能力，但本 PodGroup
未给出 Job/SubJob `networkTopology`，因此它不应为此用例提供 hard membership 或
竞争性 soft score。根因不在 ConfigMap 开关缺失。

## 范围

本任务只修复 preferred SubGroup Job-container traversal order 在 native-soft
abstention 模型中的传递，并验证它与 hard constraint 求交、018 candidate forest、
NTA native-soft 行为兼容。

不在范围内：

- 把 `preferred` 改成 `required`；
- 修改 PodGroup API、CRD 或 webhook 语义；
- 修改普通 Kubernetes NodeOrder 分数；
- 引入新的全局 placement planner；
- 重写 018 已完成的 candidate-forest 实现。

## 最终结果

- 采用已批准的 `Applied` + `Ordered` 单矩阵 hotfix；
- 生产代码只修改 scheduler 内部 API、framework gradient 组合和 GTA；
- `allocate.go`、PodGroup API/CRD、controller、webhook、Helm schema 均未修改；
- allocate 内存回归连续 50 轮全部得到一个 tier2 内的 `1p1d + 1p1d`；
- Kind K0 在 `f954233d2` 精确复现 2p2d 单点；
- Kind K1 修复后得到 pair-ab 内 a/b 各 `1 prefill + 1 decode`；
- Kind K2 的 real-hard NTA scope 固定 pair-cd 后，只使用 c/d 且各 `1p1d`；
- 018 candidate-forest 聚焦回归、scheduler 相关包、构建和 `make verify` 通过；
- 临时 5-node Kind 集群和 baseline worktree 已删除；本地验证镜像保留；
- 已在用户指定分支创建本地提交 `c0828a799`，未推送远端。
