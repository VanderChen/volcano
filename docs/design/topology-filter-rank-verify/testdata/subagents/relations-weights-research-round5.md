# 第 5 轮：社区与产品调研（关系约束与权重）

所有结论只读源码、设计文档和 KEP 得出，未改任何文件，也未跑测试。

标注说明：〔源码〕= 读源码；〔文档〕= 官方文档、KEP 或设计文档；〔推断〕= 我的归纳。

**固定版本**
- Kueue v0.20.5：`7e71578095b3…`
- LWS v1.0.0：`588881cada4f…`
- NVIDIA KAI v0.19.0：`2305acfc348c…`
- Grove v0.2.1-beta1：`c9b6835baa9d…`
- Koordinator v1.8.0：`989ca85c62ab…`
- k/k master：`dd21ea95bc59…`（v1.37.2 与之相同）
- enhancements：`72907deaa85e…`
- kubernetes/website：`2674d5095a50…`
- Slurm：`slurm-26-05-6-1`
- Hadoop：`rel/release-3.4.1`（`bad12e61f710…`）
- Volcano 本地：`b231f3ea…`（分支 `archive/topology-scheduling-refactor-20261105`）
- Volcano 上游 master：`9cca912a5…`
- volcano-sh/website：`9b9f43a00bbe…`

---

## 1. 摘要

1. 在 Kueue、LWS、KAI、Grove、Koordinator、kube-scheduler、Slurm、YARN 中，**没有一个系统把"SubGroup 之间的亲和、反亲和（必选或偏好、指定拓扑层级）"作为调度算法的原生特性**。我没找到反例，但"不存在"无法证明。
2. 各系统表达"组间关系"的方式有 5 种：
   - 注入 Pod 亲和、反亲和：LWS、JobSet、KEP-848。
   - 包含树，即父 SubGroup 带约束：KAI、Grove、KEP-6012。
   - 对"放置计数"的约束：Kueue 的 alpha 版 topology spreading，以及 k/k #142690 中的提案。
   - 标签基数约束：YARN。
   - 作业级独占：Slurm `--exclusive=topo`。
3. **复合单元（按聚合需求选一个域）有三类现成实现**：
   - Kueue 的 PodSet Group，限 2 个 PodSet，即 leader + workers。
   - KAI 的 `allocateSubGroupSet`：先按全部子树任务的聚合需求挑出候选域，再逐个候选域"检查点、放置、失败回滚、换下一个域"。
   - Koordinator 的 gang group：整组所有待调度 Pod 一起规划。
4. **KAI 是最接近本设计的先例**：父域用聚合需求预筛，域内子组仍逐个放置，失败后只在父级候选域之间重试，不重排兄弟。
5. **"逐个放置、首个定域"的失败模式在多份文档里有明文**：KEP-5732、KEP-4671、KEP-848、KEP-6012、Kueue #15057、KAI 设计文档。
6. KEP-6012 明确选择"不做兄弟回溯"，理由是复杂度从 O(C·D) 变成 O(D^C)，并承认这会在聚合容量足够时找不到放置。
7. **k8s 的两条语义直接对应你的 required 语义**：
   - required 反亲和是**对称**的，已有 Pod 的反亲和项也会拒绝新 Pod。
   - "首个 Pod 自亲和"的豁免只在集群里没有任何匹配 Pod 时才成立。
   - 它对应你的"先放的那个自由"，但不会替后续成员预留空间。
8. **Part B**：
   - k/k：各插件归一化到 [0,100]，范围越界会报错，再按 `weight × score` 求和。
   - Kueue 和 Koordinator：字典序比较，没有跨插件权重。
   - KAI：不同插件用十进制间隔的常量（10、100、…、10⁷）直接相加，实际等价于字典序。
   - Volcano：各插件内部自行乘权重后直接求和，框架层不做归一化，也没有范围检查。
9. Volcano 文档只给了权重的**配置示例**（`weight: 10`），没找到"如何选权重"的指导。官网 `nodeorder.md` 的参数名和默认值与代码不一致，见 §4。

---

## 2. Part A：组与子组之间的关系约束

| 系统 | 关系约束 | 语义（对端未放置 / 跨多个域） | 算法（整体或逐个，是否回溯） | 已知局限或缺陷 | 出处 |
|---|---|---|---|---|---|
| **Kueue TAS** `podset-group-name` | 共放：一个组正好 2 个 PodSet（leader 有 1 个副本 + workers）要落在同一拓扑域。无组间反亲和。 | 〔源码〕leader 和 workers 在**同一次决策**里一起放，所以"对端未放置"不会出现。leader 以 `leaderCount=1` 进入，workers 与 leader 都取自同一个 `currFitDomain`。 | 〔源码〕**复合单元**。`assignPodSetGroup` 对整组调用一次 `findTopologyAssignment(workers, leader, …)`，`findLevelWithFitDomains` 选一个能装下整组的域。组之间按首次出现顺序处理，首个失败就返回，**不回溯**。 | 〔源码〕校验限定"组恰好 2 个 PodSet"，不是通用的 N 元集合。 | `kueue:pkg/cache/scheduler/tas_flavor_snapshot.go:916-926, 943, 977, 985-987, 994-1018, 1078-1092, 1512-1516, 1718-1737, 2001-2100`；`pkg/controllers/jobframework/tas_validation.go:259-335`；文档 `site/content/en/docs/concepts/topology_aware_scheduling.md:124-126`；KEP `keps/2724-topology-aware-scheduling/README.md:525-533, 1877-1904`（v0.20.5） |
| **Kueue Topology Spreading**（alpha，门控 `TASTopologySpreading`，默认关） | 在 workload 之间限定同一域内最多放几个组：`rules[].topologyKey`、`maxShareAllowingPlacement`、`enforcementMode`（Required 为默认，或 Preferred）。 | 〔文档〕放置合法当且仅当 `N == 0 || count(D) <= maxShareAllowingPlacement * N`。因此**第一个组总是可放**，即"先放者自由"。Required 会整条候选条目被禁，Preferred 把域降到更低层。一个组只占一个域。规则层级低于 PodSet 所需层级时无法执行。 | 〔源码〕spread 优先级在排序里**最后应用并胜出**：`Any spreadRules are applied last and take precedence over all of the above`。逐 workload 处理，无回溯。 | 〔文档〕只覆盖 workload 之间的分散，不是 PodGroup 内部 SubGroup 之间的关系。Kueue 维护者表示 TAS 不理解 Pod 亲和：#15057（LWS 注入的反亲和被 TAS 违反），另有 #15787、#10574、#15373、#10778、#7446。 | `kueue:keps/13746-tas-topology-spreading/README.md:527-554, 612-617, 638-652`；`tas_flavor_snapshot.go:1560-1567, 2010-2012, 2574-2575`；`site/…/topology_aware_scheduling.md:472-549`（v0.20.5） |
| **LWS** `exclusive-topology` / `subgroup-exclusive-topology` | replica 或 subgroup 与拓扑域 **1:1**：组内共放，不同组不共享域。 | 〔源码〕webhook 注入 required `podAffinity`（`groupUniqueKey In [own]`）和 required `podAntiAffinity`（`key Exists AND key NotIn [own]`）。对端未放置时靠 k8s 的自亲和豁免，首个 Pod 不被钉死。组不能跨多个域，因为反亲和会拒绝任何含他组 Pod 的域。 | 〔源码〕**逐 Pod、无预留**，由默认调度器完成。只有 leader 带亲和项，workers 通过 nodeSelector 跟随（KEP-1064 提案）。 | 〔文档〕KEP-848 `L101`：`the scheduler places pods one at a time with no whole-slice look-ahead, so a slice's first pod can land in a domain that lacks room for the rest`。replica 级独占与 WAS 的 gang 约束一起使用会死锁（KEP-1064 `L316-330`）。与 Kueue 组合不工作（`L350-370`，Kueue #15057）。 | `lws:pkg/webhooks/pod_webhook.go:154-156, 169-170, 195-196, 262-304`；`site/content/en/docs/concepts/leaderworkerset/topology-placement/_index.md:15, 26-33`；`keps/1064-lws-placement-policy/README.md:147-151, 252-262, 292, 316-330, 350-370`；`keps/848-disaggregatedset-placement-policy/README.md:101, 156-169, 190-192, 198-201`（LWS v1.0.0。KEP-1064 和 848 是提案，`PlacementPolicy` 不在 v1.0.0 类型里） |
| **KAI** SubGroup | 无亲和、反亲和。共放靠**包含树**：父 SubGroup 带 `requiredTopologyLevel` / `preferredTopologyLevel`，子组落在同一父域。 | 〔文档〕"SubGroups A and B must be co-located under the same spine"。无父约束的兄弟组"possibly on different racks"，可落在不同域。〔源码〕若子树已有已分配 Pod 且有 required 约束，候选域被限制为已含这些 Pod 的域，即对端先放则对端定域。 | 〔源码〕**复合预筛 + 子组逐个放置**。`allocateSubGroupSet` 以全部子树任务调用 `SubsetNodesFn`，对每个候选域做 `Checkpoint` → `allocateMembersOnNodes` → 失败则 `Rollback` 换下一域。域内子组按 `orderedMembers` 顺序放，**不换序、不在同一父域内重试不同子域**。`getTasksAllocationMetadata` 对子树任务求和。 | 〔文档〕KAI 设计文档称首个 Pod 的选择不重试（`topology-awareness/README.md:35`）。未发现组间亲和或反亲和（文档树中 grep 无结果，不能证明不存在）。 | `kai:pkg/scheduler/actions/common/allocate.go:47-73, 78-97, 115-142`；`pkg/scheduler/plugins/topology/job_filtering.go:34-116, 118-129, 271-321, 281-292, 366-377, 407-425, 427-470, 507-536`；`docs/topology/multilevel.md:20-26, 38-41, 74, 113-158`；`docs/topology/segments.md:67`（v0.19.0） |
| **Grove** | 只有 pack（required、preferred），无亲和、反亲和、spread。 | 〔文档〕PCS→PCSG→PCLQ 层层嵌套，子约束必须相等或更窄。`rack` 要求对每个 PCS replica 独立评估，"Replicas do not all need to use the same rack"。 | 〔源码〕转成 KAI 的 SubGroup 树：PCSG 成为父 SubGroup（带 `MinSubGroup` 和约束），PCLQ 成为叶子。算法就是 KAI 的。 | 〔文档〕Grove #578 / #585 请求"Affinity Groups and Spread Constraints"，原文称 Grove "does not currently provide a way to influence whether prefill workers are placed near decode workers, or whether workers of the same type are spread"。两者 2026-10-21 因 stale 关闭，关闭时的评论是"Stale issue, if still needed, lets reopen"。Grove PR #590（开放中）提议转译到 Volcano subGroupPolicy。 | `grove:docs/user-guide/topology-aware-scheduling.md:12-17, 32, 61, 162`；`docs/proposals/244-topology-aware-scheduling/README.md:81-82`；`operator/internal/scheduler/kai/backend.go:188-225`（v0.2.1-beta1） |
| **Koordinator** gang group | 无拓扑关系。`gang.scheduling.koordinator.sh/groups` 把多个 gang 捆成一组一起放。 | 〔源码〕组内**所有待调度 Pod** 在一次 `FindOneNode` 里统一规划，`DesiredOfferSlot = len(allPendingPods)`。拓扑规格取**第一个 Pod 所属 gang** 的 `NetworkTopologySpec`。 | 〔源码〕**复合**，单一规格，无回溯。求解比较器是字典序（ExistingPodNum → OfferSlot → Score → Name）。 | 〔源码〕FIXME：仅支持无已绑定成员、count==min、无其他拓扑要求的情形。gang 之间的拓扑关系是〔推断〕"不存在"。 | `koordinator:apis/extension/coscheduling.go:46-48`；`pkg/scheduler/plugins/coscheduling/core/network_topology_workflow.go:42-45, 87-96, 131-136`；`core.go:319-324`；`network_topology_solver.go:334-351`（v1.8.0） |
| **k8s 原生 Pod 间亲和、反亲和** | required 或 preferred 的 podAffinity、podAntiAffinity，指定 topologyKey。 | 〔源码〕required 反亲和**对称**：`satisfyExistingPodsAntiAffinity` 检查调度新 Pod 是否会破坏已有 Pod 的反亲和项。自亲和豁免：集群里没有任何 Pod 匹配该 Pod 的 namespace 和 selector，且 Pod 匹配自己的 term、节点具备所需拓扑键时放行（`satisfiesSelfAffinityCheck`）。 | 〔源码〕逐 Pod。KEP-4671 指出这种亲和"do not consider the gang as a unit of scheduling"。 | 〔文档〕官方不建议在数百节点以上的集群使用。KEP-4671 `L1313-1336`：有组内依赖的 PodGroup 可能"fail to find a placement regardless of cluster state, due to the deterministic processing order"，缓解办法是降低依赖方优先级。 | `k/k:pkg/scheduler/framework/plugins/interpodaffinity/filtering.go:645-676, 717-731, 757-785`（master `dd21ea95`）；`website:content/en/docs/concepts/scheduling-eviction/assign-pod-node.md:260-264`；`enhancements:keps/sig-scheduling/4671-gang-scheduling/README.md:158-165, 1313-1336` |
| **KEP-5732 / KEP-6012 / k/k #142690** | KEP-5732 无 PodGroup 之间的关系；KEP-6012 的 `CompositePodGroupSchedulingConstraints.Topology`（最多 1 项）只表达父包含子；#142690 提议 `Exclusive *TopologyExclusivity` 或 `scope: Children` + `maxGroupsPerDomain`（未合入）。 | 〔文档〕KEP-5732 `L251-258`："Different PodGroups can land on different domain instances…"。父约束自顶向下解析。 | 〔文档〕KEP-6012 `L1613-1662`：父候选域逐个生成，在每个候选域内子组**按预排序逐个放**，**不回溯兄弟**，检查 `PlacementFeasible`，由 scorer 选最优，再回滚每个候选域的状态。 | 〔文档〕KEP-5732 `L935-945`：亲和"is evaluated per-Pod at the time of that Pod's scheduling. It does not look ahead… locking a topology domain that is too small for the rest of the group"。KEP-6012 `L1298-1312`、`L2639-2687`：无回溯"can result in failing to find a placement … even when the cluster possesses enough aggregate capacity"。`L1689-1786` 给出例子：PG-1 占满 rack-A2 使 PG-2 无处可放，整个 block-A 被判不可行。`L1670-1687` 解释放弃回溯的原因。#142690（2026-10-25 仍开放）：WAS 里没有任何机制把其他 PodGroup 排除出某个域。 | `enhancements:keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md:251-258, 316-349, 356-407, 935-945`；`6012-composite-podgroup-api/README.md:805-833, 1298-1312, 1613-1662, 1670-1687, 1689-1786, 2639-2687`；`k/k#142690` |
| **Slurm** | 作业级而非 sub-group 级：`--exclusive=topo`、`--segment`、`--consolidate-segments`、`--spread-segments`。 | 〔文档〕`--exclusive[=topo]` 使作业"can not share nodes (or topology segment with the "=topo") with other running jobs"，最接近 required 的作业间排他。`--segment`："No requirement would be placed on all segments for a job needing to be placed within the same higher-level block"。`--spread-segments` 的含义模糊，未能确认。 | 〔文档〕未读到算法。 | 异构作业（heterogeneous jobs）组件之间的拓扑共放或分散，**未能核实**（在相关文档中 grep 无结果）。 | `slurm:doc/man/man1/sbatch.1:273-277, 947-950, 2379-2383, 2436-2440`（`slurm-26-05-6-1`） |
| **YARN placement constraints / Medea** | 带标签的容器组之间：`IN`、`NOTIN`、`CARDINALITY`，范围 `NODE` 或 `RACK`，目标 tag 可用 SELF、NOT_SELF、ALL、APP_ID、APP_TAG 命名空间。 | 〔文档〕示例 `zk(3),NOTIN,NODE,zk:hbase(5),IN,RACK,zk:spark(7),CARDINALITY,NODE,hbase,1,3`。粒度是**打标签的容器**，不是 gang。 | 〔文档〕`scheduler` 处理器"currently supports anti-affinity constraints (no affinity or cardinality)"，`placement-processor` 做预处理。 | Medea 论文未读，未能核实其算法细节。 | `hadoop:hadoop-yarn-project/hadoop-yarn/hadoop-yarn-site/src/site/markdown/PlacementConstraints.md.vm:51-52, 72-77, 90-96, 112-132`（3.4.1，行号取自解码后的文件） |

### Part A 补充引文

- Kueue `topology_aware_scheduling.md:124-126`：`PodSet Group is a unit of flavor assignment and topology domain fitting. This is useful when you want to ensure that multiple PodSets are scheduled in the same topology domain.`
- LWS `topology-placement/_index.md:15, 26-33`：`exclusive-topology` "defines a 1:1 mapping between an LWS replica and a topology domain"，并写到 "Pods from different replicas will not share the same topology domain instance"。
- KEP-848 `L162`：`It is self-referential, so the first pod of a slice is not pinned to a particular domain (required podAffinity with no matching pods yet is satisfied) and later pods are drawn to its domain.`
- k/k `filtering.go:780-785`：`This pod may be the first pod in a series that have affinity to themselves. In order to not leave such pods in pending state forever, we check that if no other pod in the cluster matches the namespace and selector of this pod, the pod matches its own terms, and the node has all the requested topologies, then we allow the pod to pass the affinity check.`

---

## 3. "复合单元"问题的单独回答

**问**：有没有系统把"必须共放的多个 SubGroup"作为复合单元，用它们的聚合需求选一个域？

**答**：有，但都有范围限制，没有一个是通用的"N 个 SubGroup + 亲和项"。

1. **Kueue PodSet Group**〔源码〕。
   - 聚合需求是 `podCountWithLeader`、`leaderCount` 和 `sliceCount`，域能否容纳整组由 `fitsSlices` 判断。
   - 限 2 个 PodSet。
2. **KAI `allocateSubGroupSet`**〔源码〕。
   - 父 SubGroup 的约束用子树全部任务的 `ResReqVector` 之和与任务数做预筛（`checkJobDomainFit`，`job_filtering.go:407-425`）。
   - 这是"在一个域内能否容纳全部子树"的必要条件检查。
   - **它不保证充分**：子组随后仍逐个放，一个子组放失败就回滚，换下一个候选父域。
   - 这与"只做聚合预筛，不做兄弟回溯"的 KEP-6012 取向一致。
3. **Koordinator gang group**〔源码〕。
   - 整组待调度 Pod 一起交给求解器，`DesiredOfferSlot` 是总数。
   - 规格取自首个 gang，所以组内各 gang 的拓扑要求不能不同。
4. **KEP-6012**〔文档〕。
   - 父候选域里模拟子组的放置，但**不做聚合预检**。
   - 子组顺序放置，不回溯兄弟，它自己承认这会漏掉可行解。

**文档化的"逐个放置"失败模式**（首个子组选了装不下后继的域）：
- KEP-5732 `L935-945`
- KEP-4671 `L1328-1336`
- KEP-848 `L101, L198-201`：`Choosing a domain that fits the entire slice up front… requires gang or atomic scheduling`
- KEP-6012 `L1298-1312, L1689-1786, L2639-2687`
- Kueue #15057
- KAI 设计文档 `L35`

〔推断〕目前没有系统把"required SubGroup 亲和"建模为：用成员聚合需求在 tier-t 域里一次选域，同时要求域内能放下各成员各自的需求。最接近的是 KAI 的"聚合预筛 + 逐个放置 + 父级重试"。

---

## 4. Part B：拓扑分数与其他插件分数如何通过权重组合

| 系统 | 拓扑分数在哪里进入决策 | 权重与归一化 | 默认值 | 出处 |
|---|---|---|---|---|
| **kube-scheduler（节点分）** | 各 Score 插件（含 PodTopologySpread、InterPodAffinity）与其他插件并列求和。 | 〔源码〕`normalizeScores`：先 `NormalizeScore`，再检查 [0,100]，越界报错，再 `score × weight` 求和。〔文档〕"After the NormalizeScore phase, the scheduler will combine node scores from all plugins according to the configured plugin weights." | TaintToleration 3、NodeAffinity 2、NodeResourcesFit 1、PodTopologySpread 2、InterPodAffinity 2、DynamicResources 2、NodeResourcesBalancedAllocation 1、ImageLocality 1 | `k/k:pkg/scheduler/apis/config/v1/default_plugins.go:40-58`（master，v1.37.2 为 `40-52`）；`pkg/scheduler/framework/runtime/framework.go:1637-1693`（范围检查报错文本在 `1675-1678`，加权求和在 `1681-1686`）；`staging/src/k8s.io/kube-scheduler/framework/interface.go:330-334`（Max=100，Min=0）；`website:…/scheduling-framework.md:118-124` |
| **kube-scheduler（KEP-5732 Placement 分）** | 新扩展点 `ScorePlacement` / `NormalizePlacementScore`，对一个候选 placement 打分。 | 〔源码〕`RunPlacementScorePlugins` 与节点分同样的模式：归一化、范围检查、`weight × score` 求和（`framework.go:1736-1844`，求和在 `1812-1838`）。`NodeResourcesFit.ScorePlacement` 用 MostAllocated，累计 placement 内所有节点的已请求、已分配、可分配量（`fit.go:690-693`；`resource_allocation.go:508-547`）。 | `PodGroupPodsCount` 权重 1（master `default_plugins.go:91-94`，v1.37.2 为 `102-105`）；`TopologyPlacementGenerator` 无权重 | `k/k:pkg/scheduler/framework/plugins/podgrouppodscount/podgroup_pods_count.go:49-60, 68-90`（归一化：`Min + score*(Max-Min)/maxCount`，`maxCount == 0` 时报错，`L80` 有 TODO）；`enhancements:…/5732-topology-aware-workload-scheduling/README.md:316-349, 356-407, 411-418` |
| **Volcano 框架层** | 各插件的 `NodeOrderFn`、`BatchNodeOrderFn` 分数直接相加；`HyperNodeOrderMapFn` 返回各插件的 map，在 `PrioritizeHyperNodes` 里按 hypernode 累加。 | 〔源码〕**没有框架层权重或归一化**。每个插件内部自行乘自己的权重。节点分在 `PrioritizeNodes` 里把 map 分取整为 int64，最终分 = reduce + order + batch，节点按**精确 float64 相等**分组。hypernode 分是原始 float 相加。 | — | `volcano:pkg/scheduler/framework/session_plugins.go:927-946, 949-970, 1004-1025, 1028-1048`；`pkg/scheduler/util/scheduler_helper.go:75-137, 140-172`（`b231f3ea`）。hypernode 分的消费点：`pkg/scheduler/actions/allocate/allocate.go:864` |
| **Volcano `network-topology-aware`** | `HyperNodeOrderFn`（选 hypernode）和 `BatchNodeOrderFn`（选节点）。 | 〔源码〕hypernode binpack 分 = `Σ(w_r × (used+request)/allocatable) / Σ w_r`，落在 [0,1]；`used+request > allocatable` 记 0。最大分并列时按已放任务数打破。`scaleFinalScore = 100 × GlobalWeight × score`。普通 Pod 按层级衰减权重 `fading^(tier-1)`。 | `weight` 1；`hypernode.binpack.cpu`、`.memory`、`.resources`、`.resources.<name>` 各 1；`normal-pod.enable` true；`normal-pod.fading` 0.8。负值回退默认，fading<0 回退 0.8 | `volcano:pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go:38-65, 124-138, 157-218, 351-375, 377-430, 459-466, 714-749`；文档 `docs/user-guide/how_to_use_network_topology_aware_scheduling.md:142-152`（示例 `weight: 10`，无选值指导） |
| **Volcano `group-topology-affinity`** | `HyperNodeOrderFn`，仅处理 preferred 项。 | 〔源码〕每个候选以 `FullScore=1.0` 起步，每命中一个 preferred 项扣 `(term.Weight/100)×FullScore`，下限 0；最终 `pluginWeight × score × 100`。preferred 项权重仅 1–100 有效，否则该项被跳过。 | `weight` 1，负值回退默认 | `volcano:pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go:36-37, 53-60, 663-717, 719-779, 781, 822`；设计文档 `docs/design/group-topology-affinity.md:36, 1026, 1042-1045`（示例 `weight: 10`） |
| **Volcano `binpack`** | `NodeOrderFn`。 | 〔源码〕`BinPackingScore` 对各资源按权重加权平均，再乘 `MaxNodeScore(100) × BinPackingWeight`。`binpack.weight == 0` 则不注册该函数。资源放不下时返回错误。 | `binpack.weight` 1，`binpack.cpu`、`binpack.memory` 1，额外资源 1 | `volcano:pkg/scheduler/plugins/binpack/binpack.go:112-113, 146-151, 192-196, 206-246, 249-257`；官网 `docs/Scheduler/Plugins/binpack.md:14-15, 56-62` |
| **Volcano `nodeorder`** | `NodeOrderFn` 与 `BatchNodeOrderFn`（含 K8s 移植的分数）。 | 〔源码〕权重在插件内部应用（`float64(score) * float64(p.weight)`）。 | leastrequested 1、mostrequested **0**、nodeaffinity 2、podaffinity 2、balancedresource 1、tainttoleration 3、imagelocality 1、podtopologyspread 2（代码注释称"consistent with kubernetes default setting"） | `volcano:pkg/scheduler/plugins/nodeorder/nodeorder.go:51-66, 104-168, 325, 346-416`；`docs/user-guide/how_to_configure_scheduler.md:54-59` |
| **Kueue TAS** | 单独的排序阶段。 | 〔源码〕`sortedDomains` 是字典序比较器：preferred 节点亲和分（门控 `TASRespectNodeAffinityPreferred`）→ slice 数（BestFit 降序或 LeastFreeCapacity 升序）→ `podCount` 升序 → 层级取值；spread 优先级最后应用并胜出。无跨插件权重。 | — | `kueue:pkg/cache/scheduler/tas_flavor_snapshot.go:2534-2602`（v0.20.5） |
| **KAI** | 域内选择由 `SubsetNodesFn` 决定；节点分由各插件加分。 | 〔源码〕`NodeOrderFn` 是**不加权求和**（`session_plugins.go:519-529`）。常量按十进制间隔（`MaxHighDensity=9`、`ResourceType=10`、`Numa=100`、`Availability=1000`、`GpuSharing=10000`、`Topology=100000`、`K8sPlugins=1000000`、`NominatedNode=10000000`），拓扑 preferred 层分 = `floor((i+1)/len*10) × Topology`。代码注释 "Round down… to prevent interference between plugins"。〔推断〕实际等价于字典序。 | 见左 | `kai:pkg/scheduler/plugins/scores/scores.go:6-15`；`pkg/scheduler/plugins/topology/node_scoring.go:37-53`；`pkg/scheduler/framework/session_plugins.go:45-47, 519-529`（v0.19.0） |
| **Koordinator** | 域选择在求解器里。 | 〔源码〕比较器是 ExistingPodNum → OfferSlot → Score → Name，字典序，节点分在域选择里**不参与**（`network_topology_solver.go:32` 处有 TODO）。 | — | `koordinator:pkg/scheduler/plugins/coscheduling/core/network_topology_solver.go:32, 334-351`（v1.8.0） |

### Volcano 官方文档的状态

〔文档〕volcano-sh/website `9b9f43a0` 中：

- `docs/KeyFeatures/NetworkTopologyAware.md:133`：`Optionally set the scoring weight for network-topology-aware, default weight is 1`。`L125-128` 的 binpack 注释是 "higher weight means greater proportion of CPU resources in scoring"。`L436-437` 说明 hypernode 层级越低分越高，同层则已放 Pod 越多分越高。
- `docs/Scheduler/Plugins/binpack.md:15`：`Plugin Weight: Different plugins also need to be assigned different weights when calculating node scores.`
- `docs/Scheduler/Plugins/nodeorder.md:26`：`By adjusting weights for different scoring dimensions, you can control how workloads are distributed across the cluster`。
- 我只扫到 `group-topology-affinity` 在**本地设计文档**里有记载，官网目录里**未见**该插件页（grep 关键词为 topology、hypernode、network，只看了前 20 条匹配，没读完整目录）。

〔推断，对照代码〕官网 `nodeorder.md:40-73` 与代码不一致：

- 文档写 `nodeorder.weight`、`nodeorder.*.enable`、`nodeorder.*.weight`，且所有 weight 默认 1。
- 代码读取的是 `nodeaffinity.weight` 等不带 `nodeorder.` 前缀的键（`nodeorder.go:51-66`）。
- 我在本地和 `9cca912a5` 的 `nodeorder.go` 里 grep `nodeorder.weight` 均无匹配。
- 代码里 `mostrequested.weight` 默认 0，文档写 1。
- 这一点请以代码为准，文档疑似过时。

### B.4 权重求和的已记录陷阱

1. **k/k #88174**（已关闭）。
   - 原文："Having equal weights is problematic because of the way we score: each Scoring plugin returns a score [0, MaxScore], which get multiplied by the configured weight of the plugin, and a node score is the sum over all scores. Therefore, even if the user explicitly indicates that they prefer to place pod x on node y, this preference may not be respected…"
   - 讨论里的两条建议：Huang-Wei 提出"与 API 关联的插件才应有非默认权重"；alculquicondor 提出权重取"非 API 面向分数之和的一半"，避免被稀释。
   - 这两条是讨论里的意见，不是最终规则。
2. **k/k #122066**（已关闭）。
   - 因框架构造 bug 使 PodTopologySpread 返回 300，触发 `returns an invalid score 300, it should in the range of [0, 100] after normalizing`，导致所有 Pod 不可调度。
   - 说明 k/k 把范围检查当作硬性保护。
3. **Volcano 自身**〔推断，来自上面读到的代码〕。
   - 没有范围检查，各插件量纲不同：
     - binpack：[0,1]×100×weight。
     - GTA：100 基线上扣分。
     - K8s 移植分：取整的 0–100 × weight。
   - 相对影响完全取决于每个插件的 `weight` 配置。
   - GTA 的惩罚下限为 0，两个 preferred 项权重各 ≥50 时就都饱和，和只命中一个无法区分。
   - 节点分取整后按**精确 float 相等**分组并列，hypernode 分却是原始 float 求和，两处口径不同。
   - binpack 注释写"[0, 10(MaxPriority)]"（`binpack.go:239`），而实际乘的是 `MaxNodeScore`=100，注释已过时。

---

## 5. 对本设计的启示

1. **〔文献事实〕** 没有系统提供通用的 SubGroup 亲和或反亲和，现有系统都用 5 种间接方式表达（见摘要第 2 条）。这是你们在领域里没有现成算法可抄的位置，应在设计文档里明确这是新增能力。
2. **〔文献事实+推断〕** required SubGroup 反亲和可以映射成"被已用域排除后的可用区域"。Kueue 的 required spread 就是整条候选条目被禁。LWS 靠"其他组的 Pod 已占用的域不可用"实现。k8s 的 required 反亲和**对称**，设计里需要让"已放的 peer 排除新放的"和"新放的排除已放的"保持一致。
3. **〔文献事实〕** 反亲和里"首个自由"有明确先例：Kueue spread 的 `N == 0` 和 k8s 自亲和豁免。你们的"先放的那个自由"与它们一致，但 k8s 豁免只在**集群里没有任何匹配 Pod 时**成立，条件比"本组第一个"更严。
4. **〔文献事实〕** required SubGroup 亲和如果按"首个定域"逐个放，会出现首个选了装不下后继的域。多份文档明文记录了这种失败模式（见 §3）。
5. **〔推断〕** 应把 required 亲和集合作为复合单元：在 tier-t 层级用成员**聚合需求**选域（对应 KAI 的父级预筛、Kueue 的组预筛），再在域内逐个放，失败时回滚并换下一个候选域（KAI 的做法）。注意聚合需求只是必要条件，域内仍可能因碎片失败，这是 KAI 和 KEP-6012 都接受的权衡。
6. **〔文献事实〕** KEP-6012 明确不做兄弟回溯，是因为 O(C·D) 与 O(D^C) 的差别；KAI 也没有。所以"父级候选域间重试 + 域内不重排"是业界共同选择，不必为了完备性引入兄弟回溯。
7. **〔推断〕** 反亲和里"一个 SubGroup 自己可能跨多个 tier-t 域"的情形，文献里几乎没有现成语义：Kueue 和 LWS 都假设一个组在该层只占一个域。这部分需要你们自己定义，例如"用到的所有 tier-t 域都不得被 peer 使用"，并在设计文档里写清。
8. **〔文献事实+推断〕** 偏好型关系应放在硬过滤之后、作为排序键或分数。Kueue 把 spread 放在比较器最后且胜出，KAI 用 10⁵ 量级的常量保证拓扑压过一般分数。两者都避开了异构分数相加。
9. **〔文献事实〕** 权重设计上，k/k 是"每插件归一化到 [0,100] + 范围检查 + 加权和"；Volcano 当前没有这两步。若新增拓扑分数要与 binpack、nodeorder 相加，建议在设计文档里列出每个分数的取值范围、权重含义、并列处理（注意取整与 float 精确相等的差异）。
10. **〔推断〕** 若不想让拓扑分数依赖用户调权重，可以参考 Kueue 或 Koordinator 的字典序比较器，或 KAI 的十进制间隔常量。字典序下"偏好权重"变成优先级顺序，更可预测，但失去了 preferred 项的连续权重。

---

## 6. 检索范围与未能核实的事项

**已读**：
- Kueue、LWS（含 KEP-1064、KEP-848）、KAI、Grove、Koordinator 的源码与文档。
- k/k 的调度器源码和 enhancements 里的 KEP-4671、5732、6012。
- k/k 的 #142690、#88174、#122066。
- Slurm 的 `sbatch.1`。
- YARN 3.4.1 的 `PlacementConstraints.md.vm`。
- Volcano 本地 checkout（`b231f3ea`）的插件源码，以及官网仓库（`9b9f43a0`）的插件文档。

**未能核实**：
- Grove 只读了 v0.2.1-beta1。"没有 spread 或反亲和"是据该 tag 的文档和代码推断，**无法证明**之后没有合入。
- LWS v1.0.0 的 `PlacementPolicy` 和 DisaggregatedSet 的 API 类型没有打开，KEP-1064 和 848 是提案。
- KAI 里是否存在子组间亲和或反亲和：只能说在文档树和 `allocate.go`、`job_filtering.go` 里没找到，不能证明不存在。
- Slurm 异构作业的拓扑语义没有查到相关文字。`--spread-segments` 的含义模糊，我没有给出解读。
- YARN 的 Medea 论文未读，只读了 3.4.1 的官方文档。
- Volcano：
  - 本地 checkout 是带重构的分支（`b231f3ea`），不是上游 master（`9cca912a5`）。我只在 `9cca912a5` 对照了 `nodeorder.go` 的参数键和 `how_to_configure_scheduler.md` 的参数表，其余行号取自本地 checkout。
  - 官网没找到"如何选权重"的指导。官网目录只扫了 topology、hypernode、network 的前 20 条匹配，没有读完整树。
- Volcano issues：只用 `gh search` 搜了两次（`binpack nodeorder score`、`binpack weight`），没找到专门讨论分数量纲失配的 issue。这不等于没有。
- k8s 文档里除"well defined range"和 0–100 的范围检查外，没有找到更多关于量纲陷阱的官方说明。
- 官网 `docs/Scheduler/Plugins/` 里的页面我只扫到 9 个，未确认是否还有 `network-topology-aware` 或 `group-topology-affinity` 页。