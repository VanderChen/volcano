# 从局部修复到统一拓扑规划：前因后果

本文按问题与决策的因果关系串联历史。各期版本与证据范围见[归档入口](README.md)；原始 issue 状态按归档时文件保留，不把 DONE 理解为“整个重构已完成”。

## 1. 起点：两层容器试放逐渐承载更多语义

原有拓扑分配以 Job/SubJob 的 HyperNode gradient 为骨架：在候选域内试放任务，保存可行方案并撤销试放，用 HyperNode 分选一个方案，再恢复它；某个 gradient 成功后停止向后比较。多插件梯度求交、SubGroup 亲和／反亲和、native soft、实际 placement 恢复和多根候选相继加到这条路径上。

这套机制能表达许多场景，但三个对象逐渐混在一起：**合法节点范围、软偏好顺序、完整作业的放置方案**。一个候选 HyperNode 既像约束容器，又像评分单位，还决定搜索何时结束。后来问题的共同根源，是这些职责没有清楚分开。

应准确描述旧路径：普通 NodeOrder 并非完全不执行，也不是所有域都选完后才第一次执行。多个域的 dry-run 中会做 Predicate 和 NodeOrder；断点在于节点分被丢弃，最后跨域比较仍只看域分。required NodeAffinity 由 Predicate 检查，受到隔离的是 preferred 的跨域影响。[015 代码分析](sources/issues/features/015-unified-topology-planning-framework-IP/VOLCANO_UPSTREAM_TOPOLOGY_REFACTOR_DESIGN_CN.md)

## 2. 第一批问题：软偏好、全局范围与状态连续性

001–004、008、010、011 围绕 Job/SubJob 软亲和、全局子组搜索、无偏好插件的梯度、亲和与反亲和评分优先级、两阶段分配等逐步分析和调整。早期修复证明，单次域内试放成功，不代表 Job 各 SubGroup 的整体偏好和状态都被正确保留。

012/013 针对 soft→hard 转换缩窄搜索，保留原生 soft 语义并回移到 v1.14。这里形成的要求是：**soft 可以影响选择，但不应把合法 fallback 从候选空间消掉。** 然而，保留 soft 候选仍不能自动让所有评分在跨域方案中共同发挥作用。[012](sources/issues/bugs/012-soft-topology-conversion-narrows-search-DONE/DESCRIPTION.md)、[013](sources/issues/bugs/013-native-soft-topology-v1.14-backport-DONE/DESCRIPTION.md)

## 3. B+C 暴露“一个容器”并不能代表精确合法空间

009/014，以及后来在明确生产基线上重做的 018，处理 PodGroup required anti-affinity 后只剩多个合法根的场景。若合法范围是 B+C，单独试 B、单独试 C 可能都不足以让 Job Ready；直接放宽到它们的 LCA，又会把被排除的 A 带回来。

因此需使用精确节点并集 `nodes(B) ∪ nodes(C)`，共同祖先只帮助遍历，不能成为许可范围。这是后续 NodeSet 抽象的重要来源。018 同时澄清：生产基线已有 native soft 修复，也仍然需要多根事务；native soft 与 multi-root transaction 是两个不同缺口。[018 原始记录](sources/issues/bugs/018-podgroup-antiaffinity-bc-production-containment-DONE/DESCRIPTION.md)

此处的生产修复有边界：它处理特定入口的 exact forest，没有承担任意多 SubGroup 联合优化。其有效性和重构的必要性可以同时成立。

## 4. 015 第一版原型：统一节点放置，但目标还不够完整

2026-07-29/30 的 015 原型把候选精确范围、逐任务选择、事务与有界回退放进统一流程。历史提交为 `c4c6c3643`，基线为 `8a71ca5323`；保存的记录包含聚焦测试、benchmark、镜像和 Kind K0–K5，也包含仓库级门禁的环境失败。

它证明了一个方向：遇到后续任务失败，可以回到前序选择点，而不只是丢弃整个 Job 尝试；常见首选分支可以没有额外回退。[原型说明](sources/issues/features/015-unified-topology-planning-framework-IP/DESCRIPTION.md)、[历史验证](sources/issues/features/015-unified-topology-planning-framework-IP/artifacts/VERIFICATION_RESULTS.md)

但原型仍可能让 soft domain rank 优先于普通总分，并在首个合法方案处停止。后续要求不仅是“找到一个能跑的方案”，还包括“所有相关偏好共同决定整组质量”。因此原型不能被直接命名为最终实现，也不能拿它的性能或 Kind 结果为后续架构背书。

## 5. 016–024：把不同缺口进一步拆清楚

| 材料 | 暴露或澄清的问题 | 留给统一架构的要求 |
| --- | --- | --- |
| [016](sources/issues/features/016-native-soft-topology-scoring-OPEN/DESCRIPTION.md) | 域顺序可以先于权重决定结果，原生 soft 不只涉及锚点传递 | soft 作为有限收益进入共同候选比较。 |
| [017](sources/issues/features/017-bounded-joint-placement-search-OPEN/DESCRIPTION.md) | A 可去两节点，B 只能去一节点；A 的局部最优占住 B 唯一位置 | 即使全是 hard，也需要跨子组回退。 |
| [019](sources/issues/bugs/019-preferred-subgroup-order-lost-on-abstain-DONE/DESCRIPTION.md) | 插件 abstain 后偏好遍历次序丢失 | 插件组合合同要区分无约束与有效偏好。 |
| [020](sources/issues/bugs/020-preferred-subgroup-antiaffinity-count-blind-scoring-DONE/DESCRIPTION.md) | 只问某域有没有 peer，丢失已有 peer 数量 | 计数主体、去重和边际收益必须明确；计分修复不等于全局质量搜索。 |
| [021](sources/issues/bugs/021-soft-topology-default-lowest-tier-IP/DESCRIPTION.md) | soft 默认 tier 的含义与表现需明确 | 默认值进入显式语义合同，不能藏在遍历次序里。 |
| [022](sources/issues/bugs/022-candidate-forest-memory-scaling-DONE/DESCRIPTION.md) | 累计 Set.Union 反复复制、失败原因重复列全域名称 | 消除可确认的超线性成本，集合和诊断都需有界。 |
| [023](sources/issues/bugs/023-topology-nodeaffinity-preferred-score-isolation-OPEN/DESCRIPTION.md) | NodeAffinity 等普通节点分未进入最终跨域方案比较 | 统一评分入口必须贯穿到完整方案选优。 |
| [024](sources/issues/bugs/024-required-subgroup-antiaffinity-hard-semantics-DONE/DESCRIPTION.md) | 四域四组与四域五组验证 required 边界 | required 不得被 fallback 放宽；不可行时不能部分误提交。 |

020 的例子是 8 个 1 卡 prefill 和 8 个 2 卡 decode，放到 3 个各 8 卡节点。在 preferred 反亲和下，域都被占用后仍需区分 peer 数量。用户随后强调，不能围绕此例加专用交换算法；应该从通用约束、收益与有界搜索推导机制，再让例子验证它。[需求纠偏记录](sources/issues/features/015-unified-topology-planning-framework-IP/REQUIREMENTS_2026-09-11.md)

## 6. 9 月重设计：从“首解可行”走向“可解释的联合质量”

2026-09-11 重新明确目标：同一模型同时处理 hard/soft、多 SubGroup、全部启用评分、可行性回退和质量改进。把物理拓扑、策略关系图和搜索树分开；把固定工作集、硬约束、评分合同、状态恢复和预算作为显式输入。

这一轮不再把“第一个成功方案永远停止”视为不可改变的原则。先找到可行方案，然后在独立质量预算内探索更好方案；完整方案按共同目标比较。若有有效的安全界，可以剪枝并解释质量范围；没有证明或覆盖不足时，不能声称最优或全局无解。

模型实验用于核对算法性质和操作数：小规模穷举对照、随机实例、不同任务顺序、预算、回滚及 Bug020 等。模型不调用 Volcano 插件，不能替代 Go、镜像、Kind 或生产延迟。[完整模型](sources/issues/features/015-unified-topology-planning-framework-IP/TOPOLOGY_PLANNING_FRAMEWORK.md)、[设计核对](sources/issues/features/015-unified-topology-planning-framework-IP/DESIGN_COMPLETION_AUDIT.md)

## 7. 10 月上游重审：不能把本地生产分支当作主线

2026-10-05 用 `master@e0905bab6` 重新分析评分链、插件归一化、Statement、提名与资源／分片类别，并把 PR #5349 当作独立比较对象。结论不是增加一个 HyperNode NodeAffinity scorer，而是让统一评分在完整方案比较时仍然有效。

同一组候选归一化、权重只应用一次、单节点仍需能参与方案评价、Skip/Error 明确，以及对有状态插件的规范重放，都是这一轮细化的合同。仅把不同域试放留下的历史分数相加，并不能形成可比较的目标。

2026-10-23 的 025 又固定到 `master@0854d3a40`：此时主线已有内部树、HyperNodeIndex、generation、PodGroup GTA 和 typed gradient。于是“主线只有列表／没有索引／没有 GTA”不再成立。旧文保留原基线，最终方案明确修正事实。[025 §1](sources/issues/features/025-hypernode-tree-index-planning-OPEN/HYPERNODE_TREE_INDEX_DESIGN_CN.md)

## 8. 最后补全：统一事实、物化索引与增量规划

用户最后一轮把目标扩展为连续三阶段：API 库统一解析并展示；controller 发布 HyperNode tree index；scheduler 消费并复用整份拓扑，维护资源摘要、SubGroup 结果继承及域级选优比例，同时研究树／网络流算法。后续补充明确未来 SubGroup 亲和／反亲和要进入当前设计。

025 因此补足了 015 之前较薄弱的数据层和生命周期：用户展示与 scheduler 采用版本一致性、森林／重叠 membership、不可变 revision、CR 大小与分片、静态与动态状态成对切换、peer 依赖缓存失效、扩大搜索后的可撤销继承。

最后选定的方向是**共享索引 + 增量状态 + 有界联合规划**。matching、flow、树 DP 保留为有明确假设的子问题工具，不承担完整多资源、Gang、关系与插件生命周期的通用替代。对应完整方案见[整合稿](FINAL_DESIGN_CN.md)。

## 9. 归档时的真实终点

最后一轮设计及待实施验证计划已经存在，核心 SubGroup 补充也已写回。目标框架尚未实现，025 仍 OPEN，015 仍 IP。旧原型、局部修复和模型结果保留各自范围。本次只将这些成果整理为可离线审视、可从个人远端浏览的文档归档。
