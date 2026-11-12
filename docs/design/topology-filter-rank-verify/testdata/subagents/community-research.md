# 网络拓扑感知调度：可比系统调研报告（面向 Volcano「Filter → Rank → Verify」重构）

调研时间约 2026-11-11。所有版本、日期均取自工具返回值，按原样引用。

## 0. 先读这一节

**一句话结论**：Kueue、KAI、Koordinator、Slurm 都没有「对每个候选域完整模拟整个 gang」这一步。唯一和 Volcano 当前结构同构的是 k/k 的 KEP-5732（v1.37.2）。它在 master 和 v1.38.0-beta.0 里已经改成「串行、无打分插件时首个成功即停、带百分比采样和 shuffle」。

- **KAI 最接近 Filter → Rank → Verify。** 它先做资源数学预过滤，再做确定性排序，然后用真实 per-task 流水线逐个验证，首个成功即返回。
- **Kueue 与 Slurm 只有 Rank，没有 Verify。** 排序是确定性的，失败信息是统计型的，但 Kueue 因没有验证而在 kube-scheduler 不一致时卡 Pending。
- **没有任何系统输出「排名列表 + 逐域 verdict」。** 失败时的聚合摘要和成功时的决策日志都只有部分实现，详见 §3。
- **Volcano 上游已解决的「Filter」部分**：三态 gradient、minResource 预过滤、`QueueNodeFilterFn`（见 §2.7）。
- **Volcano 上游最接近的在途方案是 #5811。** 它是 draft，做法是在现有 dry-run 循环里加采样上限与早停，不是「先排序再验证」。

**链接约定**（下文 `XXX:path#Lx-Ly` 展开为 基址 + `/path#Lx-Ly`）：

| 缩写 | 基址 |
|---|---|
| `KUE:` | https://github.com/kubernetes-sigs/kueue/blob/v0.20.5（commit `7e71578095b353cedbfa63607b60c9739af9d501`，2026-11-05 发布；main `115135775a68` 另有差异，未读） |
| `KAI:` | https://github.com/NVIDIA/KAI-Scheduler/blob/2305acfc348c9bd9cd1ab813d3feb8dfc4ab9d11（= v0.19.0 = 读取时的 main；仓库已更名 `kai-scheduler/KAI-Scheduler`） |
| `K8S:` | https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0（master，2026-11-11） |
| `K8S137:` | https://github.com/kubernetes/kubernetes/blob/v1.37.2（`c50e5395f1ae`） |
| `KEP:` | https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/（5732 目录 `5732-topology-aware-workload-scheduling/README.md`，4671 目录 `4671-gang-scheduling/README.md`） |
| `KOORD:` | https://github.com/koordinator-sh/koordinator/blob/989ca85c62abcca92b303aa12fd2ccff2ed30fed（v1.8.0，2026-04-16）。main = `949aba5df7db`，记作 `KOORD-M:` |
| `SLURM:` | https://github.com/SchedMD/slurm/blob/slurm-26-05-6-1（`599e8ac50eaf`） |
| `VOL:` | https://github.com/volcano-sh/volcano/blob/d423e427c847d37271701bf44276156a97ee0346（上游 master，= PR #6264 合并；最新 release v1.16.0，2026-10-14） |

**验证标签**：
- `[SRC]`：读了源码。
- `[SRC✓]`：由我本人在子调研之外再次读源码核对。
- `[DOC]`：只读了文档或 KEP。
- `[PR]`：只读了 PR 或 issue 文本，未复现。
- `[NV]`：未验证。
- `[推]`：推断。

**核对范围说明**：本报告大部分事实先由 5 个并行子调研读源码得出。我对关键点重新读了源码，包括 Kueue 比较器与失败消息、KAI 的 allocate 循环与 `sortTree`、k/k 的 `placementsToEvaluate` 与 `findBestPlacement`、k/k v1.37.2 的 tie-break 代码、Koordinator 比较器、Volcano allocate 的选择与 fit-error 重置、PR #5811 的元数据、Slurm 源码与文档。这些在文中标 `[SRC✓]`。

---

## 1. 对比表

| 系统（版本） | 候选域如何生成 | 是否对每个候选做整 gang 模拟 | 排序 / tie-break | 其他插件的 filter / score 如何参与 | 成功 / 失败时用户看到什么 | 复杂度与效率控制 |
|---|---|---|---|---|---|---|
| **Volcano 上游**（`d423e427c`）[SRC✓] | 各插件 hard 候选的交集，按层（低→高）排，已有三态 gradient | **是**：每层每个 HyperNode 做完整 dry-run（SubJob 嵌套），再 Discard | 层内 `HyperNodeOrderFn` 取最高分。Job 级平局靠 Go map 序，SubJob 级 `rand.Intn` | 节点级 predicate、score 只在域内运行，不影响选域 | Condition 与 Event 带 HyperNode 过滤摘要；Node 部分只反映最后一个被尝试的候选。无选择依据，无 HyperNode 指标 | 每层 O(候选数 × task × 节点)。已有 minResource 预过滤；#5811 draft |
| **Kueue TAS**（v0.20.5）[SRC] | 叶子容量计数，自底向上聚合，每层一次排序 | **否**：只有资源算术，无 predicate/score/事件回调 | 确定性。BestFit 为 sliceCount 降序，再 podCount 升序，再 `levelValues` 字典序 | Kueue 自己复刻 taint、nodeSelector、必需 affinity、资源（可选 alpha 的 scheduler-library 跑 Filter，无 Score）。不建模 pod affinity、topologySpread、Score | 失败有统计型消息，成功写入 `topologyAssignment`；「为何选此域」只有 V(3) 日志 | 无复杂度声明，推导为 Σ d log d 量级；有拓扑树、叶子匹配等缓存；抢占复用同一算法 |
| **KAI**（v0.19.0）[SRC✓] | 资源数学（`AllocatablePods` / ratio）预过滤，产出有序 `[]NodeSet` | **否（首个成功即返回）**：逐 NodeSet 跑真实流水线，失败 `Rollback` | 父节点内 ratio 降序再 ID 升序，层级优先；无随机 | predicate、node-order 原样在 NodeSet 内运行；节点分不影响选域；preferred 时反向给节点加分 | 失败聚合文案走 PodGroup 事件与 condition；没有选域解释 | 每次调用全树重算（#2575 open）；无基准；有 ratio 缓存 |
| **k/k KEP-5732**（v1.37.2 / master `dd21ea95b`）[SRC✓] | `PlacementGenerate` 插件（如按 label 值分域） | **v1.37.2 是**（无上限、无 shuffle）；**master 仅在有 PlacementScore 插件时评估前 N 个，否则首个成功即停** | `PlacementScore` 打分后用 `rand.Int()` 破平局；placement 顺序也随机 | 复用 pod 级 Filter/Score，节点集限定为 placement；PreFilter 状态仍看全集 | 只报告一个任意 placement 的状态：`0/N placements are available, first placement status: …`；有 3 个 ALPHA 指标 | `PercentageOfPlacementsToScore`、NNN 优先、`PlacementFeasible` 早拒；placement 间串行 |
| **Koordinator**（v1.8.0）[SRC] | 从 Node label 重建拓扑树（10s 一次），MustGather 层 BFS | **否**：每节点用真实 Filter 链估算 offer slot，之后全是树上整数运算；之后每 Pod 再走一次正常周期校验 | 确定性。ExistingPodNum 多者优先，OfferSlot 小者优先（binpack），Score，Name 升序；首个成功即返回 | 复用 Filter 链估算 offer slot；节点级 Score 不参与选域 | v1.8.0 消息逐域列 offer slot 过长（约 120KB），main 已改为只给最大值，逐域明细转 klog V(4) | O(节点 × pod) 一轮 Filter；计划缓存；有 FIXME 限制 |
| **Slurm tree/block**（26.05）[DOC + 部分 SRC] | 拓扑树 / 块 | 否 | 文档：选满足的最低层 switch，再叶子 best-fit；源码：tightest fit，同距离看节点数、层级、下标 | Node `Weight` 会覆盖拓扑选择 | 有 `ESLURM_TOPO_*` 错误表；`--switches=count[@time]` 软约束 | 文档称列出全部 switch 会拖慢调度 |
| **Borg**（EuroSys 2015，可选对照）[SRC：论文 PDF 原文] | — | — | 随机顺序检查机器，凑够「足够多」可行机器再打分 | 可行性 + 打分两阶段 | — | Score 缓存、equivalence class、relaxed randomization |

---

## 2. 逐系统说明

### 2.1 Kueue TAS（`kubernetes-sigs/kueue`，v0.20.5，读的是源码）

核心文件是 `KUE:pkg/cache/scheduler/tas_flavor_snapshot.go`（已从旧的 `pkg/cache/` 下移过来）。

**算法阶段** [SRC]
- **入口**：`FindTopologyAssignmentsForFlavor` 调 `findTopologyAssignment`（`KUE:pkg/cache/scheduler/tas_flavor_snapshot.go#L1491-L1772`）。源码注释写明 Phase 1（自底向上算每个域的 pod 数与 slice 数）和 Phase 2 a–d（排序、选连续域、逐层下降、构建最低层 assignment），见 `#L1472-L1490`。
- **叶子计数**：`fillLeafCounts` 算 `podCount = requests.CountInWithLimitingResource(remaining)`，`remaining = free − tasUsage − 本周期 assumedUsage`（`#L2907-L2960`）。
- **自底向上聚合**：`fillInCountsHelper`（`#L2962-L3060`）。
- **选层**：`findLevelWithFitDomains`（`#L2001-L2175`）。
  - 顶部域放得下：BestFit 取能放下的最小域，LeastFreeCapacity 取升序里第一个能放下的域。
  - 放不下：required 失败，preferred 递归上一层，unconstrained 贪心选多域。
- **逐域模拟**：没有。只有对共享状态数组的算术。

**比较器** `[SRC✓]`（`KUE:pkg/cache/scheduler/tas_flavor_snapshot.go#L2576-L2602`）：
```go
if respectNodeAffinityPreferred && a.affinityScore != b.affinityScore { return cmp.Compare(b.affinityScore, a.affinityScore) } // 降序
if a.sliceCount[obligationNone] != b.sliceCount[obligationNone] {
    if isLeastFreeCapacity { return cmp.Compare(a, b) }   // 升序
    return cmp.Compare(b, a)                              // BestFit：降序
}
if a.podCount != b.podCount { return cmp.Compare(a.podCount, b.podCount) } // 升序
return s.compareDomainLevelValues(a, b)  // slices.CompareFunc(levelValues, strings.Compare)
```
- 平局最终落在 `levelValues` 的字典序，完全确定，没有随机。
- 注意比较器第一项 `affinityScore`：它是把节点 preferred affinity 聚合成域分，作为排序首键（见 §3 的启示）。

**profile 与 feature gate** [SRC]（`KUE:pkg/features/kube_features.go#L1028-L1298`）

| Gate | 阶段 / 默认 | 引入版本 |
|---|---|---|
| `TopologyAwareScheduling` | Beta，默认 true | alpha 0.9，Beta 0.14 |
| `TASProfileMixed` | Beta，默认 true | alpha 0.10，Beta 0.15 |
| `TASBalancedPlacement` | alpha，默认 false | 0.15 |
| `TASNodeFeasibilityForAllLevels` | Beta，默认 true | 0.20 |
| `SchedulerLibraryIntegration` | alpha，默认 false | 0.19 |
| `TASRespectNodeAffinityPreferred` | alpha | 子调研已读，具体版本未单独记录 |

- `TASProfileBestFit` 已弃用；`TASProfileLeastFreeCapacity`（0.17）和 `TASProfileMostFreeCapacity`（0.13）已删除。
- 选择矩阵（KEP）：preferred = BestFit，required = BestFit，unconstrained = LeastFreeCapacity。开 `TASBalancedPlacement` 时 preferred 改为 BalancedPlacement，失败回退 BestFit。
- 复杂度：源码与 KEP 均未给出 [NV]；我推导为叶子数 × 资源种类 + 每层 O(d log d)，没有「域数 × 节点数 × task 数」的乘积 [推]。

**失败消息（原文）** `[SRC✓]`（`KUE:pkg/cache/scheduler/tas_flavor_snapshot.go#L3062-L3085`）
- `topology %q doesn't allow to fit any of %d %s(s)`（单位为 `pod` 或 `slice`）。
- `topology %q allows to fit only %d out of %d %s(s)`。
- required 时追加 `; all pods must fit in a single %q topology domain`。
- 有排除统计时追加 `. Total nodes: %d; excluded: %s`。排除原因由 `formatReasons`（`#L806-L831`）给出，取值有 `nodeSelector: N`、`affinity: N`、`topologyDomain: N`、`taint "<taint>": N`、`resource "<res>": N`、`schedulerLibraryNoFit: N`、`draNoFit: N`。
- `Failure()` 按 PodSet 名字排序取第一项，注释说明是为了跨周期稳定（`#L638-L650`）。
- 呈现路径：
  - `AssignTopology`（`KUE:pkg/scheduler/flavorassigner/flavorassigner.go#L515-L567`）。
  - 汇总为 `couldn't assign flavors to pod set <name>: <msg>`（`…/assignment.go#L268-L286`）。
  - 通过 `requeueAndUpdate`（`KUE:pkg/scheduler/scheduler.go#L1255-L1295`）写 Workload 的 `QuotaReserved=False` 条件，并发 Warning Event，reason 为 `TopologyPlacementFailed`（`KUE:apis/kueue/v1beta2/workload_types.go#L968-L970`）。
- 对用户解释力最强的设计：失败先用当前用量试，再以 `WithSimulateEmpty(true)` 重试。「空集群也放不下」才报 `NoFit`，否则保持 `Preempt`，表示是碎片问题可靠抢占解决。

**决策记录** [SRC]
- `status.admission.podSetAssignments[].topologyAssignment`（`KUE:apis/kueue/v1beta2/workload_types.go#L431`、`#L458-L555`）。
- v1beta2 是压缩的叶子层 slice 格式；v1beta1 是 `domains[{values,count}]`。
- 执行靠 schedulingGate `kueue.x-k8s.io/topology`，由 `topology_ungater.go` 去 gate 并写 nodeSelector。

**与其他约束的交互**
- Kueue 复刻的过滤 [SRC]：
  - 节点 Ready / 可调度。
  - allocatable 减去其他 TAS workload 用量与非 TAS pod 用量（DaemonSet 等，由事件驱动的 `nonTasUsageCache` 维护）。
  - flavor nodeLabels、tolerations、pod nodeSelector、必需 nodeAffinity。
  - 位置：`KUE:pkg/cache/scheduler/scheduling_simulator_default.go#L52-L116`。
- 不建模 [PR]：pod affinity/anti-affinity、topologySpreadConstraints、Score 插件。
  - issue #3755：TAS 可能准入 kube-scheduler 层面不可调度的 workload。
  - issue #15057。
- kube-scheduler 不一致时：pod 卡 Pending，靠 `waitForPodsReady` 超时驱逐再重排，没有反馈回路 [PR]。
  - #18550（open）：硬 anti-affinity 无解的 Job 被准入，一个 pod 运行、另一个 Pending。
  - #5243（closed）。
  - #18551（open）：是否与 TAS 相关未确认 [NV]。
  - 这些都是用户报告，我未复现。
- 缓解措施：
  - 最低层不是 hostname 时，0.20 前只校验域总量；`TASNodeFeasibilityForAllLevels`（0.20 Beta）缓解。KEP 承认该问题，见 `KEP 2724 README#L2003-L2009` [DOC]。
  - 可选的 scheduler-library 只跑 Filter，不跑 Score；源码注释自承「可能比真实调度器更严格」（`KUE:pkg/cache/scheduler/was/scheduler_simulator.go#L38-L161`）。
  - 总括 issue #8871。

**节点故障** [SRC + DOC]
- `TASFailedNodeReplacement`（Beta，0.14）及若干相关 gate。
- `findReplacementAssignment`（`KUE:pkg/cache/scheduler/tas_flavor_snapshot.go#L1103-L1135`）保留原分配、只替换故障域，仅适用于 hostname 最低层。
- `status.unhealthyNodes` 最多记录 8 个。

**效率** [SRC]
- 每周期 `Snapshot`（`KUE:pkg/cache/scheduler/snapshot.go#L290-L420`）。
- 拓扑树、叶子匹配、剩余资源都有 generation 缓存。
- 抢占复用同一算法，每试一个 victim 就重跑一次完整的 `FindTopologyAssignments`（`KUE:pkg/scheduler/preemption/preemption.go#L567-L596`）。可逆性靠对 snapshot 加减用量（`snapshot.go#L178-L216`），不是事务回滚。
- 热点证据 [PR]：issue #13148（closed）称约 2k 节点、700 个 workload 模拟抢占时一个周期约 40s，其中约 90% 在 `findTopologyAssignment`，又约 90% 在 `fillInCounts`；已做缓存优化。基准规模未读 [NV]。
- 周期内冲突：`TASRecomputeAssignmentWithinSchedulingCycle`（0.19 Beta）在评估阶段重算（`KUE:pkg/scheduler/scheduler.go#L870-L940`），因为同周期不同 CQ 各自独立计算容易选到冲突域（`#L487-L492` 注释）。

**确定性与可解释性**
- 确定性：见上面的比较器、`Failure()` 排序、多层消息按域 ID 排序（`tas_flavor_snapshot.go#L3098-L3137`）。
- issue #11910（closed）指出 balanced 路径等分域可能随机，要求稳定 tie-break；balanced 路径最终实现我没读完 [NV]。
- 没有 API 级的「为何选此域」记录，只有 V(3) 日志。
- 用户诉求：#10505（open）要域用量指标；#14850、#4570 要「required 请求也能选不同策略」；#10574（open）指出贪心不是最优，如容量 [19,10,10] 放 20 会选 19+10。
- KEP 的「Future Vision」设想类似 kube-scheduler 的加权打分模型（affinity、容量打包效率）并归一化，目前未实现 [DOC]。

### 2.2 NVIDIA KAI-Scheduler（v0.19.0 = main，`2305acfc`）

**树与算法** [SRC]
- 插件 `KAI:pkg/scheduler/plugins/topology/topology_plugin.go#L44-L119`，只注册 `SubsetNodesFn`、`NodeOrderFn`、`PreJobAllocationFn`，不注册事件处理器。
- 树每个 session 建一次，所有 job 共享。
- 每次 `subSetNodesFn` 调用都全树重置并重新汇总 Idle + Releasing（`KAI:pkg/scheduler/plugins/topology/job_filtering.go#L34-L116`、`#L196-L215`、`#L484-L491`）。这是 preempt/reclaim 模拟能看到 victim 释放资源的原因，也是 #2575（open）里的性能点。
- **域可分配数是资源数学，不是模拟**：
  - 同质时取各维度所有 task 的最大值作 representor pod，逐节点算 `min_dim floor(nonAllocated/max)`，沿树求和（`#L171-L269`、`#L600-L629`）。
  - 非同质走聚合比例检查 `checkJobDomainFit`（`#L407-L425`）。
  - 非 GPU 的 DRA 维度被跳过，分数 GPU 仅近似（`#L240-L249`）。
- **required / preferred 选域**（`#L427-L470`、`#L271-L321`）：收集所有相关 level 上全部满足的域，不是只取第一层。
- **排序** `[SRC✓]`（`KAI:pkg/scheduler/plugins/topology/job_filtering.go#L511-L538`）：
  - 每个节点的 children 按 `getJobRatioToFreeResources` **降序**排，平局用 `cmp.Compare(i.ID, j.ID)`。
  - 函数注释写「按 AllocatablePods 升序」与代码不符，注释已过时。
  - 效果：父节点内 ratio 降序 + 低层优先（`sortDomainInfos`，`#L580-L594`）。**不是跨父节点的全局打分**。无随机。

**allocate 消费方式** `[SRC✓]`（`KAI:pkg/scheduler/actions/common/allocate.go#L47-L73`、`#L115-L142`）：
```go
for _, nodeSet := range nodeSets {
    cp := stmt.Checkpoint()
    if allocate...(nodeSet) { return true }   // 首个成功即返回
    stmt.Rollback(cp)
}
```
- `Checkpoint()` 返回 `len(operations)`，`Rollback` 逆序撤销（`KAI:pkg/scheduler/framework/statement.go#L45-L64`）。
- 事件处理器仍会在每次尝试和回滚时触发（`statement.go#L336-L405`、`#L439-L479`），所以 proportion、DRA 等有状态 handler 仍要可逆。
- 我没有读到跨兄弟 SubGroup 的回溯；实际是「某个父 NodeSet 内失败，则回滚并换下一个 NodeSet」，可视为父级粒度的回溯。
- 节点喂给现有 predicate / node-order：`allocateTask`（`allocate.go#L144-L209`）。`OrderedNodesByTask` 并行给所有节点打分，按分数降序再按节点名排序（`KAI:pkg/scheduler/framework/session.go#L301-L333`、`#L664-L681`）。

**node 分数与选域**：选域发生在节点打分之前，只依赖聚合资源 ratio，节点分数不影响选域。有 preferredLevel 时，拓扑插件按域在排序树里的位次给节点加分（`KAI:pkg/scheduler/plugins/topology/node_scoring.go#L37-L53`，Topology 分常量 100000，见 `…/plugins/scores/scores.go#L6-L15`），只影响域内选点。

**与其他插件和动作**
- k8s predicates、DRA、NUMA、gpu sharing 都在每个 NodeSet 内原样运行 [SRC]。
- queue/quota 的 `IsJobOverQueueCapacityFn` 在 `SubsetNodesFn` 之前（`allocate.go#L21-L45`）。
- preempt/reclaim 经 `common/action.go#L76-L126` 调同一个 `AllocateJob`；**拓扑插件不参与 victim 选择**。
- 另有场景预过滤 `TopologyAwareIdleGpus`（`KAI:pkg/scheduler/actions/common/solvers/accumulated_scenario_filters/idle_gpus/topology_aware_idle_gpus.go#L16-L198`），仅限 GPU 与 required 约束。

**效率措施**：资源数学预过滤、首个成功即退出、ratio 缓存、只排到 `maxDepthLevel`、`lowestCommonDomainID` 在 preferred 层提前停止。没找到复杂度声明或基准 [NV]。设计文档预期大集群里尝试多种拓扑会明显降速（`KAI:docs/developer/designs/topology-awareness/README.md#L200`）[DOC]。PR #531 明确：预过滤「only an optimization」，宁可假阳性。

**失败文案**（`KAI:pkg/scheduler/plugins/topology/job_filtering.go`、`KAI:pkg/scheduler/api/common_info/job_errors.go#L127-L196`）
- `topology %s, requirement %s couldn't be satisfied for job <%s/%s>`（`#L640-L641`）。
- `node-group %s can allocate only %d of %d required pods`（`#L414`）。
- `not enough resources in %s to allocate the job`。
- 集成测试渲染文案（`KAI:pkg/scheduler/actions/integration_tests/allocate/allocate_topology_test.go#L284`、`#L776`）：
  `"\nUnable to schedule podgroup.\n<rack1>: node-group rack1 can allocate only 2 of 4 required pods.\n…\ntopology rack-topology, requirement topology.kubernetes.io/rack couldn't be satisfied for job </pending_job0>."`
- 投递：PodGroup 事件 reason `Unschedulable` + `UnschedulableOnNodePool` 条件（`KAI:pkg/scheduler/cache/status_updater/default_status_updater.go#L204-L243`）。
- **没有任何事件 / 注解 / 条件说明选了哪个域**。仅有 V(6)/V(7) 调试日志；设计文档称 NodeSet 不暴露给用户（`KAI:docs/developer/designs/node-sets/README.md#L159`）[DOC]。同文档 `#L11-L13` 称拓扑插件是 best-effort，不感知 NodeAffinity、PVC。

**已知缺口与 issue**（`https://github.com/kai-scheduler/KAI-Scheduler/issues/N`，标题已核对，正文未全读）
- #1591（closed）：已满载的 required 域在 victim 搜索前就被 `checkJobDomainFit` 拒绝。
- #1921（open）：`subSetNodesFn` 在模拟中也无条件写 fit error，污染诊断。
- #2546 / #2547（open）：同质性只按资源类型判断，max representor 对异构 pod 低估容量，导致误拒。
- #2593 / #2594（open）：required 层高于最低公共域时，空闲向量为零，所有域被拒；#2594 是否合入未确认 [NV]。
- #2575（open）：`tasksResources` 重复计算。
- #1760 / #1757 / #1574：4K+ 节点 reclaim 搜索与 solver 重构。
- #2318 → PR #2321：replacement 保持在原域，已合。
- 子调研读不到 PR #503 / #529 / #558（描述为空），设计意图只能由代码与设计文档推断。

### 2.3 Kubernetes KEP-5732 / KEP-4671 与 kube-scheduler 核心契约

#### (1) KEP-5732（kubernetes/enhancements）与 4671

- 状态 [DOC，`kep.yaml`]：alpha v1.36，beta v1.38，stable v1.39；gate `TopologyAwareWorkloadScheduling`，依赖 4671 的 `GenericWorkload`（4671 目标 v1.38 stable）。
- **接口已变，旧接口名过时。** 初稿的 `PlacementStatePlugin{AssumePlacement,RevertPlacement}` 在 PR #6134（提交 `a79ba724`）里被删除。k/k 在 v1.36.0 / v1.37.0 / v1.38.0-beta.0 的 `interface.go` 都没有它（grep 计数为 0）。
- 当前接口（KEP 文本 `KEP:5732-…/README.md#L276-L350` [DOC]，并与 `K8S:staging/src/k8s.io/kube-scheduler/framework/interface.go#L801-L890` 比对）：
  - `GeneratePlacements(ctx, state PodGroupCycleState, podGroup PodGroupInfo, parentPlacement *Placement) (*GeneratePlacementsResult, *Status)`
  - `ScorePlacement(ctx, state PlacementCycleState, podGroup PodGroupInfo, placement *PodGroupAssignments) (int64, *Status)`
  - `NormalizePlacementScore`
  - 代码另有 `PlacementFeasiblePlugin`（`interface.go#L862-L875`）。
- 契约原话：「The scheduler may shuffle and evaluate only a subset of the returned Placements, so plugins must not rely on their order」（`interface.go#L810-L828`）。

**生命周期**（KEP `#L352-L407`；4671 `#L1058-L1101`）[DOC]
1. 生成 placement。
2. 对每个 placement 以「限定节点集」运行默认 workload 调度算法，逐 pod 串行，不回溯。
3. 对可行 placement 打分，取最高分，用 `nominatedNodeName` 预绑定。

- 单次 snapshot：「A single cluster state snapshot is taken for the entire group operation」（4671 `#L1086-L1087`）。
- 限制：串行无回溯（5732 `#L203-L213`、`#L922-L931`）；异构组和有 pod 间依赖的组只是 best-effort（4671 `#L1296-L1342`）。

**如何复用 pod 级插件与回滚** [SRC，master]
- 三层回滚：
  - placement 级 `AssumePlacement` + revert（`K8S:pkg/scheduler/algorithm_podgroup.go#L785-L812`）；
  - pod 级 `AssumeAndReserveInSnapshot` 返回 revert 闭包，内部跑 Unreserve、`ForgetPod`、恢复 NNN（`K8S:pkg/scheduler/algorithm.go#L786-L833`）；
  - per-placement 的 `CycleState` 用 `CopyPlacementDataInto` 填入克隆后的 StateData（`algorithm_podgroup.go#L814-L821`）。
- **placement 不是 copy-on-write 快照，只是限定候选节点列表**（`K8S:pkg/scheduler/backend/cache/snapshot.go#L711-L786`）。KEP 写「placement-scoped snapshot」（`KEP:5732 README#L857-L858`），与代码有出入。
- **PreFilter 看全集，只有 Filter / Score 的候选集被限制**（`K8S137:pkg/scheduler/framework/runtime/framework.go#L941` 对比 `K8S137:pkg/scheduler/schedule_one.go#L635`）。对 PodTopologySpread 等的实际语义我没运行验证 [NV]。
- **Reserve 会跑两次**：评估时一次，选中后提交（`AssumeAndReserveInCache`）又一次（`K8S:pkg/scheduler/algorithm.go#L745-L765`）。这对「有状态插件必须可逆」是直接约束。
- 嵌套调度时要传新的 `PodGroupCycleState`，避免内层覆盖外层（`interface.go#L604-L608`）。

**效率控制**（`K8S:pkg/scheduler/algorithm_podgroup.go`）
- `[SRC✓]` `placementsToEvaluate`（`#L709-L722`）：
  - 没有 PlacementScore 插件 → `numPlacementsToFind = 1`，即首个成功即停；
  - 有插件 → 取 `numFeasiblePlacementsToFind`；
  - `numPlacementsToFind < len(placements)` 时先 shuffle 一份拷贝。
- `[SRC✓]` `numFeasiblePlacementsToFind`（`#L726-L755`）：
  - percentage=0 时 `max(5, 100 − numAllNodes*9/500)`，`numAllNodes` 是各 placement 节点数之和；
  - 常量见 `#L42-L49`，最少 1 个。
- NNN 所在 placement 先评估（`#L440-L550`）；`PlacementFeasible` 早拒。
- **串行**：注释声明 `PodGroupSchedulingAlgorithm` 「mutates the shared nodeInfoSnapshot and is not safe for concurrent use」（`#L92`）。
- **v1.37.2 没有上限、没有 shuffle**：`K8S137:pkg/scheduler/schedule_one_podgroup.go#L971-L1066` 对每个 placement 依次 `AssumePlacement → podGroupSchedulingDefaultAlgorithm → revert`。这与 Volcano 现状几乎同构。
- **KEP 的「按总资源预过滤 placement」缓解项没有落地**：在 master 的 `algorithm_podgroup.go` 与 `fit.go` 中 grep 未找到 [SRC，NOT FOUND]。KEP 的替代方案只有「Pod Inter-Affinity」与「Standalone Schedulers (Volcano/YuniKorn)」，**没有模拟代价相关的替代方案**（`KEP:5732 README#L933-L955`）。

**tie-break 与确定性**
- KEP 无 tie-break 规则。
- `[SRC✓]` `findBestPlacement`（`#L879-L908`）给每个 placement 赋 `Randomizer = rand.Int()`，同分取 Randomizer 大者。
- placement 顺序本身也随机：TopologyPlacement 遍历 map，所以才有 shuffle。
- 没有面向用户的关闭随机的开关；仅测试把 `shufflePlacements` 置 nil（`#L110`、`#L144`）。

**可观测性**
- 指标 [SRC，ALPHA]（`K8S:pkg/scheduler/metrics/metrics.go#L699-L722`）：`generated_placements_total{profile,entity}`、`placement_evaluations_total{result=feasible|infeasible,…}`、`placement_evaluation_duration_seconds`。
- 4671 的 PodGroup 指标见 `KEP:4671 README#L1745-L1747` [DOC]。
- PodGroup condition `InitiallyScheduled`，原因 Scheduled / Unschedulable / SchedulerError（`K8S:pkg/scheduler/schedule_one_podgroup.go#L677-L696`）。
- **失败报告**：`newPodGroupPlacementFitError` 包装「`anyResult`：有 nominated placement 时取其结果，否则第一个被尝试的 placement；注释称 which one we report is otherwise arbitrary」（`algorithm_podgroup.go#L528-L537`）。消息 `"0/%d placements are available, first placement status: %s"`（`K8S:pkg/scheduler/types.go#L162-L223`，v1.37.2 同格式 `K8S137:pkg/scheduler/schedule_one_podgroup.go#L1041`）。**没有逐 placement 的 verdict 列表。**

#### (2) k/k 落地状态 [SRC]

| Gate（master `K8S:pkg/features/kube_features.go`） | 状态 |
|---|---|
| `GenericWorkload` | Alpha 1.35，Beta 1.37（`#L1519-L1522`）。KEP 写 v1.38 stable，master 尚未 GA，原因未核实 |
| `TopologyAwareWorkloadScheduling` | Alpha 1.36，Beta 1.38（`#L2046-L2049`）。v1.37.2 仍仅为 Alpha 1.36（`K8S137:pkg/features/kube_features.go#L2231-L2233`） |
| `CompositePodGroup` | Alpha 1.37，Beta 1.38 |

- `GangScheduling` 与 `WorkloadAwarePreemption` 在 master 与 v1.37.2 都不存在，KEP 称 v1.37 已并入 `GenericWorkload`（`KEP:4671 README#L1407-L1419`）。
- v1.36.0 引入 `schedule_one_podgroup.go`、`topology_placement.go`；v1.37.x 的 TAS 仍是 alpha，**没有** `PercentageOfPlacementsToScore`；v1.38.0-beta.0 与 master 才有该项与 `placement_merging.go`。
- 默认插件（`K8S:pkg/scheduler/apis/config/v1/default_plugins.go#L82-L93`）：开启 TAS gate 时加入 `TopologyPlacementGenerator` 与 `PodGroupPodsCount`（权重 1）。内置 `NodeResourcesFit` 作为 PlacementScorePlugin，用「最紧凑装箱」。
- 落地 PR（`gh search prs`，合并状态已核对）：#137128（gate）、#136944、#137083、#137201、#137271、#137489、#136652、#139604（metrics）、#139472（NNN）、#143632（e2e）。

#### (3) kube-scheduler 核心契约（v1.37.2）

- **Score 归一化**：`MaxScore=100, MinScore=0`（`K8S137:staging/src/k8s.io/kube-scheduler/framework/interface.go#L330-L334`）。
  - `normalizeScores`（`K8S137:pkg/scheduler/framework/runtime/framework.go#L1526-L1582`）先并行 `NormalizeScore`，再检查越界，超出报错 `returns an invalid score … should in the range of [%v, %v] after normalizing`，然后 `score * weight` 累加。
  - 官方文档只说「well defined range…」，没写「归一化依赖候选集」。
- **`numFeasibleNodesToFind`**（`K8S137:pkg/scheduler/schedule_one.go#L866-L891`）：
  - `minFeasibleNodesToFind=100`、`minFeasibleNodesPercentageToFind=5`；
  - percentage 为 0 时 `50 − numAllNodes/125`，不低于 5；
  - `nextStartNodeIndex` 轮转；
  - 只做 Filter、不打分时取 1 个（`#L788-L790`）。
- **Diagnosis / FitError**（`K8S137:pkg/scheduler/framework/types.go#L1419-L1521`）：
  - `RunFilterPlugins` 遇到第一个不成功的插件即 `SetPlugin` 并返回（`…/runtime/framework.go#L1117-L1138`），即 **first rejecter per node**。
  - 消息 `0/N nodes are available: <PreFilterMsg>. <FilterMsg>. <PostFilterMsg>.`，Filter 部分是「理由 → 节点数」直方图，排序后 `", "` 连接。
- **tie-break `selectHost`**：
  - v1.34.0 及以前是 reservoir sampling（`K8S v1.34.0:pkg/scheduler/schedule_one.go#L915-L919`，`rand.Intn(cntOfMaxScore) == 0`）。
  - **v1.35 起（随 OpportunisticBatching，PR #135231）改为 `SortedScoredNodes` 最大堆**，比较为 `TotalScore` 大者优先、同分 `Randomizer` 大者优先（`K8S137:pkg/scheduler/framework/sorted_nodes.go#L61-L64`）。
  - **关键细节** `[SRC✓]`：在 v1.37.2 里，节点的 `Randomizer = rand.Int()` 只在 `prioritizeNodes` 的 extender 分支里赋值（`K8S137:pkg/scheduler/schedule_one.go#L1043-L1046`）；`runtime/framework.go` 全文 grep 不含 `Randomizer`。据此，无 extender 时节点同分只靠堆的内部顺序，随机性可能比 v1.34 弱。这是代码阅读推断，未做运行时验证；「只有这两处赋值」依赖 GitHub code search，可能不完整 [推/NV]。
  - 没找到 deterministic tie-break 的 issue，也没有用户可配置开关。
- **PreScore / Score 是否依赖候选集** [SRC]：
  - PreScore 接口注释写明「called with a list of nodes that passed the filtering phase」（`interface.go#L627-L631`）。
  - 依赖候选集的：PodTopologySpread（`NormalizeScore` 在传入的 scores 上求 min/max，`…/podtopologyspread/scoring.go#L229-L268`）；InterPodAffinity（min-max 线性映射，`…/interpodaffinity/scoring.go#L260-L292`）；`DefaultNormalizeScore` 的 max 归一化（NodeAffinity、TaintToleration 使用）。
  - 不依赖的：NodeResourcesFit、BalancedAllocation（`ScoreExtensions()` 返回 nil）；ImageLocality 用全部快照节点数。
- **可逆性先例**：
  - `PreFilterExtensions.AddPod/RemovePod` 用于抢占的 dry-run，框架保证可能在克隆的 CycleState 上调用（`interface.go#L500-L538`）；`DryRunPreemption` 传 `state.Clone()` + `nodeInfo.Snapshot()`（`K8S137:pkg/scheduler/framework/preemption/preemption.go#L466`）。
  - `Unreserve` 必须幂等且可在未 Reserve 时被调用（`interface.go#L676-L681`）。
  - `StateData.Clone` 约定见 `K8S137:staging/src/k8s.io/kube-scheduler/framework/cycle_state.go#L31-L36`。

### 2.4 Koordinator（v1.8.0，`989ca85c`；PR #2638 于 2025-10-14 合入，随 v1.7.0 发布）

**算法** [SRC]
- 候选域：CRD `ClusterNetworkTopology`（名称须为 `default`），树由 `TreeManager` 从 Node informer 每 10s 重建（`KOORD:pkg/scheduler/frameworkext/networktopology/tree_manager.go#L40-L128`）。gang 约束注解 `gang.scheduling.koordinator.sh/network-topology-spec`（`gatherStrategy`：layer + `MustGather|PreferGather` + `podCountMultiple`）。解析失败只打日志，gang 就静默无拓扑约束（`KOORD:pkg/scheduler/plugins/coscheduling/core/gang.go#L178`、`#L253`）。
- 求解器 `PlacePods`（`KOORD:pkg/scheduler/plugins/coscheduling/core/network_topology_solver.go#L53-L111`）：
  1. 每个节点在克隆的 NodeInfo 与 CycleState 上逐 Pod 跑真实 `RunFilterPluginsWithNominatedPods`，通过后 `addPod` 假设放入，得到连续通过数 = offer slot（`#L113-L158`）。靠克隆而非回滚。
  2. 树上自底向上累加 OfferSlot、Score（`= −preemptionCost`，仅抢占时有值）、ExistingPodNum（`#L187-L233`）。
  3. `podCountMultiple` 向下取整；MustGather 层 BFS（`#L272-L301`）。
  4. `searchOfferSlotSatisfiedNodes` 返回**最低层**仍能容纳整 gang 的域集合（`#L303-L332`）。
  5. 比较器 `[SRC✓]`：ExistingPodNum 多者优先（沿祖先链），OfferSlot 沿祖先链比较（binpack 时小者优先），Score 高者优先，Name 升序（`#L334-L351`）。
  6. 依次 `distributeOfferSlot`，**第一个成功的候选胜出**。
- 整 gang 是否对每个域模拟：**否**。每节点一轮 Filter，之后是树上整数运算。提案假设 Pod 同构（`KOORD:docs/proposals/scheduling/20250611-networktopology-aware-scheduling.md#L491-L507`）[DOC]。
- 框架集成：自有扩展点 `FindOneNodePlugin`（`KOORD:pkg/scheduler/frameworkext/interface.go#L68-L73`；调用在 `framework_extender.go#L298-L343`）。gang 首个 Pod 的 PreFilter 算出整 gang 计划，缓存在 `GangSchedulingContext`；后续每个 Pod 的 PreFilter 返回单节点 `PreFilterResult{NodeNames:{plannedNode}}`，**再走一遍正常 Filter/Score/Reserve/Permit 作为校验**（`…/network_topology_workflow.go#L47-L166`）。
- 节点级 Score 不影响选域。源码 TODO 明言「Currently, only one score is supported for each node…」（`network_topology_solver.go#L32`）。
- 抢占：复用同一个 `PlacePods`，`nodeToScore = −cost`（`…/preemption.go#L479-L548`、`network_topology_workflow.go#L168-L242`）。
- DRA / device share 无专门处理，DRA 未找到专门代码 [NV]。

**诊断**
- v1.8.0 消息 `MessageNoCandidateTopologyNodes = "no candidate topology nodes can accommodate job, desiredOfferSlot: %d, %s, %s"`，第 2 个 `%s` 逐域列 offer slot（`network_topology_solver.go#L22`、`#L87-L110`）[SRC]。
- **main PR #3191**（2026-09-03 合入，`1d3eedc756bc`）：该消息在千节点集群可达约 120KB，超过 1024 字节事件上限被截断。现在只含 `max offer slot among N must-gather topology nodes: X`，逐域明细挪到 `klog.V(4)`，最多 100 条（`KOORD-M:…/network_topology_solver.go#L22-L28`、`#L99-L129`）。
- main 上成功时有日志 `[NetworkTopology] %s/%s placed by must-gather topology on %s/%s: pod->node, ...`（`#L92-L93`）。
- 结构化 Diagnosis（`NodeOfferSlot`、`NodeToStatusMap`）默认关，仅在 `DumpDiagnosisSetter` 开启时以 JSON 打到 klog（`KOORD:pkg/scheduler/frameworkext/schedule_diagnosis.go#L132-L166`）。
- 没有「为何选此域」的 event 或 condition，也没有排名列表。

**限制（代码内 FIXME）** `KOORD:…/network_topology_workflow.go#L42-L45`：只支持无已绑定成员、总 Pod 数 = MinMember、Pod 无其他拓扑约束。#3181（closed，未合入）想把后到成员限制在已绑定域，确认了这个缺口 [PR]。

**与 KEP-5732 的关系**：没找到 `Placement` 对象。最近的类比是 `FindOneNode`（PreFilter 中的 job 级计划）。main 上 PR #3130（2026-07-23 合入）还有仅 Alpha 的「内联批调度」，PR 自称 5000 Pod 的 job 从约 90s 降到 <10s，我未实测 [PR/NV]。

### 2.5 Slurm `topology/tree` 与 `topology/block`（26.05；文档 + 部分源码）

**文档** [DOC]（https://slurm.schedmd.com/topology.html ，页面更新于 2026-11-10，含 26.05 的 ring）
- tree：「The basic algorithm is to identify the lowest level switch in the hierarchy that can satisfy a job's request and then allocate resources on its underlying leaf switches using a best-fit algorithm」。
- 同文档承认：「This may result in an allocation with more than the optimum number of switches」，用户可用 `--switches=count[@time]` 限定最大叶子 switch 数及愿意等待的时间。管理员可用 `max_switch_wait` 限制。
- 「Slurm identifies the network switches which provide the best fit… If nodes have a Weight defined, this will override the resource selection based on network topology」。
- 「listing every switch connection definitely results in a slower scheduling algorithm」，建议只列叶子 switch 加一个顶层 switch。
- block：「prioritizes the placement of jobs to minimize fragmentation… as opposed to the tree topology, which focuses on fitting jobs on the first available resources」。三步：选能满足的最小块层级、选其下 llblocks 子集、在 base blocks 里 best-fit。限制：`-N` 给范围时需逐值评估。

**源码** `[SRC✓]`（`SLURM:src/plugins/topology/tree/eval_nodes_tree.c`）
- `_topo_compare_switches`（`#L56-L105`）：两个都能放下时节点数少者更好（tightest fit）；都放不下则沿父链比较；最后比节点数、层级。
- `_topo_choose_best_switch`（`#L108-L140`）：距离更近且能放下，或同距离且 tightest fit，则替换。
- `_eval_nodes_topo`（`#L773` 起）：先在满足的候选里确定「top switch」（`#L918-L975`，要求节点数与 CPU 够，并考虑节点权重），然后向叶子 switch 细化，`switch_required` / `_topo_choose_best_switch` 逐个补充叶子 switch。
- **我没有完整追踪「最低层 switch」与 top switch 之间的精确映射 [NV]**，也没有读 block 的 `eval_nodes_block.c`。
- 失败原因是一组枚举错误码，`ESLURM_TOPO_*`（`SLURM:slurm/slurm_errno.h#L388-L398`），文本表在 `SLURM:src/common/slurm_errno.c#L956-L985`，如 "No suitable topology unit found"、"Insufficient resources in topology unit"、"Required nodes do not fit topology"。这些原因如何呈现给 squeue 用户我没有验证 [NV]。

### 2.6 可选：学术与论文参考（只列我打开过的）

- **Borg**（EuroSys 2015；PDF `https://research.google.com/pubs/archive/43438.pdf`，本地 pdftotext 读原文，PDF 约第 416-427、563-575 行）：
  - 调度分「feasibility checking」与「scoring」两阶段。
  - 可扩展性来自三点：Score caching、equivalence classes（每个等价类只做一次可行性与打分）、relaxed randomization（随机顺序检查机器，凑够「enough」可行机器再打分选优）。
  - 论文称：关闭这些技术后「did not finish after more than 3 days」，开启后整个 cell 的全量调度约几百秒。
  - 对应：k/k 的 `percentageOfNodesToScore` 与 `PercentageOfPlacementsToScore`、Volcano issue #5537（signature-based batching）。注意 Borg 的采样是**随机的**，与「确定性排序」目标相反。
- **可解释规划（对比式解释）**：
  - Krarup 等《Contrastive Explanations of Plans Through Model Restrictions》（arXiv:2103.15575，摘要已读）：用户研究显示用户对规划结果的提问是对比式的，即「为什么 A 而不是 B」；作者据此建了问题分类法（FQ1-7）并用「限制模型 + 重新求解」生成对比解释。我只读了摘要。
  - Miller《Explanation in AI: Insights from the Social Sciences》（arXiv:1706.07269，摘要已读）：主张解释研究应借鉴人类相互解释的研究。我只读了摘要。
  - 与本任务的关系 [推]：「排名列表 + 每个被排除域的第一个拒绝者」对应「为什么 A 而不是 B」的最小答案。
- score-then-verify / lazy evaluation 的学术文献：没有打开过，不列 [NV]。

### 2.6 Volcano 上游（pinned `d423e427c847d37271701bf44276156a97ee0346`）

**本地目录 `/root/workspace/volcano-workspace/volcano` 是 fork，不是上游**，没有 `pkg/scheduler/unschedulable`。本节所有标 `[SRC]` 的都已对上游 pinned SHA 核对。本地 fork 里有用户自己的拓扑重构文档（commits `b231f3ea0`、`e0933008e`），未与上游内容比对。

**当前选择与 tie-break** `[SRC✓]`
- `selectBestHyperNodeForJob`（`VOL:pkg/scheduler/actions/allocate/allocate.go#L727-L744`）遍历 `subJobsAllocationScores` map，用严格 `>`，**平局由 Go map 迭代序决定**。
- SubJob 级 `selectBestHyperNodeForSubJob`（`#L746-L769`）走 `PrioritizeHyperNodes` + `SelectBestHyperNodeAndScore`，后者在同分里 `rand.Intn`（`VOL:pkg/scheduler/util/scheduler_helper.go#L213-L227`）。
- 每个候选前 `job.ResetFitErr()`（`allocate.go#L489-L493`），所以最终 Node 维度错误只反映**最后一个被尝试的候选**，不是各 HyperNode 失败原因汇总。
- 第一层有解就返回，更高层不再评估。`HyperNodeOrderMapFn` 任一插件报错即整体失败（`VOL:pkg/scheduler/framework/session_plugins.go#L1115-L1137`）[SRC]。

**已落地的相关工作**（别再重新提议）

| 事项 | 编号 | 机制 |
|---|---|---|
| 三态 gradient + 按名字取交集 + 层内按名字排序 | PR #5349（merge `3c90d7d8034c`，2026-10-09；commit `3f222e025`、`ce2cc51f4`、`ff4715d38`） | `HyperNodeGradientResult{Unconstrained, Gradients}`，零值为 REJECT（`VOL:pkg/scheduler/api/types.go#L427-L458`）；`resolveHyperNodeGradients`（`session_plugins.go#L1205-L1262`）；`ComputePluginExcludedHyperNodes` 产出排除统计 |
| minResource 预过滤上移到 allocate | #4940 → #5349 | `FilterGradientsByMinResource`（`VOL:pkg/scheduler/actions/allocate/allocate.go#L1303-L1341`），聚合 Idle/FutureIdle ≥ minResource；#4899 里 1000 节点/1000 SubGroup 从约 1 分钟降到数秒 |
| dry-run 放置回滚、反向拓扑索引 | #5349（`ce2cc51f4`） | `captureHyperNodePlacement` / `restoreHyperNodePlacement`（`allocate.go#L1127-L1175`） |
| `group-topology-affinity`（PodGroup 反亲和） | #5349（`ff4715d38`） | 设计文档 `docs/design/group-topology-affinity.md`；Backfill 与 enqueue 预检、API 级可观测性（`TopologyUnsatisfiable` 条件）在 §7 被明确推迟（`#L692-L704`） |
| 队列节点预过滤通用钩子 | PR #6069（merge `138edcae2e6`，2026-11-10，关联 #6059） | `QueueNodeFilterFn`（`VOL:pkg/scheduler/api/types.go#L337-L341`），缓存键 `{queueID, hyperNode}`；只降 per-task predicate 成本（PR 描述 250 节点中允许 30 个，136µs → 16.5µs，predicate-only），**不减少 HyperNode 级 dry-run 次数** |
| PodGroup 失败消息选 task 的确定性 | PR #6127（merge `e4d4f3466b75`，2026-10-28，修 #6106） | 遍历 `ji.NodesFitErrors`，取 `task.Name` 字典序最小者（`VOL:pkg/scheduler/api/job_info.go#L949-L981`）；不排序展示多个、不截断、不去重；PR 称 PG 写入从 39.88/s、104/s 降为 0 |
| 融合节点打分 | PR #5928（merge `bed6d110658e`，2026-09-22） | `RunScorePlugins` 单次 16 worker 遍历（`VOL:pkg/scheduler/plugins/util/nodescore/score_helper.go#L66-L191`）；吞吐 +22.7% 是 PR 自测，不碰 `HyperNodeOrderFn` |
| Unschedulable job cache（Alpha） | PR #5848（merge `bbf87708fb75`，2026-09-22；设计 #5644，issue #5551） | gate `UnschedulableJobCache`（默认关）；只有有 HintProvider 的插件（predicates、capacity、proportion）造成的拒绝才缓存（`VOL:pkg/scheduler/unschedulable/cache.go#L129-L144`）；`HyperNodeEvent` 在 `hint.go#L32` 定义了但没有 provider；zero-gradient 路径（`allocate.go#L473-L476`）直接 `return nil` 无 `AddRejection`，所以**拓扑拒绝基本不被缓存**（此结论为我对两处代码的推断，缓存对空 rejections 的处理未逐行核对）[推] |

**设计文档** [DOC，`docs/design/`]
- `Network Topology Aware Scheduling.md`（1028 行）：soft = 偏好，hard = 有边界；`highestTierAllowed` 与 `highestTierName` 互斥（`#L290-L338`）；选择由 `HyperNodeOrderFn`，**文档没有规定平局规则**，也没有失败报告规定。有未实现的 TODO：「propose」插件在 Gradient 0 给出推荐 HyperNode 以跳过后续层。
- NTA 打分 [SRC]：`getSubJobHyperNodeBinPackingScore`（`VOL:pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go#L412-L465`），`score=(used+request)/allocatable`，`used+request > allocatable` 则为 0。参数含 `hypernode.binpack.*`、`hypernode.gradient.evict.max-hypernodes`（默认 8）。
- `group-topology-affinity.md` §6.4-6.7 与 §7：三态含义、按名字取交集、失败聚合管线、Future Extensions（`#L454-L602`、`#L692-L704`）。
- `preempt-action-support-topology.md`：只核对了标题层，正文未读 [NV]。

**今天用户看到的失败消息** [SRC]
- PodGroup condition：`"%v/%v tasks in gang unschedulable: %v"`（`VOL:pkg/scheduler/plugins/gang/gang.go#L229-L272`）；Event 另在 `cache.go#L1793-L1806` 构造，分子用 `len(job.TaskStatusIndex[Pending])`，与 condition 不一致（#6200 open，修复 PR #6202 open）。
- HyperNode 维度摘要（`VOL:pkg/scheduler/api/unschedule_info.go#L164-L247`）：`"%d/%d hyperNodes available"`，有资源排除时加 `(minResource: <res>)`，再接 `": <tierName> final/total (n networkTopology, m podGroupAntiAffinity, k minResource); …"`，tier 与原因都排序以保证确定。设计文档示例（非实际输出）：`HyperNode: 1/4 hyperNodes available (minResource: cpu 8): supernode 1/2 (1 podGroupAntiAffinity); rack 0/2 (...); Node: worker-0: In hyperNode sn-b: 0/3 nodes are unavailable: ...`。
- Pod 层 `TaskSchedulingReason`（`job_info.go#L983-L1017`）。#6196（open）：Pipeline 回滚后消息在「试探节点名」之间抖动，原因含 `SelectBestNodeAndScore` 的 `rand.Intn`（`scheduler_helper.go#L209`）与对 `LastTransaction` 的依赖。
- **没有任何 HyperNode / 拓扑相关指标**：对 `metrics.go` 与 `job.go` 按 `hypernode|topolog` 大小写不敏感 grep 为空；`queue.go`、`namespace.go` 未查 [NV]。allocate 中除 `RegisterUnschedulableJobCacheSkip` 外没有 dry-run 次数、候选数的埋点。

**仍开放（open）**

| 方向 | 编号 | 说明 |
|---|---|---|
| 效率 | **#5811**（draft，作者 suman-ganta，2026-08-01，`[SRC✓]` 元数据） | 采样上限 `hyperNodeCandidateSampleSize=200` 与早停 `hyperNodeEarlyExitPatience=20`（来自子调研读的 diff）；PR 自测 6000 节点 / 9600 HyperNode 下约 5.5 分钟降到数秒；基于旧 allocate.go，无 maintainer review |
| 效率 | #5082 / #5496 | NTA `BatchNodeOrderFn` 并发优化 |
| 效率 | #5819 | 大规模重启恢复和延迟 |
| 效率 | #5537（open） | 面向同质 gang 的 signature-based batching，参考 KEP-5598；我读了正文：指出 Volcano 每 task 分配后 idle 变化，后续 task 打分可能不同，所以不能直接复用 kube-scheduler 实现 |
| 效率 | #6015 / #6025 / #6031 | NTA 缓存 idle/futureIdle 陈旧；#6025 称已被 #5349 绕过 |
| 可解释 | #6196、#6200 / #6202、#6081 / #6108、#6180、#5262 | 消息抖动、计数不一致、PG 与 Event 重复写；无「为何选此 HyperNode」，无平局解释，无拓扑指标，无 `TopologyUnsatisfiable` 条件 |
| 兼容 | #6137 / #6213 | 未知 tier name 的 preferred 项使整个 PG 不调度（`HyperNodeOrderFn` 报错整体失败） |
| 兼容 | #5335（closed）、#5362 / #5364、#5853 | dry-run 回滚后 vGPU annotation 残留导致超卖；`RecoverOperations` 失败后没 `Discard`；`Pod.Spec.NodeName` 回滚未恢复 |
| 功能 | #5347、#5430、#5732 / #5877 / #5752、#4374 / #5920、#6169 / #6182、#6028、#6161 | SubGroup 亲和（v1.17）、跨 PodGroup 打包、异构深度 HyperNode 树、preempt/reclaim 拓扑感知、gangreclaim 被 `maxDomains` 截断（只有前 8 个 HyperNode）、与 kube-scheduler 集成 |
| 功能 | #4811 | 拓扑感知在 pod 不带 resources 时不生效（与 minResource 预过滤相关，我未深入） |
| 功能 | #5514 / #5539 | Job + SubJob 都 soft 时 anchor 不传递 |

- 搜索 `hypernode shard`、`network topology dra` 没有命中专项 issue [NV 级空白]。分片只见到 `sharding_controller.md` 与 `predicate_helper.go` 对 `NodesInShard` 的处理（`VOL:pkg/scheduler/util/predicate_helper.go#L106-L111`）。
- 在 `gh issue list` 里用 "hypernode performance"、"allocateForJob dry-run" 等多词查询没有命中；我另用单词查询（hypernode / network topology / topology performance / gradient）核对了 open issue 列表，没发现子调研漏掉的专门讲 dry-run 成本的 open issue，除 #5811 与 #5537。

---

## 3. 对 Volcano 的启示

### 3.1 支持 Filter → Rank → Verify 的先例

1. **「先排序，后验证，首个成功即停」是已被生产代码采用的模式**。
   - KAI 是最直接的先例：资源数学预过滤 → 确定性排序 → `Checkpoint / Rollback` 逐 NodeSet 验证（`KAI:pkg/scheduler/actions/common/allocate.go#L62-L68`）。
   - Koordinator 的求解器也是「首个成功候选胜出」。
   - k/k master 在无 PlacementScore 插件时 `numPlacementsToFind = 1`，也是首个成功即停。
2. **确定性比较器是可行的，且业界主流是确定性的**。
   - Kueue：`levelValues` 字典序。
   - KAI：ID。
   - Koordinator：Name。
   - Slurm：tightest-fit 后看层级与下标。
   - 只有 k/k（`rand.Int()` + shuffle）与 Volcano（map 序 / `rand.Intn`）是随机的。k/k 的做法与「用户能复述为什么选 X」目标相冲，它没有面向用户的确定性开关。
3. **后验证（Verify）阶段需要，因为只做 Rank 会出错**。Kueue 无 Verify，kube-scheduler 不一致时只能靠 `waitForPodsReady` 超时驱逐，已有多个 issue（#3755、#18550、#5243，用户报告未复现）。Kueue 的缓解方向是 `TASNodeFeasibilityForAllLevels` 与可选的 scheduler-library（只跑 Filter 不跑 Score）。
4. **成本模型：估算阶段可以便宜，验证阶段只做一次或少数几次**。
   - Kueue 的叶子计数是 per-node 的 `floor(remaining / request)`，已比聚合求和更紧。
   - Koordinator 的 offer slot 用真实 Filter 链在克隆状态上估算，能覆盖 Volcano 已有 predicate，代价是 O(节点 × pod)，不随域数增长。
   - 这两者在「估算精度 / 成本」之间给出两个档位，可以作为 Filter 阶段的选项。
5. **失败信息的「统计型」做法已被验证有用**：Kueue 的 `allows to fit only N out of M` + 排除原因计数 + `simulateEmpty` 区分「碎片」与「永远放不下」；Volcano 上游已有 `x/y hyperNodes available (… n networkTopology …)`（`unschedule_info.go#L164-L247`）。它们是「一个域的第一个拒绝者按类别聚合」的现成形式。
6. **等价类 / 去重是正交的效率手段**：Borg 的 equivalence class 与 Volcano #5537、k/k KEP-5598 方向一致，可以和 Rank 阶段叠加，不冲突。
7. **同一个域选择入口既用于 allocate 也用于 preempt/reclaim 模拟**：KAI（`common/action.go#L76-L126`）、Koordinator（同一个 `PlacePods`，`Score = −preemptionCost`）、Kueue（每个 victim 重跑）都这样做。Koordinator 的先例说明 Rank 的排序键可以包含「抢占代价」。

### 3.2 与该方向冲突或需要警惕的先例

1. **节点级分数是否影响选域**
   - 所有对照系统（Kueue、KAI、Koordinator、k/k 的 Generate/Score 之外的节点 Score、Slurm）都**不让逐节点 Score 影响选域**。
   - 例外与变体：
     - Kueue 的 `TASRespectNodeAffinityPreferred`（alpha）把节点 preferred affinity 聚合成 `affinityScore`，作为**排序首键**（`KUE:…/tas_flavor_snapshot.go#L2576-L2580`）。这是「节点级偏好在 Rank 阶段以聚合形式进入域分」的唯一先例。
     - KAI 反过来，用域在排序树里的位次给节点加分（`node_scoring.go#L37-L53`）。
     - k/k 用 `PlacementScore`（如 `NodeResourcesFit`、`PodGroupPodsCount`）在**模拟之后**打分，用的是真实 `ProposedAssignments`，因此需要先模拟 N 个候选。
   - 若要「Rank 在模拟前」且「兼容节点级打分」，则只有 Kueue 的聚合方式可借鉴；要用真实打分，就得像 k/k 一样接受模拟成本，或用 top-K verify。
2. **cohort 依赖**：PodTopologySpread、InterPodAffinity 的 `NormalizeScore` 在传入候选集上做 min-max（§2.3）。所以「rank 只依赖 (workload, domain, session state)」意味着不能直接复用这类按候选集归一化的节点 Score 去给域打分；域分需要自己的、与候选集无关的归一化。k/k 还证明 PreFilter 看全集、Filter 看子集的组合会让语义变得微妙，其实际影响我没验证 [NV]。
3. **预过滤必须是「健全的上界」**：KAI 明确「宁可假阳性」（PR #531），但它的同质假设（取 max representor）仍造成了误拒（#2546 / #2547）；`calcSubTreeFreeResources` 只覆盖公共子树导致所有域被拒（#2593）；满载域在 reclaim 前被拒（#1591）。Volcano 若做 Filter，应按用途区分视图（Volcano 已有 `PurposeAllocate/PurposeEvict` 的做法，见 `session_plugins.go#L1205-L1262`），并用对异构 pod 不低估容量的方向（例如用最小请求而非最大请求取上界）[推]。
4. **每次调用重算整棵树的代价会被 preempt/reclaim 放大**：KAI 的 #2575 与 Kueue 的 #13148（约 2k 节点、700 个 workload 时约 40s 的周期）都说明聚合结果要带 generation 缓存与增量更新。这与你说的「aggregated-resource checks re-sum node resources per call」同类。
5. **串行 + 回溯的预期要设定清楚**：k/k 明确「串行逐 pod、无回溯」（KEP `#L922-L931`）；KAI 也只在父 NodeSet 粒度回滚重试；Koordinator 是贪心分配。**没有任何对照系统做联合 Apply/Undo 回溯搜索**。KEP-5732 的作者把无回溯写为限制，而不是特性。
6. **有状态插件可逆性是真实风险**：
   - k/k 评估阶段与提交阶段 Reserve 各跑一次；
   - KAI 的事件处理器在每次尝试与回滚都触发；
   - Volcano 已有 #5335（vGPU annotation 回滚后残留导致超卖）、#5362 / #5364、#5853。
   - Filter → Rank → Verify 只在 Verify 失败时才回滚，次数大幅减少，但 Verify 失败后的「继续下一个候选」路径必须有完整回滚。Koordinator 靠克隆避免回滚，是另一种设计。
7. **Verify 的候选数与结果质量**：Volcano 现在是「同层取全部成功者里的最优」，Filter → Rank → Verify 默认变为「排名首个成功」，结果质量取决于 Rank 与真实 `HyperNodeOrderFn` 分数的一致性。k/k 的类比是 `numPlacementsToFind`（有 PlacementScore 时评估前 N 个）。Volcano 的 #5811 是「采样上限 + 早停」，会引入不确定性。
8. **Volcano 现状的几个具体问题，对照系统多数已避免**：
   - 平局 map 序 / `rand.Intn`：只有 k/k 同样随机，Kueue、KAI、Koordinator、Slurm 都是确定的。
   - Node 维度失败信息只反映最后一个候选（`allocate.go#L489-L493`）。
   - HyperNode 相关指标为空。
9. **诊断消息的长度约束是真实的**：Koordinator 的 120KB 消息超过 1024 字节事件上限，被迫改成只报最大值 + klog V(4)（PR #3191）。Kueue 也要用 `TruncateEventMessage`。所以「ranked list + verdicts」不适合塞进 Event / condition message，需要另一个载体或截断策略（`group-topology-affinity.md` §7 把 API 级可观测性列为 Future，说明这是上游已知空白）。
10. **没有先例同时提供「选域依据」与「失败时逐域 verdict」**：Kueue 只有失败统计与 V(3) 日志；KAI 只有顶层聚合；k/k 只报一个任意 placement；Koordinator 只有 klog。这是「explainability」轴上可差异化的点，也意味着没有可抄的成熟格式。学术上，对比式解释的研究（Krarup 等）支持「为什么 A 而不是 B」是用户真实提问方式 [DOC，仅摘要]。

### 3.3 一个对照小结

| 设计选择 | 先例 | 结论 |
|---|---|---|
| 排序前不模拟 | Kueue、KAI、Koordinator、Slurm | 已成熟 |
| 排序用确定性 tie-break | Kueue、KAI、Koordinator | 已成熟；k/k 与 Volcano 现状是反例 |
| 排序后首个成功即停 | KAI、Koordinator、k/k（无 PlacementScore 时） | 已成熟 |
| 验证用真实 per-task 流水线 | KAI、k/k、Koordinator（后续每 Pod 校验） | 已成熟；Kueue 无此步而踩坑 |
| 节点分数在排序前聚合进域分 | Kueue（仅 preferred affinity，alpha） | 仅有一个先例 |
| top-K 验证后用真实分数选最优 | k/k（`PlacementScore` + percentage） | 有先例，但 N 与采样有随机性 |
| 联合回溯搜索 | 无 | 没有先例 |
| 排名列表 + 逐域 verdict 的用户可见输出 | 无 | 没有先例 |

---

## 4. 搜索范围与缺口

**已做**
- 5 个并行子调研分别读了 Kueue、KAI、k/k + KEP、Koordinator、Volcano 上游，都固定到 tag 或 SHA。
- 我自己做的：
  - 对上述关键源码二次核对；
  - Slurm 文档与源码；
  - Borg 论文 PDF；
  - 两篇可解释规划摘要；
  - Volcano 的 open issue / PR 交叉核对，以及 #5811、#5537、#4811 的正文。

**无法验证或未读**
- **Kueue**：
  - 复杂度只有推导，无声明。
  - main 与 v0.20.5 的差异未读。
  - `V1Beta2From` 压缩编码未读。
  - 基准的规模配置未读。
  - #10815、#10929 只看了标题。
  - 部分失败字符串（如 `no topology domains at level: %s`）只确认存在，行号未记。
  - 所有 issue 内容都是用户报告，未复现。
- **KAI**：
  - 子调研读不到 PR #503 / #529 / #558 的描述（为空）。
  - `github.com/kai-scheduler/api` 的 Go 类型未读。
  - 「只有拓扑插件注册 `SubsetNodesFn`」来自逐目录扫描 `pkg/scheduler/plugins/*`，代码搜索被限流，未用全仓搜索交叉验证。
  - 没有找到复杂度声明或基准。
  - #2594、#2547、#1921 状态为 open 或未合入。
- **k/k / KEP**：
  - 「PlacementFeasible 仅 gangscheduling 插件实现」「只有这两个文件给 `Randomizer` 赋值」两条依赖 GitHub code search，可能不完整。
  - 节点级 `Randomizer` 在无 extender 时的实际 tie-break 行为是代码阅读推断，需要最小测试确认。
  - PodTopologySpread / InterPodAffinity 在 placement 限定节点集下的真实行为只做了静态阅读。
  - KEP-5710、5729、6012、6089 只读了 kep.yaml 或 PR 标题。
  - KEP 与代码有不一致（例如 `PlacementProgress.Unschedulable`、metric 多了 `entity` 标签、condition 名称、「placement-scoped snapshot」），以代码为准。
  - `GenericWorkload` 的 stable 版本（KEP 称 v1.38，master 仍为 Beta 1.37）原因未核实。
- **Koordinator**：
  - 网站文档称特性「默认启用」，我只在 Helm chart 验证了启用，代码默认值仍为 false。
  - `batch/engine.go` 只读了提案与 PR 正文，没读实现；PR #3130 的 90s → <10s 是 PR 声明，未实测。
  - `distributePods` 内部按 Name 重排对 index 语义的影响未验证。
  - DRA 是否有专门处理未找到。
  - 第 d 节所列 PR / issue 只核对了元数据。
- **Slurm**：
  - 只读了 `eval_nodes_tree.c` 的部分段落，「最低层 switch」与 top switch 的精确关系、block 的 `eval_nodes_block.c` 都没读。
  - `ESLURM_TOPO_*` 如何呈现给用户未验证。
- **Volcano 上游**：
  - PR #5349 的 `ff4715d38`（插件实现）没有逐行读。
  - PR #5811 只读了描述和 diff，无 maintainer 意见；其性能数据为作者自测。
  - `preempt-action-support-topology.md` 正文未读。
  - `queue.go`、`namespace.go` 的 metrics 未查。
  - 「拓扑拒绝不被缓存」是推断。
  - 没有查分片、DRA 与 HyperNode 的专项设计，也没有查到相关 issue。
- **学术文献**：
  - 没有打开过关于 score-then-verify / lazy evaluation 的论文。
  - 两篇可解释规划文献只读了摘要。
- **全局**：没有跑任何基准或测试；所有性能数字（#5811、#3130、#13148、PR #6069、#5928）都是各 PR / issue 自述。所有 issue 与 PR 状态以 2026-11-11 前后的 `gh` 返回值为准。