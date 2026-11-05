> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Feature 015：统一拓扑约束与有界放置

## 当前入口与状态

2026-10-05：本 Feature 的重构材料统一由 [README.md](README.md) 导航。本次上游设计见 [上游拓扑调度重构方案](VOLCANO_UPSTREAM_TOPOLOGY_REFACTOR_DESIGN_CN.md)，代码与打分分析见 [上游调度打分代码导读](VOLCANO_UPSTREAM_SCORING_CODE_WALKTHROUGH_CN.md)。

目标重构仍处于设计阶段，issue 保持 `IP`，目标实现与验收尚未完成。下文保留早期原型的状态、范围和决定；其中“实现与验证完成”仅指历史原型，不代表当前目标方案已完成。

## 历史原型状态与原始方案

- 类型：Feature / Scheduler architecture refactor
- 状态：IP
- 创建日期：2026-07-29
- 当前阶段：实现与验证完成，等待 reviewer 审阅
- 当前代码对比基线：
  `fix/014-podgroup-antiaffinity-multi-domain-v114-backport@8a71ca5323`
- 演进审视起点：`54c1d018`
- 历史审阅区间：`54c1d018^..8a71ca5323`
- 建议实现基线：`fix/014-podgroup-antiaffinity-multi-domain-v114-backport`
- 建议工作分支：`feat/015-unified-topology-placement`
- 最终实现提交：`c4c6c3643efed8a17f1ef31e8991d9ce1e25f891`
- 备份分支：`feat/015-unified-topology-placement-backup`，固定在
  `7c0eb9d4f8f78294b23907492d8dda77278942b2`
- 实现状态：代码、benchmark、镜像和 K0-K5 Kind 验证已完成；当前分支已删除
  legacy/observe 运行路径，所有 topology Job 默认进入统一 planner，
  `maxExtraPlacementAttempts` 默认值为 `8`

## 用户目标

`54c1d018` 将拓扑调度从单 plugin gradient 扩展为多 topology plugin 的 HyperNode
gradient 交集，并把 minResource 粗筛统一到 allocate。之后 PodGroup anti-affinity、
SubGroup affinity/anti-affinity、native soft topology、placement 恢复与
`B+C` exact forest 都继续建立在 Job gradient、SubJob gradient 和
`allocateForJob/allocateForSubJob` 的逐层 first-success 骨架上。

本 Feature 从 `54c1d018` 开始重新审视完整的 topology candidate、policy composition、
selection、state 和 transaction 模型，而不是只重构最近的 SubGroup/soft/forest
修改。

013/014 已修 bug 不依赖本 Feature 才能工作。015 会把这些能力作为回归基线保留，
并允许统一架构自然覆盖单 SubJob `B+C`、`B+C + hard NTA`、多 PodGroup/SubGroup
联合关系和 fit-first 有界回退等新场景；这类新场景是重构收益，不属于应被排除的
“杂糅功能”。应隔离的是 soft score error policy、extender 新契约、跨周期搜索
进度等与拓扑规划主框架无关的策略变化。

本 Feature 要重新整理整体拓扑调度能力，目标不是获得数学最优解，而是：

1. hard/required 与 soft/preferred 的语义直接、容易解释；
2. `B+C`、required relation、soft fallback 和 fit-first dead-end 使用同一条主流程；
3. 第一个局部 fit 不是最终结论，后续失败时能在有限成本内尝试前序 alternative；
4. 常见成功路径接近当前贪心性能，复杂场景有一个明确预算；
5. 继续复用 Volcano 当前处理 Predicate、NodeOrder、Ready 和 EventHandler 的方式；
6. 删除 policy-specific helper 和 route，而不是把补丁升级为更复杂的通用框架。

## 核心决定

### 1. 一条 topology-aware ordered placement 流程

不再分别设计 fast、forest、relation repair 和 improvement route。对于需要 topology
的 Job，统一执行：

```text
构造 ordered worksheet frontier，并跟踪本分支实际 touched SubJob
  -> 当前 partial placement 下的 Job/SubJob exact scope 交集
  -> existing Predicate
  -> soft topology score + existing NodeOrder
  -> 按排序结果尝试 Node（显式随机 BestNode 保持其 contract）
  -> failure-atomic Statement 暂占
  -> 后续无候选则回退上一选择
  -> 第一个完整 hard-valid placement 即停止：Ready 调用 Commit，Pipelined 不 Commit
```

第一次沿最高分候选向下走，天然就是贪心快路径；它成功时没有额外搜索，失败时也不会
直接把 Job 留在 Pending，而是在同一循环中尝试下一个候选。

### 2. hard 只过滤，soft 只排序

- hard topology 复用 Predicate 思维：不满足就拒绝 Node；
- soft topology 复用 NodeOrder 思维：只贡献分数，不删除任何 hard-feasible Node；
- 所有 hard filter 取交集；
- soft 高分分支失败时，低分 fallback 仍在同一有序候选列表中；
- 不再进行 soft-to-hard conversion；
- 不增加全局 soft optimizer，首个完整 hard-valid placement 即停止搜索；Ready
  调用现有 Commit，Pipelined 不 Commit。

### 3. 以物理 Node 为放置候选

HyperNode 继续作为 topology tree、ancestor/LCA 和 Node mask 的索引，但不再先被选成
一个必须容纳整个 Job 的 allocation container。

- 单根 domain 是一个 Node set；
- `{B,C}` exact forest 也是一个 Node set：`leaves(B) ∪ leaves(C)`；
- B、C 的 LCA 只用于遍历或解释，不会把 A 引入 allowed set；
- Job scope 与 per-SubJob scope 分别由现有 hard callback 展开，再按物理 Node
  交集；
- `NominatedNodeName` 只有通过当前 Task exact scope 后才能保持优先；
- Job/SubJob hard tier 通过增量 LCA 和 final validator 表达；
- required SubGroup relation 基于当前 partial placement 动态重算 exact scope；
- 因此不再需要 `shouldUsePodGroupAntiAffinityCandidateForest` 一类场景分支。

### 4. 有界顺序搜索，不追求最优

- worksheet 保留 ordered pending SubJob/Task；未满足 SubJob 优先，`touched`
  防止已 Ready/`minAvailable=0` Job 空提交，Ready/Pipelined callback 是权威完成
  条件；
- 除显式随机 BestNode 外，candidate 按 soft score、现有 NodeOrder 和稳定 Node
  名称排序；
- 后续 task 无候选时，按选择栈逆序撤销，尝试最近的 alternative；
- 只使用一个核心预算 `maxExtraPlacementAttempts`；第一条贪心分支按 working set
  大小自然获得基础额度，该参数只限制失败后的额外探索；
- 预算耗尽表示本轮未找到，不代表全局不可调度；
- 不使用 conflict graph、backjump、beam search、branch-and-bound 或 improvement
  pass。

### 5. 复用当前 plugin 事务边界

当前 topology dry-run 已经通过 `framework.Statement` 工作：

- Predicate/PrePredicate/NodeOrder/Ready 在当前 Session view 上运行；
- `Statement.Allocate/Pipeline` 更新 Node、Task、Job，并广播 `AllocateFunc`；
- failed `Allocate/Pipeline` 必须在返回前执行自身 inverse；
- rollback/Discard 逆序广播 `DeallocateFunc`；
- `Statement.Commit` 才进入 bind/cache 路径。

015 继续使用这套机制，不新增
`ReadOnly/Reversible/SnapshotRestore/CommitOnly/Unsupported` 分类，也不新增
plugin checkpoint SPI。

只明确当前机制本来就隐含的一条规则：参与 Statement 试探的可变状态必须能被
Allocate/Deallocate 对称恢复。发现 `task-topology` 等现有不对称实现时，修复具体的
既有事务缺口；extender 使用 paired endpoint 量化回退调用次数、device 等执行重复
rollback 测试，不把它们变成 topology planner 的新类型系统，也不改变 extender
当前允许单边 verb 的配置语义。

## 预期收益

- 语义收益：hard 是 filter，soft 是 order，fallback 不再依赖 helper 所在层次。
- 场景收益：B+C、required relation 和 fit-first 回退走同一选择栈。
- 性能收益：常见成功场景只走第一条分支；失败后的额外搜索由一个 budget 限制。
- 架构收益：统一路径不再让 gradient layer 控制终止，也不再进入
  Job/SubJob/forest/soft-anchor 专用 route；当前源码中不再保留第二套 legacy
  allocator，历史对照由备份分支和 Git history 承担。
- plugin 收益：不要求所有 plugin 学习新协议，继续使用当前 Session 和 Statement。
- 可解释性收益：能说明 hard reject、soft order、选择、回退次数和预算终止原因。

## 非目标

- 不保证 soft/preferred 全局最优。
- 不穷举整个 Job 的所有 pending replica，只处理最小 working set。
- 不把 preempt、reclaim、shuffle 和 topology 做联合优化。
- 不重写所有 scheduler plugin 或引入通用 plugin transaction framework。
- 不为每一种 affinity/anti-affinity 组合增加专用 route。
- 不为追求完整搜索而继续提高默认 extra-attempt budget。
- 不在本 Feature 中改变 API、CRD 或用户 topology 字段语义。
- 不把现有 void `Statement.Commit()` 改造成 failure-atomic bind transaction。

## 当前交付范围

当前交付状态：

- 在 `feat/015-unified-topology-placement` 实现统一 planner 与 Statement
  精确回退；
- 删除 legacy/observe 运行路径；`topologyPlanningMode` 配置键仍可解析，
  `legacy`/`observe`/`unified` 旧值都保持启动兼容，但不再改变运行路径；
- 默认 `maxExtraPlacementAttempts=8`，用户仍可显式配置覆盖，负值归一为 `0`；
- 聚焦单元测试、性能数据、新镜像、默认配置 Kind K1-K4 和集群清理均已完成，
  issue 在代码审阅前继续保持 `IP`；
- soft score error policy、extender 新契约和跨周期持久搜索游标不在本轮顺手改变。

详细方案见 `PROPOSAL_COMMIT.md`。

从 `54c1d018` 开始的架构演进、当前实现与目标架构的 reviewer-facing 对照见
`TOPOLOGY_SCHEDULING_REFACTOR.md`。
