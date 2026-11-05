# 参考资料与使用边界

资料按“本地需求与原始证据 → 固定版本源码 → 社区讨论 → 算法一手资料”组织。外部链接是参考文献，不是新增功能已经实现的证明。动态页面状态不回写成历史事实。

## 1. 需求、设计和源码导读

| 材料 | 在本次重构中的作用 |
| --- | --- |
| [2026-09-11 用户需求](sources/issues/features/015-unified-topology-planning-framework-IP/REQUIREMENTS_2026-09-11.md) | 通用架构、全部评分、多组质量、效率与理论边界的评价标准。 |
| [015 完整模型](sources/issues/features/015-unified-topology-planning-framework-IP/TOPOLOGY_PLANNING_FRAMEWORK.md) | 约束与收益模型、统一搜索、传播、安全界和成本推导；附录有完整论文与源码引用。 |
| [015 上游方案](sources/issues/features/015-unified-topology-planning-framework-IP/VOLCANO_UPSTREAM_TOPOLOGY_REFACTOR_DESIGN_CN.md) | 固定上游基线的评分与状态合同，以及 PR #5349 比较。 |
| [015 评分代码导读](sources/issues/features/015-unified-topology-planning-framework-IP/VOLCANO_UPSTREAM_SCORING_CODE_WALKTHROUGH_CN.md) | 多 Pod 的具体评分算例与调用链。 |
| [025 需求](sources/issues/features/025-hypernode-tree-index-planning-OPEN/DESCRIPTION.md) | 三阶段、资源摘要、SubGroup 缓存、域比例和树／flow 研究需求。 |
| [025 最后修订](sources/issues/features/025-hypernode-tree-index-planning-OPEN/HYPERNODE_TREE_INDEX_DESIGN_CN.md) | 最终数据层、增量状态、联合规划及核心 SubGroup 四类关系。 |
| [025 验证计划](sources/issues/features/025-hypernode-tree-index-planning-OPEN/PROPOSAL_COMMIT.md) | U/B/K 的未来验收，全部 NOT RUN。 |

归档还核对了最后一轮对话中的追加要求：“subgroup亲和/反亲和在未来也会合入master，所以可以考虑进去这个场景”。该要求已反映在 025 原文 §8.4 及 U17–U19、相关 Kind 计划中；对话原始 ID 为 `01a16912-aab3-7192-92bd-d27620db4a51`。本仓库保存最终文档，不复制整份聊天或工具输出。

## 2. 最终设计的固定源码基线

以下均为 `volcano-sh/volcano@0854d3a4018c9304f8a25ded7a6f136c539f753c`，不是移动的 master 链接。

| 位置 | 为什么引用 |
| --- | --- |
| [HyperNodeInfo](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/scheduler/api/hyper_node_info.go) | 已有树结构、成员集合与构建成本。 |
| [HyperNodeIndex](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/scheduler/api/hyper_node_index.go) | 已有反向归属、祖先与重叠兼容能力；公共索引是在其上演进。 |
| [Session](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/scheduler/framework/session.go) | 拓扑与 placement generation、Session 生命周期。 |
| [Allocate](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/scheduler/actions/allocate/allocate.go#L1054) | 节点分丢弃与外层域方案选优的断点。 |
| [NTA](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go) | 现有资源 cache、Allocate/Deallocate 全域扫描及评分。 |
| [GTA](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go) | 此基线的 PodGroup 反亲和合同。 |
| [member selector](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/util/hypernode/member_selector.go) | 公共 resolver 抽取的现有语义基础。 |
| [HyperNode controller](https://github.com/volcano-sh/volcano/blob/0854d3a4018c9304f8a25ded7a6f136c539f753c/pkg/controllers/hypernode/hypernode_handler.go) | nodeCount 的实际计数含义。 |

015 的主线分析固定于 [e0905bab6](https://github.com/volcano-sh/volcano/commit/e0905bab6fe49b5df2948edb001703c523525784)，原文保留其行号和版本。历史原型固定于 [c4c6c3643](prototype/README.md)，本归档另附补丁便于离线审视。

## 3. 社区问题与比较对象

| 链接 | 本地材料如何使用 |
| --- | --- |
| [Volcano #5205](https://github.com/volcano-sh/volcano/issues/5205)、[#5206](https://github.com/volcano-sh/volcano/pull/5206) | soft NTA 相关历史背景。 |
| [Volcano #5514](https://github.com/volcano-sh/volcano/issues/5514)、[#5539](https://github.com/volcano-sh/volcano/pull/5539) | Job/SubJob soft 锚点问题及相关修复；不等同于完整统一评分。 |
| [Volcano #5347](https://github.com/volcano-sh/volcano/issues/5347) | SubGroup 关系与联合搜索的需求背景。 |
| [Volcano PR #5349](https://github.com/volcano-sh/volcano/pull/5349) | 015 上游分析的独立比较对象；当时的状态和提交见原文，不声称其当前状态。 |

这些页面可能随时间更新，关键源码判断应优先使用原文的固定 SHA。归档日期没有把所有 PR 重新分析一遍，也没有对社区发起提交或评论。

## 4. 算法与系统一手资料

| 文献 | 原始出处 | 本方案借鉴什么，以及不作什么推论 |
| --- | --- | --- |
| William D. Harvey, Matthew L. Ginsberg. *Limited Discrepancy Search*. IJCAI 1995 | [论文](https://www.ijcai.org/Proceedings/95-1/Papers/080.pdf) | 首选路径优先、按偏离层探索的次序思想；不能据此保证 Volcano 的固定预算全局最优。 |
| Richard E. Korf. *Improved Limited Discrepancy Search*. AAAI 1996 | [论文](https://cdn.aaai.org/AAAI/1996/AAAI96-043.pdf) | 减少重复路径探索；收益依赖实际搜索树及剪枝。 |
| Rina Dechter, Judea Pearl. *Tree-Clustering Schemes for Constraint-Processing*. AAAI 1988 | [论文](https://cdn.aaai.org/AAAI/1988/AAAI88-027.pdf) | 利用约束依赖结构；物理网络是树，不表示工作负载约束图也是树。 |
| Rina Dechter, Irina Rish. *Mini-Buckets: A General Scheme for Bounded Inference*. JACM 50(2), 2003, pp. 107–153 | [作者版论文](https://ics.uci.edu/~dechter/publications/r62.pdf) | 有界推理及质量界的思路；具体资源聚合界仍需针对本模型证明。 |
| Kevin Wayne. *Algorithm Design: Intractability I* | [Princeton 教学资料](https://www.cs.princeton.edu/~wayne/kleinberg-tardos/pdf/08IntractabilityI.pdf) | 3-SAT／3-coloring 背景；受限放置归约在 015 文档中另行说明。 |
| Michael Isard et al. *Quincy: Fair Scheduling for Distributed Computing Clusters*. SOSP 2009 | [论文](https://www.sigops.org/s/conferences/sosp/2009/papers/isard-sosp09.pdf) | 用容量和费用表达调度目标；不自动覆盖任意 Volcano 多资源与组关系。 |
| Ionel Gog et al. *Firmament: Fast, Centralized Cluster Scheduling at Scale*. OSDI 2016, pp. 99–115 | [USENIX 出版页](https://www.usenix.org/conference/osdi16/technical-sessions/presentation/gog)、[论文](https://www.usenix.org/system/files/conference/osdi16/osdi16-gog.pdf)、[项目](https://www.cl.cam.ac.uk/research/srg/netos/camsas/firmament/) | 聚合图、增量费用流和求解工程；论文性能不能移植为本方案性能承诺。 |
| Kubernetes. *Scheduler Performance Tuning* | [官方文档](https://kubernetes.io/docs/concepts/scheduling-eviction/scheduler-perf-tuning/) | 可行节点采样与开销的权衡；HyperNode 大小不等、Gang 跨域，不能直接照搬单 Pod 比例。 |

Firmament 的 [coco_cost_model.cc](https://github.com/camsas/firmament/blob/master/src/scheduling/flow/coco_cost_model.cc) 是 025 对资源聚合、slot fit 与等价任务类的实现参考。这里保留原文移动分支链接并标明其性质；具体行号应结合原文调查日期，不作为不可变快照。

2026-11-05 归档时重新访问了 Quincy、Firmament 出版页、ILDS、Tree-Clustering、Mini-Buckets 和 Kubernetes 官方文档。IJCAI 的 LDS 链接因抓取重定向未能读取，Princeton 课件返回 403；保留原始引用，不写成此次已重验。未下载或转载第三方论文全文。
