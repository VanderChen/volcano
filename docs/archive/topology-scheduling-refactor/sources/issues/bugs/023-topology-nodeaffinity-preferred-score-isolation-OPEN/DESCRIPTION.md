> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 023：拓扑调度隔离原生 preferred NodeAffinity 打分

## 状态

- 类型：Bug / Scheduler architecture
- 状态：OPEN
- 创建日期：2026-08-10
- 影响基线：历史 HyperNode allocator 至当前 `dev-1.14`
- 处理决定：纳入后续拓扑调度架构重构，不做独立补丁式修改
- 关联任务：`issues/features/015-unified-topology-planning-framework-IP/`

## 问题摘要

对于进入 HyperNode allocator 的 Job，Volcano 当前先通过
`HyperNodeOrderFn` 选择 HyperNode，再在选定 HyperNode 的物理 Node 范围内执行
普通 `NodeOrder`。Kubernetes 原生
`preferredDuringSchedulingIgnoredDuringExecution` NodeAffinity 由 `nodeorder`
插件在 Node 层打分，不能参与跨 HyperNode 候选比较。

因此，即使 preferred NodeAffinity 指向的 Node：

- 通过全部 required topology 和普通 Predicate；
- 具有充足资源；
- 所在 HyperNode 也是完整可行方案；

调度器仍可能选择另一个不匹配 preferred NodeAffinity 的 HyperNode。提高 Pod
preferred term weight 或 scheduler `nodeaffinity.weight` 不能改变跨 HyperNode 结果。

该问题与具体 topology plugin 无关：

- `network-topology-aware` 提供的 NTA gradient/HyperNode score 会触发；
- `group-topology-affinity` 提供的 PodGroup/SubGroup topology
  gradient/HyperNode score 也会触发；
- 根因位于公共 HyperNode allocator 对 `HyperNodeOrder` 和 `NodeOrder` 的分层组合。

## 当前行为

当前 topology-aware 调度主路径为：

```text
HyperNode gradient / hard intersection
  -> 按 gradient layer 遍历 HyperNode container
  -> 在每个 container 内对 Task 执行 Predicate 和 NodeOrder dry-run
  -> 只保存达到 SubJobReady/JobReady 的 container 方案
  -> 使用 HyperNodeOrder score 选择 container
  -> 恢复所选方案
```

关键断点：

1. `HyperNodeOrderMapFn` 只聚合注册了 `HyperNodeOrderFn` 的 topology plugin；
2. preferred NodeAffinity 只进入 `NodeOrderMapFn`；
3. `allocateResourcesForTasks` 调用 `bestNode, _ := prioritizeNodes(...)`，丢弃
   NodeOrder 分数；
4. Job 外层只累计 SubJob HyperNode score；
5. 当前资源/shard candidate class 只有一个 Node 时直接选中，NodeOrder 不执行；
6. 第一个存在完整方案的 gradient layer 会终止搜索，后续 layer 不参与比较。

这形成两个互不连通的 soft score 平面：

```text
HyperNodeOrder: NTA / GTA topology contribution
NodeOrder:      NodeAffinity / resource / image locality / other node contribution
```

## 与 required NodeAffinity 的边界

该问题专指 preferred NodeAffinity 的跨 HyperNode 可见性，不表示 required
NodeAffinity 被跳过。

required NodeAffinity 属于 Predicate。每个候选 HyperNode 都会先在其物理 Node
上执行 Predicate dry-run；只有达到 SubJobReady/JobReady 的方案才进入最终
HyperNode 选择。因此，缺少 required 匹配 Node 的 HyperNode 会被判为不可行。

required NodeAffinity 可能与既有逐 Task 贪心、候选采样和无回溯问题组合，导致
存在另一种合法 task-to-node 排列时仍然 Pending；这是 placement search 完备性问题，
不是本 Bug 所描述的 soft score 隔离本身。

## 最小复现场景

```text
HyperNode A -> node-a
HyperNode B -> node-b

两个 HyperNode：
  - 都通过 topology hard constraint；
  - 都有足够资源；
  - 都能完成 Job/SubJob dry-run。

Pod：
  preferred NodeAffinity 指向 node-b，term weight=100。
```

可能结果：

- topology HyperNode score 选择 A；或者
- A/B 的 HyperNode score 相同后随机/非稳定选择 A；
- Pod 最终运行在 node-a；
- NodeAffinity preferred 不会给 HyperNode B 增加外层方案分数。

## 影响

- topology Job 成功运行，但落到非 preferred NodeAffinity Node；
- 调高 `nodeaffinity.weight` 对跨 HyperNode 选择无效；
- HyperNode topology score 相同时无法用 NodeAffinity 打破平局；
- preferred Node 所在 HyperNode 位于后续 gradient layer 时完全不参与比较；
- 普通非 topology Job 的 NodeAffinity 表现正常，而相同 Pod 模板进入
  topology/SubGroup 路径后表现不同；
- 所有普通 NodeOrder soft contribution 都有相同的跨 HyperNode隔离风险，
  NodeAffinity 是当前生产触发案例。

## 历史结论

- `ef7aed782`（2024-11-27，初版 NTA scheduler）已经引入独立
  `PrioritizeHyperNodes` 与 container 内 `PrioritizeNodes`，问题从初版 HyperNode
  allocator 即存在；
- `548d1f64^`（`e2de98c6`）仍然丢弃 NodeOrder score，并仅累计 HyperNode score；
- `548d1f64` 只更新 v1.14.1 版本和镜像元数据，不改变调度行为；
- `b8af4d76` 没有建立 HyperNodeOrder/NodeOrder 桥接，但通过 soft-to-hard
  conversion 让更多 soft topology Job 进入 HyperNode allocator，扩大问题暴露面；
- 后续 native soft、PodGroup/SubGroup topology 修复保留了相同的两级打分结构。

## 期望语义

后续拓扑调度重构应满足：

1. required topology 与 required NodeAffinity 只决定 hard-feasible physical Node；
2. topology soft contribution 与现有 NodeOrder contribution 在同一批
   hard-feasible Node 上组合；
3. preferred NodeAffinity 不被转换为 hard filter，目标不可行时保留 fallback；
4. topology plugin 不需要理解 Kubernetes NodeAffinity，也不复制 nodeorder 逻辑；
5. topology 与 NodeOrder 的权重只应用一次，组合规则可解释、可测试；
6. 普通非 topology Job 保持现有行为；
7. NTA 与 GTA 使用同一个 framework-level 组合语义。

## 本任务非目标

- 不在当前 `dev-1.14` 上增加临时 HyperNode NodeAffinity scorer；
- 不把 preferred NodeAffinity 提升为 required；
- 不通过调大 `nodeaffinity.weight` 或关闭 topology plugin 规避；
- 不在 NTA/GTA 内复制 Kubernetes NodeAffinity 匹配逻辑；
- 不单独修改 API、CRD、controller 或 webhook；
- 不在本 Bug 中启动实现、构建镜像或 Kind 验证。

## 完成条件

本 Bug 保持 `OPEN`，直到拓扑调度架构重构：

- 明确统一 soft score composition；
- 实现 topology soft contribution 与现有 NodeOrder 的 Node-level 组合；
- 完成 NTA、GTA、NodeAffinity preferred/fallback 组合验证；
- 在本任务记录最终验证结果和实现提交；
- 经用户确认后将任务目录改为 `DONE`。
