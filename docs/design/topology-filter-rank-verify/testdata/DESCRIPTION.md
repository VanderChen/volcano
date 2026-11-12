# 004 — 重新审视 Volcano 拓扑调度重构（效率 / 调度过程可解释性 / 插件兼容）

- Target project: `volcano`（scheduler：`allocate` action、`network-topology-aware`、
  `group-topology-affinity`、`framework` 的 HyperNode 扩展点）。
- Target baseline（主）：upstream `volcano-sh/volcano` `master`
  @ `d423e427c847d37271701bf44276156a97ee0346`（2026-11-11，Merge PR #6264）。
- Target baseline（交叉核对）：个人 fork `VanderChen/volcano` `dev-1.14`
  @ `7947e875600ff450318b412bb0c8c39f7020238a`（归档分支
  `archive/topology-scheduling-refactor-20261105` 的源码底座）。
- 两条基线分别取证；一条基线上的结果不代表另一条。
- 基线选择是本任务的假设（重构最终面向上游，生产底座是 dev-1.14），待用户确认。

## 背景

此前的设计归档在 `volcano/docs/archive/topology-scheduling-refactor/`：

- `FINAL_DESIGN_CN.md`：共享索引 + 增量状态 + 有界联合规划（P1/P2/P3a/P3b/P3c），未实施。
- `EXPLAINABILITY_SUPPLEMENT_CN.md`（2026-11-08）：可解释性数据合同（对账、评分分解、
  确定性 tie-break、覆盖记录），未实施。

## 需求（用户原话）

> 再次重新审视volcano拓扑调度重构的任务，需要兼顾效率，调度过程可解释性，与其他plugin的兼容，
> 可以通过一些简单的调度test验证你的想法

拆解：

1. 重新审视重构任务本身（目标、范围、取舍），不是在旧方案上继续加内容。
2. 三个目标要同时成立：
   - 效率：调度周期内的试放、谓词、事件回调与状态复制的成本。
   - 调度过程可解释性：过程本身能被复述（不仅是事后补记录）。
   - 与其他 plugin 的兼容：predicates / nodeorder / binpack / gang / capacity /
     proportion / nodegroup / DRA / deviceshare 等现有扩展点继续按各自合同工作。
3. 用简单的调度测试验证想法（scheduler 单测框架 `uthelper`）。

## 本阶段范围

提案阶段：刻画现状、给出重审后的方案与验证计划、用探测测试取证。
**不修改 `volcano/` 的实现代码**；探测测试与原型补丁只保存在本任务目录的 `probes/`。
实现需用户明确批准后才开始。

## 影响组件

- `pkg/scheduler/actions/allocate/allocate.go`（`allocateForJob` / `allocateForSubJob` /
  `allocateResourcesForTasks` / `selectBestHyperNode*`）
- `pkg/scheduler/framework/session_plugins.go`（HyperNode gradient 与 order 聚合）
- `pkg/scheduler/util/scheduler_helper.go`（`PrioritizeHyperNodes`、`SelectBest*`）
- `pkg/scheduler/plugins/network-topology-aware`、`pkg/scheduler/plugins/group-topology-affinity`
- 失败信息：`pkg/scheduler/api/unschedule_info.go`、upstream `pkg/scheduler/unschedulable`

## 第二轮（2026-11-11）

### 用户反馈（原话）

> "筛选 → 排序 → 验证" 这个和我之前的构想比较一致，但是这里可能得考虑一个全局最优的trade-off，
> 避免造成本来能调度现在调度不了的问题,同时提供一些历史bug供参考思考

### 用户提供的历史缺陷（原文）

HyperNode 拓扑中有三个 tier-2 `volcano.sh/hyperinstance-group`，记作 A、B、C。每个 tier-2 下有两个
tier-1 `volcano.sh/hypernode`，每个 tier-1 对应一个 Kubernetes Node；每个 Node 有
`huawei.com/ascend-1980: 8`。

另一个已运行 PodGroup `pg-myq-2` 与目标 PodGroup 使用相同标签：

```yaml
topology.volcano.sh/group: llama-70b-prod
```

`pg-myq-2` 已位于 A。目标 PodGroup `pg-myq` 声明：

- `minMember: 4`；
- required PodGroup anti-affinity 位于 `volcano.sh/hyperinstance-group`；
- `prefill` 和 `decode` 各要求两个 SubGroup；
- 每个 SubGroup 大小为 1；
- 每个 SubGroup 使用 hard network topology，最高层级为 `volcano.sh/hypernode`；
- 每个 Pod 请求 `huawei.com/ascend-1980: 6`。

预期放置是四个单 Pod SubJob 分别使用 B、C 下的四个 tier-1 节点。每个 Pod 只需要在自己的 tier-1 内满足
hard network topology；PodGroup anti-affinity 只禁止使用 A，并不要求目标 PodGroup 整体收敛在 B 或 C
中的某一个。

实际 PodGroup 保持 `Inqueue/Pending`。状态显示两个任务可调度、两个任务不可调度，典型错误为：

```text
2/4 tasks in gang unschedulable
Pending: 2 Schedulable, 2 Unschedulable
subJob ... 0/6 hyperNodes available
```

已确认根因（用户给出）：`group-topology-affinity` 对 required PodGroup anti-affinity 做 Job 级过滤后，
A 被排除，B、C 保留，B、C 的共同父级因在 tier-2 没有唯一 ancestor 而被排除；Job gradient 中保留的是
同层候选 `[B, C]`；framework 做的是 HyperNode 名称集合交集，不会产生表示 `B ∪ C` 的对象；
`allocateForJob` 逐个处理该层的 HyperNode，一次 Job dry-run 的搜索根只能是 B 或 C，任一单根最多放置
两个 Pod。`[B, C]` 被解释为"B 或 C 两个完整 Job 容器"，而 required PodGroup anti-affinity 的正确含义是
"B、C 都是允许目标 PodGroup 使用的域"。

### 第二轮要回答的问题

1. "排序 → 验证"里的提前停止会不会让今天能调度的作业变成调度不了。
2. 现行搜索本身漏掉了哪些合法放置（以上述缺陷为起点），重构应当怎样处理，代价是什么。
3. 在"可调度、层更窄、分数更高、成本更低"之间怎样排先后，哪些地方接受贪心。

第二轮仍是提案阶段：不修改 `volcano/` 的实现代码。结果写在 `PROPOSAL_COMMIT.md` 第 15 节。

### 第二轮新增的影响组件

- `pkg/scheduler/framework/session_plugins.go`：gradient 结果的组合方式（按名字求交集）。
- `pkg/scheduler/plugins/group-topology-affinity`：required PodGroup anti-affinity 的 gradient。
- `pkg/scheduler/actions/allocate/allocate.go`：`allocateForJob` 里 SubJob 的放置顺序；dev-1.14 的候选森林。
- 上游 `pkg/scheduler/framework/hyper_node_candidate.go`：提名快速路径。
- 上游设计文档 `docs/design/group-topology-affinity.md` §6.2 对"更粗的候选"的表述。

## 第三轮（2026-11-12）

### 用户要求（原话）

> 审视完成后，请形成完整的重构方案设计，讲清楚背景，设计目标，方案，以及为什么有效，进一步的关于局部最优/全局最优与调度效率的tradeoff可以从学术论文角度寻找支撑，但是不要为此引入不可直观理解的复杂算法，当前三阶段的方案我很喜欢，但是要讲清楚具体怎么排序，依据是什么

> 完成后，将完整方案记录为本地一个issues/feature，并把方案作为一个独立分支推送远端，不需要在社区提issues或者pr

### 第三轮的交付物

1. 完整的设计文档：本目录 `DESIGN.md`。内容包括背景、设计目标、方案、为什么有效、局部最优与全局最优的取舍及文献依据、排序的每一个键和依据。
2. 设计分支：推送到 `origin`（`VanderChen/volcano`），分支名和 commit 记录在 `PROPOSAL_COMMIT.md` §13。分支只包含文档、原型补丁、探测和证据，不包含对调度器实现代码的改动。
3. 不在上游社区创建 issue、PR 或评论。

### 第三轮的约束

- 不为全局最优引入不能直观理解的算法。文献只用来说明取舍，不作为引入新算法的理由。
- 保留"筛选 → 排序 → 验证"三段流程。
- 仍是提案阶段：不修改 `volcano/` 的实现代码，任务状态保持 `IP`。过程记录在 `PROPOSAL_COMMIT.md` §16。
