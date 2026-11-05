> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 025：HyperNode 共享树索引与拓扑调度规划

日期：2026-10-23。状态：OPEN，分析与设计，尚未实施。

## 用户需求

1. 用户可直观看到与 Volcano 相同语义的 HyperNode 拓扑。先提供 API 库中的统一解析方法，再由 controller 生成偏静态 HyperNode tree index CR，最后 scheduler 消费树并优化调度。
2. 丰富 scheduler 的 HyperNode 动态资源摘要；缓存并继承 SubGroup 的调度结果；引入 HyperNode 级候选选优比例，控制规模扩大后的开销。
3. 重新设计逐层回退选优，研究树算法、网络流与亲和/反亲和的结合，并继承既有 015 重构设计。
4. 用户补充：SubGroup 亲和/反亲和未来会合入 master，因此按目标能力纳入本次架构、缓存、算法及核心验收，不作为可选的远期扩展。

## 基线与范围

- 上游默认分支实际是 `master`；按用户“main”的主线含义，使用 `volcano-sh/volcano@0854d3a4018c9304f8a25ded7a6f136c539f753c`，提交日期 2026-10-23。
- 独立分支：`feat/025-hypernode-tree-index-planning`；只读分析 worktree：`/Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025`。
- 原 `volcano/` 保持 `dev-1.14`。不修改、合并或提交其实现。
- 继承 015 最新的 `VOLCANO_UPSTREAM_TOPOLOGY_REFACTOR_DESIGN_CN.md`，但其 2026-10-05 主线事实必须重新核实；不以旧原型或历史 Kind 结果证明本方案。
- 本任务只完成设计和待审批验证计划；不调用集群、不修改现有 API、CRD 或调度实现。
- 当前源码事实仍固定于上述提交；面向未来 master 的设计同时覆盖 SubGroup required/preferred affinity/anti-affinity。最终 API 字段与 term 匹配语义由适配层对齐，不能将未来能力描述为当前已经存在。

## 交付入口

- [架构与算法设计](HYPERNODE_TREE_INDEX_DESIGN_CN.md)
- [提案状态、实施阶段和验证用例](PROPOSAL_COMMIT.md)

实施需批准设计和验证用例。阶段 1/2 的展示与物化索引可独立落地；阶段 3 的新调度语义单独启用和验收。
