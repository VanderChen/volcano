# 社区与产品调研（第二轮）：排除类约束、多 SubGroup 顺序与失败后修复

- 角色：社区与产品调研员（独立 subagent，agent id `5a016957-307f-4a43-91d7-741e03410be0`，只读）。
- 范围：协调者给出 Q1–Q5（可用域并集 / 反亲和与共置的组合 / 子单元顺序与重试 / 上游是否已知 B+C 问题 / 失败后修复的先例）。
- 下文为该 subagent 的原始报告。协调者的测试结果不在本文件内，见 `PROPOSAL_COMMIT.md` 第 15 节。

---

**图例与证据标签**
- **[SRC]**：在固定版本源码中核实。
- **[DOC]**：仅在文档或 KEP 中核实。
- **[NV]**：未能核实。
- 链接前缀缩写，均为固定版本：
  - K = `github.com/kubernetes-sigs/kueue/blob/v0.20.5/`，tfs = `pkg/cache/scheduler/tas_flavor_snapshot.go`
  - A = `github.com/kai-scheduler/KAI-Scheduler/blob/2305acfc/`（仓库已从 NVIDIA 改名到 kai-scheduler，v0.19.0）
  - O = `github.com/koordinator-sh/koordinator/blob/989ca85c/`（v1.8.0）
  - G = `github.com/kubernetes/kubernetes/blob/dd21ea95/`（master），G137 = 同仓库 tag `v1.37.2`
  - E = `github.com/kubernetes/enhancements/blob/72907dea/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md`
  - S = `github.com/SchedMD/slurm/blob/slurm-26-05-6-1/`
  - V = `github.com/volcano-sh/volcano/blob/d423e427c/`
- 引用本地 volcano 仓库只用 d423e427c，不引用其后的本地提交。

## (a) Q1–Q3 逐系统表

**Q1 的总体结论：没有找到任何系统像 Volcano 那样，因为一个祖先域部分被排除，就把它从候选中整体丢掉。** 唯一接近的是 Kueue 的 Required topology spreading，见下方 Q1 补充。

| 系统 | Q1 部分排除的祖先域是否仍为候选（容器∩范围） | Q2 跨 workload 拓扑级反亲和及其与同域共置的组合 | Q3 多子单元顺序；失败后重试/回溯 |
|---|---|---|---|
| Kueue v0.20.5 | **是 [SRC]**。`fillInCounts`（K tfs#L2606-2656）按 `FindFeasibleNodes`（taint、nodeSelector、必需亲和、资源）过滤叶子，不可行叶子计数为 0。`fillLeafCounts`（K tfs#L2931-2960）用 `BelongsTo(...)` 做范围过滤（L2934）。`fillInCountsHelper`（K tfs#L2962-3060）把父域计数设为子域之和（L3007、L3025）。父域保留，容量变小。 | **无通用 PodGroup 反亲和 API**。最接近的是 KEP-13746 topology spreading（alpha），按域内同类 PodSet group 数量禁用域。组合方式：`filterOutBannedDomains` 作用于搜索层（K tfs#L2012）；required 时只选一个域（L2069-2093）；非 required 时在剩余域的并集上累积（L2097-2165）。KEP 写明被禁候选"整条丢弃，而非只丢节点"（[KEP L638-652](https://github.com/kubernetes-sigs/kueue/blob/v0.20.5/keps/13746-tas-topology-spreading/README.md#L638-L652)）。[DOC+SRC] | 按 `Spec.PodSets` 首次出现顺序分组（`pkg/scheduler/flavorassigner/flavorassigner.go#L334-380`）。组内 leader 与 worker 至多 2 个 PodSet，数量大者为 worker（K tfs#L1078-1092）。已放置的用量累积（L996-1018）。**首个失败的 group 直接 `return`（L943、977、985、987），不重排、不回溯。** 只在 flavor 之间重试。[SRC] |
| KAI v0.19.0 | **是（范围用 NodeSet 表示），但预过滤对范围不敏感 [SRC]**。`subSetNodesFn`（A `pkg/scheduler/plugins/topology/job_filtering.go#L34-116`）以 `nodeSet` 为范围。`getJobAllocatableDomains`（L271-321）遍历所有层级的域，部分覆盖的祖先仍是候选。最终 NodeSet 是 `domain.Nodes ∩ validNodes`（L103-113）。容量计算按完整 `domain.Nodes` 算（L171-215），是高估。拓扑插件看不到 taint、NodeAffinity、PVC，只能尽力而为（A `docs/developer/designs/node-sets/README.md#L11-13` [DOC]）。 | **PodGroup 级拓扑反亲和：docs/topology 与设计文档中未找到 [NV：证明不了不存在]**。Pod 级走嵌入的 k8s InterPodAffinity 谓词（`pkg/scheduler/k8s_internal/predicates/predicates.go`，由搜索定位，未逐行读 [NV]）。 | 按 `SubGroupOrderFn` 的分配比例排序（`pkg/scheduler/plugins/subgrouporder/subgroup_order.go#L33-80`、`pkg/scheduler/actions/common/allocate.go#L339-344`）：未满足 gang 者优先，required 优先，已分配少者优先；**不看约束紧度**。逐个 NodeSet 做 Checkpoint→分配成员→失败则 Rollback→下一个 NodeSet（`allocate.go#L47-97, 115-142`）。NodeSet 内各成员取首个成功（L132-141）。因此回滚只发生在父 NodeSet 级，不重排、不重试前一个成员的次优解。[SRC] |
| Koordinator v1.8.0 | **是 [SRC]**。`calculateNodeOfferSlot`（O `pkg/scheduler/plugins/coscheduling/core/network_topology_solver.go#L113-158`）对每个节点真正跑 Filter 插件，失败节点 slot 为 0。`evaluateTopologyNode`（L212-233）把 slot 加到所有祖先。 | **无 gang 级反亲和**。Pod 级 InterPodAffinity 在叶子 slot 计算里作为 Filter 生效（同上 L113-158，推断：它不影响容器层，只影响叶子 slot）[SRC+推]。 | 待放置 pod 按 index 排序（O `network_topology_workflow.go#L91`）。源码 FIXME（L42-45）：只支持无已绑定成员、总数等于 minMember、无其他拓扑要求的单个 gang。`PlacePods` 一次返回（L150-159）。无 gang-group 联合放置，无回溯。[SRC] |
| k/k master dd21ea95（KEP-5732 beta） | **是：Placement 就是节点集合 [SRC]**。`GeneratePlacements` 只按父 placement 内节点的 label 分组（G `pkg/scheduler/framework/plugins/topologyaware/topology_placement.go#L78-97`）。taint、亲和等在 placement 内逐 pod 的默认 Filter 里生效（[E L377-385](https://github.com/kubernetes/enhancements/blob/72907dea/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L377-L385) [DOC]），所以 placement 保持候选、可用节点变少。多个 Generate 插件的结果用节点集合求交的叉积合并（G `pkg/scheduler/framework/runtime/placement_merging.go#L49-90`）。 | **没有 gang 级拓扑反亲和**。Pod 反亲和是 placement 内的逐 pod Filter，与 placement 正交。KEP 自述反亲和"按 Pod 评估，不前瞻"（[E L935-945](https://github.com/kubernetes/enhancements/blob/72907dea/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L935-L945) [DOC]）。 | CompositePodGroup 的子组按创建时间→名称→类型排序（G `pkg/scheduler/framework/types.go#L1249-1270`）。子组串行评估，前一子组的结果被 assume 后才评估下一个（G `pkg/scheduler/algorithm_podgroup.go#L1041-1081`）。失败则整个 placement 回滚（L1013-1022）。**只在不同的父 placement 之间重试（L619-641）。** 无子组重排或回溯。KEP："avoiding complex backtracking"（[E L212-213](https://github.com/kubernetes/enhancements/blob/72907dea/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L212-L213)）。[SRC] |
| k/k v1.37.2 | 同上：placement 是节点集合 [SRC]。**只用第一个 Generate 插件，没有合并**（G137 `pkg/scheduler/framework/runtime/framework.go#L2222`）。 | 同上 | 评估**所有** placement，没有提前停止（G137 `pkg/scheduler/schedule_one_podgroup.go#L1005-1036`），再打分选优。顺序逻辑与 master 相同（composite 在 L1073、L1321）。[SRC] |
| Slurm slurm-26-05-6-1 | **是 [SRC]**。`node_map` 位图是范围，资源不足的节点被清除（S `src/plugins/select/cons_tres/job_test.c#L1089-1095`）。`switch_node_bitmap[i] &= best_nodes_bitmap` 后重算 `switch_node_cnt`（S `src/plugins/topology/tree/eval_nodes_tree.c#L522-527`）。交换机保留，节点数变少。 | **未找到交换机级作业间反亲和 [NV]**。 | 单作业内无 SubJob 概念。het-job 各组件的放置顺序和回溯：**未核实 [NV]**。 |
| K8s Pod 反亲和（基线） | 不适用 | 语义是"Pod 不应运行在已有匹配 Pod 的域 X 中"（[k/website assign-pod-node.md#L244-248](https://github.com/kubernetes/website/blob/main/content/en/docs/concepts/scheduling-eviction/assign-pod-node.md#L244-L248)，main 分支，未固定 [DOC]）。源码：只要当前节点所在域的 `clusterWideAntiAffinityCounts > 0` 就拒绝该节点，节点缺少该 label 则不拒绝（G `pkg/scheduler/framework/plugins/interpodaffinity/filtering.go#L719-731` [SRC]）。**这是逐域排除，没有任何要求收敛到某一个域的语义。** | — |

**Q1 补充：Kueue 是唯一"丢弃候选域"的先例，但语义与 Volcano 的情形不同。**
- Required topology spreading 会整条丢弃被禁的搜索层候选域（K tfs#L2012）。这是有意设计，按域内同类 PodSet group 的占比来禁，不是按节点排除。
- 非 required 时，剩余域仍可取并集（K tfs#L2097-2165），不会出现"每个剩余域单独装不下就失败"。
- 相关 bug #15787（已关闭，[链接](https://github.com/kubernetes-sigs/kueue/issues/15787)）：Preferred 选中了"容量只在被禁子域里"的父域，下钻后无合格子域，调度器却发布了不完整的 assignment 且没有失败原因。这是父域容量聚合没有扣除被排除部分造成的。

## (b) Q4：Volcano 上游 d423e427c

先回答：我没有找到上游 issue/PR 讨论 B+C 并集问题。这是"未找到"，不等于"确认没有人知道"。

**设计文档**（[V docs/design/group-topology-affinity.md](https://github.com/volcano-sh/volcano/blob/d423e427c/docs/design/group-topology-affinity.md)）[DOC]：
- L408："Required PodGroup anti-affinity excludes every domain occupied by an applicable peer." 语义是排除已占用域。
- L140："Anti-affinity never reduces a multi-domain placement to a single LCA." 这句针对的是对端 PodGroup 的占用，不是本作业的放置。
- L406："A coarser candidate spans multiple domains and is not a legal final candidate for a required term. A required term fails closed if its tier cannot be resolved or the candidate has no ancestor at that tier."
  - 这是**文档明示的设计**：比比较层更粗的候选不合法。
  - 文档**没有**说作业必须装进一个合格 HyperNode，也**没有**讨论多个兄弟域的并集。后果"B+C 并集不可用"文档没有讨论。
- L452：SubJob 按确定顺序评估，前面 SubJob 的暂定放置对后面可见，放弃 Job 级候选时整体回滚。
- L460："The Framework intersects gradients by HyperNode name, not by layer index."
- L510：合法 HyperNode 的节点 dry-run 失败则试下一个候选；没有候选则保持 Pending。
- §7（L692 起）把 enqueue 前"全局域耗尽检测"列为 Future。

**源码** [SRC]：
- 插件 `satisfiesAntiAffinity`（[V pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go#L262-272](https://github.com/volcano-sh/volcano/blob/d423e427c/pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go#L262-L272)）在 `AncestorAtTier(candidate, tier)==""` 或祖先在占用集合时拒绝候选。这就是公共父域被丢掉的路径。
- 梯度构造 `buildPodGroupAntiAffinityGradient`（同文件 L209-243）。
- 框架按名称求交：`intersectHyperNodeGradients`（[V pkg/scheduler/framework/session_plugins.go#L1263](https://github.com/volcano-sh/volcano/blob/d423e427c/pkg/scheduler/framework/session_plugins.go#L1263)）。
- `allocateForJob`（[V pkg/scheduler/actions/allocate/allocate.go#L455-530](https://github.com/volcano-sh/volcano/blob/d423e427c/pkg/scheduler/actions/allocate/allocate.go#L455-L530)）：
  - L478/L484：每个候选 HyperNode 作为整个作业的容器。
  - L496-514：弹出 SubJob 顺序分配，没有重排和重试。

**Issue/PR 检索**：
- [#5347](https://github.com/volcano-sh/volcano/issues/5347) 仍 open。正文提到"four prefill shards on four cabinets"。SubGroup 级亲和（Task 3）未勾选。5 条评论没有涉及并集、回溯或有界联合放置。
- [#5349](https://github.com/volcano-sh/volcano/pull/5349)（PodGroup 反亲和，已合并，合并提交 `3c90d7d8`）：16 条评论和 29 条 review 讨论 API、性能、缓存、minResource 精度，没有讨论多域容器。
- 用 `podGroupAntiAffinity`、`backtrack`、`union` 等关键词检索，只命中 #5347、#5349、#6137/#6210/#6213（未知 tier 名的 preferred 项使 PodGroup 无法调度，也是"fail closed 导致不可调度"一类）。
- 未检索到 "2 Schedulable, 2 Unschedulable" 一类报告。
- 其他 open 项只看了标题：#5430（跨 PodGroup 拓扑 packing）、#5811（限制候选扫描）、#5860/#5861（SubJob 排序比较器）。

## (c) Q5：失败才升级的先例与两篇经典文献

**真实调度器**：
- Kueue：非 required 时，单域装不下就向上一层搜索（`findLevelWithFitDomains`，K tfs#L2094-2096），到顶层再跨域累积（L2097-2165）。required 失败即失败。[SRC]
- KAI：失败后换下一个 NodeSet，先 Rollback（A `allocate.go#L47-97`）。[SRC]
- k/k master：无 PlacementScore 插件时，找到第一个可行 placement 就停（G `pkg/scheduler/algorithm_podgroup.go#L709-721`）。**只有在已有成功时才提前停**；全部不可行才报失败（L644-663），所以提前停不会把"可调度"变成"不可调度"。[SRC]
- Slurm：`--switches=count@time` 先等到超时再放宽（`eval_nodes_tree.c#L184-190, 708-737`）。[SRC]
- **没有系统在失败后重排子单元再试**（见表 Q3）。

**经典文献**：
- Squeaky Wheel Optimization（Joslin & Clements，JAIR 10，1999，[链接](https://jair.org/index.php/jair/article/view/10231)，已核对摘要和正文）：
  - 贪心构造，分析"麻烦元素"，把它们提到序列前面再构造，如此循环。
  - **不保证**最优、完备或不陷入小环。原文称不保证"not become trapped in a small cycle"，实现中靠加随机扰动和周期重启缓解。
  - 注意：第一轮报告的 JAIR 链接 `.../10240` 是错的（指向另一篇），正确为 `10231`。
- Limited Discrepancy Search（Harvey & Ginsberg，IJCAI 1995，[PDF](https://ijcai.org/proceedings/1995/0080.pdf)，已核对）：
  - 按"偏离启发式的次数"递增搜索。
  - 迭代到最大深度即穷举，**保证有解必找到、无解必终止**。
  - 代价是完整运行是指数级的；只有受限预算内才是有界的，此时无完备性保证。

## (d) 对本设计的启示

1. **"容器∩范围"有先例支持。**
   - Kueue（叶子计数为 0 后向上求和）、Koordinator（offer slot）、Slurm（`switch_node_bitmap &= node_map`）、k/k Placement（节点集合）、KAI（NodeSet 求交）都在叶子或节点层屏蔽被排除的节点，祖先域保留并带缩小后的容量。
   - 没有找到任何系统把"部分被排除的祖先域"整体丢弃。Volcano 当前的做法在这批系统里是孤例。
2. **排除与共置分离也有对应实践。**
   - k/k 把 placement 生成（共置）和逐 pod 的反亲和 Filter（排除）分开，K8s 基线语义是逐域排除、不要求收敛。
   - Kueue 的 required spreading 虽然丢整个候选，但它本身就是"选一个域"的容器语义，而且非 required 时仍可取并集。
3. **"失败才修复"只有部分先例。**
   - 先例有：Kueue 向上一层、KAI 换 NodeSet、Slurm 等待后放宽、k/k 失败才报整体失败。
   - 没有先例的是"重排失败子单元后重试"。这一步只有 SWO 作理论依据，且 SWO 无最优和完备保证。
   - 因此设计里必须保留两条规则：硬预算，预算截断时不得报"全局不可行"，而应报"未完成搜索"。
4. **别人踩过的坑。**
   - **范围不敏感的容量聚合**：Kueue #15787（父域容量含被禁子域，下钻为空却发布了不完整 assignment）；KAI 预过滤按完整 `domain.Nodes` 算容量，且 #2546 是预过滤误拒异构 pod 的开放问题。排序阶段的容量估计必须按"容器∩范围"计算，且下钻为空时要显式失败。
   - **预过滤的上界必须可靠**：KAI #1591（已关闭）是预过滤在抢占求解前就消灭了域；#2546 也是预过滤误拒。过滤阶段只能做"安全上界"，不能做精确裁决。
   - **聚合估计与下钻不一致**：Kueue #7446（已关闭，维护者称需让"两级调度"逻辑可靠）和 #10778（stateWithLeader 高估导致无限调度循环）。
   - **上界高估的反面**：Kueue #15373（只用 worker 的可行性检查 leader，放行后 leader 无处可去，仍占用配额）。验证阶段必须是真实的逐 task 流水线。
   - **贪心的次优**：Kueue #10574（open，贪心域选择非最优），KAI 设计文档自认首 pod 的选择不会重试（A `docs/developer/designs/topology-awareness/README.md#L35`）。
   - **有状态插件的回滚风险**：k/k 在评估和提交时各跑一次 Reserve，因此插件状态必须可逆；Volcano 侧对应 #5362（缺 `Statement.Discard()`）。
5. **候选循环可借鉴 k/k 的提前停止条件**：只在找到可行解之后才提前停，而不是在排序之后。这与"rank 后按序 verify、第一个成功即停"一致，也符合"不能把可调度变成不可调度"的要求。

## (e) 检索范围与缺口

**已做**：
- 在固定版本读取了 Kueue、KAI、Koordinator、k/k master 与 v1.37.2、KEP-5732、Slurm、Volcano d423e427c 的相关源码。
- 通过 gh 检索 Volcano、Kueue、KAI、Koordinator、k/k 的 issue/PR。

**缺口**：
- Slurm het-job 多组件放置顺序和回溯，以及 Slurm 交换机级反亲和，均**未核实**。
- KAI 的 Go API 类型、PodGroup 级反亲和未读完，只确认文档里没有。
- Koordinator 多 gang（gang group）联合放置，只依据 FIXME 判断为不支持。
- Kueue main 与 v0.20.5 的差异未比较。
- Kueue KEP-13746 为 alpha，未核实 feature gate 默认值。
- k/k 只检索了 issue 标题，没有发现专门描述"贪心串行导致 false negative"的 issue。
- GitHub Search API 限流（30 次/分钟），检索靠关键词，**不能证明相关 issue 不存在**。
- 所有 issue 内容都是用户报告，未逐一复现。
- 部分 issue（#5430、#5811、#5860/#5861、KAI #2077/#2055、Kueue #16083）只看了标题。
- 上游 K8s 文档页引用的是 website `main` 分支，未固定提交。
