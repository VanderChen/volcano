# Volcano 上游 network-topology-aware allocate 重构调研：已落地、在途与未解决

## 0. 摘要

- 上游已经做了三件事：
  - 三态 gradient 组合，由 PR #5349 引入。
  - HyperNode 最小资源预过滤，先由 #4940 放进 NTA 插件，再被 #5349 上移到 allocate。
  - 用于诊断的 HyperNode/Node 两维失败摘要。
- #6127 让失败消息变确定。#6069 只加速了单任务的 predicate 阶段。#5928 只融合了节点级打分。三者都没有减少「每个 HyperNode 做一遍 dry-run」的次数。
- 核心结构至今没变：`allocateForJob` 对每层的每个 HyperNode 做完整 dry-run，SubJob 级 dry-run 嵌套在 Job 级 dry-run 里重复执行。
- 平局选择不确定：Job 级按 Go map 迭代序，SubJob 级用 `rand.Intn`。
- 没有「为什么选这个 HyperNode」的决策记录，也没有 HyperNode 相关的 metrics。
- 在途工作：
  - #5811（draft）：给 dry-run 加采样上限和 early-exit，实测 6000 节点/9600 HyperNode 下 5.5 分钟降到数秒。
  - #6196、#6200、#6108、#6081：消息与事件的稳定性和一致性。
  - #6182、#6169：gangreclaim 的 maxDomains 截断。
  - #5514、#5539：soft 模式 anchor 问题。
- 已有一个 #5551/#5848 的 unschedulable-job cache（Alpha，默认关），但 NTA 与 group-topology-affinity 不是 HintProvider，拓扑拒绝基本不可缓存（部分为推断）。
- 本文提到 Job/SubJob/HyperNode 的「zero-gradient」与「Filter→Rank→Verify」时，设计上最大的空白是：Rank 阶段无剪枝、无复用 dry-run 结果、无决策 trace。

**标记说明**：V = VERIFIED IN SOURCE（已在 pinned SHA 源码核对）；PR = PR-DISCUSSION ONLY（只在 PR/issue 讨论里出现）；NV = NOT VERIFIED。

**永久链接约定**：`B` = `https://github.com/volcano-sh/volcano/blob/d423e427c847d37271701bf44276156a97ee0346`，下文链接写作 `B/<path>#Lx-Ly`。

## 1. Pinned 版本

- Pinned SHA 为 `d423e427c847d37271701bf44276156a97ee0346`，即 PR #6264 "avoid double rollback in gang eviction simulation" 的 merge。查询时它同时是 master HEAD，没有更新的提交（V）。
- 最新 release 是 **v1.16.0**（2026-10-14）。同期还有 v1.15.3 和 v1.14.6（2026-09-30）。另有 tag v1.17.0-alpha.0；v1.17.0 预计 2027-01-30，见 #6166（V/PR）。
- 本地 `/root/workspace/volcano-workspace/volcano` 是 fork，不是上游，且没有 `pkg/scheduler/unschedulable`。本文所有 V 标记都是对上游 pinned SHA 核对的。

## 2. 逐项结论

### 2.1 PR #6069 "scheduler: prefilter nodegroup candidates per queue and HyperNode"

**基本信息**：作者 ruanwenjun，MERGED 2026-11-10，merge commit `138edcae2e6458056c0ed9c2c440cfa68fb74984`，关联 issue #6059（已于同日关闭）。

**机制（V）**：
- 新增通用扩展点 `QueueNodeFilterFn`，类型为 `func(*QueueInfo, []*NodeInfo) []*NodeInfo`：[B/pkg/scheduler/api/types.go#L337-L341]。
- Session 的分发在 [B/pkg/scheduler/framework/session_plugins.go#L867-L885]：
  - 按 tier 内插件顺序遍历。
  - 使用 `EnabledQueueNodeFilter`，未设置时回退到 `EnabledPredicate`。
  - 配置项为 `enableQueueNodeFilter`。
- allocate 侧缓存键是 `queueNodeCacheKey{queueID, hyperNode}`，每轮 `Execute` 结束后清空：[B/pkg/scheduler/actions/allocate/allocate.go#L933-L965]。
- 采样预算 `numNodesToFind` 基于**过滤前**的节点数计算，再传给 `NewPredicateHelperWithFeasibleNodeTarget`，所以过滤后候选变少不会缩小 feasible-node 目标。
- nodegroup 插件把静态亲和检查拆给 filter，资源和配额检查仍留在 per-task predicate。
- 基准数据（PR 描述，PR）：250 节点中允许 30 个，predicate 阶段 136µs→16.5µs。这是 predicate-only 微基准，不是端到端数据。

**与三个轴的关系**：
- 效率：只降 per-task predicate 成本，不减少 HyperNode 级 dry-run 次数。
- 兼容性：给其他插件提供了通用节点预过滤钩子，可复用，不要重复提议。
- 可解释性：无。

### 2.2 PR #6127 "stabilize PodGroup scheduling failure messages"

**基本信息**：作者 JesseStutler，MERGED 2026-10-28，merge commit `e4d4f3466b758b459628dd45ab4c937124a92b18`，修复 #6106，属于 #6081 的 R1。

**问题**：`schedulingDimensions` 遍历 task map 后取第一个有 fit error 的 task，导致 PodGroup condition 的 Node 部分随机变化，引发无意义的 PG 更新和 Event（#6106，PR）。

**机制（V）**：现在遍历 `ji.NodesFitErrors`，取 `task.Name` 字典序最小者，忽略 nil error 和不在 `ji.Tasks` 里的 task，格式化为 `"%s: %s"`（`taskName: FitErrors.Error()`）：[B/pkg/scheduler/api/job_info.go#L949-L981]。

**没有做的事**：不排序展示多个 task，不截断，不去重，只展示一个代表 task。每个 Pod 仍保留自己的诊断。PR 里的测试期望字符串（PR）：

```
pod group is not ready, 2 minAvailable, 3 Pending; Pending: 3 Unschedulable. HyperNode: No eligible hypernode; Node: task-a: Insufficient memory
```

**效果（PR）**：kind 上 1s 周期测试，PG 写入从 39.88/s、104/s 降为 0（100 和 300 个 PG）。

**轴**：可解释性中的「确定性」。不涉及决策依据。

### 2.3 #5349：tri-state gradients / placement lifecycle / group-topology-affinity

**基本信息**：作者 wangyang0616，MERGED 2026-10-09，merge commit `3c90d7d8034c3a0a38962846cb161e26b174c876`，对应 issue #5347 的 Task 1–2。组成 commit：
- `472845271`：API 配置。
- `ce2cc51f4` "harden hypernode placement lifecycle"。
- `3f222e025` "compose tri-state hypernode gradients"。
- `ff4715d38` "support podgroup topology anti-affinity"。
- `f5eff6d65`：e2e。
- `c2aa350bd`：设计文档。

**`3f222e025` tri-state gradients（V）**：
- `HyperNodeGradientResult{Unconstrained bool; Gradients [][]*HyperNodeInfo}`，零值表示 REJECT：[B/pkg/scheduler/api/types.go#L427-L458]。
- `HyperNodeGradientForJobFn/SubJobFn(…, purpose SearchPurpose)`，`PurposeAllocate/PurposeEvict`。
- Session 侧 `resolveHyperNodeGradients` 的三种情形（[B/pkg/scheduler/framework/session_plugins.go#L1205-L1262]）：
  - 无回调：返回 `{{root}}`。
  - 全部 Unconstrained：BFS 展开 root 子树。
  - 有受约束插件：交给 `intersectHyperNodeGradients`（#L1263-L1290）。
- 交集按 HyperNode **名字**，不是按层号。`rebuildGradientsByTier`（#L1292 起）层内按名字排序，层间 allocate 升序、evict 降序。所以**候选层的顺序是确定的**。
- Unconstrained 同时带候选会被当作拒绝并记 error。
- `ComputePluginExcludedHyperNodes` 产出各插件的排除统计，供失败摘要使用。
- 新增 `framework/hyper_node_candidate.go`：`AddHyperNodeCandidateFn` 是 nomination 校验用的可选快速路径，`HyperNodeContainsNomination` 校验候选（V，文件整体较小）。
- **min-resource 预过滤上移到 allocate**：`FilterGradientsByMinResource`，聚合 `Idle` 或 `FutureIdle` ≥ minResource 即保留，已有 `allocatedHyperNode` 时跳过：[B/pkg/scheduler/actions/allocate/allocate.go#L1303-L1341]。NTA 在 `PurposeEvict` 仍用 total allocatable 做检查。
- 这个预过滤的前身是 PR #4940（V/PR，作者 3th4novo，MERGED 2026-01-28，merge commit `3223dbc5737a01e6fbd9fa69b4be51ce22ac2355`）。它解决 #4899（1000 节点/1000 SubGroup 场景，调度时间约 1 分钟降到数秒；100 个 SubGroup 时只快 0.5s，PR）。
- 这也说明 #6015（NTA 缓存的 idle/futureIdle 陈旧）已被 #5349 绕过；#6025 的 PR 描述同样提到（PR）。#6031（open）想修的缓存同步仍在审，实际价值有限。

**`ce2cc51f4` placement lifecycle（V，commit 级核对）**：
- 反向拓扑索引 `api/hyper_node_index.go`（`DomainsContaining`）。
- 基于 generation 的 `AllocatedHyperNodeGeneration`/`Dirty`。
- dry-run 的放置回滚：`captureHyperNodePlacement`/`restoreHyperNodePlacement` + `shouldTrackHyperNodePlacement`：[B/pkg/scheduler/actions/allocate/allocate.go#L1127-L1175]。
- cache 回写保护、`job_updater.go` 同步 `AllocatedHyperNode` annotation：[B/pkg/scheduler/framework/job_updater.go#L103-L145]。
- `unschedule_info.go` 新增大量诊断函数（见 2.7）。

**`ff4715d38` group-topology-affinity（V）**：
- 插件路径 `pkg/scheduler/plugins/group-topology-affinity/{constraints.go,group_topology_affinity.go}`，需在 scheduler conf 显式启用。
- 支持 required/preferred `topologyAffinity.podGroupAntiAffinity`。
- 失败标签映射 `hyperNodeExclusionLabels`：`group-topology-affinity→podGroupAntiAffinity`、`network-topology-aware→networkTopology`：[B/pkg/scheduler/api/unschedule_info.go#L107-L112]。
- 限制：BestEffort 的 backfill 不受约束（设计文档 §7 将 Backfill 列为 Future Extension：[B/docs/design/group-topology-affinity.md#L692-L704]）。
- 已知问题：preferred 项的 `topologyTierName` 未知时，`HyperNodeOrderMapFn` 报错，SubJob 每轮被丢弃。见 #6137（open），修复 PR #6213（open，PR）。这是「HyperNodeOrderFn 插件报错即整体失败」的兼容性风险，源码侧错误传播见 [B/pkg/scheduler/framework/session_plugins.go#L1115-L1137]（V）。

**Task 3（SubGroup 级亲和/反亲和）不在 #5349 范围**，仍由 #5347 跟踪，并列入 v1.17 roadmap #6166（PR）。

### 2.4 `pkg/scheduler/unschedulable`（拒绝缓存/hint）

**基本信息**：PR #5848，作者 JesseStutler，MERGED 2026-09-22，merge commit `bbf87708fb75d9ef62d0ab026ddea04d5880d9da`。关联：issue #5551，设计文档 PR #5644（`docs/design/unschedulable-job-cache.md`）。后续有 `c9d394916`（index resource-fit wakeups）和 `5702c0538`（isolate unschedulable job cache）（PR）。

**机制（V）**：
- Feature gate `UnschedulableJobCache`，Alpha，默认关：[B/pkg/features/volcano_features.go#L57-L60]。
- 缓存键是 Job UID；记录 `[]Rejection{Plugin, Source(predicate|allocatable|enqueue), Tasks, HintKeys, Queues}` 和 `retryAfter`（默认 5 分钟，watchdog 每分钟）。
- 失效靠事件：informer → `OnEvent` → `JobHintFn` 返回 Wakeup/Skip；另有 Job 变化失效、freshness 校验。
- **只有有 HintProvider 的插件拒绝才会缓存**：[B/pkg/scheduler/unschedulable/cache.go#L129-L144]。目前只有 predicates（kube + resource_fit）、capacity、proportion 有 HintProvider。
- `HyperNodeEvent` 在 [B/pkg/scheduler/unschedulable/hint.go#L32] 里定义了，但没有 provider 使用（V）。
- 消费方：enqueue、allocate、backfill 通过 `Skip` 决定；preempt/reclaim 忽略它（PR + 源码，部分 V）。
- 记录拒绝：`ssn.CollectJobRejections` 按候选作用域收集；allocate 只合并最佳 HyperNode 的拒绝，以及 hard 模式下 LCA tier ≤ `highestAllowedTier` 的候选的拒绝：[B/pkg/scheduler/actions/allocate/allocate.go#L704-L725]。
- zero-gradient 路径（#L473-L476）直接 `return nil`，没有 `AddRejection`（V）。因此 NTA/group-topology-affinity 造成的拓扑拒绝**不会被缓存**。这是推断，缓存内部对「无 rejection」Job 的处理没逐行核对（部分 NV）。

### 2.5 PR #5928 "perf(nodeorder): run score plugins in a single node traversal"

**基本信息**：作者 songmingming0827，MERGED 2026-09-22，merge commit `bed6d110658ee036db7d694635f3829559c50590`。

**融合了什么（V）**：新增 `RunScorePlugins(preScoreEntries, scoreEntries, cycleState, pod, nodeInfos)`：[B/pkg/scheduler/plugins/util/nodescore/score_helper.go#L66-L191]。
- PreScore 串行。
- 一次 16 worker 并行遍历，对每个节点跑所有 Score 插件。
- NormalizeScore 按插件并行，加权聚合按节点并行，任一错误则丢弃全部结果。
- 调用方：`nodeorder`、`predicates`、`agentscheduler/plugins/predicates`。

**效果（PR）**：agent-scheduler 基准（1000 KWOK 节点、10k pods）吞吐 762.8→936.2 pods/s（+22.7%），P50 延迟 −50.75%。仍是 N×M 次 Score 调用，只是减少并行开销。

**与 HyperNode 的交互**：只作用于每个 HyperNode dry-run 里的节点打分。它不碰 `HyperNodeOrderFn`。NTA 的 `AddBatchNodeOrderFn`（[B/pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go#L311]）不在融合路径内。#5496（open）想并行化 NTA 的 batchNodeOrderFn，#5082 是对应 issue。

### 2.6 设计文档（pinned SHA 的 `docs/design/`）

相关文件（V，目录列表）：
- `Network Topology Aware Scheduling.md`（1028 行）、`group-topology-affinity.md`、`hyperNode-auto-discovery.md`、`hypernode-controller-standalone.md`。
- `preempt-action-support-topology.md`、`gang-aware-eviction-design.md`、`evictablefn-evolution-for-gang-eviction.md`。
- `sharding_controller.md`、`capacity-dra-support.md`、`task-topology-plugin.md`。
- `unschedulable-job-cache.md`、`scheduling-reason.md`、`podgroup-condition-reason.md`、`min-avaiable-member-resource.md`、`metrics.md`。
- 没有文件名含 *subgroup* 或 *affinity*（`group-topology-affinity.md` 除外）。

**`Network Topology Aware Scheduling.md`（V）**：
- Tier 选择：`highestTierAllowed` 与 `highestTierName` 互斥。soft = 偏好、无硬边界；hard = 有边界：[B/docs/design/Network Topology Aware Scheduling.md#L290-L338]。
- 文档里的 gradient 签名还是旧的 `[][]*HyperNodeInfo`，已落后于代码。
- 流程：gradient 从低到高，每层对每个 HyperNode dry-run；当前层有解则跳过后续层。
- 选择：SubJob 级取 `HyperNodeOrderFn` 最高分；Job 级取各 SubJob 最佳分之和最高者。**文档没有规定平局规则。**
- NTA 的 HyperNode 打分是「HyperNode 资源利用率越高分越高」的 bin-pack。
- 文档里标了 TODO：「propose」插件，在 Gradient 0 给出推荐 HyperNode 以跳过后续层（未实现，仍是空白）。
- Performance 章节只提到由 hyperNode CRD 构建 nodeGroup 可能耗时。**没有关于失败报告的规定。**

**NTA 打分与参数（V，源码）**：
- bin-pack 分 `getSubJobHyperNodeBinPackingScore`：[B/pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go#L412-L465]。`score=(used+request)/allocatable` 按资源加权，若 `used+request > allocatable` 则为 ZeroScore。
- 平局内部破解用 `scoreWithTaskNum`（任务分布比例），最后 `scaleFinalScore`（`MaxNodeScore*weight*score`，约 #L808-L845）。
- 参数（#L35-L66）：`weight`、`hypernode.binpack.cpu`、`hypernode.binpack.memory`、`hypernode.binpack.resources`（及 `.resources.<name>`）、`hypernode.binpack.normal-pod.enable`（默认 true）、`hypernode.binpack.normal-pod.fading`（默认 0.8）、`hypernode.gradient.evict.max-hypernodes`（默认 8）。

**`group-topology-affinity.md`（V）**：
- §6.4（[B/docs/design/group-topology-affinity.md#L454-L485]）：三态结果含义表、按名字取交集、层内按名字排序保证确定性、预选候选集不得被单个插件提前截断。
- §6.5（#L487-L533）：HyperNode selection 与 Node selection 两级；注意「通过拓扑检查不保证节点 predicate 通过」；min-resource 预过滤只对 `PurposeAllocate`。
- §6.7（#L550-L602）：失败聚合管线、示例、日志级别；并写明「Pod status 只在 reason/message/nominated Node 变化时更新」。
- §7（#L692-L704）：Future Extensions，其中列了「Enqueue pre-check」、「API-level observability（拓扑域 annotation 或 `TopologyUnsatisfiable` 条件）」、Backfill 支持、Cross-PodGroup affinity。这些**设计上被明确推迟，不是已做**。

**`preempt-action-support-topology.md`（V，仅标题层核对）**：有 In Scope/Out of Scope/Key Function Modifications/Limitations，正文未逐段阅读（NV）。

### 2.7 今天的失败消息（V，pinned SHA）

**PodGroup 层**：
- gang 插件 `OnSessionClose` 构造 condition：Type `Unschedulable`，Status True，Reason `v1beta1.NotEnoughResourcesReason`（PR 里写作 `NotEnoughResources`），`TransitionID=ssn.UID`，Message：

```
"%v/%v tasks in gang unschedulable: %v"
```

即 `unreadyTaskCount = MinAvailable − schedulableTaskNum`、`len(job.Tasks)`、`job.FitError()`：[B/pkg/scheduler/plugins/gang/gang.go#L229-L272]。
- PodGroup Warning Event 另在 `RecordJobStatusEvent` 构造，用 **`len(job.TaskStatusIndex[Pending])`** 作分子，所以与 condition 的计数不一致：[B/pkg/scheduler/cache/cache.go#L1793-L1806]。这就是 #6200（open）和修复 PR #6202（open）的问题（PR）。
- `job.FitError()` 形如 `"<PodGroupNotReady|Ready>, <n> <status>…, <n> minAvailable; Pending: <n> Unschedulable. <HyperNode: …; Node: task: …>"`：[B/pkg/scheduler/api/job_info.go#L909-L981]。

**Pod 层**：
- `TaskSchedulingReason`（[B/pkg/scheduler/api/job_info.go#L983-L1017]）：
  - Allocated：`"Pod ns/name can possibly be assigned to <node>, once minAvailable is satisfied"` + dimension 摘要。
  - Pipelined：`"…, once resource is released and minAvailable is satisfied"`。
  - Pending：`FormatSchedulingDimensions(JobFitErrors, fe.Error())`，即 `"HyperNode: <x>; Node: <y>"`。
- 事件 reason 是 `FailedScheduling`：[B/pkg/scheduler/cache/cache.go#L1237-L1240]。
- #6196（open）显示：Pipeline 回滚后消息会在「试探节点名」之间抖动，原因包括 `SelectBestNodeAndScore` 的 `rand.Intn` 随机破平局（[B/pkg/scheduler/util/scheduler_helper.go#L209]）和依赖 `LastTransaction`（PR 讨论，JesseStutler 提议停止用 `LastTransaction` 构造发布诊断，并已 assign 去出 proposal，PR）。

**HyperNode 维度摘要（V，[B/pkg/scheduler/api/unschedule_info.go#L164-L247]）**：
- 格式 `"%d/%d hyperNodes available"`，有资源排除时加 `" (minResource: <res>)"`，再接 `": <tierName> final/total (n networkTopology, m podGroupAntiAffinity, k minResource); …"`。tier 与原因都排序以保证确定性。
- 设计文档示例（PR/文档，非实际输出）：

```
HyperNode: 1/4 hyperNodes available (minResource: cpu 8): supernode 1/2 (1 podGroupAntiAffinity); rack 0/2 (1 networkTopology, 1 minResource); Node: worker-0: In hyperNode sn-b: 0/3 nodes are unavailable: 2 Insufficient cpu, 1 node(s) pod number exceeded
```

- SubJob 失败用 `"; subJob <id>: <summary>"` 合并到 Job 基线：`MergeHyperNodeFitSummary`。
- `FitErrors.Error()`（#L392-L420）：默认 `"0/N nodes are unavailable"`，否则 `"<err>: <count reason>, …"`（原因直方图排序），设置了 hyperNode 时加前缀 `"In hyperNode <name>: "`。
- 常量（#L27-L43）：`NodePodNumberExceeded = "node(s) pod number exceeded"`、`NodeResourceFitFailed = "node(s) resource fit failed"`、`AllNodeUnavailableMsg = "all nodes are unavailable"`。

**重要局限（V）**：Node 维度的错误来自**最后一个被尝试候选**。`allocateForJob` 对每个候选调用 `job.ResetFitErr()`（[B/pkg/scheduler/actions/allocate/allocate.go#L489-L493]），所以最终 `NodesFitErrors` 只反映最后一次 dry-run，不是「各 HyperNode 失败原因汇总」。

### 2.8 平局与选择（V）

- Job 级 `selectBestHyperNodeForJob` 遍历 `subJobsAllocationScores` map，用严格 `>`：[B/pkg/scheduler/actions/allocate/allocate.go#L727-L744]。**平局结果取决于 Go map 迭代序，是不确定的。**
- SubJob 级 `selectBestHyperNodeForSubJob`（#L746-L769）走 `PrioritizeHyperNodes` + `SelectBestHyperNodeAndScore`，后者在同分候选里 `rand.Intn` 随机选：[B/pkg/scheduler/util/scheduler_helper.go#L213-L227]。
- 第一层有解就 `return finalStmt`，更高层不再评估（#L549-L575）。
- `HyperNodeOrderMapFn` 任一插件报错即整体失败（见 2.3 末尾）。

### 2.9 Metrics（V）

对 `metrics.go` 与 `job.go` 按 `hypernode|topolog` 做大小写不敏感 grep，**没有任何 HyperNode/拓扑相关指标**。`queue.go`、`namespace.go` 没查（NV）。现有相关指标都是通用的：
- `plugin_scheduling_latency_milliseconds{plugin,OnSession}`、`action_scheduling_latency_milliseconds{action}`、`scheduling_stage_duration_milliseconds{stage}`（#L117-L148）。
- `unschedule_task_count{job_id}`、`unschedule_job_count`（#L186-L197）。
- `unschedulable_job_cache_{skips,wakeups,watchdog_expirations}_total`（#L202-L219，由 debug 开关控制）。
- 链接：[B/pkg/scheduler/metrics/metrics.go#L114-L220]。
- allocate 里除 `RegisterUnschedulableJobCacheSkip` 和 e2e 耗时外，没有对 dry-run 次数、候选数计数的埋点（grep allocate.go，V）。

## 3. 已解决 vs 仍开放

### 3.1 已在上游解决（别再重新提议）

| 事项 | 编号 |
|---|---|
| 三态 gradient + 按名字取交集 + 按层确定性排序 | #5349 (`3f222e025`) |
| HyperNode 最小资源预过滤（NTA 内 → allocate 内） | #4940 → #5349 |
| HyperNode 级失败摘要（按插件/tier 的排除统计、标签映射） | #5349 (`ce2cc51f4`) |
| PodGroup 反亲和插件（Job/SubJob 级 required、preferred 打分） | #5349 (`ff4715d38`) |
| dry-run 放置回滚、反向索引 | #5349 (`ce2cc51f4`) |
| 队列节点预过滤通用钩子 | #6069 (#6059) |
| 融合节点打分 | #5928 |
| PG 失败消息选 task 的确定性 | #6127 (#6106) |
| Unschedulable job cache（Alpha） | #5848 (#5551, 设计 #5644) |
| soft 模式 job anchor（部分） | #5206 |

### 3.2 仍开放或在途

**效率**：
- #5811（draft）：采样上限 `hyperNodeCandidateSampleSize=200` 和 `hyperNodeEarlyExitPatience=20`（PR diff，PR）。基于旧 allocate.go，没有 maintainer review。
- #4899（closed）里「SubGroup 失败后不继续尝试」这一半没做。
- #5819：大规模重启恢复和延迟（open）。
- #5082 / #5496：打分并发，NTA batchNodeOrderFn 并行化（open）。
- NTA「propose」TODO 与「Enqueue pre-check」：设计文档推迟，未实现。

**可解释性**：
- #6196：Pipeline 回滚导致消息抖动（open，JesseStutler 在做 proposal）。
- #6200 / #6202：gang 不同载体里的未就绪计数不一致。
- #6081 / #6108：PG 状态与 Event 重复写（R1–R5）；R1 要求去掉节点计数、直方图计数、节点名，与 #6127 的方向相关。
- #6180：首轮 gang 调度时误报 Unschedulable。
- 无「为何选此 HyperNode」记录，无平局解释，无拓扑 metrics，无 `TopologyUnsatisfiable` condition（设计文档 §7 列为 Future）。#5262（open）做 Pod 状态/FailedScheduling 事件节流。

**兼容性**：
- #6137 / #6213：未知 tier name 的 preferred 项导致整 PG 不调度。
- #5335（closed）：dry-run 回滚后 vGPU annotation 残留导致超卖，说明 dry-run 副作用对其他插件有风险。
- #5362 / #5364：`RecoverOperations` 失败后没 `Discard`。#5853：`Pod.Spec.NodeName` 回滚未恢复。
- #5514 / #5539：Job+SubJob 都 soft 时 anchor 不传递。

**拓扑功能**：
- #5347：SubGroup 亲和/反亲和（v1.17）。
- #5430：跨 PodGroup 拓扑打包。
- #5732 / #5877 / #5752：异构深度的 HyperNode 树。
- #4374 / #5920：preempt/reclaim 的拓扑感知（#5920 为 open PR）。
- #6169 / #6182：gangreclaim 被 `maxDomains` 截断，导致只能放在第 9+ 个 HyperNode 的 Job 永远调度不了（open）。
- #6065：gangreclaim 驱逐多余 victim。
- #6028：与 kube-scheduler 集成。
- #6161 / #6192 / #6172 / #6173：拓扑层级定义与 HyperNode 状态可见性。
- #5751：通用 xPU 拓扑。#4233：任务 rank 的节点重排（open）。

**分片 / DRA / 拓扑**：
- 对 `hypernode shard`、`network topology dra` 等关键词的搜索**没有命中专项 issue**（NV 级别空白）。DRA 与拓扑的关系只在 #6166 roadmap 里间接出现。分片相关只见到 `sharding_controller.md` 和 `predicate_helper.go` 对 `NodesInShard` 的处理（[B/pkg/scheduler/util/predicate_helper.go#L106-L111]）。

## 4. 对 Filter→Rank→Verify 的提示

- 上游已经把「Filter」的一部分做了：三态 gradient、最小资源预过滤、nomination 快速路径、队列节点过滤。再提 Filter 阶段的拓扑/资源预过滤时，必须说明与这些的差异。
- 上游「Rank」仍是拿 dry-run 结果打分后选择最高。#5811 是最接近的在途方案，但它是在 dry-run 循环里加早停，不是在 dry-run 之前排序。
- 「Verify」基本对应现在的 dry-run + `RecoverOperations` 重放。重放 winner 的开销和失败后的回滚（#5362/#5364）已有讨论。
- 可解释性目前的上限是「HyperNode 过滤摘要 + 最后一次 dry-run 的节点错误」。

## 5. Gaps / 不确定项

- **PR #5349 的各 commit 细节**主要靠 commit 级文件列表与 diff 抽查，没有逐行读 `ff4715d38` 的 plugin 实现（NV 级别）。
- **「拓扑拒绝不可缓存」**是推断：只核对了 `cache.go#L130-L144` 的 HintProvider 规则和 allocate 的 zero-gradient 路径，没跑实验或逐行读 `Record` 对空 rejections 的处理。
- **PR #5811**：只核对了 PR 描述和 diff，没有 maintainer 审核意见；其 5.5 分钟→数秒的数据是作者自测（PR）。
- **PR #6031/#6025**：状态以 PR 描述为准；#6025 称 #6015 已被 `3f222e025` 解决，我核对了 allocate 侧 `FilterGradientsByMinResource` 使用实时数据（V），没有逐行核对 NTA 缓存是否还保留 idle/futureIdle 字段。
- 没查：`queue.go`/`namespace.go` 里的 metrics；`preempt-action-support-topology.md` 正文；sharding 与 DRA 与 HyperNode 的专项设计。
- 日期是上游返回的值（2026 年），按原样引用。
- 本地 fork 里有用户自己的拓扑重构文档（commits `b231f3ea0`、`e0933008e`），未与上游内容比对。