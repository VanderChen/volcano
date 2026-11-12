# 004 Filter→Rank→Verify 原型：客户行为与回归分析报告

## 0. 范围与证据口径

- **快照**（行号以此为准）：
  - `master2`：`allocate.go` md5 `83ef4afb…`，`rank_verify.go` md5 `8ebb0dbc…`
  - `dev114`：`allocate.go` md5 `4b651ee5…`，`rank_verify.go` md5 `8b5f138a…`
- **分析期间 master2 被改动**（13:23–13:24 UTC）：新增 `subGroupJobTopK` 和 `preRankForSubGroupJob`（`allocate.go:134-137,510-514,578-580`，`rank_verify.go:552-596`）。简报未提及，我只做了初步分析。
- **证据三类**：
  - 【S】我的静态分析。本报告除显式标注外全部属于此类，全部未经运行验证。
  - 【T】协调者已取得的测试结果，我没有运行。
  - 【K】Kind 观察：目前没有任何 Kind 证据。
- **我的操作**：只读（`git status/diff`、grep、sed）。为定位 module cache 执行过一次 `go list -m`，事后 `git status` 确认无文件变化。
- **与简报不一致的事实**【S】：
  1. conf 键名是 `hyperNodeRankThenVerify` / `hyperNodeFitBound` / `deterministicTies`（`master2 allocate.go:161-163`），不是 `rankThenVerify`。
  2. dev114 的 `parseArguments`（`allocate.go:127-130`）没有改动，原型只能通过环境变量 `VC_PROBE_*`（`115-117`）开启。
  3. upstream BEFORE 的层内候选顺序是按名字排序的（`session_plugins.go:1292-1303`），不是随机。随机只发生在选优环节（job 级 map 顺序、SubJob 级和节点级 `rand.Intn`）。
  4. dev114 的 `RequiresHyperNodeAllocate` 包含软模式（`job_info.go:1385-1389`），所以软模式普通 Job 也会进入 ranked job 级路径。

**默认关闭声明核对**【S】：成立，前提是三个环境变量未设置且 conf 无上述键。
- 所有新分支都受 `alloc.rankThenVerify` 或 `alloc.deterministic()` 保护：`master2 allocate.go:501,511,578,658,781,802,1293`；`dev114 allocate.go:412,703,862,883` 及 `prioritizeNodes`。
- 关闭时 `verifyTopK` 恒为 0，`trace` 为 nil。
- dev114 新增的 `Statement.Merge`（`statement.go:442-449`）在关闭时无调用方。

## A. Before/After 对比表

"开关关闭"列统一为**无变化**（见上），表中省略。

| 维度 | BEFORE 可观察行为 | 开关打开后的变化 | 分类 |
|---|---|---|---|
| API / 配置 / 校验 | 无相关参数；CRD、PodGroup 注解 `volcano.sh/job-allocated-hypernode`、feature gate 不涉及 | master2 新增 3 个 action 参数和 3 个环境变量；dev114 只有环境变量。无校验：`hyperNodeFitBound` 单独开启静默无效（只在 `rank_verify.go:391` 使用）。`nodePreferenceWeight`、`subGroupJobTopK` 无配置入口。CRD、注解、默认值、feature gate 无变化 | 中性；产品化缺口见 R12 |
| 放置：HyperNode | 层内全量 dry-run 后选最高分。【T】相同 rack 时 40 次会话选出 4 个不同 rack | 按（分数降序、名字升序）取第一个验证通过者。【T】400 个随机场景与"全量+确定性"相比 0 差异。【S】仍可能不同的路径见 R1、R8、R9、R10 | 预期变更，附回归风险 |
| 放置：节点 | `util.SelectBestNodeAndScore` 同分随机 | `selectBestNodeDeterministic` 取名字最小。作用于**所有**经 allocate 的 Job，包括无拓扑 Job（`master2 allocate.go:444→1293`）。preempt、reclaim、backfill 仍随机 | 回归风险 R3 |
| Gang 语义 | job 级接受 Ready 或 Pipelined，不偏好 Ready；`minAvailable<replicas` 提交后重入 | 接受条件相同（`rank_verify.go:517` 对应 `allocate.go:569`）；重入和 worksheet 继承不变；`AllocatedHyperNode` 记账等价（见 C）。【T】fuzz 含 `minMember=tasks-1` 和部分运行 Job，0 差异。【S】fuzz 没有 Releasing/Pipelined 场景 | 中性 |
| SubGroup Job | job 级和 SubJob 级都全量 | job 级仍全量（除非 `subGroupJobTopK>0`），同分确定；SubJob 级走 ranked，打分 cohort 从"可行候选"变为"整层" | 预期变更，附 R8 |
| dev114 软模式 / 候选森林 | `preferJobSoftTopology*` 在 dry-run 后过滤；森林路径用 `saveCandidateForestDecision` | 见 C6；【S】未发现绕过。【T】dev114 套件兼容性未知 | 中性（未验证） |
| upstream nomination 快速路径 | 第一个不包含 pin 的 job 级候选会使 nomination 失效；候选按名字顺序 | 顺序变为排名顺序，nomination 能否兑现取决于 pin 所在域是否排第一 | 回归风险 R10 |
| 状态 / 条件 / 事件 / 消息 | 失败时留下层内最后一个候选的 `NodesFitErrors`。【T】upstream 是最后一个，dev114 是另一个。成功时留下的也是最后评估者而非胜者。Pod 消息中的节点名受随机同分影响 | 失败时留下排名最后的候选（随分数变化）；成功时留下胜者自身的 fit errors；节点名稳定。fitBound 剪枝的候选不产生 `LastTransaction` 和 `NodesFitErrors`，剪枝原因只在 trace 里 | 预期改善，附 R4、R5 |
| 不可调度缓存（upstream alpha） | 每个可达候选的 rejection 都被收集，按节点和维度建 hint key | 规则 #5 覆盖"胜者有 rejection 且还有未评估候选"的情形。【T】58 个包在 ranked 和 ranked+fitBound 下通过。【S】仍有 rejection 更少的路径 | 回归风险 R2、R6、R7 |
| 插件状态与可逆性 | 【T】T 个 task、R 个 rack 时 Allocate 2·T·R+T 次、Deallocate 2·T·R 次；胜者用克隆的 TaskInfo 重放 | 【T】Allocate T 次、Deallocate 0 次。【S】按值记账的插件终态一致；Reserve、vGPU、task-topology 的差异见 C4 | 预期变更（多为改善，未验证），附 R9 |
| 发布 / 恢复 | — | 无新增持久化状态，重启、主备切换、会话间切换开关都安全。删除 conf 键不会关闭开关；多调度器同分会趋同 | 中性，附 R12、R3 |
| 可用性 / 性能 | 【T】T=4、R=32 时 260 次 Allocate、256 次 Deallocate | 【T】降到 4 次和 0 次；fuzz 总量 Allocate −70%、predicate −45%。【S】全不可行层、fallback、fitBound 分组、trace 有额外开销。队列和 Job 的重排逻辑未改 | 预期改善，附 R7、R13 |
| 升级兼容 | 第三方 `HyperNodeOrderFn` 只在 dry-run 之后、只对可行候选被调用 | 改为 dry-run 之前调用，cohort 包含不可行候选，job 级每个候选单独调用一次，返回 error 时更早中止 | 回归风险 R8 |

## B. 回归风险清单（按严重度排序，全部为【S】且未验证）

**R1（高；两条基线；需开 fitBound）— `fitUpperBound` 不是上界，会误剪枝**
- 机制：`copiesThatFit` 用 `InitResreq` 作除数（`master2 rank_verify.go:290-293,305,318-339`；`dev114 rank_verify.go:191-194,206,219-240`）。节点记账扣的是 `Resreq`（`api/node_info.go:444-451,482`）。
- 当 initContainer 请求大于业务容器之和时，真实可放数量大于 `floor(avail/InitResreq)`。例：节点 4 CPU，init 2 CPU，业务 0.5 CPU，实际可放 5 个，界算出 2 个。结果是今天能调度的 Job 变成不可调度。
- 协调者测试：未覆盖。`util.BuildPod` 只有单容器，`InitResreq == Resreq`。
- 最小测试：uthelper，1 个 rack、1 个 4 CPU 节点，hard tier-1 Job 共 5 个 Pod（手工加 InitContainers）。断言 ranked+fitBound 的绑定数与全量模式相同，都是 5。

**R2（高；upstream；fitBound + `UnschedulableJobCache`）— 被剪枝的候选不产生 rejection，丢失唤醒 hint**
- 机制：剪枝在 `CollectJobRejections` 之前直接 `continue`（`rank_verify.go:391-396`）。resourcefit 的 hint key 是按节点和维度建的（`plugins/util/resourcefit/resourcefit.go:97-131`）。
- `mustCollectAllRejections` 用 `i+1` 计数（`rank_verify.go:111-113,422`），把剪枝的候选也算作"已评估"，所以 fallback 不会补齐。
- 后果：其它候选留下 rejection 时 Job 会被缓存，被剪枝 rack 上释放资源不会唤醒它，直到其它事件发生。
- 协调者测试：probes 没有开启该 gate；现有套件通过不能证明这一点。
- 最小测试：gate 开启，rack-a 被剪枝（4 个里只放得下 3 个），rack-b 过界但因 taint 失败。断言记录的 `HintKeys` 包含 rack-a 节点的 `pod-release/...` 且与全量模式相等。第二个会话在 rack-a 释放一个 Pod 后，Job 不得被 `Skip.Allocate`。

**R3（中高；两条基线；`rankThenVerify` 或 `deterministicTies` 任一开启）— 节点同分固定取名字最小，且影响所有 Job**
- 机制：`master2 allocate.go:1293-1294`，`rank_verify.go:598-616`。
- 后果：
  - 没有 nodeorder 区分度时退化为按名字 first-fit，热点集中在字典序靠前的节点和 rack。
  - 软模式 Job 的第一个 task 没有锚点，会全部落到同一个 rack。
  - 多调度器或软分片（`prioritizeNodes` 里的 `SoftShardingMode`）下各实例选同一个节点，bind 冲突概率上升。
  - 与 preempt、backfill 的随机行为不一致。
- 提案中的"hash(job UID, candidate)"没有实现。节点级需要按 (task, node) 取 hash。
- 协调者测试：P3/Q3 只证明了确定性，没有度量分散性和冲突。
- 最小测试：N 个相同的空节点、20 个单 Pod 无拓扑 Job、不启用 nodeorder，断言节点分布。Kind 上用双调度器或分片测 bind 冲突数。

**R4（中；两条基线；fitBound）— 客户可见的状态文本和 reason 变化**
- 机制：被剪枝的候选不做 dry-run，因此没有 `Discard` 触发的 `GenerateLastTxContext`（`framework/statement.go:381`），也不写 `NodesFitErrors`。
- 后果：
  - `TaskSchedulingReason`（`api/job_info.go:983-1016`）对这些 Pod 从 `Schedulable`（"can possibly be assigned to X"）变成 `Unschedulable`。
  - gang 条件里的 `N/M tasks in gang unschedulable`（`gang.go:241-253`）中 N 变大。
  - `FitError()` 的 Pending 直方图变化。
  - 全部候选被剪枝时，节点级原因消失。"resources fit at most k of n"只写进 trace（`rank_verify.go:394`），不进 `JobFitErrors`。
  - 依赖 `Unschedulable` reason 的自动化（如 cluster-autoscaler）行为可能变化。
- 协调者测试：未覆盖。Q4 只断言 trace，Q2 只比较放置。
- 最小测试：4 个 Pod 中 3 个放得下的场景，对比两种模式的 `FitError()` 和 `TaskSchedulingReason`。Kind 上检查 Pod 和 PodGroup 的 condition。

**R5（中；两条基线）— 失败消息的归属由分数决定，可能增加抖动**
- 机制：BEFORE 留下的是名字最后的候选，稳定。AFTER 留下的是排名最后的候选（`rank_verify.go:193-198`），NTA binpack 分数随利用率变化。
- 后果：`fitErrors.SetHyperNode` 和节点直方图跟着变，可能增加 PodGroup condition 和 Pod status 的写入次数（对应 #6196 一类问题）。
- 反向收益：节点同分确定后，"assigned to <node>"里的节点名更稳定。净效果未知。
- 协调者测试：P5 只刻画了基线。
- 最小测试：两个会话之间只改变某个 rack 的利用率，断言不可调度 Job 的消息是否变化。Kind 上统计 N 个周期内 PodGroup 的 `resourceVersion` 增量。

**R6（中；upstream alpha）— 不开 fitBound 时仍有 rejection 更少的路径**
- (a) 胜者没有 rejection 时不触发 fallback。只记录排名在它之前的失败候选（`rank_verify.go:435-438,529-532`），全量模式还会记录排名在它之后的可达候选。当 SubJob 级胜者所在的 job 级候选最终失败时，`recordJobRejections`（`allocate.go:615,738`）的集合是全量模式的子集。
- (b) `subGroupJobTopK` 提前 `break`（`allocate.go:578-580`），没有对应的 fallback。
- 协调者测试：规则 #5 修复的那个用例只覆盖"胜者有 rejection"。
- 最小测试：SubGroup Job，SubJob-1 在 W1 成功，排名更低的 X1 会拒绝它，SubJob-2 处处失败。断言会话结束时缓存的 rejection 和 hint key 与全量模式相等。

**R7（中；upstream alpha）— fallback 让弹性 Job 比今天更慢**
- 机制：胜者带 rejection 时（典型是 `minAvailable<replicas` 的富余 task）丢弃后整层重跑（`rank_verify.go:422-427,518-525`）。成本是 ranked 尝试加全量。
- 协调者测试：只证明了正确性，没有度量成本。
- 最小测试：gate 开启下跑 `BenchmarkProtoAllocate` 的弹性 Job 变体，统计 Allocate 和 predicate 计数。

**R8（中低；两条基线）— SubJob 级打分的 cohort 改变**
- 机制：NTA 只在最高 binpack 分出现并列时才加 task 数奖励（`network_topology_aware.go:391-405`）。全量模式的 cohort 是可行候选，ranked 是整层（`rank_verify.go:372`）。
- 触发条件：SubJob 自己已有部分 task 落位，SubJob 级 tier≥2，可行候选之间精确同分，并且存在分数更高但不可行的候选。此时全量模式选 task 多的域，ranked 按名字选。
- 返回的 `rec.Score` 还会进入 SubGroup Job 的 job 级求和。
- 做 cohort 归一化或依赖 dry-run 副作用的第三方 `HyperNodeOrderFn` 受影响更大。
- 协调者测试：未覆盖。fuzz 的 case 3 是普通 Job，job 级 cohort 恒为 singleton，奖励项不会触发。
- 最小测试：SubGroup Job，SubJob hard tier 2，SubJob 有 2 个 Running Pod 在 rack-x；rack-c 与 rack-x binpack 同分，rack-b 分数更高但被 taint。断言新增 task 落在 rack-x。

**R9（中低；两条基线）— `task-topology` 插件只有 `AllocateFunc`，没有 `DeallocateFunc`**
- 机制：`topology.go:207-215,350`；`TaskBound` 不可逆（`manager.go:315-329`，`bucket.go:103-109`）。BEFORE 的每次 dry-run 都留下幻影计数，AFTER 的事件数量和顺序不同。
- 后果：同时使用 task-topology 注解和 hard 拓扑的 Job，rack 内选点和 task 顺序可能变化。更接近正确行为，但属于行为变化。
- 协调者测试：未覆盖。
- 最小测试：带 task-topology 注解的 hard Job，对比全量确定性模式和 ranked 的 pod→node 结果。

**R10（中低；upstream）— nomination 快速路径对候选顺序敏感**
- 机制：`allocateFromNomination` 在约束不满足时会 `invalidateSubJobNomination`（`allocate.go:840-856,904-923`），并经 `MarkJobDirty` 持久化。ranked job 级把候选顺序从名字改成排名（`rank_verify.go:466,482-496`）。
- 后果：gangpreempt 钉住的域如果不是排名第一，nomination 会被清除。
- 协调者测试：现有套件通过；没有"pin 不在第一位"的专门用例。
- 最小测试：hard Job，`NominatedHyperNode=rack-b`，rack-a 排名更高。断言 task 落在 rack-b 上被 nominate 的节点，且行为与全量模式一致。

**R11（中低；upstream；fitBound + `SchedulingGatesQueueAdmission` alpha）— 剪枝跳过门控移除**
- 机制：`SchGateManager().Enqueue` 在 dry-run 循环里（`allocate.go:1039-1043`），被剪枝的候选不会执行到。
- 后果：Pod 更久停留在 `SchedulingGated`。
- 基线已有同类行为：`FilterGradientsByMinResource` 过滤完所有候选时也不会移除门控。fitBound 扩大了这个范围。
- 最小测试：带门控的 Pod，所有 rack 都被界剪枝，断言是否入队移除门控。

**R12（低中；发布）— 开关的粘性和隐藏入口**
- `GetBool` 在键缺失时不改值（`framework/arguments.go:82-90`），而 Action 是进程级单例（`actions/factory.go:37`）。从 conf 删除键不会关闭开关，必须显式写 `false` 或重启。
- `VC_PROBE_*` 环境变量是隐藏的配置入口。
- dev114 没有 conf 入口。
- 最小测试：连续两个会话，先给 `true` 再删除键，断言行为。

**R13（低）— 额外开销**
- job 级每个候选一次 singleton 打分（`rank_verify.go:157-166`）。全不可行的层，BEFORE 不打分。
- 每个失败候选都会构造 `verifyFailureReason` 字符串（`342-356,418,541`），即使 `trace==nil`。
- `fitUpperBound` 每个候选都重新 clone 并 pop worksheet，调用 `InitResreq.String()`，每个节点 clone 一次 `FutureIdle()`（`287-305`）。
- `trace` 在 `Execute` 里不清零，若在生产启用会无界增长。
- `nodePreferenceWeight>0` 时对整层节点做一次节点打分，并在 rejection 作用域之外调用 `PrePredicateFn`（`rank_verify.go:219`）。
- 最小测试：全不可行场景下 R=500、T=1000 的 benchmark。

**R14（低；健壮性）— 可能的空指针和 NaN**
- `job.SubJobs[job.DefaultSubJobID()]` 没有判空（`rank_verify.go:459`）。
- `ssn.HyperNodes[name]` 为 nil 时 `rankHyperNodes` 解引用（`allocate.go:803-806`）。
- NaN 分数会破坏排序比较函数。
- 可达性我没有确定。

## C. 代码路径正确性复核【S】

**1. 放置记账：与全量路径等价**
- SubJob 级顺序是 capture（`401`）→ dry-run（`402-406`）→ restore（`413`）→ 胜者 LCA 和 `updateJobAllocatedHyperNodeFromSubJob`（`431-433`）。
- 全量路径（`allocate.go:674→698→720-722`）最终字段相同，包括 `AllocatedHyperNodeDirty`。
- job 级 `RecoverSubJobStatus` 在闭包内（`508`），早于 Ready/Pipelined 判定（`517`），与全量路径（`552,569`）一致。
- `SaveSubJobDecision`（`439`）和 `SaveJobDecision`（`533`）的键相同；提交点的 `UpdateDecisionToJob` 没有改。
- fallback 时，被放弃的 ranked 尝试写入的 `subJobDecisions` 会被全量重跑以相同的键覆盖。

**2. Worksheet：等价**
- `ShallowCopyFrom`（`434,528`）和 SubJob 的重新入队（`500-502`）与全量路径相同。

**3. fit error 重置点**
- job 级在闭包内重置 baseline 并调用 `ResetFitErr`（`490-491`），等价。
- SubJob 级只在未剪枝时调用 `ResetSubJobFitErr`（`403`），剪枝后保留上一个候选的错误（见 R4）。

**4. live 语句与 Discard+RecoverOperations 的差异**
- `TaskInfo.Clone` 丢弃 `JobAllocatedHyperNode` 和 `EvictionOccurred`（`api/job_info.go:347-386`）。live 路径保留 `JobAllocatedHyperNode`。分配之后我没有找到读取它的地方（NTA 只在打分时读，`network_topology_aware.go:589`），下一次快照 clone 时会清掉。
- `Pod.Spec.NodeName` 的语义相同。两条路径的 `unallocate` 都不重置它。
- Reserve 行为改变：
  - BEFORE 重放时 `runReservePlugins`（`predicates.go:875-890`）读取按 task UID 存放的 CycleState，它可能已被后续候选的 PreFilter 覆盖。
  - 对 volumebinding，`podVolumesByNode[node]` 缺失会让 `allBound=true`（k8s v1.37.1 `volume_binding.go:534-544`）。
  - AFTER 在 dry-run 当次完成 Reserve，同时消除了 `RecoverOperations` 失败且不回滚的路径。
  - 这很可能是改善，但我无法确认基线缺陷是否真实可触发。
  - SubGroup Job 的 job 级仍然重放，这条不适用。
- dev114 vGPU：`Allocate` 内直接 `patchPodAnnotations` 调用 API（`devices/nvidia/vgpu/device_info.go:289-315`）。AFTER 的 patch 次数从约 2TR+T 降到约 T，#5335 一类的残留窗口变小但没有消除（排名更高的失败候选仍会留下）。
- `LastTransaction` 的陈旧语义两种模式同类：Pipelined 的胜者可能显示更早失败候选上的节点。
- `Statement.Merge`：按参数顺序追加，源语句清空。Commit 正序、Discard 整体逆序，无重复提交或重复丢弃。dev114 的 backport 与 upstream 语义一致。

**5. rejection 记账**
- 截断点 `rejectionsBefore` 在层入口捕获（`370,460`），fallback 截断（`424,522`）保留更早层的累积，正确。
- 嵌套作用域的归属正确。
- 缺口见 R2 和 R6。

**6. dev114 特有部分**
- `allowedRoots` 只影响 `SaveSubJobDecision` 的保护条件（`rank_verify.go:333-335`），与全量路径（`allocate.go:768-770`）一致；梯度过滤在进入循环前已完成（`677-681`）。
- 软域偏好：`preferJobSoftTopologyCandidates` 读取的状态在 dry-run 前后相同（因为有 restore）。"偏好集在前的稳定排序"（`283-295`）与"可行候选中有偏好的就只留偏好的"等价，差别只在 cohort（R8）。
- `preferJobSoftTopologyScoreCandidates`（`allocate.go:494`）要求 `ContainsSubJobPolicy`，而 ranked job 级只用于无 SubGroup 的 Job，两者不相交。
- 候选森林：job 级不走 ranked（`412`），`captureCandidateForestPlacement` 和 `saveCandidateForestDecision`（`455,513-516`）没有被绕过；SubJob 级 ranked 设置的 `AllocatedHyperNode`（`329-331`）能被 capture 读到。

**7. 无 SubGroup Job 的"job 分 = singleton 分"**
- 成立的原因是 `SubJobReady` 和 `SubJobPipelined` 对这类 Job 直接委托给 `JobReady` 和 `JobPipelined`（`session_plugins.go:437-440,461-464`，两条基线相同），因此每个候选只跑一轮。
- 我能找到的唯一例外是 Pipelined 且 worksheet 有剩余、第二轮又放成功，这时 BEFORE 的分数是两轮之和。这个路径很窄，我没有验证能否触发。

**8. 非默认投票配置**
- 【T】关闭 `EnabledSubJobPipelined` 时 fitBound 多调度了 3/400 个 Job。
- 【S】关闭 `EnabledJobPipelined` 时 job 级有同类分歧。

## D. Kind 上必须验证的内容（目前【K】为空）

1. **真实 bind 路径**：hard tier-1 和 tier-2 gang 在多个等价 rack 下调度。确认运行的 Pod 镜像是原型构建，Pod 落点满足拓扑，PodGroup 注解 `volcano.sh/job-allocated-hypernode` 正确。
2. **不可调度时的文本**：采集 PodGroup `Unschedulable` condition 和事件、Pod `PodScheduled` 的 reason 和 message、`FailedScheduling` 事件。对比关闭、ranked、ranked+fitBound 三种模式（R4）。连续 20 个以上周期统计 PodGroup 和 Pod 的 `resourceVersion` 增量（R5）。
3. **重启恢复**：gang 部分运行（`minAvailable<replicas`）后重启调度器，再扩容。新 Pod 仍应落在 `AllocatedHyperNode` 约束内。会话之间切换开关重复一遍。
4. **运行中 gang 的多副本扩容**：tier 2，已有 Pod 跨 2 个 rack。观察新 Pod 落在哪里（R8）。
5. **WaitForFirstConsumer PVC + hard 拓扑 + 至少 2 个可行 rack**：PVC 必须绑定到所选节点。基线也要跑，用来确认 C4 中 Reserve 的推断。
6. **DRA 和 vGPU（如环境具备）**：检查 claim 分配是否与所选节点一致，Pod 的 vGPU 注解是否与最终节点一致。在 dev114 上统计 apiserver 审计日志里的 Pod PATCH 次数。
7. **双调度器或分片**：同分场景下的 bind 冲突和失败事件数（R3）。
8. **带 initContainer 的 Pod**：init 请求大于业务容器，开 fitBound（R1）。
9. **upstream gate**：`UnschedulableJobCache` + fitBound 的唤醒（R2）；`SchedulingGatesQueueAdmission` + fitBound（R11）。
10. **配置回滚**：从 ConfigMap 删除键后行为是否真的回退（R12）。

## E. 未能确定的事项

- dev114 全套单测在 ranked 模式下是否通过。【T】仍在运行，结论未知。
- C4 所述 BEFORE 的 Reserve/CycleState 缺陷在真实集群能否触发。它与 dev114 的 `TestAllocateWithPVC` 抖动是否相关，我没有分析。
- R10 中 gangpreempt 为 hard 拓扑 Job 设置 nomination 的完整链路，以及现有测试是否覆盖"pin 不在第一位"。
- R5 的净效果，即消息写入次数是增是减。这需要实测。
- dev114 的 `applyPreferredHyperNodeGradientOrder` 的具体顺序语义，以及 SubGroupTopologyAffinity 路径与 ranked SubJob 级的交互。我没有逐行审阅。
- R14 两处空指针是否可达。
- `subGroupJobTopK` 只做了初步阅读，它的放置差异和 rejection 缺口没有深入分析。
- cluster-autoscaler 对 reason 变化的实际反应（R4）。