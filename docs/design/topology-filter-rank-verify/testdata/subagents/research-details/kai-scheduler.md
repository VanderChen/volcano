# KAI-Scheduler 拓扑感知调度调研报告(面向 Volcano「Filter → Rank → Verify」重构)

## Summary

1. KAI 的拓扑调度已经接近「Filter → Rank → Verify」。拓扑插件通过 `SubsetNodesFn` 扩展点,用纯资源数学(`AllocatablePods` / 资源比)预过滤 domain,并按确定性的 ratio 排序,返回有序的 `[]NodeSet`。
2. allocate 公共代码按顺序逐个 NodeSet 执行真实的 per-task 流水线(PrePredicate、Predicate、NodeOrder、`stmt.Allocate`)。它在每个 NodeSet 前打 `Checkpoint`,失败则 `Rollback`,**第一个成功就返回**,不会把所有候选都完整模拟一遍。
3. 拓扑树每个 session 只建一次。但每次 `subSetNodesFn` 调用都会全树重置并重新汇总 Idle+Releasing,这也是 preempt/reclaim 模拟能看到 victim 释放资源的原因。
4. 过滤只是近似。它假设 pod 同质(取各维度最大值作 representor),并忽略 DRA、分数 GPU 的精确性,所以仍可能出现「过滤通过但验证失败」。
5. 排序不是全局打分,而是「每个父节点内 children 按 ratio 降序,再按 ID 升序」,最后按「低层级优先 + 树中从左到右」展开。无随机 tie-break。
6. node 级分数**不影响** domain 选择。它只在 `preferredTopologyLevel` 存在时,由拓扑插件按 domain 排序位次给节点加分,影响 domain 内的节点选择。
7. 可解释性很弱:只有「失败时」的聚合文案(顶层 domain 的 `can allocate only X of Y required pods`)。**没有任何**事件、注解、条件或日志说明选了哪个 domain,也没有排序列表或逐 domain 裁决。
8. 已知缺口见 d)、f):满载的 required domain 在 preempt/reclaim 前被拒、同质假设导致误拒、`JobFitErrors` 被模拟污染等。

## Pinned versions

- 最新 release:`v0.19.0`(published 2026-11-11T12:22:18Z)。`main` 与 `v0.19.0` 在读取时都指向同一 commit `2305acfc348c9bd9cd1ab813d3feb8dfc4ab9d11`,以下所有永久链接均钉在此 SHA。
- 仓库已更名为 `kai-scheduler/KAI-Scheduler`。`NVIDIA/KAI-Scheduler` 会重定向,Go import 路径为 `github.com/kai-scheduler/KAI-scheduler`。
- 约定:`B/` = `https://github.com/NVIDIA/KAI-Scheduler/blob/2305acfc348c9bd9cd1ab813d3feb8dfc4ab9d11/`。
- 标签:【SRC】= VERIFIED IN SOURCE CODE,【DOC】= DOCS ONLY,【NV】= NOT VERIFIED。

## a) 算法

### 树的构建与生命周期【SRC】

- `OnSessionOpen` 从 `ssn.ClusterInfo.Topologies` 和 `Nodes` 建树。插件只注册 `AddSubsetNodesFn`、`AddNodeOrderFn`、`AddPreJobAllocationFn`,**不注册事件处理器**:`B/pkg/scheduler/plugins/topology/topology_plugin.go#L44-L56`。
- 树每个 session 建一次,被所有 job 共享:`topology_plugin.go#L58-L119`。
  - `DomainsByLevel` 以 level 和 domain ID 为键。domain ID 是祖先 label 值的点号拼接(`topology_structs.go#L96-L103`)。
  - `Children` 由遍历 `nodes` map 得到,初始顺序是 map 迭代顺序,之后会被排序覆盖。
  - 缺任一 level label 的节点被排除(`common.go#L70-L77`)。
- **每次 `subSetNodesFn` 调用都重算**(`B/pkg/scheduler/plugins/topology/job_filtering.go#L34-L116`):
  - `lowestCommonDomainID` 找入参 nodeSet 的最低公共 domain(`common.go#L17-L67`)。
  - `treeAllocatableCleanup` 把**所有** domain 的 `AllocatablePods` 置 -1 并清空资源向量(`job_filtering.go#L484-L491`)。
  - `calcSubTreeFreeResources` 自底向上重新求和 `node.IdleVector + ReleasingVector`(`#L196-L215`)。

### domain 可分配 pod 数是资源数学,不是模拟【SRC】

- 条件:`useRepresentorPodsAccounting(tasks)` 为真(近似同质,`#L600-L629`),才走 `calcTreeAllocatable` → `calcSubTreeAllocatable`(`#L171-L194`)→ `calcNodeAccommodation`(`#L217-L269`)。
- 做法:取各资源维度在所有 task 上的最大值作为 representor pod,逐节点算 `min_dim floor(nonAllocated[dim]/max[dim])`,再沿树求和。
  - 0<gpu<1 时加 `AvailableSharedGPUFractions()`(`#L236-L238`)。
  - 非 GPU 的 DRA 维度置零,即跳过(`#L240-L249`)。
- 不满足同质条件时,走 `checkJobDomainFit`(`#L407-L425`)的聚合比例检查:`getJobRatioToFreeResources > 1.0`(`#L543-L576`)。缺失资源的 ratio 取 `requiredResourceNotInDomainRatio=1000`。

### required 与 preferred 的 domain 选择【SRC】

- `calculateRelevantDomainLevels`(`#L427-L470`)。level 数组按 lowest→highest 排,`root` 在最后:
  - 只有 required:只看 required 层。
  - 只有 preferred:从 preferred 层一路到 root。
  - 两者都有:从 preferred 到 required(含)。
  - level 不在 topology 中则返回 `newTopologyConstraintConfigError`。
- `getJobAllocatableDomains`(`#L271-L321`):
  - 收集**所有相关 level 上全部满足的 domain**,不是只取第一层。
  - 若 job 已有活跃 pod 且有 required,则限制在含活跃 pod 的 required 层子树内(`#L366-L401`)。
  - 无一满足则追加顶层 fit error 和 `getNoTopologyMatchError`。

### 排序与 tie-break(确定性)【SRC】

- `sortTree`(`#L511-L538`)对每个节点的 children 按 `getJobRatioToFreeResources` 降序排序,tie-break 为 `cmp.Compare(i.ID, j.ID)`。
  - 只排到 `maxDepthLevel`(preferred,否则 required)。
  - ratio 在单次调用内缓存(`domainRatiosCache`)。
- 函数注释写「按 AllocatablePods 升序」,与代码(ratio 降序)不符,注释已过时。
- `sortDomainInfos`(`#L580-L594`)按 `reverseLevelOrder`(`topology_utils.go#L20-L55`)输出:低层优先,同层按排好序的树从左到右。
- 效果:
  - 这是「父节点内 ratio 降序 + 层级优先」,**不是跨父节点的全局打分**。
  - 无随机 tie-break。
  - 复杂度约为 O(|tree| × |candidates|)。
- 方向:ratio 高 = 资源越紧 = 优先,即 bin-pack。docs 工作示例:3-pod job,Zone A 有 5 GPU,Zone B 有 6 GPU,选 Zone A,并在其中先填 rack A1【DOC】`B/docs/topology/README.md`(约 L55-L150)。

### 子组 / 层级【SRC + DOC】

- API 在 PodGroup 的 `spec.topologyConstraint` 和 `spec.subGroups[].topologyConstraint`,含 `topology`、`requiredTopologyLevel`、`preferredTopologyLevel`,另有 `minMember`、`minSubGroup`、`parent`:`B/deployments/kai-scheduler/crds/scheduling.run.ai_podgroups.yaml#L147-L196`。Go 类型在外部模块 `github.com/kai-scheduler/api`,未读。
- Topology CRD:cluster-scoped,`kai.scheduler/v1alpha1`,`levels` 1–16 项(`nodeLabel` + 可选 `alias`),`kubernetes.io/hostname` 必须是最低层:`B/deployments/kai-scheduler/crds/kai.scheduler_topologies.yaml#L60-L107`。
- `allocate.go` 对每个 SubGroupSet 和 PodSet 分别调用 `SubsetNodesFn`。子级的候选节点是父级选定的 NodeSet,再经 `lowestCommonDomainID` 收窄。
- 未设约束的子组直接返回 `[]NodeSet{nodeSet}`。
- 设计文档假设 pod 同质(`B/docs/developer/designs/topology-awareness/README.md#L118-L120`)【DOC】。
- 多层与分段:`B/docs/topology/multilevel.md`、`segments.md`【DOC】。分段是 PodGrouper 创建 `ceil(R/S)` 个 segment SubGroup,各自独立放置,可能落在不同 domain。
- 例外:段内 replacement 受 active-pod 保护。PR #2321 修复了整个叶子子组丢失的情况【SRC】:`job_filtering.go#L366-L401`。

## b) SubsetNodes 扩展点与 allocate 的消费

### 类型与链式调用【SRC】

- 类型:`SubsetNodesFn func(podGroup, subGroup *SubGroupInfo, podSets map[string]*PodSet, tasks []*PodInfo, nodeSet NodeSet) ([]NodeSet, error)`,见 `B/pkg/scheduler/api/types.go#L32-L34`。
- `Session.SubsetNodesFn`(`B/pkg/scheduler/framework/session_plugins.go#L415-L438`):从 `[]NodeSet{initNodeSet}` 开始,链式调用各插件,前一个插件输出的每个集合都成为后一个的输入(flat-map)。出错即中止。日志在 V(7),`podGroup.Namespace` 被打印两次(L423、L451),是个 typo。
- `pkg/scheduler/plugins` 下只有拓扑插件注册 `SubsetNodesFn` 和 `PreJobAllocationFn`。这是逐目录扫描文件的结果,代码搜索被限流,见 Gaps。

### 消费方式:逐个尝试,首个成功即返回,失败回滚【SRC】

`B/pkg/scheduler/actions/common/allocate.go#L47-L73`(`allocateSubGroupSet`)和 `#L115-L142`(`allocatePodSet`):

```go
nodeSets, err := ssn.SubsetNodesFn(...)   // 失败只日志 "Failed to run SubsetNodes on job <%s/%s>: %v" 并 return false (L57-58)
for _, nodeSet := range nodeSets {
    cp := stmt.Checkpoint()
    ok := allocate...(nodeSet)            // 真实 per-task 流水线
    if ok { return true }                 // 首个成功即返回,不评估其余
    stmt.Rollback(cp)
}
```

- **没有全局 dry-run + `Discard`**。这是 Statement 级 checkpoint/rollback(`B/pkg/scheduler/framework/statement.go#L45-L64`:`Checkpoint()` 返回 `len(operations)`,`Rollback(cp)` 逆序撤销)。
- 与 Volcano 的差异:KAI 按插件给出的顺序做 first-fit,不会对所有候选 HyperNode 都跑完再打分选最优。
- 事件处理器仍会在每次尝试和回滚时触发。`Allocate` 触发 `AllocateFunc`(`statement.go#L336-L405`),`unallocate` 触发 `DeallocateFunc`(`#L439-L479`),所以 proportion、DRA 等有状态 handler 必须可逆。

### 节点如何喂给现有 predicate 和 node-order【SRC】

- `allocateTask`(`allocate.go#L144-L209`):`PrePredicateFn` → `ssn.OrderedNodesByTask(nodes, task)` → 逐节点取第一个 `ssn.FittingNode` 通过者 → `allocateTaskToNode`,含 NUMA、`gpu_sharing.AllocateFractionalGPUTaskToNode`、bind 或 pipeline。
- subset 的节点就是传入的 `nodes` slice。
- `OrderedNodesByTask`(`B/pkg/scheduler/framework/session.go#L301-L333`)用 worker pool 并行给**所有**节点打分(`NodeOrderFn` 为各插件分数之和),`sortNodesByScore`(`#L664-L681`)按分数降序、再按节点名排序,保证确定。
- `FittingNode`(`#L240-L286`)在 simulation 模式下跳过 `PredicateFn`,真实模式才跑。
- allocate action:`nodes := maps.Values(ssn.ClusterInfo.Nodes)`,job 逐个处理,成功 `stmt.Commit()`,否则 `stmt.Discard()`(`B/pkg/scheduler/actions/allocate/allocate.go#L79-L113`)。

### domain 内节点打分,以及它是否影响 domain 选择【SRC】

- `nodeOrderFn`(`B/pkg/scheduler/plugins/topology/node_scoring.go#L17-L35`)查子组的预计算分数,或祖先子组的分数。
- `calculateNodeScores`(`#L37-L53`):仅当设置了 `preferredLevel`,才按 preferred 层 domain 在排序树里的位次计算 `floor(((i+1)/n)*10) * scores.Topology`,其中 `scores.Topology=100000`(`B/pkg/scheduler/plugins/scores/scores.go#L6-L15`)。
- 该分与其他插件分数相加,只影响 domain **内**的节点选择。
- **domain 选择在节点打分之前,仅依赖聚合资源 ratio,因此节点分数不影响 domain 选择**。
- 文档说 preferred 子 domain 内「可用资源更多者优先」,与 domain 选择的 bin-pack 方向相反【DOC】。

### 设计文档【DOC】

- `B/docs/developer/designs/node-sets/README.md#L99-L109`:"attempt each NodeSet until one succeeds"。
- 同文档 L11-L13:拓扑插件是 best-effort,因为它不感知 NodeAffinity、PVC 等。
- 同文档 L159:用户看到的只是「unschedulable on cluster」,NodeSet 不对外暴露。
- 文档中的函数签名与实际实现不同。

## c) 效率措施

- **资源数学预过滤**(无模拟):`AllocatablePods` 为 O(nodes)/调用【SRC】。每次调用另有 O(domains) 的全树清理、O(nodes) 的重新汇总,以及 `sortDomainInfos` 的 O(tree × candidates)。
- **首个成功即退出**:`allocate.go#L62-L68`【SRC】。
- **比例缓存**:`domainRatiosCache`。PR #604 "Cache ratio results of topology tree sorting",并且只对与 nodeSubset 相关的 domain 计算 `AllocatablePods`【DOC:PR 描述,缓存本身 SRC】。PR #588 "Topology plugin node sorting performance improvement"(描述为空)。PR #601 "Topology domain sort based on resource ratio"。
- **宁可假阳性不要假阴性**:PR #531 改用 `IsTaskAllocatableOnReleasingOrIdle`,并说明预过滤「only optimization」。
- **只排到 `maxDepthLevel`**(`#L511-L538`)【SRC】。
- **`lowestCommonDomainID` 在 preferred 层提前停止**(`common.go#L17-L67`)【SRC】。
- **效率缺陷**:
  - 每次调用都全树重算,包括 preempt/reclaim 里每次模拟的分配。
  - issue #2575(open):`tasksResources` 在 `sortTreeFromRoot` 里被重复计算(`job_filtering.go#L493-L505`)。
  - 设计文档预期在大集群里尝试多种拓扑会有明显性能下降(`topology-awareness/README.md#L200`)【DOC】。
  - 设计文档提出用事件处理器维护树(`#L265-L266`),实现里没有这样做。
- **复杂度与基准**:没有找到复杂度声明或基准。`test/e2e/scale/topology/topology.go` 只是 KWOK 的 Topology / NodePool 生成器(`#L19-L60`),`job_filtering_test.go` 里没有 `func Benchmark`。这一项【NV】。
- 相关 perf issue:#1760(open,TopologyFirst scenario generator,面向 4K+ 节点集群的 reclaim 搜索)、#1757、#1574(solver 重构设计 PR)。

## d) 与其他插件和动作的共存

- **predicate / node-order**【SRC】:拓扑只改变候选节点**集合**。k8s predicates、DRA、NUMA、gpu sharing、`nodeplacement`、`podaffinity`、`gpusharingorder`、`nominatednode` 等都在每个 NodeSet 内原样运行。分数常量见 `scores.go#L6-L15`:Topology 100000,高于 GpuSharing 10000,低于 K8sPlugins 1000000 和 NominatedNode 10000000。
- **predicate 之后拒绝节点时如何保持正确**【SRC】:由 `Checkpoint`/`Rollback` 加「试下一个 NodeSet」保证(`allocate.go#L62-L68`)。预过滤宁可放过,再由真实流水线最终裁决。
- **DRA 与分数 GPU**:预过滤跳过非 GPU DRA 维度(`job_filtering.go#L240-L249`、`#L560-L564`),分数 GPU 只做近似。PR #604 记录了「分数 GPU 未计入 free resources」这个已知问题,之后部分通过 `AvailableSharedGPUFractions` 补上。
- **queue/quota**【SRC】:`IsJobOverQueueCapacityFn` 在 `AllocateJob` 里先于 `SubsetNodesFn` 执行(`allocate.go#L21-L45`)。
- **preempt/reclaim/consolidation**【SRC】:
  - 它们经 `common/action.go#L76-L126` 的 `TryToVirtuallyAllocatePreemptorAndGetVictims` 调用 `AllocateJob`,所以 `subSetNodesFn` 在模拟中也会运行。
  - 因为每次调用都重算树,它能看到虚拟驱逐释放的资源。
  - 求解器另有场景预过滤 `TopologyAwareIdleGpus`:`B/pkg/scheduler/actions/common/solvers/accumulated_scenario_filters/idle_gpus/topology_aware_idle_gpus.go#L16-L198`,注册于 `solvers/pod_scenario_builder.go#L71-L74`。它仅限 GPU、仅看 required 约束,按 domain 空闲容量贪心匹配并计入 victim 释放的 GPU。
  - 求解本身是模拟:驱逐 victims → 模拟分配循环,失败则 Rollback + Discard(`solvers/by_pod_solver.go#L63-L103`)。
  - **拓扑插件不参与 victim 选择**,没有拓扑感知的 victim 选择钩子。
- **已知缺口**:
  - #1591(closed,维护者没有快速修复,计划通过 #1574 / #1760 重构):已满载(Idle 与 Releasing 都为零)的 required domain 在 victim 搜索之前就被 `checkJobDomainFit` 拒绝。
  - #1921(open):`subSetNodesFn` 在模拟中也无条件调用 `AddSimpleJobFitError` / `AddJobFitError`,污染 fit errors,该 PR 计划限定到权威尝试。
  - #2546/#2547(open):同质性只按资源类型判断,最大值 representor 对异构 pod 会低估容量,造成误拒。
  - #2593/#2594(open):required 层高于最低公共 domain 时,free-resource 向量为零,所有 domain 被拒(例如只有一个节点的池)。原因是 `calcSubTreeFreeResources` 只覆盖公共子树,而 `getJobAllocatableDomains` 检查的是全局树的 required 层 domain。#2594 是 PR,未确认是否已合入。

## e) 用户被告知什么

### 失败文案(精确字符串)【SRC】

在 `job_filtering.go` 中:
- `Requested topology %s does not exist`(`#L40-L43`)。
- `not enough resources in the cluster to allocate the job` 与 `not enough resources in %s to allocate the job`(`#L72-L79`)。
- `topology %s, requirement %s couldn't be satisfied for job <%s/%s>`,可选后缀 `, subgroup %s` 和 `: <suffix>`(`#L640-L641`)。
- `node-group %s can allocate only %d of %d required pods`(`#L414`)。
- `no topology constraints were found for subgroup %s, with topology name %s`(`#L434-L435`)。
- `topology constraint error: %s specified '%s' as the %s topology constraint level, but the topology tree '%s' does not contain a level with this name`(`#L479-L481`)。
- `domain not found for node set in topology %s`(`#L52`)。这个错误只在 `allocate.go#L57-L58` 记日志。

`B/pkg/scheduler/api/common_info/job_errors.go#L20-L21,L127-L196`:
- 原因 `Not enough resources for the workload`,默认文案 `Unable to schedule podgroup`。
- `TopologyFitError.DetailedMessage` 的格式为 `<%v>: %v`。
- 简短形式:`node-group(s) didn't have enough resources: GPUs` / `CPU cores` / `memory` / `<res>`,以及 `node-group(s) didn't have enough of mig profile: %s`。

`allocate.go#L274-L310`(`handleFailedTaskAllocation`):
- `Resources were not found for pod %s/%s due to: %s`。
- `Resources were found for %d pods while %d are required for gang scheduling. Additional pods cannot be scheduled due to: %s`。
- `Resources were found for %d pods from all sub-groups while sub-group %s requires %d pods for gang scheduling. Additional pods cannot be scheduled in this sub-group due to: %s`。

集成测试里完整渲染的文案(可见最终用户能看到什么):
- `B/pkg/scheduler/actions/integration_tests/allocate/allocate_topology_test.go#L284`:
  `"\nPodSchedulingErrors.\ntopology rack-topology, requirement topology.kubernetes.io/rack couldn't be satisfied for job </pending_job0>: not enough resources in the cluster to allocate the job."`
- 同文件 `#L776`:
  `"\nUnable to schedule podgroup.\n<rack1>: node-group rack1 can allocate only 2 of 4 required pods.\n<rack2>: …\n<rack3>: …\n<rack4>: …\ntopology rack-topology, requirement topology.kubernetes.io/rack couldn't be satisfied for job </pending_job0>."`

### 投递路径【SRC】

`B/pkg/scheduler/cache/status_updater/default_status_updater.go#L204-L243,L346-L367,L400-L434,L522-L539`:
- PodGroup 事件的 reason 为 `Unschedulable`。
- 当没有活跃已分配 task 时,写入 `UnschedulableOnNodePool` 条件,`Reasons` 为每个 fit error 的 `ToUnschedulableExplanation()`。
- 同时写 pod 条件。
- `detailedFitErrors` 开关切换 `JobFitErrorsToDetailedMessage` 与简短的 `JobFitErrorsToMessage`;否则详细版本只在 V(6) 记日志。

### 是否解释了「为什么选这个 domain」

**没有找到**【SRC,在已读范围内】。唯一的输出是 V(7) 的 `logNodeSetsPluginResult`(`session_plugins.go#L440-L454`)和 V(6) 的调试日志。

- 没有「排序列表」,也没有「逐 domain 裁决」。失败时只有顶层 domain 的聚合 fit error,较低层级不会列出。
- 设计文档明确说 NodeSet 不暴露给用户(`node-sets/README.md#L159`)【DOC】。
- issue #2593 的报告者提到,调度器日志对每个 action 只显示 "Could not allocate resources for job",信息不足。
- #2058(open)是一个 `kai-diagnose-pending-workload` 技能。

## f) 文档化的限制与相关 issue

链接格式:`https://github.com/kai-scheduler/KAI-Scheduler/issues/N`。标题均已核对。

- **性能**:#2575(重复计算 `tasksResources`)、#1760(TopologyFirst scenario generator,4K+ 节点)、#1574(solver 重构设计)、#1757(智能选择 scenario generator)。
- **正确性**:
  - #2546 / #2547(同质假设误拒)。
  - #2593 / #2594(required 层高于最低公共 domain 时全部被拒)。
  - #1591(满载 domain 在 reclaim 前被拒)。
  - #2318 → PR #2321(replacement 保持在原 domain,已合,backport 为 #2485 / #2486)。
  - #2338(e2e 测试)。
  - #1921(fit-error 作用域)。
  - #2426(preempt/reclaim 驱逐了只在别处才放得下的 victim)。
  - #2301(对反复失败的 reclaim/preempt 搜索退避)。
- **功能请求**:
  - #2478 / #2479(spread 策略)。
  - #2077(非索引 block topology 与自动子分组)。
  - #1658(跨 ResourceClaim 的设备拓扑约束)。
  - #1419(Ray 自动分段)。
  - #2580(调度器忽略 pod 自带的 `topologySpreadConstraints` DoNotSchedule)。
  - #1969(大型 gang 的 reservation/backfill)。
- **历史**:#66(原始 TAS issue)、#1498 / #1788(level alias)。
- **关键演进 PR / commit**:
  - #503 `e465cdb61`(拓扑实现为 SubsetNodes,2025-09-17)。
  - #529 `7c58a03dd`(多 domain 决策)。
  - #531 `33309e522`。
  - #558 `52b9162c4`(Domain Packing + Node Sorting)。
  - #560 `056a9b409`(任意层级子组约束)。
  - #588 `641c5cd72`。
  - #601 `a765b2503`。
  - #604 `626bc15ef`。
  - #742 `33114362d`(explainability)。
  - #905 `6b2d46654`。
  - #834 `b668a48d7`。
  - #969 `30b4ef2ef`。
  - #2321 `e6a1fb4bd`。
  - #503、#529、#558 的 PR 描述为空,设计意图只能靠代码与设计文档推断。

## 对 Volcano 设计的映射

- **可借鉴**:
  - 两阶段结构:资源数学预过滤 + 真实流水线验证 + 首个成功即停。
  - 确定性排序,以 ID 兜底。
  - Statement 级 checkpoint/rollback。
  - 把 domain 约束表达为「节点集合」,让现有 predicate 与 node-order 保持不变。
  - 把 preempt/reclaim 的虚拟分配也走同一入口。
- **KAI 尚未解决的点,可作为差异化设计**:
  - 没有「选中 domain / 排序列表 / 逐 domain 裁决」的解释输出。
  - 过滤只是同质近似,没有「第一个拒绝者」的逐 domain 记录。
  - 每次调用全树重算,没有增量或缓存。
  - 排序是父节点内排序,不是全局可比分数。
  - 节点分数不影响 domain 选择。
  - 事件处理器在每次尝试与回滚时仍会触发。
  - 满载 domain 先于 preempt/reclaim 被拒。
  - fit error 被模拟污染。

## Gaps

- 代码搜索被限流(10 次/分钟)。「只有拓扑插件注册 `SubsetNodesFn`」这条是逐目录扫描 `pkg/scheduler/plugins/*` 文件得出的,未用全仓代码搜索交叉验证。
- 复杂度声明与基准:【NV】。scale 测试只是 KWOK 拓扑生成器,没有找到性能数字。
- PR #2594、#2547、#1921 状态为 open 或未合入,它们的行为不在钉住的代码里。
- 没读 segmented-subgroups 设计文档。
- 没读 PR #503 / #529 / #558 的描述(为空),也没读 `github.com/kai-scheduler/api` 中的 Go 类型定义。
- 没有完整追踪 `JobFitErrors` 在周期之间是否清理。推断是每个 snapshot 重建 `PodGroupInfo`,所以会被清理【NV】。
- 没有确认其他组件(如 binder、metrics、snapshot 工具)是否会记录选中的 domain。在已读文件中没有看到,但这不是排除性的证明。