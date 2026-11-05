> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Soft Candidate Forest 内存增量与决策完整性报告

## 1. 结论摘要

“scheduler 本身内存吃水线较高，而本次优化后 soft forest 的额外增量较小”这个理解基本
正确，但需要加一个边界：

1. 已优化的 **forest 构建、Job minResource 聚合和失败 diagnostics** 增量已经从最坏
   GiB 级降到单 Job 数 MiB 级；在三层 `8192/1024/128` roots 的同输入 UT 中，相对
   legacy 路径净增量为 `-0.13 / +3.12 / +2.26 MiB/op`（empty / sufficient /
   insufficient）。
2. 这些对象是每个 Job 决策过程中的临时对象。三层规模 UT 重复 1,000 次后，测试进程
   最大 RSS 仅从单次的 61.4MiB 墠到 72.3MiB，没有按 Job 数累积。
3. soft forest 扩大可行候选集合后，**所有成功 candidate Statement/worksheet 的保留**仍
   可能成为主要内存风险，空间上界约为 `O(C*T)`。该路径没有被本次修复裁剪，因为裁剪
   会改变评分、soft fallback 和最终选择结果；它属于 issue 017 的独立搜索治理问题。
4. 本次内存优化没有简化调度决策：没有限制 roots、没有采样候选、没有提前停止搜索、
   没有丢弃成功 solution。它只改变等价集合的构造方式和 diagnostics 表示。因此没有
   新增“原本可调度、优化后无法处理”的场景。

## 2. 口径与测试模型

本报告中的“soft forest 内存增量”定义为：

```text
soft candidate-forest 路径的内存 - 未引入 forest 的 legacy single-root 路径内存
```

不是“本次补丁相对旧 forest 实现节省了多少”。

测试环境：Apple M2 Pro、darwin/arm64、Go 1.26.5。生产规模 UT 模型：

```text
tier1: 8192 roots，每 root 1 Node
tier2: 1024 roots，每 root 聚合 8 Nodes
tier3:  128 roots，每 root 聚合 64 Nodes
唯一 Kubernetes Nodes: 8192
总 forest roots: 9344
```

`B/op` 是一次决策产生的累计 allocation traffic，不等于同等大小的常驻 RSS。RSS 还受
GC 时机、Go heap page 保留、scheduler cache、informer 和其他插件影响。

### 2.1 `MiB/op` 的准确含义

- `MiB` 是二进制内存单位，`1 MiB = 1,048,576 bytes`；
- `op` 是 Go benchmark 的一次被测操作。在本报告的三层规模 benchmark 中，一次 op
  等于对一个模拟 Job 的完整 `8192/1024/128` 三层 gradients 调用一次
  `FilterCandidateForestGradientsByMinResource`；不是一个 root、一个 Node、一个 Pod 或
  整个 scheduler session；
- Go `testing.B` 根据多次操作前后 runtime 累计分配字节的差值计算 `B/op`，再换算成
  `MiB/op`。因此它会统计同一次调用中先后创建、很快失去引用的短生命周期对象；
- `3.941 MiB/op` 表示一次上述 Job filter 调用累计向 Go heap 申请约 3.941MiB，并不表示
  调用结束后仍常驻 3.941MiB，更不表示 scheduler Pod RSS 立即增加 3.941MiB；
- 报告中的 `+3.118 MiB/op` 是 soft forest 与 legacy 对同一输入的差值：
  `3.941 - 0.823 = 3.118 MiB/op`。这是 soft forest 造成的额外 GC/allocation traffic；
- 如果每秒处理 J 个同规模 Job，`J × MiB/op` 可以近似理解为每秒新增的 heap allocation
  rate，而不能直接理解为 RSS 增长率。例如 10 Job/s × 3.118MiB/op 约为 31.18MiB/s
  额外分配流量；对象被 GC 回收后不会每秒永久增加 31.18MiB RSS；
- 只有多个操作的对象生命周期重叠、GC 来不及回收，或对象被 Statement/cache 等长期结构
  持有时，部分累计分配才会转化为更高 live heap/RSS。为此报告另用 maximum RSS 衡量
  重复 1/100/1000 次后的进程高水位。

这个 op 只覆盖 candidate-forest 的 Job minResource/filter helper，不包含 scheduler cache、
informer、整个 allocate action、SubJob Statement/worksheet 和其他插件；因此不能用
`MiB/op` 直接推导完整 scheduler Pod 的绝对内存。

### 2.2 规模 UT 不进入 CI 源码

报告中的 1k/8k benchmark、三层 `8192/1024/128` correctness/RSS 压力 UT 和 legacy 对照
都通过临时同包 `_test.go` harness 执行。数据采集后，临时源码与测试二进制均已删除。

最终 commit 不包含任何 1k/8k benchmark 或生产规模 UT；CI 中只保留 16-root diagnostics、
空资源和集合语义等轻量回归。因此普通 `go test` 不会重复构建 8k Nodes/9344 roots，也不会
承担本报告规模测试的 CPU/内存负担。后续需要复测时，应依据本报告模型临时生成 harness，
测试完成后删除，而不是把规模 fixture 提交到仓库。

## 3. soft forest 为什么被引入

legacy 路径把一个 gradient layer 中的每个 root 当作完整 Job 的独立尝试：

```text
eligible roots = [B, C]
try whole Job in B -> fail
try whole Job in C -> fail
```

当 B、C 单独资源不足、但 B+C 联合足够时，legacy 无法完成 gang transaction。

soft forest 路径将该 layer 解释为一个 exact union：

```text
allowed roots = [B, C]
envelope = LCA(B, C)，只用于 traversal
actual scope = descendants(B) ∪ descendants(C)
allocate all SubJobs in one Job transaction
```

envelope 下被 required PodGroup anti-affinity 排除的 sibling 会在 SubJob gradient 后再次被
exact-forest filter 删除，不会重新进入决策范围。

## 4. 优化前：soft forest 引入的内存增量

### 4.1 小规模与中等规模实测

旧 forest 相对 legacy minResource 路径：

| Roots / 唯一 Nodes | 旧 forest 总量 | Legacy 总量 | soft forest 净增量 |
| ---: | ---: | ---: | ---: |
| 2 / 4 | 1,072 B/op | 560 B/op | +512 B/op |
| 2 / 1,024 | 169,568 B/op | 33,200 B/op | +136,368 B/op |
| 8 / 1,024 | 285,728 B/op | 33,248 B/op | +252,480 B/op |
| 32 / 1,024 | 822,720 B/op | 33,440 B/op | +789,280 B/op |
| 8 / 8,192 | 约 2.50MiB/op | 约 0.25MiB/op | 约 +2.25MiB/op |

小 forest 的增量确实只有 KB/MB；问题出现在一个 Job 的 candidate layer 本身接近 8k
roots，而不是 session 中单纯存在很多 Job。

### 4.2 旧正常/资源满足路径：`O(NR)`

旧实现每合入一个 root 都执行 `nodeNames = nodeNames.Union(nodes)`。Kubernetes Set 的
`Union` 会 clone 已累计 map。一个 layer 有 R 个 roots、最终覆盖 N 个唯一 Nodes 时，
累计复制 entry 约为：

```text
N * (R + 1) / 2
```

三层 `8192/1024/128` 均覆盖 8192 Nodes 时，总复制约 38.3 million map entries。根据
1,024-root 实测外推，旧 soft forest 相对 legacy 增加约 **1.7–1.9GiB/Job 的累计
allocation traffic**。旧 map 会逐步失去引用，因而这不等于 1.9GiB 同时 live；但它会
显著提高 GC 频率和 heap/RSS 高水位。

### 4.3 旧资源不足路径：`O(R²)` live diagnostics

旧实现为每个失败 root 单独生成一份包含完整 forest root 列表的 reason，并将所有 reason
保存在 `ExcludedByReason` 中直到本次决策结束：

```text
R reasons × R root names × average-name-length
```

底层 R=8192 时，仅 live reason 字符串估算为：

| 平均 root 名长度 | 估算 live strings |
| ---: | ---: |
| 16 | 约 1.1GiB |
| 32 | 约 2.1GiB |
| 64 | 约 4.1GiB |

旧实现 1,024 roots 已实测 normal 约 24.3MiB/op、insufficient 约 73.4MiB/op。按复杂度
外推，三层 8k 失败场景累计 allocation 可达约 4.7GiB，且其中 1–4GiB 字符串可能在函数
返回前同时 live。这是 10GB scheduler limit 下最直接的 OOM 风险。

这些 8k 旧实现数字是基于 1,024-root 实测和明确复杂度外推；为避免本地环境本身 OOM，
没有直接运行旧 8k failure benchmark。

## 5. 最坏触发场景

必须同时满足以下条件才接近上述最坏值：

1. Job 存在 required PodGroup anti-affinity；
2. Job 不是 real hard Job topology，因此进入 candidate-forest 路径；
3. anti-affinity 过滤后，一个 gradient layer 仍保留接近 8k 个同层 roots；
4. Job 配置了非空 minResource，触发 forest-wide Node union；
5. 若 forest 总资源仍不足，则触发旧 `O(R²)` failure reasons；
6. root 名越长，失败路径 live strings 越大；
7. GC 来不及回收或多个高分配 Job 紧密连续执行时，RSS 高水位会进一步提高。

“最低层有 8k HyperNode”本身不是充分条件。如果绝大部分 Job 经过过滤后只有 `{B,C}`，
R=2，soft forest 增量仍是 KB/MB 级。

另一个独立最坏场景是：大量 roots 对某个 SubJob 都可行，且每个 candidate 都产生很多
Task operations。此时成功 Statement/worksheet 会全部保留到评分完成，约为 `O(C*T)` live
state；即使 minResource helper 已线性化，该项仍可能很大。

## 6. 优化方案

提交 `dc4fffbe3e9a234837378e91d68099da69f70d99` 做了四项修改：

### 6.1 空 minResource fast path

仍对每层 roots 做 nil 过滤、去重和稳定排序，并保留 tier stats；但空资源不读取
`RealNodesSet`/`Nodes`，因为零资源要求对任何 forest 都成立。

### 6.2 单个 mutable set 聚合 Nodes

用同一个 set 原地 `Insert` Node name，保持 exact set-union 和 overlap 去重语义：

```text
旧：O(NR) allocation
新：O(N + R) allocation
```

### 6.3 每层共享一个 bounded failure reason

reason 改为：

```text
minResource (...) in candidateForest (roots=N)
```

每个 root 仍保留 exclusion map key，但所有 value 共享同一字符串。空间从 `O(R²*L)` 降为
`O(R*L)` map keys 加一个 bounded value。

### 6.4 日志有界

forest 日志保留 root count，只展示最多 8 个稳定 sample。该修改只影响可观测性，不参与
调度选择。

## 7. 优化后：soft forest 引入的内存增量

### 7.1 与 legacy 同输入对照

当前实现使用同一三层 8k fixture 对比：

| 场景 | Legacy | 当前 soft forest | soft forest 净增量 |
| --- | ---: | ---: | ---: |
| empty | 0.823MiB/op | 0.693MiB/op | **-0.130MiB/op** |
| sufficient | 0.823MiB/op | 3.941MiB/op | **+3.118MiB/op** |
| insufficient | 2.927MiB/op | 5.191MiB/op | **+2.264MiB/op** |

说明：

- empty 下当前 forest 反而更省，因为新增 fast path 避免了 legacy 对每个 root 的资源遍历；
- sufficient 对照使用每 root 都可满足的 1m CPU，因此两条路径都保留全部 roots；
- insufficient 下两条路径都排除全部 roots，当前 forest 的净增量约 2.26MiB；
- 对“单 root 不足但多个 roots 联合满足”的真实引入场景，legacy 会错误拒绝，无法做完全
  等价的结果对照；当前 forest 总消耗仍约 3.94MiB/op。

因此，就本次已优化的 minResource/forest 构建部分而言，soft forest 在 8k 三层模型上的
新增吃水约为 **0–3.2MiB/Job**，而不是 GiB。

### 7.2 连续 Job 的 RSS

同一三层规模 UT、`GOGC=100`：

| 顺序重复次数 | 最大 RSS |
| ---: | ---: |
| 1 | 61.4MiB |
| 100 | 71.0MiB |
| 1,000 | 72.3MiB |

1,000 轮仅 filter 操作就产生至少约 9.6GiB 累计 allocation traffic，另有 fixture 重建，
但最大 RSS 相比单轮只增加约 10.8MiB。这支持以下结论：该 helper 的临时对象在 Job 完成
后可由 GC 回收，不会因 session 内顺序存在大量 Job 而线性驻留。

需要注意：Go runtime 可能保留已经申请的 heap pages，因此某次极高峰过后 RSS 不保证
立即回落；修复的关键是单 Job 已不再制造 GiB 级峰值。

### 7.3 其他 soft forest 增量组件

已单独测得：

| 组件 | 规模 | 约内存 |
| --- | ---: | ---: |
| exact-forest ancestor filter | 8,192 candidates | 459KB/SubJob |
| HyperNode candidate universe | 8,192 HyperNodes | 1.30MB/构建 |
| placement snapshot | 1,024 SubJobs | 160KB/Job |

这些组件是线性的，但可能按 SubJob 次数重复产生 allocation traffic。8k candidate ancestor
filter 的 synthetic CPU 约 533ms，CPU 放大比内存更明显，尚未在本次修复中处理。

小规模 full-action UT 中，2-Pod forest 约 1.04MB/op，强制 legacy 约 1.05MB/op；4-Pod
B+C forest 约 1.15MB/op，说明常见小 forest 下整体 action 内存主要由既有 scheduler
transaction/worksheet 结构占据，soft forest 固定增量很小。

## 8. 是否因优化简化决策而产生无法处理的场景

### 8.1 本次内存优化：没有新增决策缺口

| 修改 | 是否改变候选/可行性/评分 |
| --- | --- |
| empty fast path | 否；零资源要求恒成立，roots 仍规范化 |
| `Union` 改原地 `Insert` | 否；仍按 Node name 做 exact set union 和 overlap 去重 |
| failure reason 共享 | 否；只改变 diagnostics value，root exclusion key 仍存在 |
| 日志只取 8 个 sample | 否；只影响日志展示 |

本次修复明确没有：

- 对 root 数设上限；
- 随机或固定采样 candidates；
- 只保留前 K 个成功 Statement；
- 提前停止候选 dry-run；
- 改变 score、soft fallback、LCA/envelope 或 actual placement；
- 将 native-soft constraint 错误转换为 hard filter。

所以不存在因本次内存优化而新增的“原本可调度、现在无法调度”场景。

### 8.2 当前算法本来就存在、但不是本次优化引入的边界

1. **没有跨 SubJob 的全局回溯。** 每个 SubJob 会 dry-run 所有当前可行 candidates，选择
   评分最佳项后再处理下一个 SubJob。理论上存在：SubJob-A 可选 B/C 且 B 分高，SubJob-B
   只能选 B；贪心先把 A 放到 B 后，B 无法容纳 B，而 `A->C, B->B` 实际可行。当前实现
   不会回溯 A。这是既有搜索策略，不是本次内存线性化造成的。
2. **成功 candidate 全量保留。** 一个 SubJob 的 C 个候选都成功、每个有 T 个 task
   operations 时，Statement/worksheet live state 约为 `O(C*T)`。限制它需要定义 top-K、
   branch-and-bound 或全局搜索 contract，否则可能改变最终最优解或 soft fallback。
3. **无合法 envelope。** 若 roots 来自损坏或不连通的 HyperNode cache，找不到 LCA 时该
   layer 会跳过。合法显式树应总能解析到共同上层；这也是原 forest 行为。
4. **real hard Job topology 不进入 forest。** 这是 activation predicate 的设计边界，hard
   Job 保持 legacy single-root 语义；不是本次优化新增的限制。
5. **不完整 RealNodesSet/Nodes。** 缺失 Node 延续既有忽略语义。它可能让 diagnostics 或
   资源判断依赖 cache 完整性，但本次实现没有改变该 contract。

## 9. 10GB limit 下的 OOM 判断

仅针对已优化的 soft forest helper：

- 三层 8k 单 Job 总量约 0.69–5.19MiB/op；
- 相对 legacy 净增量约 -0.13–+3.12MiB/op；
- 1,000 次顺序执行最大 RSS 约 72.3MiB；
- 因此它本身不再是 10GB limit 下的主要 OOM 来源。

生产 OOM 风险仍取决于：

```text
scheduler 固有 cache/informer 基线
+ 同时活跃 Job/Task 对象
+ C*T candidate Statement/worksheet live state
+ 日志格式化与其他插件临时对象
+ Go heap 高水位和容器非 Go 内存
```

建议生产重点监控每个 Job 的 forest root count、可行 candidate 数 C、task operations T、
`go_memstats_heap_alloc_bytes`、heap in-use、GC frequency/pause、process RSS 和 container
working set。10GiB limit 场景可把 `GOMEMLIMIT` 设置在约 7.5–8GiB，为 goroutine stack、
runtime metadata、mmap、日志和非 Go 内存留出余量；它只能促使更早 GC，不能解决真正的
`O(C*T)` live set。

## 10. 最终判断

soft forest 的功能收益是让 legacy 无法处理的 B+C 联合 gang allocation 成为可调度。
优化前，其 forest-wide minResource 和 diagnostics 实现会在 8k roots 下把这个功能增量
放大到 GiB；优化后，同一部分的净增量已经降到约 0–3.2MiB/Job，且顺序 Job 不累积。

本次优化没有通过简化决策换内存，因此没有牺牲可调度场景。仍需单独治理的是扩大候选集
之后的 `O(C*T)` solution retention 和跨 SubJob 缺少全局回溯；这两项才是后续可能同时
影响内存上界与决策完整性的核心问题。
