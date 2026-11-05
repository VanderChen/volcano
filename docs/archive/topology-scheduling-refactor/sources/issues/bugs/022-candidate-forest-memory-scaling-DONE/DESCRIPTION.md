> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 022：Candidate Forest 内存放大修复

## 状态

- 类型：Bug / Scheduler / Allocate
- 阶段：实现、门禁与 Kind 验证完成
- 状态：DONE
- 创建日期：2026-08-08
- 生产基线分支：`dev-1.14`
- 修复分支：`fix/022-candidate-forest-memory-scaling`
- 源码基线：`71f1d3248 fix(scheduler): preserve preferred subgroup traversal order`

## 问题

`fix(allocate): schedule anti-affinity jobs across candidate forest` 允许 required
PodGroup anti-affinity Job 在同一 candidate layer 的多个合法 HyperNode root 上联合调度。
联合路径保持了 exact forest 语义，但 Job 级最小资源检查存在两个与 forest root 数相关的
超线性内存放大点：

1. `candidateForestSatisfiesMinResource` 对每个 root 调用 `Set.Union`，而 `Union` 每次都会
   clone 已累计的集合。对于 R 个同层 root、共覆盖 N 个唯一 Kubernetes Node，累计
   map-entry 拷贝约为 `N * (R + 1) / 2`，复杂度为 `O(NR)`。
2. forest 资源不足时，对每个 root 都重新格式化一次完整 root-name 列表。R 个 reason
   各自包含 R 个名称，累计分配和返回前的存活字符串都为 `O(R²)`。

此外，未配置 `PodGroup.spec.minResources` 时，`JobInfo.GetMinResources()` 返回非 nil 的空
Resource；当前 forest filter 只判断 nil，因此仍执行全部 node-set union 和资源聚合。

生产计划使用三层 HyperNode tree，最低层约 8k HyperNode。集群规模本身不必然触发最坏
路径，但当一个 Job 的 anti-affinity candidate layer 接近 8k roots 时，当前实现会从小
forest 的 KB/MB 级临时分配放大到 GB 级 allocation traffic；资源不足时还可能形成 GB 级
同时存活的诊断字符串，威胁约 10GB limit 的 scheduler Pod。

## 已完成的基线分析

在 Apple M2 Pro、darwin/arm64、Go 1.24 环境使用临时 benchmark 测得：

| Roots / 唯一 Nodes | Forest 正常路径 | Legacy 逐 root 路径 |
| ---: | ---: | ---: |
| 2 / 4 | 1,072 B/op | 560 B/op |
| 2 / 1,024 | 169,568 B/op | 33,200 B/op |
| 8 / 1,024 | 285,728 B/op | 33,248 B/op |
| 32 / 1,024 | 822,720 B/op | 33,440 B/op |
| 8 / 8,192 | 约 2.50 MB/op | 262,624 B/op |

大 root 失败路径：

| Roots / Nodes | 正常 | 资源不足 |
| ---: | ---: | ---: |
| 128 / 128 | 未单独记录 | 约 1.22 MB/op |
| 256 / 256 | 未单独记录 | 约 4.68 MB/op |
| 1,024 / 1,024 | 约 24.3 MB/op | 约 73.4 MB/op |

8k roots 的 GB 数值来自上述实测与明确复杂度的外推，没有在受限本地环境直接执行 8k
旧实现失败 benchmark。临时 benchmark 文件已经删除，基线工作树保持干净。

## 期望行为

1. candidate forest 的筛选和调度结果不变：同层 roots 去重、稳定排序，重叠 Node 不重复
   计数，不能通过 LCA envelope 引入被 anti-affinity 排除的分支。
2. 空 `minResource` 不遍历 `RealNodesSet` 或 `Nodes`，只保留必要的 root 规范化和统计。
3. 非空 `minResource` 对 forest 覆盖的唯一 Node 只构建一次集合，内存随 `N + R` 线性
   增长，不随 `N * R` 增长。
4. 资源不足 diagnostics 保持可定位的 minResource 和 forest 大小信息，但不重复包含完整
   root 列表；内存随 R 线性增长。
5. candidate-forest activation、SubJob candidate filter、soft fallback、actual placement、
   recorder 和普通/Job-hard 路径语义保持不变。

## 范围

预计修改：

- `pkg/scheduler/actions/allocate/allocate.go`
- `pkg/scheduler/actions/allocate/podgroup_antiaffinity_forest_test.go`
- 可新增同包 focused benchmark/test 文件。

不修改：

- framework/plugin callback contract；
- API、CRD、controller、webhook；
- recorder 数据结构；
- 全局回溯、组合搜索或 candidate Statement 有界保留策略；
- 普通 Job 与真实 Job hard network-topology 分配语义。

8k candidates 下保存所有成功 Statement/worksheet 的通用空间上界属于
`017-bounded-joint-placement-search` 的更大搜索治理问题。本任务只消除 candidate forest
构建和 Job minResource diagnostics 中已经确认的超线性放大，不在小修复中改变候选选择
语义。

## 完成条件

- [x] Proposal 和验证用例获得用户明确批准；
- [x] 空资源路径不再遍历/合并 forest Nodes；
- [x] 非空联合资源检查由 `O(NR)` 降为 `O(N + R)`；
- [x] 失败 diagnostics 由 `O(R²)` 降为 `O(R)`；
- [x] 8,192-root synthetic benchmark 满足约定内存门槛；
- [x] focused/unit/verify 门禁通过；
- [x] 本地镜像构建并完成约定 Kind 语义验证；
- [x] 命令、结果、镜像、日志、清理和最终 commit 记录到 `PROPOSAL_COMMIT.md`；
- [x] 全部验证完成后目录改为 `DONE`。
