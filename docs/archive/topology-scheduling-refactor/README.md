# Volcano 拓扑调度重构归档

归档日期：2026-11-05（Asia/Shanghai）。用途：个人归档、回看设计依据和审视取舍。

**最终方向是：共享库定义拓扑事实，controller 发布偏静态索引，scheduler 维护动态资源与占用，在精确硬约束内让全部软偏好共同参与整组放置。** HyperNode 的树结构用于查询、剪枝、聚合和解释；后续 SubGroup 可以促使规划器重新考虑前序选择。

这里的“最终版”指本次找到的最后一轮设计及补充要求：2026-10-23 的 **025 共享树索引方案**，包含后补充的 **SubGroup required/preferred affinity/anti-affinity**。它继承 015 的统一评分、完整方案评价和有界联合搜索。最终设计尚未实施；7 月的 015 原型及各局部修复是历史证据。

## 阅读顺序

| 顺序 | 内容 | 回答的问题 |
| --- | --- | --- |
| 1 | [前因后果与决策演变](HISTORY_CN.md) | 从哪些问题走到重构，为什么局部修复和第一版原型还不够？ |
| 2 | [最终方案整合稿](FINAL_DESIGN_CN.md) | 最后决定如何分层、如何评分与回退、缓存和性能边界是什么？ |
| 2a | [补充设计：可解释性合同](EXPLAINABILITY_SUPPLEMENT_CN.md) | 整合稿 §7、§10 承诺的“解释各来源贡献”“记录截断与覆盖”，需要什么数据合同、对账不变量与验收？ |
| 3 | [025 完整架构与算法原文](sources/issues/features/025-hypernode-tree-index-planning-OPEN/HYPERNODE_TREE_INDEX_DESIGN_CN.md) | 树视图、CR 发布、状态一致性、SubGroup 关系与算法细节。 |
| 4 | [015 上游重构设计](sources/issues/features/015-unified-topology-planning-framework-IP/VOLCANO_UPSTREAM_TOPOLOGY_REFACTOR_DESIGN_CN.md) | 共同评分 cohort、完整 evaluator、插件生命周期和上游兼容边界。 |
| 5 | [015 完整模型与复杂度](sources/issues/features/015-unified-topology-planning-framework-IP/TOPOLOGY_PLANNING_FRAMEWORK.md) | 目标函数、约束传播、安全界、搜索及成本对账。 |
| 6 | [验证状态与实施边界](EVIDENCE_CN.md) | 哪些是历史实测，哪些只是模型，哪些尚未运行？ |
| 7 | [参考资料](REFERENCES_CN.md) | 源码、社区讨论、论文各自支持什么结论？ |
| 8 | [原始材料索引](SOURCE_INDEX.md) | 按 issue 找到需求、提案、历史 YAML、脚本及模型结果。 |

需要逐行理解旧实现打分时，阅读 [015 上游评分代码导读](sources/issues/features/015-unified-topology-planning-framework-IP/VOLCANO_UPSTREAM_SCORING_CODE_WALKTHROUGH_CN.md)。原始设计全文、评审和修订材料均留在 `sources/`，不要求读者先按时间翻完它们。

## 版本与解释优先级

| 对象 | 固定版本 | 如何理解 |
| --- | --- | --- |
| 本归档分支的源码底座 | `dev-1.14@7947e875600ff450318b412bb0c8c39f7020238a` | 方便在现有个人 fork 保存；本次新增内容仅在本归档目录。 |
| 最终设计的上游审计基线 | `master@0854d3a4018c9304f8a25ded7a6f136c539f753c` | 025 的源码判断以此为准；不是归档分支底座，也不代表 11 月最新主线。 |
| 015 的上游审计基线 | `master@e0905bab6fe49b5df2948edb001703c523525784` | 10 月 5 日材料中的“当前上游”；已变化事实由 025 明确纠正。 |
| 015 历史原型 | `c4c6c3643efed8a17f1ef31e8991d9ce1e25f891` | 已有实现与历史验证，但不等于最后目标方案。 |
| 9 月统一模型的生产基线 | `dev-1.14@7947e875600ff450318b412bb0c8c39f7020238a` | 用于问题、模型和现有源码成本分析。 |

有冲突时，按“025 最后补充的目标与事实 → 015 上游方案中仍适用的详细合同 → 9 月完整模型 → 更早原型和局部问题记录”理解。025 更新了数据层、基线事实及实施顺序；没有放弃 015 对共同评分和完整布局评价的要求。本归档的整合稿解释这种关系，不新增另一套运行时方案。

尤其要区分：10 月 5 日的上游材料把 GTA 视为未来能力；10 月 23 日基线已经有 PodGroup anti-affinity、typed gradient、内部 HyperNodeIndex 和 generation。未来 SubGroup 四类关系进入核心设计与验收，不等于当时主线已经提供对应 API。

## 归档范围和保真方式

- 收录本地拓扑相关 issue 001–025 的文档、测试配置、复现 YAML、历史辅助脚本及模型结果，共 218 个源文件；026 的 Pending Pod 更新调查不属于本次重构材料。
- 每份原始 Markdown 顶部有归档提示；本机 Markdown 链接改成相对链接，源码链接固定到对应归档版本。原有命令、日期、试验数据和未完成标记作为历史记录保留。
- [文件清单与 SHA-256](SOURCE_MANIFEST.json) 记录原始路径、原文件摘要、归档副本摘要以及源码链接迁移记录。原工作区资料未被改写。
- 另保存 [015 原型补丁](prototype/015-unified-topology-placement.patch) 和[基线记录](prototype/README.md)，可在对应历史基线上复查原型；它不作为最终实现合入本分支。
- 外部文献以书目信息、原始链接和适用边界归档，不复制第三方全文。历史记录里的 `/tmp` 日志、已删除集群、镜像和本机目录不代表它们随仓库一起保存。
- 旧材料里的 Python 命令仅为历史记录。重新运行模型时应使用当前所选项目根目录的 `.venv` / `uv` 工作流。

本次是文档和证据归档，没有修改调度器、API、CRD 或集群；没有把任何未完成的重构 issue 标成 DONE。2026-11-08 增补了 [可解释性补充设计](EXPLAINABILITY_SUPPLEMENT_CN.md)，同样只含文档，不新增运行时方案。远端目标是个人 fork `VanderChen/volcano` 的 `archive/topology-scheduling-refactor-20261105` 分支，没有创建社区 PR。
