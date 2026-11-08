# 补充设计：拓扑调度的可解释性合同

日期：2026-11-08。性质：对 [最终方案整合稿](FINAL_DESIGN_CN.md) 的**补充与修正**，仍是设计归档，不是实现说明。不改变“树提供事实，统一规划决定放置”的方向，只补上整合稿 §7、§10 承诺了但没有给出数据合同的部分。

代码判断基于归档底座 `dev-1.14@7947e875600ff450318b412bb0c8c39f7020238a`（`pkg/scheduler/...`）。其中的类型、字段、指标和文案均为**拟议**，实施时冻结。

## 1. 与最终设计的关系

| 整合稿位置 | 类型 | 补充内容 |
| --- | --- | --- |
| §1 比较先遵守服务等级，再比较软收益 | 补充 | 现有实现还有一层“tier 层优先于分数”，整合稿没有写明。见 §3。 |
| §3 declared / resolved / adopted | 补充 | 每次决策记录采用的拓扑 revision 与规则版本，才能解释展示与实际调度的暂时不一致。见 §4。 |
| §6 虚拟根不能充当真实 hard tier 证据 | 修正落实 | 树内候选森林路径把根集合的 LCA（envelope）当作父域传下去，日志与 Recorder 里的 “hyperNode” 因此不是真实硬约束域。解释层必须报告 roots。见 §8。 |
| §7 结果应能解释各来源贡献 | 补充 | 给出评分来源合同。见 §6。 |
| §8 Apply/Undo | 补充 | journal 同时作为搜索轨迹。见 §12。 |
| §10 稳定轮转 | 修正 | 轮转与 tie-break 必须是确定函数，不能依赖 `rand` 或 map 遍历序。见 §7。 |
| §10 记录截断原因与覆盖范围 | 补充 | 给出 `Coverage` 结构。见 §11。 |
| §12 验收 | 补充 | 增加对账、确定性、worked example 三类验收。见 §13。 |

## 2. 现状缺口

下表是对树内代码的核对结果，行号对应 `7947e875`。

| # | 缺口 | 证据 |
| --- | --- | --- |
| G1 | 并列时随机选择；Job 级还依赖 map 遍历顺序，同一输入可能得到不同结果。 | `util/scheduler_helper.go:204`、`:222` 使用 `rand.Intn`；`actions/allocate/allocate.go:838` 在 map 上用严格 `>` 取最大。 |
| G2 | 评分明细被丢弃。各插件分数求和后只保留总分，明细仅在 V(5) 日志。GTA 的 preferred 评分从 `FullScore` 起逐项扣减并在 0 处截断，截断后的额外项无法看出。NTA 仅在最高分并列时才叠加 taskNum 分，是隐藏的第二阶段规则。 | `scheduler_helper.go:147-152`；`group-topology-affinity/group_topology_affinity.go:663`、`:768-770`；`network-topology-aware/network_topology_aware.go:351-374`、`:743`。 |
| G3 | FitError 摘要不对账。重叠排除被分别计数，数字不可相加；候选森林过滤改写了通过数，却没有对应的排除标签。 | `api/unschedule_info.go:164`（`FormatHyperNodeFitSummary`）、`:107`（标签表）；`allocate.go:624` 覆盖 `IntersectedByTier`；`allocate.go:616` 只把候选森林原因写进 `ExcludedByReason`，不进入计数。 |
| G4 | 遍历顺序隐式取决于插件配置顺序：第一个适用插件的顺序生效，用户看不到。 | `framework/session_plugins.go:1068`、`:1104`、`:1177`。 |
| G5 | 候选森林用 LCA envelope 作伪根；`minResource` 在整层并集上检查，只是必要条件，失败时由整层共同承担。 | `allocate.go:560`（`candidateForestEnvelope`）、`:1224`（`FilterCandidateForestGradientsByMinResource`）。 |
| G6 | “第一个有可行解的 tier 层即返回，层内才比分”是词典序，但没有对外说明；用户无法知道其他层为何没有被考虑。 | `allocate.go:346-510`（`allocateForJob`）。 |
| G7 | Recorder 只保存选中的 hyperNode ID，没有理由与备选。 | `actions/allocate/recorder.go`。 |

G3 的两个例子来自一次临时探测（构造 `HyperNodeGradientStats` 后调用 `FormatHyperNodeFitSummary`，探测文件已删除，未提交）：

```text
A  总数 10；GTA 保留 6；NTA 保留 7；交集保留 4
   → 4/10 hyperNodes available: tier-2 4/10 (4 podGroupAntiAffinity, 3 networkTopology)
B  总数 10；GTA 保留 8；候选森林过滤后保留 3
   → 3/10 hyperNodes available: tier-2 3/10 (2 podGroupAntiAffinity)
```

A 中 4 + 3 + 4 ≠ 10，两项排除有重叠；B 中缺失的 5 个没有任何原因。G1–G7 共同的后果是：同一次调度的结果、排除原因、得分来源都无法被用户复述。

## 3. 设计原则

| 编号 | 原则 |
| --- | --- |
| E1 同源 | 解释记录由决策路径在决策点写入，不事后重算，避免解释与决策不一致。 |
| E2 对账 | 每层候选数 = 入选 + 各原因独占排除 + 未评估；任何无标签的差额都视为缺陷。 |
| E3 确定 | 相同输入得到相同选择与相同解释，包括 tie-break 和输出排序。 |
| E4 有界 | 解释的内存、日志、事件大小都有上限，截断可见。与 `7947e8756` 的内存治理一致。 |
| E5 分层 | 一句话结论、可复述的摘要、可审计的明细分开输出，默认开销最小。 |
| E6 语义守恒 | 解释特性不改变选择结果。唯一例外是 G1：把随机选择收敛为确定选择，仍落在原来允许的结果集合内。 |

### 比较序契约

整合稿 §1 只说“先服务等级，再软收益”。结合现有实现，比较序应写成明确的向量，并作为解释输出的一部分：

```text
(服务等级 Ready/Pipelined 与分片等级, tier 层, 加权评分之和, tie-break 键)
```

- tier 层：`allocateForJob` 自低 tier 向高 tier 逐层尝试，某层存在可行解就返回，不再评估更高层。这里“更高层”被跳过不等于“更差”，解释里应写成“未评估”，不能写成“被打败”。
- 当前把 tier 层当作与评分无关的词典序。是否应改成可配置的收益项，是未决事项，不在本补充中决定（见 §14）。
- 在该决定之前，文档与解释输出一律按词典序表述。

## 4. 决策记录：`PlacementExplain`

数据结构借鉴 kube-scheduler 的 `Diagnosis`（按插件归因的拒绝原因）和 KEP-5732 中 `Placement.Name`（“用于诊断与调试”的候选名）。以下为拟议形状。

```go
type PlacementExplain struct {
    Scope    string            // job 或 subJob 标识
    Revision string            // 采用的拓扑 revision 与规则版本
    Order    []string          // 比较序各分量名，如 serviceLevel, tierLayer, score, tieBreak
    Stages   []StageSummary    // 固定阶段顺序，见下
    Top      []CandidateRecord // 胜者与前 K 名，K 有上限
    Coverage Coverage          // 见 §11
    Winner   string
}

type StageSummary struct {
    Name       string          // hardGradient / candidateForest / minResource / dryRun / score / select
    In, Out    int
    ExcludedBy map[Reason]int  // 独占归因，求和等于 In-Out
    Joint      map[string]int  // 多个原因同时成立的数量，仅作诊断，不参与对账
}

type CandidateRecord struct {
    ID        string
    Tier      int
    Verdict   string           // selected / evaluated / excluded / deferred
    Rejecter  *Reason          // excluded 时填写
    Terms     []ScoreTerm      // 仅 Top 保存
    Rank      int
}

type Reason struct{ Plugin, Code, Term string }

type ScoreTerm struct {
    Source       string        // 与完整方案 evaluator 的来源表同一份注册
    Detail       string
    Raw          float64
    Normalized   float64
    Weight       float64
    Contribution float64
}
```

`Verdict` 的四种取值覆盖整合稿没有表达的“被跳过”：`deferred` 表示该候选所在 tier 层因更低层已成功而没有被评估。

`Revision` 沿用整合稿 §3 的 declared / resolved / adopted 区分：记录的是 scheduler **采用**的 revision。

## 5. 对账合同

1. **首个拒绝者归因。** 每个候选只记到固定先后顺序里的第一个拒绝它的来源，因此每层直方图之和等于该层候选数。这与 kube-scheduler 的做法一致：每个节点只对应一个拒绝它的插件。先后顺序由静态表给出（硬约束插件 → 候选森林 → minResource → dry-run），不取决于 `plugins` 配置顺序，由此同时消除 G4 在归因上的影响。
2. **保留联合计数。** `Joint` 保存“多个来源都会拒绝”的数量，回答“NTA 单独也会排除多少”，不参与对账。
3. **为每个收窄步骤设独立标签。** 候选森林过滤为 `candidateForest`，整层并集检查为 `minResource(layerUnion)`，避免出现无标签差额。
4. **摘要只由结构化记录生成。** 一行 FitError、事件与日志从同一份 `StageSummary` 渲染。

不变量：

```text
对每个 tier： total = selected + evaluated + Σ excluded + deferred
任何 excluded 的 Reason 都非空
```

把 §2 的两个例子在新合同下重写，作为首批回归用例（A 的重叠数可由 4 + 3 − 6 = 1 推出）：

```text
A  tier-2 4/10（排除 6：podGroupAntiAffinity 4，networkTopology 2；其中 1 个同时不满足 networkTopology）
B  tier-2 3/10（排除 7：podGroupAntiAffinity 2，candidateForest 5）
```

文案不冻结，冻结的是不变量。

## 6. 评分来源合同

1. **保留已有数据。** `PrioritizeHyperNodes` 内部已拿到按插件划分的分数（`scheduler_helper.go:147-152`），只是求和后丢弃。改为同时返回 `map[plugin]map[hyperNode]score`，总分计算不变。
2. **GTA 记录逐项惩罚。** 每个 preferred term 记录 `{termIndex, tier, conflictHyperNode, weight, 截断前, 截断后}`，并标出“已触底，后续项无效”。
3. **NTA 的 taskNum 单列。** taskNum 分仍并入原分数（保持结果不变），但在记录中作为独立子项，并标注触发条件“最高分并列”。
4. **量纲写成契约。** kube-scheduler 的契约是各插件 Normalize 到 [0,100] 后乘权重。当前 GTA 以 1.0 起扣再乘 `weight*100`，NTA 乘 `100*GlobalWeight`，两者的量纲关系没有在文档里说明。本补充只要求先**写明现状换算**；是否统一到 Normalize→[0,100]→weight 属于行为变更，需独立语义版本开关。
5. **区分“分数差”与“层差”。** 解释里不允许把 tier 层差换算成分数；两者分开列。

## 7. 确定性 tie-break

- 现状：节点级 `SelectBestNodeAndScore` 与域级 `SelectBestHyperNodeAndScore` 随机选，Job 级由 map 遍历顺序决定（G1）。
- 合同：并列时按 `hash(jobUID, subJobID, candidateID)` 升序取第一个。键固定后，同输入同输出，可重放；不同 Job 的哈希不同，避免所有并列都偏向字典序最小的域，仍有分散效果。
- 解释里记录 `tieSet`（并列集合，有上限）和是否由哈希决定。回答“为什么是 A 不是 B”时，诚实的答案就是“并列，由固定哈希决定”。
- 备选方案：按名称字典序。更容易复述，但会长期偏向低编号域，列入未决事项。
- 参考：Kueue 在生成失败信息时，对最佳域并列用域 ID 打破，并在代码里注明 `domainsPerLevel` 是 map、遍历顺序随机（`pkg/cache/scheduler/tas_flavor_snapshot.go` 的 `multiLayerNotFitMessage`，固定于 `6b79acb1`）。
- 与整合稿 §10 的“稳定轮转”一致：轮转起点也应取自 `(revision, jobUID, generation)` 的确定函数。

## 8. 候选森林的解释

- 输出 `candidateForest{roots=N, sample=[≤8], envelope=<LCA>}`。envelope 只是遍历容器，不得出现在 Recorder 或日志的 `hyperNode` 字段，那里只能出现真实 root（呼应整合稿 §6）。
- `minResource` 目前是对各 root 并集资源的必要条件检查。失败时标注为“整层并集不足”，不归咎到任何单个 root；通过时也要标注“仅必要条件，仍需 dry-run”。
- 未被选中的 root 记录为 `deferred` 或 `evaluated`，不与 `excluded` 混用。

## 9. 空交集与最小冲突集

当硬约束交集为空，只告诉用户“无可用 hyperNode”不足以指导修改。借鉴 QuickXplain（Junker, AAAI 2004）：在一组约束中找出按偏好排序的最小冲突集。

- 输入：各插件的各 term 在候选域上的通过集合，用按 `HyperNodeIndex` 编号的位集保存，规模为 hyperNode 数量级。
- 算法：约束数通常只有几个到几十个，用删除法或 QuickXplain 递归，每次检查只是集合求交，不做 dry-run，不触碰资源。
- 只在失败路径上计算，结果按 revision 与输入签名缓存，避免同一失败在每个周期重复计算。
- 输出示例（说明性）：`最小冲突集：GTA required antiAffinity term#1@tier2 ∧ NTA highestTierAllowed=1：两者没有共同域。放宽任一项，各有 N 个域满足。`
- 边界：只解释集合层面的冲突。资源、Predicate、Gang 的失败属于节点级 FitError，不并入该结果。放宽建议只是提示，不自动放宽。

## 10. 对比式解释与输出分层

“为什么选 A 而不是 B”比“为什么选 A”更接近用户的实际问题（Miller 2019）。只为胜者与前 K 名保存完整 `ScoreTerm`，其余只保留首个拒绝者，记录有界。

| 层 | 内容 | 位置 | 默认 |
| --- | --- | --- | --- |
| L0 | 无法调度时的一行摘要，由 `StageSummary` 渲染 | PodGroup 条件消息（已有路径） | 开 |
| L1 | 已调度：胜者、所在 tier 层、与第二名的分差分解、是否并列 | Event 或 Recorder 记录，大小有上限 | 开，仅在决策变化时写 |
| L2 | 各阶段直方图与前 K 名分解 | V(3) 日志 | 随日志级别 |
| L3 | 有界的完整记录 | V(5) 或调试转储 | 关 |
| L4 | `topologyDecision`：各 tier/SubGroup 选中的域、revision、规则版本 | PodGroup status | 另行评审，见 §14 |

L4 参照 Kueue 把决策写入 Workload `status.admission.podSetAssignments[].topologyAssignment` 作为权威结果的做法。仅在变化时写，避免给 etcd 增加压力。

## 11. 覆盖与预算

整合稿 §10 要求记录截断原因与覆盖范围。拟议结构：

```go
type Coverage struct {
    Generated, EvaluatedFeasible, EvaluatedInfeasible int
    SkippedByBudget int
    Budget          string // domainVisits / nodePredicates / time
    Limit, Used     int64
    Truncated       bool
}
```

- `SkippedByBudget > 0` 时只能说“覆盖不足”，不得缓存或输出“全局无解”。
- 指标命名参照 KEP-5732：生成数、评估数（按可行与否）、评估耗时。具体前缀沿用 `pkg/scheduler/metrics` 现有约定，实施时冻结。
- `Coverage` 与 `StageSummary` 共用同一份计数，避免两处统计不一致。

## 12. 与未来统一规划器的衔接

- Apply/Undo journal 本来就记录每次选择与回退，可直接回放成搜索轨迹：`s1→A 失败（原因 X），回退 s1→B，s2→A 成功`。轨迹步数同样有上限，超限标注截断。
- 默认路径保持两句话能说清：“先用硬约束收窄候选，再按 tier 层从低到高找到第一个可行层，层内按加权分选，并列由固定哈希决定。” LDS／ILDS 回溯作为显式开启的能力，输出里标注“经回溯深度 d 找到”或“预算耗尽，已评估 n 个”。
- 完整方案 evaluator 的**来源表**与 `ScoreTerm.Source` 使用同一份注册，不另建一套，否则统一规划上线后解释与评分来源会再次分叉。
- 回溯会使“首个拒绝者”依赖搜索路径。对账只对同一次最终提交的路径成立，回溯中途的失败记入轨迹而不是 `StageSummary`。

## 13. 分期与验收

| 期 | 内容 | 语义变化 | 关键验收 |
| --- | --- | --- | --- |
| P0-1 | 对账合同与 FitError 重构（§5） | 无 | §2 的 A、B 两例转成回归测试；对随机生成的统计输入断言不变量。 |
| P0-2 | 保留评分分解（§6.1–6.3） | 无 | 新旧实现在随机输入上总分逐位一致（差分测试）。 |
| P0-3 | 确定性 tie-break（§7） | 随机收敛为确定 | 同输入重复运行 100 次结果一致；不同 Job 的并列分布不全部落在同一域。 |
| P0-4 | 候选森林输出规范（§8） | 无 | 日志与 Recorder 中不再出现 envelope 作为 hyperNode。 |
| P1 | `PlacementExplain` 挂到 Recorder；最小冲突集；对比式解释；golden worked example | 无 | 记录大小有上限；失败路径开销有压测数据。 |
| P1 | 量纲统一为 Normalize→[0,100]→weight | **有** | 语义版本开关；前后对比的差分报告。 |
| P2 | `topologyDecision` 写入 PodGroup status；指标；搜索轨迹 | 无（新增输出） | API 评审；etcd 写入量评估。 |

### worked example（说明性，非实测）

用于 golden 测试的第一个例子，参照 KAI-Scheduler 拓扑文档“逐步解释为什么选了 Zone A”的写法。

```text
拓扑：tier-2 S1{R1,R2}, S2{R3,R4}；tier-1 为 rack
空闲节点：R1=4  R2=8  R3=8  R4=2
已有 PodGroup A 占用 S1。新 Job B：8 个 Pod，每节点一个，
对 A 在 tier-2 有 required 反亲和，NTA highestTierAllowed=2

阶段 hardGradient：反亲和排除 S1 及其下的 R1、R2         （3 个，podGroupAntiAffinity）
阶段 tier-1 层  ：R3 空闲 8 满足；R4 空闲 2 不满足          （1 个，minResource）
阶段 dryRun     ：R3 通过 → tier-1 层已有可行解，返回
结果            ：选中 R3；S2 属于更高层，未评估（deferred）

对账：总数 6 = 选中 1 + 排除 4 + 未评估 1
```

## 14. 未决事项

1. tier 层是否保留为硬序，还是转为可配置收益项；影响 §3 与整合稿 §1。
2. tie-break 采用固定哈希还是名称字典序（§7）。
3. 首个拒绝者的先后顺序由静态表给出，还是由插件声明；后者对第三方插件更友好，但增加接口面。
4. 解释记录的默认开启条件，以及每 Job 每周期额外内存与时间的门槛，需要实测。
5. `topologyDecision` 属于 API 变更，需要上游兼容评审。
6. 评分量纲统一的默认值与迁移路径。

## 15. 参考资料与核验状态

| 材料 | 用于 | 核验状态 |
| --- | --- | --- |
| [kube-scheduler `Diagnosis`/`FitError`](https://raw.githubusercontent.com/kubernetes/kubernetes/master/pkg/scheduler/framework/types.go) | 按插件归因与直方图 | 本次调研读过源码；分支为移动引用。 |
| [KEP-5732 拓扑感知 workload 调度](https://raw.githubusercontent.com/kubernetes/enhancements/master/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md) | `Placement.Name`、归一化与权重、指标、评估比例 | 本次调研读过 README；链接 2026-11-08 返回 200。 |
| [Kueue TAS 文档](https://kueue.sigs.k8s.io/docs/concepts/topology_aware_scheduling/) 与 [`tas_flavor_snapshot.go`@`6b79acb1`](https://raw.githubusercontent.com/kubernetes-sigs/kueue/6b79acb1d56f2eaf7650f9e0730c25fdb7831250/pkg/cache/scheduler/tas_flavor_snapshot.go) | 决策写入 status；失败信息含 `allows to fit only X out of Y`、`Total nodes: N; excluded: …`；并列按域 ID 打破 | 失败信息与 tie-break 来自该 SHA 的源码（`notFitMessage`、`multiLayerNotFitMessage`）。未核对其排除计数是否独占。 |
| [KAI-Scheduler 拓扑文档](https://raw.githubusercontent.com/NVIDIA/KAI-Scheduler/main/docs/topology/README.md) | 域选择与域内节点排序的 worked example | 本次调研读过；分支为移动引用。 |
| Junker. [*QuickXplain*](https://cdn.aaai.org/AAAI/2004/AAAI04-027.pdf). AAAI 2004 | 最小冲突集 | 链接 2026-11-08 返回 200。 |
| Fox, Long, Magazzeni. [*Explainable Planning*](https://arxiv.org/abs/1709.10256). 2017 | 规划解释的框架 | 链接 2026-11-08 返回 200。 |
| Miller. *Explanation in AI: Insights from the Social Sciences*. Artificial Intelligence 267, 2019 | 对比式解释 | **未联网核对**，凭记忆，引用前请复核。 |
| Labreuche. *A general framework for explaining the results of a multi-attribute preference model*. Artificial Intelligence 175, 2011 | 加权和模型的贡献解释 | **未联网核对**，同上。 |
| Bogaerts, Gamba, Guns. *A framework for step-wise explaining how to solve constraint satisfaction problems*. Artificial Intelligence, 2021 | 逐步解释 CSP | **未联网核对**，同上。 |

## 16. 验证边界

- 本补充只增加文档，没有修改调度器代码、API 或 CRD；没有运行 Go 全量测试、模型或集群验证。
- §2 的两个输出来自 2026-11-08 的一次临时探测测试（`go test ./pkg/scheduler/api/ -run <临时用例>`），文件已删除，未提交。复现方法见该节。
- §13 的 worked example 与 §9 的输出示例是设计示意，不是在真实集群上运行的结果。
- 没有检索 Volcano 上游是否已有可解释性相关 issue 或 PR（本机 `gh` 未登录），因此不对上游现状作结论。
- 与整合稿冲突时，运行时方案以 [最终方案整合稿](FINAL_DESIGN_CN.md) 为准；本补充只约束解释与对账的合同。更多证据边界见 [验证状态](EVIDENCE_CN.md) 与 [参考资料](REFERENCES_CN.md)。
