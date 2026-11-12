# KEP-5732 / KEP-4671 与 kube-scheduler 核心契约：调研报告

## Summary

- **KEP-5732 已演进，旧接口名过时。** 它在 v1.36 进入 alpha，v1.38 进入 beta，代码已在 k/k 落地。KEP 初稿的 `PlacementStatePlugin{AssumePlacement,RevertPlacement}` 在 beta 改版（PR #6134）中被删除，现在没有这个接口。
- **现在的状态载体和评估方式。** 状态由 `PodGroupCycleState` 和 `PlacementCycleState` 承载。每个 Placement 用"限定候选节点集 + 复用 pod 级 Filter/Score + 逐 pod assume/revert"来评估。
- **v1.37.2 与 Volcano 现状几乎同构。** 它对所有 placement 逐个完整 dry-run，没有数量上限，也没有 shuffle。master 和 v1.38.0-beta.0 才加入 `PercentageOfPlacementsToScore`、shuffle、NNN 优先和 `PlacementFeasible` 早拒。
- **Placement 之间是串行评估，不并行。** 对多个 PlacementScore 插件只取"前 N 个可行 placement"打分；没有 PlacementScore 插件时 N=1，即第一个成功就停。
- **失败解释很弱。** 只报告"某一个 placement 的 status"（代码注释自称 arbitrary），KEP 的排错手段是 scheduler 日志。
- **tie-break 是随机的。** placement 用 `rand.Int()` 作 Randomizer，KEP 没有 tie-break 规则。节点级随机性在 v1.35 之后的实现里有疑点，见 (3.4)。
- **KEP 没有"模拟代价"方面的替代方案。** 只讨论了 Pod Inter-Affinity 和独立调度器（Volcano/YuniKorn）。
- **核心契约结论。** Score 归一化到 [0,100] 后乘权重。Filter 对每个节点首个拒绝插件即返回。`FitError` 按"每节点一条理由"聚合。`PreScore`/`NormalizeScore` 确实依赖候选节点集合（PodTopologySpread、InterPodAffinity 是 min-max 型）。Reserve/Unreserve、AddPod/RemovePod 和 `CycleState.Clone` 是有状态插件可逆性的现成先例。

## Pinned versions

| 仓库 | 引用 |
|---|---|
| kubernetes/enhancements master | `72907deaa85eae6e208bc95c5f74dd27c1911345`（2026-11-06）。KEP-5732 路径最新提交 `fb2833e7ed8dd1c7d3c8a60a26dc90238ad19d81`（2026-09-12）；KEP-4671 最新 `91ac5bf5…`（"Update Gang Scheduling KEP for GA in v1.38"） |
| kubernetes/kubernetes | master `dd21ea95bc597663b263c040dc7abf43c037a7d0`（2026-11-11） |
| kubernetes/kubernetes | 最新稳定 **v1.37.2** = `c50e5395f1ae2d4a2c05f28fdcc38c0916cc1a32`（2026-10-28）；v1.37.0 = `f54c212e3a2f75d674b717a9b29052b20b60aefc`；最新预发布 v1.38.0-beta.0 = `1ca7499c9a5669c1f025dbe14f1da7dd9a0eb58a` |
| kubernetes/website main | `18f8e9f6d2b0bc9d33e82ceac8abcfffdf8c1ca8` |

- 第 (3) 部分默认引用 v1.37.2。
- master 上这些逻辑已重构到 `pkg/scheduler/algorithm.go`，引用时会注明。

## Findings (1)：KEP-5732 / KEP-4671

### 1.1 状态与历史（VERIFIED：kep.yaml 与 git 历史）

- **阶段与门控。** 5732 的 stage 为 beta（alpha v1.36、beta v1.38、stable v1.39），gate 为 `TopologyAwareWorkloadScheduling`，依赖 4671 的 `GenericWorkload`。4671 目标 stable v1.38。
- **PlacementStatePlugin 的存废（DOCS/KEP ONLY）。**
  - 提交 `bfb40c4e95`（初稿）到 `25fc933945`，README 都含 `PlacementStatePlugin`，共 6 处。
  - 提交 `a79ba724`（PR #6134，2026-06-11）里为 0。
  - 初稿接口：[PlacementGeneratorPlugin / PlacementStatePlugin / PlacementScorerPlugin(float64)，L423-482](https://github.com/kubernetes/enhancements/blob/bfb40c4e95/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L423-L482)。
  - PR 描述："Adding support for defining multiple PlacementGeneratePlugins … Moving DRA support … to KEP-5729"。
  - k/k 的 interface.go 在 v1.36.0、v1.37.0、v1.38.0-beta.0 中都没有 `PlacementStatePlugin`、`AssumePlacement` 或 `RevertPlacement`（grep 计数为 0，VERIFIED IN SOURCE CODE）。

### 1.2 当前接口（KEP 文本 [L276-350](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L276-L350)；DOCS/KEP ONLY，并与代码比对）

```go
type Placement struct { Name string; Nodes []NodeInfo }
type GeneratePlacementsResult struct { Placements []*Placement }   // 允许重叠
GeneratePlacements(ctx, state PodGroupCycleState, podGroup PodGroupInfo,
    parentPlacement *Placement) (*GeneratePlacementsResult, *Status)
type PlacementScore struct { Placement *Placement; Score int64 }
NormalizePlacementScore(ctx, state PodGroupCycleState, podGroup PodGroupInfo,
    placementScores []PlacementScore) *Status
ScorePlacement(ctx, state PlacementCycleState, podGroup PodGroupInfo,
    placement *PodGroupAssignments) (int64, *Status)   // + PlacementScoreExtensions()
```

- **代码中的位置与差异（VERIFIED IN SOURCE CODE）。**
  - 位置：[staging/.../kube-scheduler/framework/interface.go L801-890（master）](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/staging/src/k8s.io/kube-scheduler/framework/interface.go#L801-L890)。
  - 代码里 `ScorePlacement` 签名与 KEP 一致。
  - 代码另有 `PlacementFeasiblePlugin`（L862-875）。其 `PlacementProgress` 在 KEP-4671 中是 {Remaining, Scheduled}，master 多了 Unschedulable。
- **Placement 与 PodGroupAssignments 的类型。**
  - [`Placement`（types.go L728-741）](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/staging/src/k8s.io/kube-scheduler/framework/types.go#L728-L741)：同一插件内名称唯一，且不得含 "/"。
  - `PodGroupAssignments{*Placement; ProposedAssignments}`：[L756-764](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/staging/src/k8s.io/kube-scheduler/framework/types.go#L756-L764)。
- **PlacementGeneratePlugin 的契约（master）。** [L810-828](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/staging/src/k8s.io/kube-scheduler/framework/interface.go#L810-L828) 写明："The scheduler may shuffle and evaluate only a subset of the returned Placements, so plugins must not rely on their order"。
- **多个 Generate 插件的结果合并。** 取非空交集：[KEP L420-472](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L420-L472)。
- **内置插件。** 见 [KEP L409-418](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L409-L418)，由 [topology_placement.go L58-98](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/framework/plugins/topologyaware/topology_placement.go#L58-L98) 实现。
  - `TopologyPlacementGenerator`：每个不同的 label 值一个 placement，`Name` 即域值，无该 label 的节点被丢弃。
  - `NodeResourcesFit` 作为 PlacementScorePlugin，使用"最紧凑装箱"。
  - `PodGroupPodsCount`：[podgroup_pods_count.go L53-90](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/framework/plugins/podgrouppodscount/podgroup_pods_count.go#L53-L90)，分数为 `scheduledPodsCount+len(ProposedAssignments)`，按最大值归一化。

### 1.3 生命周期（KEP [L352-407](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L352-L407) + 4671 [L1058-1101](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/4671-gang-scheduling/README.md#L1058-L1101)；DOCS/KEP ONLY）

1. **Phase 1：生成。** `GeneratePlacements`。若 pod 已有已调度成员，placement 必须包含它们所在节点。
2. **Phase 2：逐 placement 评估。** 引用 KEP 原话："Run default workload scheduling algorithm with the given set of nodes"。
   - 逐 pod 串行处理。
   - 至少 minCount 个 pod 放得下则可行；Basic 策略只要 ≥1 个。
3. **Phase 3：打分选优。** 对可行 placement 调 `ScorePlacement`，取最高分，然后逐 pod 用 `nominatedNodeName` 预绑定。
4. **单次 snapshot。** 4671 [L1086-1087](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/4671-gang-scheduling/README.md#L1086-L1087)："A single cluster state snapshot is taken for the entire group operation"。
5. **pod 排序。** 同构子组的排序要确定且稳定（[4671 L1161](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/4671-gang-scheduling/README.md#L1161)）。放得下的 pod 被"temporarily assumed and reserved on the selected node"（L1169）。
6. **限制。**
   - 仅串行逐 pod，无回溯（[5732 L203-213, L922-931](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L922-L931)）。
   - 异构组和有 pod 间依赖的组只是 best-effort（[4671 L1296-1342](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/4671-gang-scheduling/README.md#L1296-L1342)）。

### 1.4 复用 pod 级插件与状态回滚（VERIFIED IN SOURCE CODE，master）

**KEP 与代码的差异。** KEP L857-858 说"placement-scoped snapshot"，但代码里只是限制候选节点集，不是 copy-on-write 快照（见 2.3）。

**回滚机制（三层）：**
- **Placement 级。** `evaluatePlacement` 先 `AssumePlacement`，运行默认算法，再 revert：[algorithm_podgroup.go L785-812](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L785-L812)。
- **Pod 级。** 每个 pod 的 `AssumeAndReserveInSnapshot` 返回 revert 闭包，闭包内会：
  - 运行 Unreserve；
  - 执行 `ForgetPod`；
  - 恢复 NNN。

  见 [algorithm.go L786-833](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm.go#L786-L833)。
- **Per-placement 数据。** `newPlacementCycleState` 创建新的 `CycleState`，并用 `CopyPlacementDataInto` 填入克隆后的 StateData：[L814-821](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L814-L821)。

**两个相关细节：**
- **失败时的整体回滚。** 当 `PlacementFeasible` 或某个 pod 失败时，该 placement 内所有 assume 都会被回滚：[L225-299](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L225-L299)。
- **Reserve 会跑两次。** 评估阶段一次，选中后提交时 `AssumeAndReserveInCache` 又一次（[algorithm.go L745-765, L692](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm.go#L745-L765)）。这对 Volcano 的"有状态插件必须可逆"是直接启示。

### 1.5 效率控制（beta，v1.38）

**KEP 描述（DOCS/KEP ONLY）：**
- `PercentageOfPlacementsToScore`：自适应，小集群 100%，5000 节点时 10%，下限 5%。
- NNN 所在 placement 先评估。
- `PlacementFeasible` 早拒。

出处：[KEP L420-472](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L420-L472)。

**代码（VERIFIED IN SOURCE CODE，master）：**
- **自适应公式。** 在 [algorithm_podgroup.go L726-755](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L726-L755)：
  - 常量 `minFeasiblePlacementsToFind=1`、`minFeasiblePlacementsPercentageToFind=5`（L42-49）。
  - percentage=0 时，公式为 `max(5, 100 - numAllNodes*9/500)`。
  - 这里的 `numAllNodes` 是各 placement 节点数之和。
- **取前 N 个。** `placementsToEvaluate`（[L709-722](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L709-L722)）：
  - 没有 PlacementScore 插件时 N=1，即第一个成功就停；
  - 有插件且 N 小于总数时，先 shuffle 一份拷贝再取前 N。
- **NNN 优先与早停。** [L440-550](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L440-L550)：
  - 先评估 `nominatedPlacement`（L831+）。
  - 当 `len(successfulResults) >= numPlacementsToFind` 时 break。
- **默认值。** `DefaultPercentageOfPlacementsToScore=0`（[types.go L267-270](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/apis/config/types.go#L267-L270)），TAS gate 开启时在 `v1/defaults.go` L135-136 设置，另有 per-profile 覆盖。
- **串行。** 注释声明 `PodGroupSchedulingAlgorithm` "mutates the shared nodeInfoSnapshot and is not safe for concurrent use"（[L92](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L92)）。**Placement 之间无并行。**
- **KEP 缓解项未落地。** KEP 的"按总资源预过滤 Placement"在 master 的 algorithm_podgroup.go 和 fit.go 中 grep 未找到（NOT FOUND）。
- **PlacementFeasible 的实现者。** 仅在 `gangscheduling` 插件中找到（GitHub code search，可能不完整）。

### 1.6 Tie-break 与确定性

- **KEP 无 tie-break 规则。** grep 无结果（VERIFIED，全文检索）。
- **代码：随机。** `findBestPlacement` 给每个 placement 赋 `Randomizer = rand.Int()`，同分取 Randomizer 大者：[algorithm_podgroup.go L879-908](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L879-L908)；字段定义在 [interface.go L310-321](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/staging/src/k8s.io/kube-scheduler/framework/interface.go#L310-L321)。
- **placement 顺序本身也随机。** TopologyPlacement 遍历 map（无序），所以才有 shuffle。
- **关闭随机的选项。** 只有测试用：`shufflePlacements` 置 nil 可"keep tests deterministic"（[L110, L144](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L110)）。没有面向用户的开关。

### 1.7 可观测性

**Metrics（VERIFIED IN SOURCE CODE，ALPHA 稳定性）：** [metrics.go L699-722](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/metrics/metrics.go#L699-L722)：
- `generated_placements_total{profile,entity}`
- `placement_evaluations_total{result=feasible|infeasible,profile,entity}`
- `placement_evaluation_duration_seconds{...}`，buckets 为 `ExponentialBuckets(0.001, 2, 15)`
- 在 `scheduler_` 前缀下导出，名称同 KEP kep.yaml。
- `entity` 标签是代码相对 KEP 文本多出来的。

**4671 的 metrics（DOCS/KEP ONLY）：**
- `scheduler_podgroup_schedule_attempts_total`
- `scheduler_podgroup_scheduling_attempt_duration_seconds`
- `scheduler_podgroup_scheduling_algorithm_duration_seconds`

见 [L1745-1747](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/4671-gang-scheduling/README.md#L1745-L1747)。

**PodGroup condition：**
- 条件类型为 `InitiallyScheduled`，原因为 Scheduled / Unschedulable / SchedulerError。
- 见 [schedule_one_podgroup.go L677-696（master）](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/schedule_one_podgroup.go#L677-L696)。
- KEP 文本里写的是 `PodGroupInitiallyScheduled`，与代码略有出入。

**失败诊断（KEP 层面）：**
- 依靠 scheduler 日志（v=4/6）。
- 另有 Pod Events / Status 中"minCount pods could not be scheduled"之类的消息（[KEP L763-812, L884-910](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L884-L910)）。

### 1.8 失败报告（VERIFIED IN SOURCE CODE）

- **FitError 构造。** `newPodGroupPlacementFitError` 包装 `anyResult`（[algorithm_podgroup.go L528-537](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L528-L537)）。
- **代码注释的原话：** "anyResult is the nominated placement's result when one was evaluated, otherwise the first placement tried. Which one we report is otherwise arbitrary"。
- **消息格式。** [types.go L162-223](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/types.go#L162-L223) 中：`"0/%d placements are available, first placement status: %s"`（L222）。
- **v1.37.2 的版本。** 同样的格式出现在 [schedule_one_podgroup.go L1041](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one_podgroup.go#L1041)。
- **结论。** 没有逐 placement 的 verdict 列表，也没有"哪个域被哪个插件拒绝"的汇总。

### 1.9 被考虑/否决的替代方案（DOCS/KEP ONLY）

- [KEP L933-955](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md#L933-L955)：只有"Pod Inter-Affinities"和"Standalone Schedulers (Volcano/YuniKorn)"。
- 动机部分（L126-135）也提到 Kueue 和 Volcano。
- 初稿的 "Potential Optimization: Pre-filtering can check aggregate resources…"（旧版 L517-518）和性能风险（旧版 L965-973）只是缓解建议，不是替代方案。
- **KEP 没有"模拟代价"相关的替代方案。**
- **未来扩展（L488-508）：** placement 带 Rank 的优先级排序、`Parent` 字段的多级约束、NodeTopology。
- **DRA（L476-486）：** DRA 的 placement 生成推迟，并入 KEP-5729。

## Findings (2)：k/k 实现状态

### 2.1 Feature gate（VERIFIED IN SOURCE CODE，master `kube_features.go`）

| Gate | 状态（master） | 备注 |
|---|---|---|
| `GenericWorkload` | Alpha 1.35，Beta 1.37（[L1519-1522](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/features/kube_features.go#L1519-L1522)） | KEP 写 stable v1.38，master 上尚未 GA |
| `TopologyAwareWorkloadScheduling` | Alpha 1.36，Beta 1.38（[L2046-2049](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/features/kube_features.go#L2046-L2049)） | 依赖 `GenericWorkload`（L2682）。v1.37.2 中仍仅为 Alpha 1.36（[L2231-2233](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/features/kube_features.go#L2231-L2233)） |
| `CompositePodGroup` | Alpha 1.37，Beta 1.38（L1332-1335） | |
| `DRAWorkloadResourceClaims` | Alpha 1.36，Beta 1.37 | |

- `GangScheduling` 和 `WorkloadAwarePreemption` 在 master 和 v1.37.2 中都不存在。KEP 称 v1.37 中它们已合并进 `GenericWorkload`（[4671 L1407-1419](https://github.com/kubernetes/enhancements/blob/72907deaa85eae6e208bc95c5f74dd27c1911345/keps/sig-scheduling/4671-gang-scheduling/README.md#L1407-L1419)）。

### 2.2 各 tag 包含的内容（VERIFIED，按文件检查）

- **v1.35.0：** 没有 podgroup 调度代码，只有 alpha 铺垫。
- **v1.36.0：** 有 `schedule_one_podgroup.go`（779 行）、`topology_placement.go` 和 `podgrouppreemption.go`。
- **v1.37.0 / v1.37.2：** `schedule_one_podgroup.go` 为 1448 行，TAS 仍是 alpha。**没有** `PercentageOfPlacementsToScore`，没有 `placement_merging.go`。
- **v1.38.0-beta.0：** 有 `algorithm_podgroup.go`、`placement_merging.go` 和 `PercentageOfPlacementsToScore`。

**v1.37.2 的 placement 循环：** [schedule_one_podgroup.go L971-1066](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one_podgroup.go#L971-L1066)。
- 对每个 placement 依次执行：`AssumePlacement` → `podGroupSchedulingDefaultAlgorithm` → `placementRevertFns.revert()`。
- **没有数量上限，也没有 shuffle。**
- 选最优用 `findBestPlacement`（[L1196-1225](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one_podgroup.go#L1196-L1225)）：同分取更大的 `rand.Int()`；只有一个可行 placement 时跳过打分。

### 2.3 状态 fork / 快照（VERIFIED，master）

- **单次快照。** `scheduleOnePodGroup` 每个周期只调一次 `Cache.UpdateSnapshot`（[schedule_one_podgroup.go L45-90](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/schedule_one_podgroup.go#L45-L90)）。
- **placement 只限制候选节点列表。** [backend/cache/snapshot.go L711-786](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/backend/cache/snapshot.go#L711-L786) 中，`AssumePlacement` / `ForgetPlacement` / `ListNodesInPlacement` 只切换候选节点列表。
- **v1.37.2 同样如此。** `ListNodesInPlacement` 在无 placement 时回退到 `List()`，否则返回 `placement.Nodes`（[L810-815](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/backend/cache/snapshot.go#L810-L815)）。
- **Plugin 视角的泄漏。** `RunPreFilterPlugins` 用 `NodeInfos().List()` 取全部节点（[framework.go L941](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/runtime/framework.go#L941)）。而 `findNodesThatFitPod` 的候选集用 `ListNodesInPlacement()`（[schedule_one.go L635](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L635)）。
  - 结果是 PreFilter 状态（如 PodTopologySpread 的计数）看到整个集群，只有 Filter/Score 的候选节点被限制到 placement 内。
  - 这个语义对 PodTopologySpread / InterPodAffinity 的实际效果我没有做运行时验证（NOT VERIFIED）。
- **事务式变更。** `StartMutations/EndMutations` 会深拷贝 nodeInfoMap、列表、`usedPVCRefCounts` 和 podGroupStates，结束时还原（[snapshot.go L259-316](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/backend/cache/snapshot.go#L259-L316)）。
- **嵌套调度时的状态隔离。** 嵌套调用时应传新的 `PodGroupCycleState`，避免内层 pod 级插件覆盖外层状态（[interface.go L604-608](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/staging/src/k8s.io/kube-scheduler/framework/interface.go#L604-L608)）。
- **每个 pod 的 CycleState。** 每个 pod 通过 `initPodSchedulingContext` 获得独立的 `CycleState`，并带上 `SetPodGroupCycleState` / `SetPlacementCycleState`（[algorithm_podgroup.go L153-182](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm_podgroup.go#L153-L182)）。
- **选中后的提交。** 保存的 podResults 和 per-pod CycleState 被提交：`prepareForBindingCycle` → `AssumeAndReserveInCache` → Permit/binding（[schedule_one_podgroup.go L626-637](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/schedule_one_podgroup.go#L626-L637)）。

### 2.4 打分与默认插件（VERIFIED）

- **`RunPlacementScorePlugins`**（[runtime/framework.go L1736-1844](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/framework/runtime/framework.go#L1736-L1844)）：
  - 通过 `Parallelizer().Until` 对 placement 并行打分；
  - 然后 `NormalizePlacementScore`，再乘权重；
  - 分数超出 [MinScore, MaxScore] 即报错（L1825-1829）；
  - 注释自称"mostly duplicates RunScorePlugins"。
- **默认插件。** [default_plugins.go L82-93](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/apis/config/v1/default_plugins.go#L82-L93)：开启 TAS gate 时向 MultiPoint 加入 `TopologyPlacementGenerator` 和 `PodGroupPodsCount`（权重 1）。

### 2.5 落地 PR（`gh search prs`，VERIFIED 于合并状态）

| PR | 内容 | 合并时间 |
|---|---|---|
| #137128 | TAS feature gate | 2026-02-19 |
| #136944 | prepare placement simulation | 2026-03-05 |
| #137083 | placement generator interfaces | 2026-03-13 |
| #137201 | placement scorer interfaces | 2026-03-12 |
| #137271 | PodGroup `SchedulingConstraints` + TopologyPlacement 插件 | 2026-03-17 |
| #137489 | 把 TAS 集成进 podgroup 周期 | 2026-03-18 |
| #136652 | NodeResourcesFit 的 placement 打分 | 2026-03-16 |
| #139604 | TAS metrics | 2026-07-21 |
| #139472 | TAS 支持 NNN | 2026-09-10 |
| #143632 | TAS e2e 测试 | 2026-11-03 |

- 另有相关 KEP PR：#5733（5732 初稿）、#6134（beta v1.37 更新）、#6234（metric 改名）、#6346（beta 1.38）、#5730（4671 workload scheduling cycle 与 delayed preemption）。
- KEP 引用的测试在 k/k 提交 `1b2ebe52…`：`test/integration/scheduler/podgroup/topology_aware_scheduling/tas_test.go` 和 `test/integration/scheduler_perf/podgroup/tas/performance-config.yaml`（DOCS/KEP ONLY）。

## Findings (3)：kube-scheduler 核心契约（v1.37.2）

### 3.1 Score 归一化契约（VERIFIED IN SOURCE CODE）

- **范围。** `MaxScore=100, MinScore=0`；`MaxNodeScore` 已标 deprecated（[interface.go L330-334](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/interface.go#L330-L334)）。
- **执行顺序。** [runtime/framework.go L1526-1582](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/runtime/framework.go#L1526-L1582) 的 `normalizeScores`：
  - 先并行执行各插件的 `NormalizeScore`（L1540-1555）；
  - 再逐节点检查 `score > MaxScore || score < MinScore`，超出则报 "returns an invalid score … should in the range of [%v, %v] after normalizing"（L1564-1569）；
  - 然后 `weightedScore := score * int64(weights[i])`，累加为 `TotalScore`（L1570-1575）。
- **Score 本身并行。** `RunScorePlugins`（L1405 起）对节点并行执行。
- **文档。** 官方文档只说"well defined range… After the NormalizeScore phase … combine node scores … according to the configured plugin weights"（[scheduling-framework.md L120-124](https://github.com/kubernetes/website/blob/18f8e9f6d2b0bc9d33e82ceac8abcfffdf8c1ca8/content/en/docs/concepts/scheduling-eviction/scheduling-framework.md#L120-L124)，DOCS）。

### 3.2 `percentageOfNodesToScore` / `numFeasibleNodesToFind`（VERIFIED IN SOURCE CODE）

- **常量。** `minFeasibleNodesToFind = 100`，`minFeasibleNodesPercentageToFind = 5`：[schedule_one.go L50-62](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L50-L62)。
- **函数。** [L866-891](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L866-L891)：
  - 节点数小于 100 时返回全部节点；
  - 优先使用 profile 的 percentage，否则用全局值；
  - 为 0 时用 `percentage = 50 - numAllNodes/125`，并且不低于 5；
  - `numNodes = numAllNodes*percentage/100`，小于 100 则返回 100。
- **只用 Filter、没有 Score 时直接取 1 个。** [L788-790](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L788-L790) 在无 extender filter 且没有打分时 `numNodesToFind = 1`。
- **轮转起点。** `nextStartNodeIndex`：
  - 每轮结束后更新：`(next + processedNodes) % len(allNodes)`（[L692-695](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L692-L695)）；
  - 检查节点时按它轮转（[L798, L816](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L813-L816)）。
- **并行与早停。** 并行 Filter 用原子计数收集可行节点，够数后 cancel（[L824-831, L848](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L824-L848)）。
- **文档。** "linear formula that yields 50% for a 100-node cluster and … 10% for a 5000-node cluster. The lower bound … is 5%"（[scheduler-perf-tuning.md L76-78](https://github.com/kubernetes/website/blob/18f8e9f6d2b0bc9d33e82ceac8abcfffdf8c1ca8/content/en/docs/concepts/scheduling-eviction/scheduler-perf-tuning.md#L76-L78)），以及 round-robin 说明 [L137-143](https://github.com/kubernetes/website/blob/18f8e9f6d2b0bc9d33e82ceac8abcfffdf8c1ca8/content/en/docs/concepts/scheduling-eviction/scheduler-perf-tuning.md#L137-L143)（DOCS）。
- **master。** 逻辑移到 `SchedulingAlgorithm` 方法：[algorithm.go L641-655](https://github.com/kubernetes/kubernetes/blob/dd21ea95bc597663b263c040dc7abf43c037a7d0/pkg/scheduler/algorithm.go#L641-L655)，公式不变。
- **对比。** 这与 placement 版的 `100 - n*9/500`、下限 5%、最少 1 个结构相同，只是常数不同。

### 3.3 Diagnosis / FitError（VERIFIED IN SOURCE CODE，[framework/types.go L1419-1521](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/types.go#L1419-L1521)）

- **Diagnosis 字段。** `NodeToStatus *NodeToStatus`（只记录被拒节点，通过的不记）、`UnschedulablePlugins`、`PendingPlugins`、`PreFilterMsg`、`PostFilterMsg`（L1423-1436）。
- **每节点一个拒绝插件。** `RunFilterPlugins` 遇到第一个不成功的插件就 `status.SetPlugin(pl.Name())` 并返回（[framework.go L1117-1138](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/runtime/framework.go#L1117-L1138)），所以 "first rejecter per node"。文档同样写明 "the remaining plugins will not be called for that node"（[scheduling-framework.md L99-102](https://github.com/kubernetes/website/blob/18f8e9f6d2b0bc9d33e82ceac8abcfffdf8c1ca8/content/en/docs/concepts/scheduling-eviction/scheduling-framework.md#L99-L102)）。
- **聚合。** `AddPluginStatus`（L1450-1466）把拒绝插件名收进 `UnschedulablePlugins`，Pending 的收进 `PendingPlugins`。
- **消息格式。**
  - 注释：`"0/X nodes are available: <PreFilterMsg>. <FilterMsg>. <PostFilterMsg>."`（L1469），其中 `NoNodeAvailableMsg = "0/%v nodes are available"`（L1447）。
  - 构造：`reasonMsg := fmt.Sprintf(NoNodeAvailableMsg+":", f.NumAllNodes)`（L1471），PreFilter 失败时直接追加 `" %v."`。
  - 否则按"理由 → 节点数"直方图：
    ```go
    reasons[reason]++   // 遍历 ForEachExplicitNode
    // 缺省节点: reasons[reason] += NumAllNodes - NodeToStatus.Len()
    fmt.Sprintf("%v %v", v, k)   // 排序后以 ", " 连接，末尾加 "."
    ```
    （L1486-1511）。
  - 如 `0/5 nodes are available: 3 Insufficient cpu, 2 node(s) didn't match Pod's node affinity/selector.`（样例为示意，非源码文本）。

### 3.4 `selectHost` 与 tie-break（VERIFIED IN SOURCE CODE，但结论里有推断）

- **v1.34.0 及以前。** `selectHost` 在最高分节点中做 reservoir sampling：[v1.34.0 schedule_one.go L890-927](https://github.com/kubernetes/kubernetes/blob/v1.34.0/pkg/scheduler/schedule_one.go#L890-L927)，核心为 `cntOfMaxScore++; if rand.Intn(cntOfMaxScore) == 0 { selectedIndex = cntOfMaxScore - 1 }`（L915-919）。
- **v1.35.0 起（随 OpportunisticBatching，PR #135231）。**
  - `selectHost` 被 `SortedScoredNodes`（最大堆）取代。
  - 比较函数为 `TotalScore` 大者优先，同分时 `Randomizer` 大者优先（[sorted_nodes.go L61-64](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/sorted_nodes.go#L61-L64)）。
  - `schedulePod` 取堆顶：[schedule_one.go L608-614](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L608-L614)。
- **疑点（我的推断，NOT VERIFIED 于运行时）。**
  - 在 v1.37.2 和 master 中，节点的 `Randomizer = rand.Int()` 只出现在 `prioritizeNodes` 的 extender 分支里（`if len(extenders) != 0 && nodes != nil`，L989 起；赋值在 [L1041-1047](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/schedule_one.go#L1041-L1047)）。
  - `RunScorePlugins` 构造 `NodePluginScores` 时没有设置 `Randomizer`（[framework.go L1469-1473 一带](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/runtime/framework.go#L1405-L1470)）。
  - GitHub code search 显示只有 `schedule_one.go` 和 `algorithm_podgroup.go` 给 `Randomizer` 赋值（其余是测试和类型定义）。
  - 若如此，无 extender 时节点同分只靠 `container/heap` 的内部顺序，输入顺序受 `nextStartNodeIndex` 轮转和并行 Filter 的写入顺序影响。也就是说，随机性可能比 v1.34 弱。
  - 建议用小测试确认。我没有跑。
- **字段说明。** 字段注释是 "Randomizer is used to provide randomness when randomizing nodes within a common score"（[interface.go L294-296](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/interface.go#L294-L296)）。
- **确定性选项/issue。** 用 `gh search issues/prs`（"Randomizer scheduler tie"、"deterministic scheduling tie-break" 等）没有找到相关 issue（NOT FOUND）。没有用户可配置的开关。

### 3.5 PreScore/Score 对候选节点集的依赖（"cohort 依赖"）

**文档层面（VERIFIED IN SOURCE CODE）。**
- `PreScorePlugin` 的注释写明："Plugins will be called with a list of nodes that passed the filtering phase"（[interface.go L627-631](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/interface.go#L627-L631)）。
- `ScoreExtensions` 的注释是 "NormalizeScore is called for all node scores produced by the same plugin's Score method"（L643-648）。
- 没有找到官方文档对"归一化依赖候选集"的显式声明，这是从实现读出来的（NOT VERIFIED 为文档化契约）。

**随候选集变化的插件（VERIFIED IN SOURCE CODE）：**
- **PodTopologySpread**（[scoring.go](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/podtopologyspread/scoring.go)）：
  - `initPreScoreState` 按 `filteredNodes` 统计拓扑域和 `TopologyNormalizingWeight`，hostname 约束时 `sz = len(filteredNodes) - len(IgnoredNodes)`（[L61-115](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/podtopologyspread/scoring.go#L61-L115)）。
  - 计数用的是全部节点，但只累加到候选域上（[L125, L182-190](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/podtopologyspread/scoring.go#L125-L190)）。
  - `NormalizeScore` 按传入的 scores 求 min/max，再用 `MaxNodeScore*(maxScore+minScore-s)/maxScore`（[L229-268](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/podtopologyspread/scoring.go#L229-L268)）。
- **InterPodAffinity：** `NormalizeScore` 对传入 scores 做 min-max 线性映射到 [0,100]（[scoring.go L260-292](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/interpodaffinity/scoring.go#L260-L292)）。
- **`DefaultNormalizeScore`**（max 归一化，可 reverse）：[helper/normalize_score.go L27-50](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/helper/normalize_score.go#L27-L50)。被 NodeAffinity（[L298](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/nodeaffinity/node_affinity.go#L298)）和 TaintToleration（[L213](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/tainttoleration/taint_toleration.go#L213)）使用。

**不依赖候选集的插件：**
- **NodeResourcesBalancedAllocation：** `ScoreExtensions()` 返回 nil（[balanced_allocation.go L173-175](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/noderesources/balanced_allocation.go#L173-L175)），分数只由节点自身和 pod 请求决定。
- **NodeResourcesFit：** `ScoreExtensions()` 返回 nil（[fit.go L107-108](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/noderesources/fit.go#L107-L108)）。
- **ImageLocality：** 用全部快照节点数 `totalNumNodes := len(nodeInfos)`，不是候选节点数（[image_locality.go L70-78](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/imagelocality/image_locality.go#L70-L78)）。

**对设计的含义。** 若先按域排序再逐 pod 模拟，节点分数会随候选节点集（域）变化，所以"跨域直接比节点分"不成立。KEP 用独立的 PlacementScore 层，部分原因就在这里（该解读是我的推断，KEP 没有这样写）。

### 3.6 可逆性与状态克隆的先例（VERIFIED IN SOURCE CODE + DOCS）

- **Filter 并行：** "Nodes may be evaluated concurrently"（[scheduling-framework.md L102](https://github.com/kubernetes/website/blob/18f8e9f6d2b0bc9d33e82ceac8abcfffdf8c1ca8/content/en/docs/concepts/scheduling-eviction/scheduling-framework.md#L102)）。
- **Filter 与被修改的 nodeInfo。** 插件应看传入的 `nodeInfo` 而非 snapshot，因为抢占会传入去掉部分 pod 的副本（[interface.go L558-564](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/interface.go#L558-L564)）。
- **PreFilterExtensions。**
  - `AddPod` / `RemovePod` 用于增量更新预计算状态（[interface.go L500-515](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/interface.go#L500-L515)）。
  - 框架保证它们只在 PreFilter 后调用，"possibly on a cloned CycleState"，并可在同一次 Filter 前多次调用（L532-538）。
  - 抢占里使用：`DryRunPreemption` 为每个节点传 `state.Clone()` 和 `nodeInfo.Snapshot()`（[preemption.go L466](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/preemption/preemption.go#L466)），`SelectVictimsOnNode` 调 `RunPreFilterExtensionRemovePod` / `AddPod`（[default_preemption.go L316, L332](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/defaultpreemption/default_preemption.go#L316-L332)）。
  - PodTopologySpread 的实现：[filtering.go L156-183](https://github.com/kubernetes/kubernetes/blob/v1.37.2/pkg/scheduler/framework/plugins/podtopologyspread/filtering.go#L156-L183)（`updateWithPod` 以 ±1 增量更新）。
- **Reserve/Unreserve。**
  - "Unreserve … must be idempotent and may be called … even if the corresponding Reserve method … was not called"（[interface.go L676-681](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/interface.go#L676-L681)）。
  - 文档：Unreserve 按 Reserve 的相反顺序对所有 Reserve 插件执行，"must be idempotent and may not fail"（[scheduling-framework.md L194-201](https://github.com/kubernetes/website/blob/18f8e9f6d2b0bc9d33e82ceac8abcfffdf8c1ca8/content/en/docs/concepts/scheduling-eviction/scheduling-framework.md#L194-L201)）。
- **Clone 约定。**
  - `StateData.Clone`：[cycle_state.go L31-36](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/cycle_state.go#L31-L36)。注释写明："clone should make shallow copies for members … that are not impacted by PreFilter's optional AddPod/RemovePod methods"。
  - `CycleState.Clone()`：[L93-95](https://github.com/kubernetes/kubernetes/blob/v1.37.2/staging/src/k8s.io/kube-scheduler/framework/cycle_state.go#L93-L95)。

## Gaps

- **KEP 缺口。**
  - KEP 里没有 tie-break 规则。
  - 没有"模拟代价"的替代方案对比。
  - 没有 `PlacementStatePlugin`（已删除，不是遗漏）。
  - "按聚合资源预过滤 placement"没有落地代码（NOT FOUND）。
- **KEP 与代码的不一致。** 已见：
  - `ScorePlacement` 的 state 类型；
  - metrics 多了 `entity` 标签；
  - `PlacementProgress.Unschedulable`；
  - condition 名称；
  - KEP 声称的 "placement-scoped snapshot"，实际只是节点集限制。
- **PodTopologySpread / InterPodAffinity 在 placement 限定节点集下的真实行为。** 只做了静态阅读，PreFilter 看全集、Filter 看子集的组合语义 NOT VERIFIED。
- **节点级 `Randomizer`。** 无 extender 时是否为 0、tie-break 是否实际退化，是代码阅读推断，需要最小测试确认。
- **未精读的材料。** KEP-5710（WAP）、5729（DRA）、6012（CompositePodGroup）、6089（WAS Controller APIs）我只读了 kep.yaml 或 PR 标题，没有读完整文本。
- **k/k 阶段信息。** KEP 称 `GenericWorkload` 在 v1.38 stable，但 master 的 gate 仍是 Beta 1.37，可能是 KEP 先行，也可能是我读取时点早于该 PR，没有进一步核实。
- **GitHub code search 可能不完整。** 文中"只有这些文件给 Randomizer 赋值"和"PlacementFeasible 仅 gangscheduling 实现"两条结论依赖它，需要在本地 checkout 上 `grep` 复核。