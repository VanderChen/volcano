# Kueue TAS 调研报告(面向 Volcano 网络拓扑感知分配重构)

**图例**
- 链接缩写:`B` = `https://github.com/kubernetes-sigs/kueue/blob/v0.20.5`。
- `S` = `B/pkg/cache/scheduler/tas_flavor_snapshot.go`。
- `K` = `B/keps/2724-topology-aware-scheduling/README.md`。
- `D` = `B/site/content/en/docs/concepts/topology_aware_scheduling.md`。
- `FA` = `B/pkg/scheduler/flavorassigner`。
- `I#N` = `https://github.com/kubernetes-sigs/kueue/issues/N`。
- 链接写法为 `S#L2576-L2602`,即 `S` 加行号锚点。
- 验证标签:**[SRC]** = VERIFIED IN SOURCE CODE;**[DOCS]** = DOCS ONLY;**[ISSUE]** = 仅读了 issue 文本(用户报告,未复现);**[NV]** = NOT VERIFIED;**[DERIVED]** = 我自己的推导。

## Summary

1. Kueue TAS 的域选择只做资源算术,不做逐域模拟 **[SRC]**。
   - 流程是:先按节点算出每个叶子能放多少 pod,再自底向上累加(`fillInCounts`),然后对每层域做一次确定性排序。
   - 之后选层、选域,自顶向下细化到叶子,最后写出 `topologyAssignment`。
2. 节点级可行性(taint/nodeSelector/affinity)由可插拔的 `SchedulerSimulator` 判定,默认实现只做这三类检查。
   - 可选的 scheduler-library 实现(alpha,默认关闭)会调用 kube-scheduler 的 Filter 插件,但不做 Score。
3. 排序比较器完全确定:先按容量,再按 podCount,最终按 `levelValues` 字典序打破平局,没有随机性。
   - 失败信息是统计型的:汇报“能放多少 / 需要多少”和各类节点排除计数,不是逐域裁决。
   - 没有“为什么选这个域”的决策记录,只有 V(3) 日志。
4. 决策以 `status.admission.podSetAssignments[].topologyAssignment` 记录。
   - 这是叶子层的压缩切片格式(v1beta2),旧 v1beta1 是 domains 列表。
   - 执行靠 `kueue.x-k8s.io/topology` schedulingGate,由 TopologyUngater 去掉 gate 并写 nodeSelector。
5. Kueue 不建模 pod affinity/anti-affinity、topologySpreadConstraints 和 Score 插件。
   - kube-scheduler 与 Kueue 不一致时 pod 会 Pending,靠 `waitForPodsReady` 超时驱逐再重排,没有反馈回路。
   - 这是最大的已知局限(I#3755 等)。
6. 抢占复用同一算法:每个候选 victim 都重跑一次完整的 `findTopologyAssignment`。
   - 可逆性靠对 snapshot 加/减用量,不是靠事务回滚。
   - 大规模下这里就是热点(I#13148:2k 节点、700 个待抢占 workload 时一个周期约 40s)。

## Pinned versions

- 最新 release:**`v0.20.5`**(published 2026-11-05T15:25:26Z),commit `7e71578095b353cedbfa63607b60c9739af9d501`。以下全部行号均基于该 tag。
- `main` HEAD:`115135775a68083a72aefa358513b68d2a63b962`(2026-11-11),与 release 不同。
  - main 的 `tas_flavor_snapshot.go` 约 131.5KB,tag 上约 127.8KB,另有 `pkg/cache/scheduler/tas_assignments_result.go`。
  - 我只在 tag 上读源码;main 与 tag 的具体差异 **[NV]**。

## a) 算法阶段

**入口与总体结构**
- `FindTopologyAssignmentsForFlavor`(`S#L896`)按 PodSetGroup(leader+workers)分组,调用 `findTopologyAssignment`(`S#L1491-L1772`)**[SRC]**。
- 源码注释给出 Phase 1 / Phase 2 a–d(`S#L1472-L1490`)**[SRC]**:
  - Phase 1:自底向上算每个域的 pod 数和 slice 数。
  - Phase 2:a) 按策略排序;b) 在请求层选出能放下的连续域;c) 逐层向下并尽量减少使用的域数;d) 构建最低层的 assignment。

**叶子容量与自底向上聚合**
- `fillInCounts`(`S#L2606`)取可行叶子:来自 `SchedulerSimulator.FindFeasibleNodes`,或在 `TASCacheNodeMatchResults` 打开时来自缓存。
- `fillLeafCounts`(`S#L2931-L2960`):`podCount = requests.CountInWithLimitingResource(remaining)`。
  - `remaining = free − tasUsage − 本周期 assumedUsage`(`S#L2907-L2930`)。
  - `simulateEmpty` 时跳过 tasUsage 的扣减。
  - 有 leader 时另算 `leaderCount` 和 `podCountWithLeader`。
- `fillInCountsHelper`(`S#L2962-L3060`)自底向上累加:
  - 父域 podCount = Σ 子域。
  - 多层 slice 时向下取整到内层 slice 大小。
  - `sliceCount[4]` 按 obligationMask(none/leader/tail/both)分档。
- 每个域的可变状态是 `domainState`(`S#L81-L124`),叶子是 `leafCapacity`(`S#L163-L180`)。静态拓扑树按 nodesCache 的 generation 缓存。

**逐域模拟:无** **[SRC]**
- 没有任何“对每个候选域跑 predicate/score/事件回调”的逻辑。只有对共享状态数组的算术。
- 唯一类似模拟的是节点级 `FindFeasibleNodes`(见 d 节)。

**选层**:`findLevelWithFitDomains`(`S#L2001-L2175`)**[SRC]**
- `levelDomains := slices.Collect(maps.Values(domains))` 的顺序是随机的,所以必须先做确定性排序,取 `sorted[0]`。
- 顶部域能放下时:
  - BestFit 用 `findBestFitDomainBy`(`S#L1963`)选“能放下的最小域”。
  - LeastFreeCapacity 取升序里第一个能放下的域。
- 顶部域放不下时:
  - `required`:失败。开启 `TASRespectNodeAffinityPreferred` 或有 spread 规则时会继续扫描,因为此时顺序不再单调按容量。
  - `preferred`:递归上一层(`searchLevelIdx-1`)。
  - 到最高层或 `unconstrained`:贪心选多个域(先 leader,最后一个域做 best-fit 优化)。

**自顶向下细化**:`S#L1696-L1760`
- 流程是 `updateCountsToMinimumGeneric`(`S#L2326`)→ 逐层对子域重排 → 最后 `buildAssignment`(`S#L2468`)。

**排序比较器(关键代码)** `S#L2576-L2602`,平局规则 `S#L2519-L2528` **[SRC]**
```go
slices.SortFunc(result, func(a, b *domain) int {
  if respectNodeAffinityPreferred && a.affinityScore != b.affinityScore { return cmp.Compare(b.affinityScore, a.affinityScore) } // desc
  if a.sliceCount[obligationNone] != b.sliceCount[obligationNone] {
    if isLeastFreeCapacity { return cmp.Compare(a.sc, b.sc) }   // 升序
    return cmp.Compare(b.sc, a.sc)                               // BestFit:降序
  }
  if a.podCount != b.podCount { return cmp.Compare(a.podCount, b.podCount) } // 升序
  return s.compareDomainLevelValues(a, b)  // slices.CompareFunc(levelValues, strings.Compare);叶子是节点且同 parent 时只比最后一级
})
return s.sortedBySpreadPriority(result, spreadRules)
```
- `sortedDomainsWithLeader`(`S#L2534`)在此之前先按 `leaderCount` 降序。
- 因此平局最终落在 `levelValues`(域名字符串)字典序上,完全确定,没有随机选择。

**算法 profile 与 feature gate**
- 选择逻辑:`useLeastFreeCapacityAlgorithm = unconstrained && Enabled(TASProfileMixed)`,否则 BestFit(`S#L2190-L2198`)**[SRC]**。
- KEP 选择矩阵(`K#L1759-L1771`)**[DOCS]**:preferred=BestFit,required=BestFit,unconstrained=LeastFreeCapacity;开 `TASBalancedPlacement` 时 preferred 改为 BalancedPlacement。

| Gate | 阶段 / 默认 | 引入版本 | 备注 |
|---|---|---|---|
| `TopologyAwareScheduling` | Beta,默认 true | alpha 0.9,Beta 0.14 | 其余 TAS gate 都依赖它 |
| `TASProfileMixed` | Beta,默认 true | alpha 0.10,Beta 0.15 | |
| `TASBalancedPlacement` | alpha,默认 false | 0.15 | 仅在非 required、非 unconstrained、无 tail 时启用,失败回退 BestFit |
| `TASProfileBestFit` | 已不存在 | 0.15 弃用 | |
| `TASProfileLeastFreeCapacity` | 已删除 | 0.17 | |
| `TASProfileMostFreeCapacity` | 已删除 | 0.13 | 见 `K#L2367-L2398`,删除理由是代码简洁和缺少用例 |
| `TASNodeFeasibilityForAllLevels` | Beta,默认 true | 0.20 | 在非 hostname 最低层注入虚拟 hostname 层 |
| `SchedulerLibraryIntegration` | alpha,默认 false | 0.19 | |

- 上表 gate 的定义见 `B/pkg/features/kube_features.go#L1028-L1298`,依赖关系见 `#L938-L972` **[SRC]**。
- `TASBalancedPlacement` 的启用条件见 `S#L1565-L1580`,我只读到约略位置。

**复杂度**
- 源码和 KEP 均未给出复杂度 **[NV]**。
- **[DERIVED]**:单个 PodSet 的 Phase 1 为 O(叶子数×资源种类 + 域数),排序为每层 O(d log d),没有“域数 × 节点数 × task 数”的乘积。
- 用户文档承认:Kueue 要追踪所有 Pod 和节点,会占内存、拉长调度(`D#L593-L598`)**[DOCS]**。

## b) 失败信息格式与呈现位置

**格式串** `S#L3062-L3085`(`notFitMessage`)**[SRC]**
- `topology %q doesn't allow to fit any of %d %s(s)`,单位在 `sliceSize==1` 时为 `pod`,否则为 `slice`。
- `topology %q allows to fit only %d out of %d %s(s)`。
- required 时追加 `; all pods must fit in a single %q topology domain`。
- 有排除统计时追加 `. Total nodes: %d; excluded: %s`。
  - `formatReasons`(`S#L806-L831`)的取值为 `nodeSelector: N`、`affinity: N`、`topologyDomain: N`、`schedulerLibraryNoFit: N`、`draNoFit: N`、`taint "<taint>": N`、`resource "<res>": N`。
- 多层 slice:`topology %q doesn't allow to fit` + `; %d/%d slice(s) fit on level %s`(`S#L3098-L3137`)。
  - 注释写明域 ID 平局用于“deterministic messages”,因为 map 迭代顺序随机。
- `Failure()`(`S#L638-L650`)按 PodSet 名字排序取第一个失败项,注释:“Sorting keeps the reported PodSet stable across scheduling cycles”。
- 其他字符串(`no topology domains at level: %s`、`topology level not specified`、`no requested topology level: %s`、`unable to calculate domain capacities for PodSet %s, error: %s`、`cannot place the partial slice of PodSet %s in a single topology domain`)我读到了但未逐一记录行号。
  - 这些只能引用为 `S` 文件内的字符串,具体行号 **[NV]**。

**Flavor 层不匹配原因** `FA/tas_flavorassigner.go#L199-L239` **[SRC]**
- `Flavor %q does not support TopologyAwareScheduling`
- `Flavor %q information missing in TAS cache`
- `Flavor %q does not contain the requested level`
- `Flavor %q supports only TopologyAwareScheduling`

**配额类消息** **[SRC]**
- `insufficient unused quota for %s in flavor %s, %s more needed`(`FA/flavorassigner.go#L1143-L1144`)。
- `insufficient quota for %s in flavor %s, previously considered podsets requests (%s) + current podset request (%s) > maximum capacity (%s)`(`FA/flavorassigner.go#L1102-L1109`)。

**呈现路径** **[SRC]**
1. `AssignTopology`(`FA/flavorassigner.go#L515-L567`):
   - 配额 Fit 时先在当前用量下找放置。
   - 失败则记为 `Preempt`,并附 `NewStatus(failure.Reason)`。
   - 再以 `WithSimulateEmpty(true)` 重试:空集群也放不下才变 `NoFit`,reason 为 `TopologyPlacementFailed`;否则保持 `Preempt`,表示可能靠抢占解决碎片。
2. 各 PodSet 的状态汇总成 `couldn't assign flavors to pod set <name>: <msg>`(`FA/assignment.go#L268-L286`)。
3. `requeueAndUpdate`(`B/pkg/scheduler/scheduler.go#L1255-L1295`):
   - 用 `UnsetQuotaReservationWithCondition(wl, condReason, e.inadmissibleMsg)` 写 Workload 的 `QuotaReserved=False` 条件。
   - 同时发 Warning Event,Event 的 reason 取 `condReason`,消息经 `TruncateEventMessage`。
4. reason 常量 `TopologyPlacementFailed`(`B/apis/kueue/v1beta2/workload_types.go#L968-L970`)。
   - `Preempt` 且无候选时 reason 为 `WaitingForQuota`(`scheduler.go#L529-L535`)。
   - 这种情况下消息里是否带 TAS 文本,我只读了装配逻辑,没有端到端实测 **[NV]**。

## c) 决策记录与执行

**API 类型** **[SRC]**
- `PodSetAssignment.topologyAssignment`:`B/apis/kueue/v1beta2/workload_types.go#L431`。
- `TopologyAssignment{levels[], slices[]}`(`#L458-L477`),CEL 规则要求每个 slice 的 `valuesPerLevel` 长度等于 `levels` 长度。
- `TopologyAssignmentSlice{domainCount, valuesPerLevel[], podCounts}`(`#L481-L498`)。
- 值按 level 分为 `universal`(整个 slice 共用一个值)或 `individual{prefix, suffix, roots[]}`(`#L501-L536`)。
- `podCounts` 同样是 `universal` 或 `individual[]`(`#L539-L555`)。
- 限制:slices ≤1000,roots/individual ≤100000,levels ≤16。
- `TASAssignmentsEncodingByHostnamePrefix` 是 Beta,默认 true(0.19),用于按主机名前缀分组编码(`kube_features.go#L1090`)。具体编码代码 `V1Beta2From` 我没读 **[NV]**。
- v1beta1 旧格式是 `TopologyAssignment{levels, domains[{values[], count}]}`(`B/apis/kueue/v1beta1/workload_types.go#L270-L300`)。
- KEP 说明 v1beta2 重构是为了处理超大 workload,并提取节点名公共前后缀(`K#L1188-L1310`)**[DOCS]**。
- 若 `levels` 含 `kubernetes.io/hostname`,assignment 只含该层(`K#L1195`)**[DOCS]**。
- 内部类型是 `utiltas.TopologyAssignment`(Values + Count),落盘时转换(`FA/assignment.go#L293`)。
- 分配总是落到最低层,以避免 workload 之间的竞争(`K#L696-L706`)**[DOCS]**。
- 延迟拓扑(ProvisioningRequest / MultiKueue):`delayedTopologyRequest` Pending/Ready(`#L433-L456`),准入检查就绪后做第二轮(`D#L153-L170`、`K#L2059-L2143`)。

**执行** **[SRC]**
- 常量:schedulingGate `kueue.x-k8s.io/topology`(`B/apis/kueue/v1beta2/topology_types.go#L75`),用户注解 `kueue.x-k8s.io/podset-{required,preferred,unconstrained}-topology`、`podset-slice-required-topology`、`podset-slice-size`(`#L28-L69`)。
- `kueue.x-k8s.io/workload` 是注解而非 label(`#L77-L80`)。
- 旧的 `kueue.x-k8s.io/tas` label 在 0.14 去掉、0.17 移除评估(`K#L725-L731`、`K#L1906-L1935`)**[DOCS]**。
- `topologyUngater` 是 Workload 的 reconciler:
  - 对 pod 事件按 `UpdatesBatchPeriod` 批处理,用 expectations store 避免重复(`B/pkg/controller/tas/topology_ungater.go#L155-L188`、`#L272-L298`)。
  - 对每个 pod 去掉 gate 并 `maps.Copy(pod.Spec.NodeSelector, nodeLabels)`(`#L278-L285`)。
- pod 到域的映射(`#L379-L394`):
  - 能读到 rank(Index label、PodGroup index、offset)时按 rank 映射到 `rankToDomainID`(`#L575-L588`、`B/pkg/util/tas/ranks/ranks.go`)。
  - 读不到或越界则回退到贪心分配(`#L472`)。
  - rank 映射中出现已运行 pod 与预期域不一致时只记日志(`#L560-L567`)。

## d) 与其他约束的交互、局限、节点故障

**Kueue 自己复现的过滤** **[SRC]**
- 节点 Ready 且可调度;默认路径丢弃 NotReady/unschedulable 节点,仅 `SchedulerLibraryIntegration` 开启时保留(`B/pkg/cache/scheduler/tas_nodes_cache.go#L57-L123`;文档 `D#L61-L72`)。
- 容量 = allocatable − 其他 TAS workload 用量 − 非 TAS pod 用量(DaemonSet 等)。
  - 由事件驱动的 `nonTasUsageCache` 维护,来自 I#8449(已关闭)。
  - 因此“非 TAS pod 用量”是按 pod 事件在缓存里增量维护,不是每周期重新 list。
- `pods` 资源通过把 `ResourcePods`=count 加入用量来计入(`updateTASUsage`/`Fits`,`S#L377`、`S#L661`)。
- flavor 的 nodeLabels、tolerations 合并、pod 的 nodeSelector、必需 nodeAffinity,以及 preferred affinity(`TASRespectNodeAffinityPreferred`,alpha)。
- 默认检查在 `B/pkg/cache/scheduler/scheduling_simulator_default.go#L52-L116`。
- 可选能力:`KueueDRADeviceFeasibility`(alpha)、scheduler-library 的 `CanSchedulePod`(`B/pkg/cache/scheduler/was/scheduler_simulator.go#L38-L161`,alpha)。
  - 后者源码注释自承可能“比真实调度器更严格”。
  - 它只跑 Filter,不跑 Score。

**不建模的约束**
- pod affinity/anti-affinity、topologySpreadConstraints、Score 插件 **[ISSUE]**:
  - I#3755 开篇即说 TAS “may currently schedule workloads which are unschedulable at the kube-scheduler level”,例子是 pod affinity/anti-affinity。
  - I#15057 说 TAS 只支持 affinity-like 放置,不支持 anti-affinity。
  - 源码里没有对应逻辑。
- Kueue 的 `TASTopologySpreading`(alpha)是跨 workload 的另一功能,不等于 pod 的 topologySpreadConstraints。
- KEP 风险(`K#L735-L743`、`K#L769-L780`)**[DOCS]**:
  - 非独占节点使用:TAS 假设同 flavor 的节点只被 TAS workload 独占。
  - DaemonSet 用量统计存在竞争。

**kube-scheduler 与 Kueue 不一致时的行为**
- 最低层不是 hostname 时,pod 仅被 nodeSelector 约束到域内,域内选节点由 kube-scheduler 决定。
  - 0.20 前只校验域总量,pod 可能因逐节点不可放而 Pending。
  - 该问题在 KEP 中承认(`K#L2003-L2009`),由 `TASNodeFeasibilityForAllLevels`(0.20 Beta)缓解 **[DOCS]**。
- 已有 issue 证实 pod 可能卡 Pending 并反复超时重排 **[ISSUE]**:
  - I#18550(open,v0.16.9):硬 anti-affinity 无解的 Job 被 TAS 准入,一个 pod 运行,另一个 Pending,`PodsReadyTimeout` 后驱逐再准入。
  - I#5243(closed,2025):抢占释放的 GPU 分散在多节点,高优先级 8-GPU workload 被准入却无节点可放。
    - 最终修复 PR 我未核对 **[NV]**。
  - I#18551(open,v0.16.9):CPU 碎片,3 个节点各剩 3 核,请求单 pod 4 核,被准入后 kube-scheduler 报 Insufficient cpu。
    - 该 issue 自述为“non-TAS quota admission”,是否与 TAS 相关我未确认 **[NV]**。
  - 这些都是用户报告,未复现。
  - 无自动反馈,依赖 `waitForPodsReady` 超时驱逐,未见其他回路。
- 缓解方向:I#3755(open)的跟进评论称计划做 alpha 集成,仍由 Kueue TAS 决策,把 Pod 调度/抢占模拟委托给 scheduler-library(评论见 `I#3755#issuecomment-4242756368`);总括见 I#8871。
- 其他相关 issue:
  - I#13700(open):WAS 集成缺少对已 assumed 但 pod 未建的 workload 的预期占用,会“over-admit”冲突域。
  - I#13283(open):scheduler-library 模拟器缺少细粒度排除原因。
  - I#13658(closed):TAS 放置失败使 workload 在负载下被钉在第一个 flavor。
  - I#16624(closed):冷启动后在缓存不全时准入,TAS 节点被重复预订。
  - I#10815、I#10929(open):仅读了标题 **[NV]**。

**节点故障**
- 功能:hot swap(`TASFailedNodeReplacement`,Beta,0.14)、FailFast、`TASReplaceNodeOnPodTermination`、`TASReplaceNodeOnNodeTaints`、`TASReplaceMultipleFailedNodes`(alpha)。
  - gate 在 `kube_features.go#L1047` 一带。
- `status.unhealthyNodes`(`workload_types.go#L721`,最多 8)记录故障节点。
- `findReplacementAssignment`(`S#L1103-L1135`)保留原分配的其余部分并仅替换故障域,仅适用于 hostname 最低层。
  - required 域无法确定时返回 `cannot replace the node: required topology domain of the remaining assignment cannot be determined`。
- 文档 `D#L172-L328` 推荐配合 `waitForPodsReady.recoveryTimeout` **[DOCS]**。
- 调度周期内的冲突:同周期内不同 CQ 的 TAS nominations 各自独立计算,容易选到冲突域。
  - 源码注释 `scheduler.go#L487-L492` 称这会让后评估的 CQ 反复输给先评估的 CQ。
  - KEP 记录过 Workload 因此“hours”无法准入(`K#L1773-L1797`),`TASRecomputeAssignmentWithinSchedulingCycle`(0.19 Beta)在评估阶段重算(`scheduler.go#L870-L940`)**[SRC/DOCS]**。

## e) 效率控制与抢占

**周期开销与缓存** **[SRC]**
- 每周期 `Cache.Snapshot`(`B/pkg/cache/scheduler/snapshot.go#L290-L420`)为每个 TAS flavor 调 `TASFlavorCache.snapshot`(`tas_flavor.go#L180-L214`),**[DERIVED]** 总开销为 O(叶子数 + 用量条目数)。
- 缓存项:
  - 拓扑树按 nodesCache generation 缓存(`TASCacheTopologyTree`,Beta,0.20;`tas_flavor.go#L225-L240`)。
  - 匹配叶子结果缓存(`TASCacheNodeMatchResults`)和延迟计算的剩余资源缓存(`TASCachingRemainingResources`,`kube_features.go#L1282-L1290`)。
  - 精简的 Node 拷贝,以及 `VectorizedResourceRequests`。
- 周期内增量:`assumedUsage`(`S#L1235`)和 `ClusterQueueSnapshot.AddUsage/RemoveUsage`。

**性能数据 / issue** **[ISSUE]**
- I#13148(closed):在约 2k 节点、700 个 workload 需要模拟抢占的规模下,TAS 周期可达 40s。
  - 90% 时间在 `findTopologyAssignment`,其中约 90% 在 `fillInCounts`。
  - 已做的优化:缓存匹配节点、只算一次 feature gate、hostname 层只比节点名、域状态按叶子 generation 缓存、资源 map 优化。
  - 同 issue 里的 `BenchmarkSchedulerTAS`:release-0.18 tip 约 133 ms/op,v0.18.3 约 373 ms/op。
  - 该基准的规模和配置我没读 **[NV]**。
- I#8449(closed):按周期遍历所有 Node/Pod 统计非 TAS 用量,大规模时约占调度总时间 90%,因此改成事件驱动。
- I#13541(open):为 TAS+WAS 补性能测试,“TAS is performance critical”。
- I#13695(open):持续负载下的调度行为总括。
- 基准文件存在:`tas_flavor_snapshot_bench_test.go`、`scheduler_tas_bench_test.go`,内容我没读 **[NV]**。

**抢占复用同一算法** **[SRC]**
- `workloadFits = workloadQuotaFits && workloadTopologyFits`(`B/pkg/scheduler/preemption/preemption.go#L567-L596`)。
  - 其中 TAS 部分每次都调用 `FindTopologyAssignments`,即每试一个 victim 就重跑一次完整 TAS。
- `getTargets`(`#L385-L417`)先逐个累加候选直到 fit,再 `fillBackWorkloads`(`#L426-L438`)按相反顺序尝试把 victim 加回去。
- 可逆性靠对 snapshot 加减用量:`AddWorkload/RemoveWorkload`,以及 `SimulateWorkloadUsageRemoval` 返回的 restore 函数(`snapshot.go#L178-L216`)。
  - 因此需要“可撤销用量”,不需要插件事件回滚。
  - TAS 不触发 plugin 事件处理器,但默认模拟器的 `PreemptWorkload` 是 no-op。
- `simulateEmpty` 在 `WithSimulateEmpty(true)` 下表示“空集群是否能放下”,用来判断抢占是否有意义,以及为等待抢占的 workload 预留容量(`simulator/types.go#L69-L72`)。
- 为抢占提供的 `SchedulerSimulator.PreemptWorkload`/`Simulate` 接口(`simulator/interface.go#L64-L88`)是 WAS 集成用的。

## f) 可解释性 / 确定性

- 确定性依据 **[SRC]**:`levelValues` 字典序平局(`S#L2519-L2528`)、`Failure()` 按名排序(`S#L638-L650`)、多层消息按域 ID 排序(`S#L3098-L3137`)、flavor 迭代用排序后的 key(`clusterqueue_snapshot.go#L225-L270`)。
- I#11910(closed):指出 `TASBalancedPlacement` 下等分域可能被随机选中,要求所有排序路径有稳定平局规则,建议回退到 `levelValues`。
  - 对 `sortedDomains` 我验证了回退到 `levelValues` 已在代码中;balanced 路径的最终实现 **[NV]**。
- 决策可解释性:
  - 失败有“能放几个/共几个 + 排除原因计数”,但没有逐域裁决或候选排名输出。
  - “为什么选这个域”只有 V(3) 日志,没有 API 级产出 **[NV]**:源码里我只看到日志里 dump 每域空闲容量(`S#L573`),没有产品化的决策记录。
- I#10505(open):用户要求暴露 `kueue_tas_domain_usage` 类指标,以弄清 Kueue 视角与 kube-scheduler 视角的差异。
- I#14850(open)与 I#4570:希望可配置算法(如 required 请求也用 LeastFreeCapacity),说明“选哪个满足约束的域”目前不可配。
- KEP “Future Vision”(`K#L2048-L2057`)**[DOCS]**:设想类似 kube-scheduler 的加权打分模型(affinity、容量打包效率),归一化后合成最终分数。目前 affinity 分严格先于放置策略,尚未实现。
- I#10574(open):贪心域选择不是最优,例如容量 [19,10,10] 放 20 单位会选 19+10,可用 DP 做最优选择(BalancedPlacement 已实现其一部分)。

## 对 Volcano 设计的启示(我的解读,非 Kueue 声明)

- “Filter → Rank → Verify”与 Kueue 的差别:
  - Kueue 的 Rank 只依赖聚合容量和排序键,没有 Verify 环节。
  - 它靠“节点级可行性模拟器”收窄叶子集合,再用资源算术保证“数量能放下”。
  - 结果是 kube-scheduler 不一致时只能靠超时重排兜底,这正是 I#18550、I#5243 的症状。
  - Volcano 若保留“按排名顺序在候选上跑真实插件管线并在首个成功处停止”的 Verify 环节,就能避免这类问题,代价是单次验证成本高于 Kueue 的纯算术。
- Kueue 证明了用“确定性比较器 + levelValues 平局”替代随机平局可行,也证明了统计型失败信息的价值。
- 抢占场景下 Kueue 反复重跑完整评估,并已成为性能热点,所以 Volcano 若要复用算法,需要让 Filter/Rank 阶段足够便宜。

## Gaps

- **未验证**:
  - 复杂度声明(仅 DERIVED)。
  - main 与 v0.20.5 的具体差异。
  - `V1Beta2From` 压缩编码实现。
  - 基准的规模与配置。
  - I#10815、I#10929 的内容。
  - `TASResidualPodAccounting`、KEP 13746(spreading)、`node_controller.go` 的详细逻辑。
- 部分 `S` 中的失败字符串(`no topology domains at level: %s` 等)只在阅读时确认存在,未逐一记录行号。
- 所有 issue 内容均为用户报告,未复现,状态以 2026-11-11 前后为准。
- 未发现 Kueue 有“为什么选这个域”的 API 级决策记录,只检索了 issue 标题与正文,不排除遗漏。