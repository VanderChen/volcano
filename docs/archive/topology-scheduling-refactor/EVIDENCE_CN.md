# 验证状态、证据范围与未完成工作

## 1. 不能混为一谈的四类证据

| 证据 | 已有材料 | 可以证明什么 | 不能证明什么 |
| --- | --- | --- | --- |
| 历史局部修复 | 各 issue 的 DESCRIPTION、PROPOSAL、YAML 和脚本 | 对应基线与用例中的行为及修复边界 | 最终统一架构已实现。 |
| 015 历史原型 | `c4c6c3643`、历史测试／benchmark／Kind 记录 | 早期节点级有界 planner 的实现经验 | 025 或后期 015 的完整评分、质量搜索和共享索引性能。 |
| 9 月独立模型 | Python 模型与记录的 JSON | 有限模型的 oracle、边界、预算和成本恒等式 | Volcano 插件、Reserve、缓存、Bind 或真实吞吐的正确性。 |
| 025 最终设计 | 设计原文、U/B/K 验证计划 | 范围、合同和待验收条件已写明 | 测试已经存在、已经运行或已经通过。 |

## 2. 015 原型的历史记录应怎样读

[VERIFICATION_RESULTS.md](sources/issues/features/015-unified-topology-planning-framework-IP/artifacts/VERIFICATION_RESULTS.md) 记录了 2026-07-30 的聚焦 Go 包、race、构建、镜像及 Kind K0–K5。它同时记录了 `make unit-test` 被 Darwin 上 Linux-only 包阻断、`make verify` 的 generator 环境失败，以及 agent 镜像拉取失败。不能将它摘要成“全部仓库 gate 全绿”。

其中 first-branch benchmark 的约 3.77 倍是 Apple M2 Pro、darwin/arm64、特定 1000 Pod／100 SubGroup synthetic workload 的历史结果，不是通用加速比。镜像在 dirty source 上构建、内部版本字符串仍显示基线等 provenance 也由原文说明。此次没有重建这些镜像或重跑 benchmark。

[原型补丁](prototype/015-unified-topology-placement.patch) 保留实现差异供阅读；[基线说明](prototype/README.md) 记录精确父提交。应用原型不会得到本归档描述的最终架构。

## 3. 独立模型的保存范围

| 模型 | 结果 | 原始用途 |
| --- | --- | --- |
| [unified_model.py](sources/issues/features/015-unified-topology-planning-framework-IP/design-validation/unified_model.py) | [unified-results.json](sources/issues/features/015-unified-topology-planning-framework-IP/design-validation/unified-results.json) | 统一搜索与穷举、小实例顺序、预算、聚合界。 |
| [bug020_model.py](sources/issues/features/015-unified-topology-planning-framework-IP/design-validation/bug020_model.py) | [bug020-results.json](sources/issues/features/015-unified-topology-planning-framework-IP/design-validation/bug020-results.json) | 计数目标与联合放置案例。 |
| [performance_cost_model.py](sources/issues/features/015-unified-topology-planning-framework-IP/design-validation/performance_cost_model.py) | [performance-cost-results.json](sources/issues/features/015-unified-topology-planning-framework-IP/design-validation/performance-cost-results.json) | 操作数、费用恒等式、按需求值和栈所有权模型。 |
| [早期 review 模型](sources/issues/features/015-unified-topology-planning-framework-IP/review-2026-09-11/model_checks.py) | [model-results.json](sources/issues/features/015-unified-topology-planning-framework-IP/review-2026-09-11/model-results.json) | 对应早期评审假设；由后续完整模型补充。 |

本次只保存源码和既有结果，没有重新执行模型；再次运行须按当前项目 Python 隔离规则使用项目 `.venv`。上述脚本不被复制进调度器运行路径。

## 4. 最终设计的待实施验证

025 的 [PROPOSAL_COMMIT.md](sources/issues/features/025-hypernode-tree-index-planning-OPEN/PROPOSAL_COMMIT.md) 是完整待审批计划，含 setup、拟议测试入口、命令、期望和通过标准。里面明确指出相关新增 fixture／测试尚未实现；`no tests to run` 不能算通过。

| 分组 | 核心内容 | 归档时状态 |
| --- | --- | --- |
| U1–U5 | 公共解析、异常／重叠、详情一致性、controller 发布、分片与版本 | NOT RUN |
| U6–U7 | 动态资源与拓扑成对切换、Undo、聚合与真实可行性边界 | NOT RUN |
| U8–U11 | 缓存失效、跨组回退、共同评分与完整质量比较 | NOT RUN |
| U12–U15 | PodGroup／SubGroup 关系、四域四组／五组、采样、插件恢复 | NOT RUN |
| U16 | 受限 flow／matching／DP 与 oracle 及不适用时拒绝接管 | NOT RUN |
| U17–U19 | 跨 tier 关系、peer 主体计数、未决关系、peer 改变与缓存 | NOT RUN |
| B 组 | 索引／membership 规模、增量资源、缓存、搜索、关系密度与尾部成本 | NOT RUN |
| K1–K12 | 真实 Kind 下的展示、发布、调度、评分、关系、回退与扩样 | NOT RUN |
| 项目门禁 | API codegen（如涉及类型）、生成、Go、镜像及 Kind | NOT RUN |

Kind 计划继续遵守最多 8 个可见 Kubernetes 节点、task-specific 集群、本地镜像先构建／加载、验证后清理。不能以小模型或旧 020/022/024 的 Kind 结果替换新架构的必需验收。

## 5. 本次归档核验

本次检查范围是文档、文件与 Git 状态：源文件摘要、归档副本摘要、链接可达的本地文件、固定源码引用的对象及行号范围、Markdown 围栏、补丁与历史提交的一致性、差异范围及远端分支 SHA。具体结果见 [ARCHIVE_CHECKS.md](ARCHIVE_CHECKS.md)。

本次没有调度器代码变更，不运行 Go、构建镜像或 Kind，不声称新增实现验证。原工作树与 issue 状态不变。归档成功仅意味着资料已经保存并推送。
