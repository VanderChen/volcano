# 第五轮客户行为与回归分析：§4.7 关系约束、§4.8 权重

**约定。** `dev:` 指 `/tmp/vc-r5-dev`（dev-1.14 加第五轮原型，行号是打补丁后的工作树），`up:` 指 `/tmp/vc-r5-up`。文件简称：

- GTA.go = `pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go`
- NTA.go = `pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go`
- alloc.go = `pkg/scheduler/actions/allocate/allocate.go`，rel.go = 同目录 `relation_scope.go`
- tai.go = `pkg/scheduler/api/topology_affinity_info.go`
- wh.go = `pkg/webhooks/admission/podgroups/validate/validate_podgroup.go`
- rel.log、w-dev.log、w-up.log = `evidence/round5/` 下的关系探测和两条基线的权重探测日志

我没有运行任何测试。"测试结果"都引自现有日志，"静态分析"是读码，"推断"是由读码推出、没有证据直接支持的结论。

## 1. 结论

1. §4.7 的分型与代码读法一致，日志支持"修复两类漏解、丢失 0、落位变化 1"。
2. "现行能调度的仍能调度"只是这批样本的结果。读码有一个反例：亲和组的容量筛选用第一个成员自己的排除集计算整组可用量，不开重试时会丢掉今天能调度的作业（F1，未经测试验证）。
3. 反亲和放宽另有三个缺口：比较层在候选之下不存在时，约束从"全部拒绝"变成"不生效"（F2）；dev-1.14 的提名节点捷径绕过节点排除（F3）；失败信息不再说明原因（F4）。
4. §4.8 的公式、参数名、默认值与两条基线的代码相符，8 行实测在两条基线上与公式一致。有三处表述要改：例子关掉了内存权重；并列加分触发时"W=100 必胜"今天不成立；§4.3.4 改成第三键会改变部分运行单元上的有效权重（F6、F7）。
5. 建议 F1–F4 在实施前处理，设计文档按 F5–F10 修改，§9 补第 6 题的用例。

## 2. 逐题回答

### 第 1 题：各关系约束的现行行为

**API 与校验【静态分析】**

- **字段。** `spec.topologyAffinity.{podGroupAntiAffinity, subGroupAffinity, subGroupAntiAffinity}.{required, preferred}[]`（dev: `staging/src/volcano.sh/apis/pkg/apis/scheduling/v1beta1/types.go:303-372`）。上游只有 `podGroupAntiAffinity`（up 同文件 :304-333）。
- **默认值。** 没有；这些类型上没有 `kubebuilder:default`。
- **CRD。** `weight` 出现时必须是 1–100，`topologyTier` ≥ 0，`subGroups` 至少一项。
- **Webhook 规则**（Helm 默认启用 `/podgroups/validate`，dev: `installer/helm/chart/volcano/values.yaml:48`）：
  - required 项不许写 weight（wh.go:181、201）。
  - preferred 项的 weight 必须是 1–100（:184、204），所以省略或写 0 在创建时被拒。
  - 未定义的策略名被拒（:220）。
  - 亲和至少两个名字（:210）；单名字反亲和要求 `matchLabelKeys` 或 `minSubGroups ≥ 2`（:225）。
  - tier 与 tierName 二选一（:189-194、228-233）。
  - 同名策略的亲和层不得低于反亲和层，只比较数字层（:245-264）。
- **基线差别。** dev-1.14 只校验 Create（wh.go:48），上游校验 Create 和 Update（up wh.go:48）。dev-1.14 的 `namespaceSelector` 不起作用，只匹配同命名空间（tai.go:220-226）；上游按 Namespace 标签解析（up `constraints.go:152-190`）。
- **不校验层是否存在。** dev-1.14 的设计文档写着要校验（dev: `docs/design/group-topology-affinity.md:1021`），代码没有。

**异常输入在调度时的表现【静态分析】**

| 输入 | 表现 | 位置 |
| --- | --- | --- |
| required，未知 tierName | 候选全部被拒，Pending | GTA.go:569、623、650 |
| required，数字层在树上不存在 | 候选全部被拒，Pending | GTA.go:577、629、669 |
| preferred，未知 tierName | 打分函数返回错误，子组本轮调度不了，条件里没有原因 | GTA.go:753、776、816、857；alloc.go:807 |
| preferred，数字层不存在 | 不扣分，静默无效 | GTA.go:785 |
| weight 超出 1–100（绕过 webhook 时） | 该项被忽略 | GTA.go:779、811、852 |

**求值位置【静态分析】**

| 约束 | required | preferred |
| --- | --- | --- |
| PodGroup 反亲和（dev-1.14） | Job 级和子组级 gradient（GTA.go:98-130、132-168）；作业没有硬拓扑时走候选森林（alloc.go:582-588） | 域打分，对每个子组的候选求值（GTA.go:684-737）；Job 级分数是子组分数之和（alloc.go:482） |
| PodGroup 反亲和（上游） | Job 级和子组级 gradient 共用一个函数，另有提名校验（up GTA.go `hyperNodeGradient`、`AddHyperNodeCandidateFn`） | 域打分（up GTA.go `hyperNodeOrderFn`） |
| SubGroup 亲和（dev-1.14） | 只在子组级 gradient（GTA.go:612-643） | 域打分（:802-841），并改变 Job 级层序（:207-258） |
| SubGroup 反亲和（dev-1.14） | 只在子组级 gradient（:645-679） | 域打分（:843-880），并改变 Job 级层序 |

**不可满足时用户看到的【静态分析】**

- PodGroup 条件是 `N/M tasks in gang unschedulable: …; HyperNode: a/b hyperNodes available: <层> f/t (k podGroupAntiAffinity, …); subJob <id>: …; Node: …`（dev: `pkg/scheduler/plugins/gang/gang.go:237-238`，`pkg/scheduler/api/unschedule_info.go:168-235`）。
- GTA 排除的域一律标成 `podGroupAntiAffinity`，SubGroup 约束也是（`unschedule_info.go:111-114`）。
- V(3) 日志带原因：`conflictWithMatchingPodGroup`、`notWithPeerSubGroups`、`conflictWithPeerSubGroup`、`emptyAncestorHyperNode`、`resolveTermTierFailed`。

**提案会改变的**

- API、默认值、校验不变。
- required PodGroup 反亲和和 required SubGroup 反亲和从"按域名过滤候选"改成"保留候选、排除节点"。
- required SubGroup 亲和的第一个成员多一道整组容量筛选。
- preferred 的公式不变，只是打分提前。
- 摘要里的计数和原因会变（F4）。

### 第 2 题：第五轮原型前后

对照表见第 3 节。四点补充：

- "现行"一列是原型关掉开关、并列固定的配置，不是未修改的二进制（`probes/relations-dev114/zz_probe_relations_test.go:219`）。【静态分析】
- 随机对照是 348、348、391、350、394、397，丢失全为 0，落位变化 0、0、0、1、1、1，与 §4.7.4 一致（rel.log 的 `:532` 各行）。【测试结果】
- preferred 的 600 个场景只比较了"现行"与"只排序"，没有打开第五轮开关，也没有重试（同文件 :314-316）。【静态分析】
- 探测里每个子组都带硬层上限，作业都是严格 gang、全新、没有 PodGroup 反亲和（`probes/prototype2/zz_probe_round2_test.go:134-146`，关系探测 :66-86）。【静态分析】

### 第 3 题：两处原型改动的回归风险

**3.1 会不会丢作业或换位置**

那一例落位变化（seed 24）的经过：

- 作业是 p0（2 个 4 核 Pod）、p1、p2，p0 与 p1 机架亲和，p2 与 p0 机架反亲和（rel.log `:538`）。【测试结果】
- 现行：在 z0 里 p0 选了更满的 z0-r00，只剩 8 核，p1 进不去，z0 失败，落在 z1。
- 开关打开：z0-r00 提供不了整组的 12 核，被筛掉，p0 和 p1 进 z0-r02，z0 也成功。
- 两个 zone 都成功后按 Job 级分数选了 z0（我手算约 103 对 97）。带 SubGroup 策略的作业在 Job 级仍是逐个试放再比分（alloc.go:450）。【推断】

**同一机制能丢作业（F1）。**【静态分析，未测】整组可用量用 `nodesInScope` 计算（rel.go:127），而它会去掉当前子组自己的反亲和排除集（`scope_repair.go:81`，排除集在 alloc.go:724 设置）。同组的其他成员未必受这条排除约束。

反例：
- 拓扑：1 个 zone，2 个机架，各 1 个 8 核节点。
- 子组顺序：p2（1 个 4 核 Pod）、p0（2 个 4 核 Pod）、p1（1 个 4 核 Pod），都限定在机架内。
- 约束：p0 与 p1 在 tier 2 亲和，p2 与 p0 在 tier 1 反亲和。webhook 允许这种配置（wh.go:255）。
- 现行 4/4：p2 占机架 A 的 4 核，p0 占满机架 B，p1 进 A。
- 开关打开、不重试：p0 的整组需求是 12 核，可用量只算 B 的 8 核，候选被筛光，作业 0/4。预算至少 1 的重试能救回。
- 随机生成器命中这种形状的概率约 0.14%，所以"丢失 0"不能排除它。【推断】

读码排除了的丢失途径【静态分析】：

- 弹性作业不筛选（rel.go:94）。
- 同组已有成员落位时不筛选（rel.go:109-112）。
- `FutureIdle` 与任务级资源谓词一致（alloc.go:1301），已预留的资源本来就不能用。
- 严格 gang 下被调度门控的 Pod 本来就使作业无法就绪，多算它的需求不造成新的丢失。

**反亲和放宽也能改变 Job 级的选择（F5）。**【推断，未测】同层的另一个 Job 级候选域由失败变为成功后，可能凭分数胜出，子组因此跨多个机架，而原来选中的域里它能放进一个机架。没有硬层上限的子组现在最宽可以跨到整个 Job 容器。

**3.2 组合情形**【静态分析；全部没有测试】

| 情形 | 结论 |
| --- | --- |
| PodGroup 反亲和加 SubGroup 约束，森林路径 | 子组候选先经 PodGroup 反亲和过滤，再经森林根过滤（GTA.go:153-157，alloc.go:728-732）。放宽出的宽候选仍在允许的域内，PodGroup 反亲和不被削弱 |
| 森林路径与重试 | 森林路径不记录可重试的候选（alloc.go:516），顺序类漏解在该路径上不被重试修复 |
| 同伴已绑定（Pod 重建、扩容） | 排除集按同伴任务的实际位置计算（tai.go:506-527）；亲和筛选因同伴已落位而跳过 |
| 子组已有 `AllocatedHyperNode` | 两道总量检查都跳过（`scope_repair.go:342`），宽候选可能白试一次，不影响正确性 |
| 同伴既有已分配又有 Pipelined 的任务 | dev-1.14 只统计已分配的任务（tai.go:381）。子组跨域后，Pipelined 任务所在的域不在排除集里（F13） |
| 提名域 | dev-1.14 没有；上游有，但没有 SubGroup 关系约束 |
| 抢占、回收 | dev-1.14 不调用 gradient 回调。上游以 `PurposeEvict` 调用（up `pkg/scheduler/actions/utils/util.go:89`、`simulate.go:93`），原型在该用途下不启用可用域（up `pkg/scheduler/framework/session_plugins.go` 的原型 diff） |

**3.3 放宽之后约束是否仍被强制**【静态分析】

- dev-1.14 上子组 gradient 只有一个调用点（alloc.go:727），排除集在它之前设置。
- 排序路径和逐个试放路径都经过 alloc.go:974 的节点过滤，没有发现"放宽生效而排除不生效"的分支。
- **例外一（F3）。** 提名节点捷径直接取 `ssn.Nodes[NominatedNodeName]`，不检查它是否在过滤后的节点列表里（alloc.go:1019-1025；未修改代码在 HEAD:926-930）。dev-1.14 的抢占只跳过带 `networkTopology` 的作业（dev: `pkg/scheduler/actions/preempt/preempt.go:138-141`），只带 `topologyAffinity` 的作业可以被提名。这是现存漏洞；提案把强制手段全部移到节点过滤之后，它成了唯一的旁路。
- **例外二（F2）。** 候选高于比较层、但它下面没有该层的域时，同伴占用集为空，候选被接受，排除集也为空（GTA.go:656-668，rel.go:66-82）。例如 `topologyTier: 0`，或树上缺这一层。现行是全部拒绝并 Pending。
- **结构性风险（F14）。** 放宽在插件里，排除在 allocate 里。以后任何新的 gradient 调用方都会拿到放宽的候选而没有排除。原型还不看 GTA 是否启用。
- backfill 不看任何拓扑约束，现状如此，提案不改。

**3.4 状态与诊断（F4）**【静态分析】

- 亲和组筛选删掉的候选不进任何统计。候选被删光时，摘要仍按筛选前的计数写"k/n hyperNodes available"，没有原因（alloc.go:740-745）。
- 节点排除不产生文本。域内节点全被排除时只有一条 V(4) 日志（alloc.go:976）。
- 宽候选在摘要里计为可用。
- RA3 类场景的失败点从后放的成员（带 GTA 标签）移到第一个成员（没有标签）。

**3.5 开关回退**【静态分析，未测】

- 旧代码只按任务位置检查同伴，跨多个比较层域的子组在旧代码下仍然合法，不会被驱逐。
- 重建单个 Pod：能进任何不被同伴占用的比较层域。
- 同一子组同时重建多个 Pod：必须挤进一个比较层域，放不下就 Pending，表现与修复前相同。

### 第 4 题：作业之间的对称性与先后

**确认单向。**【静态分析】约束只从正在调度的作业自己的条目构造（dev GTA.go:98、360；up `constraints.go:118-150`），按它的选择器找对端（tai.go:203-218、564-600）。没有任何代码反查"谁声明了要避开我"。

| 情形 | 用户观察到的 | 提案是否改变 |
| --- | --- | --- |
| A 声明，B 不声明，B 后到 | B 可以进入 A 的域（第二轮 RB5 实测，`subagents/behavior-regression-analysis-round2.md:45、158`）。之后 A 重建的 Pod 把自己原来的域视为被占用，只能去别处或 Pending【推断】 | 不变。§4.7.6 明确不做对称 |
| 两者都声明，同一周期都 Pending | 先被处理的先落位，后者避开，没有域可用时后者 Pending | PodGroup 反亲和改为可用域后，排在前面的作业能把剩余域用完（第二轮 RB6 实测，§10.3 第 2 项）。第五轮改动不涉及作业之间 |
| 对端只是 Pipelined | dev-1.14 不算占用（tai.go:365；试放后的落位记录被恢复，只在提交时写入，alloc.go:335-338）。上游把带节点名的 Pipelined 任务算作占用（up tai.go:140-149） | 不变。两条基线不同，§4.7.1 的表述没有区分 |

### 第 5 题：权重

**§4.8 与代码核对【静态分析】**

- **只有两处用权重。** 域打分只有 GTA 和 NTA 注册（dev GTA.go:82、NTA.go:267；up GTA.go:104、NTA.go:307），框架直接相加（dev: `pkg/scheduler/util/scheduler_helper.go:140-172`）。
- **NTA 域分。** `100 × weight × b`（NTA.go:743-748）。并列最高者另加已有任务占比，范围到 `200 × weight`（:364-369）。`weight` 默认 1，负数回落到 1，0 合法（:160-169）。`hypernode.binpack.cpu`、`.memory` 默认 1。
- **GTA 域分。** `100 × weight × g`，g 从 1 起每命中一项扣 W/100，下限 0（dev GTA.go:721、785-790；up `hyperNodeOrderFn`）。`weight` 默认 1（GTA.go:52-57）。
- **节点级。** nodeorder 的八项默认值相符（dev: `pkg/scheduler/plugins/nodeorder/nodeorder.go:136-143`）；binpack 相符（dev: `pkg/scheduler/plugins/binpack/binpack.go:113-150`）；NTA 节点分相符（NTA.go:432-485、532-560）。
- **表里漏掉的参数。** `hypernode.binpack.resources.<名字>` 只有当资源同时列在 `hypernode.binpack.resources` 里才生效（NTA.go:181-200）。上游 nodeorder 另有 `balancedresource.resources`。
- **b 的两个细节。** 默认只含 CPU 和内存，不含 GPU。它按子组的全部任务求和，包括已在运行的（NTA.go:380），所以部分运行的子组常得 0 分（:409）。
- **节点级权重不进域分数，成立。** 但它决定域内贪心放置的先后，能改变某个域试放是否成功，从而间接影响选域（F9）。【推断】

**实测。**【测试结果】8 行在两条基线上分数、分项、选择都与公式一致，现行与排序流程相同（w-dev.log、w-up.log 的 `:121` 各行）。探测把内存权重设成了 0（`probes/weights/zz_probe_weights_test.go:36`），文档没有说明。默认内存权重下这个例子的 b 约为 0.26 和 0.13，第 8 行不再并列。【推断】

**对用户的回答**

1. **默认权重下 preferred 项何时输给装箱。** 被扣分的域 X 胜过未被扣分的域 Y，当且仅当 `100 × (b_X − b_Y) > W`。另有四种 preferred 不起作用的情形【静态分析】：
   - 候选高于条目的比较层时不扣分（GTA.go:785）。单元放不进一个比较层域时，这条偏好无效。
   - 层严格优先，偏好不能让单元去更宽的层。
   - 命中的权重之和超过 100 之后不再区分。
   - 并列加分触发时，W=100 也可能输。例：X 的 b 为 0.9 且并列最高，子组的任务全在 X，命中 W=100，得 190；Z 的 b 为 0.1，未命中，得 110。【推断】
2. **实施后现有配置的变化。** 首次放置的单元：公式不变，只有并列从随机变为固定。隐藏的变化有三处【推断】：
   - §4.3.4 第三键：并列加分今天是分数的一部分，最多值 `100 × w_nta`，能与 preferred 项相抵。改成第三键后，任何分数差都压过它。例：两个机架 b 并列 0.5，X 有子组一半的任务并命中 W=30。今天 X 得 170，Y 得 150；改后 X 得 120，Y 得 150。只影响部分运行的单元。
   - §4.3.9 节点偏好项：没有定义归一化。默认 nodeorder 权重下节点原始分可达上千，其中 `leastrequested` 与域装箱方向相反。默认权重 0 时无影响。
   - 排序流程对全层候选一起打分，并列加分的候选集合与今天"只在成功者之间"不同。设计已说明。
3. **升级兼容。** 没有现有配置键改变含义或默认值，新开关默认关闭。两项待批准的改动会改变范围：并列加分移出后 NTA 域分上限从 `200 × weight` 降到 `100 × weight`；域分范围要求（§10.3 第 11 项）会影响第三方打分插件。
4. **默认启用。**【静态分析】
   - 内置默认配置在两条基线上都不含 binpack、NTA、GTA（`pkg/scheduler/util.go:38-51`）。
   - Helm 的 `volcano-scheduler.conf`：dev-1.14 含 binpack、`network-topology-aware`、`group-topology-affinity`；上游只多 binpack，两个拓扑插件要显式配置。
   - 插件列出后 `enabledHyperNodeGradient`、`enabledHyperNodeOrder` 默认为 true（`pkg/scheduler/plugins/defaults.go:48、52`）。
   - 没有启用 GTA 时 required 条目不被强制，也没有提示（dev: `pkg/scheduler/framework/session_plugins.go:1079-1081`）。【推断】

### 第 6 题：§9 之外需要的回归覆盖

**单元测试，关系约束**

1. F1 的反例：开关打开、预算 0 时仍 4/4，落位与现行相同。
2. 比较层不存在（`topologyTier: 0`、树上缺层、部分节点没有该层的域）：保持不可调度并带原因，或记录经批准的新行为。
3. 提名节点在同伴的域里、或在作业可用域之外：不被采用。
4. 子组跨两个比较层域之后：单 Pod 和多 Pod 重建、同伴重建、调度器重启后占用集正确；开关打开后关闭各做一遍。
5. 弹性 gang 和 `minSubGroups` 小于组数的作业：结果与现行逐 Pod 相同；记录哪些子组运行。
6. 同伴为 Pipelined，以及已分配与 Pipelined 混合的情形。
7. PodGroup 反亲和加 SubGroup 约束：森林路径、作业级硬拓扑路径、可用域开关开和关。
8. 两个同层 Job 容器，放宽前只有一个可行：断言选择，并记录子组跨了几个域。
9. required 与 preferred 条目并存，含 dev-1.14 的 Job 级层序重排。
10. 失败文本：亲和组筛光候选时写明哪一组、需要多少、域能提供多少；节点被排除时写明哪些域。
11. GTA 未启用或 `enabledHyperNodeGradient: false` 时，allocate 不做排除和筛选。
12. 现有的 `TestHyperNodeGradientForSubGroupHardTerms` 换成带节点集合的夹具，固定开关前后的行为（F11）。
13. 千节点、数十个副本时排除集计算的耗时基准。

**单元测试，权重**

1. 默认内存权重下的例子；把 GPU 列入 `hypernode.binpack.resources` 的例子。
2. 并列加分与 preferred 项相抵的两个例子，现行与第三键各断言一次。
3. 候选高于比较层时 preferred 不扣分。
4. dev-1.14 三类 preferred 条目共用一个 g；Job 级按子组求和。
5. `weight: 0`、负数、`enabledHyperNodeOrder: false`。
6. 部分运行的子组 b 为 0 的情形。
7. 节点偏好项权重为 0 时分数逐位相同；启用时不超出 `100 × weight`。

**Kind**

1. RA1 的回退：新构建放下后换回旧构建，分别删 1 个和 2 个 Pod。
2. 跨域子组在调度器重启后的落位记录与同伴重建。
3. 一个作业同时带 PodGroup 反亲和与 SubGroup 约束，集群里有运行中的对端。
4. A 声明、B 不声明、B 后到，然后删 A 的一个 Pod，两个构建各做一遍。
5. 不存在的层和未知层名：两个构建的条件文本。
6. 启用 preempt 时的提名节点场景。
7. K16 用 Helm 默认配置（内存权重 1）再做一遍，加一个部分运行后重建的作业。

## 3. 前后行为对照表（第 2 题）

数字来自 rel.log。"开关"指第五轮的 `ProbeSubJobRelations`。

| 情形 | 现行 | 只排序 | 排序加重试 4 | 排序加开关 | 开关加重试 4 | 性质 | 证据 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| (a) 子组必须跨多个比较层域（RA1） | 0/3 | 0/3 | 0/3 | 3/3 | 3/3 | 有意变更，语义待批准（§10.3 第 9 项）；附带风险 F2、F5 | RA1 行，测试结果 |
| (b) 先后相关的反亲和（RA2） | 0/2 | 0/2 | 2/2 | 0/2 | 2/2 | 开关中性；重试是有意变更 | RA2 行；随机 348 到 391，测试结果 |
| (c) 亲和组只是作业的一部分（RA3） | 0/3 | 0/3 | 0/3 | 3/3 | 3/3 | 有意变更；落位变化 1/348，同层；F1 是回归风险 | RA3 行、seed 24 行为测试结果；F1 为静态分析 |
| (d) 亲和组就是整个作业（RA5） | 2/2 | 2/2 | 2/2 | 2/2 | 2/2 | 中性 | RA5 行，测试结果 |
| (e) preferred 条目（600 个场景） | 550 | 550，丢失 0，落位变化 0，试放 15201 降到 8187 | 未测 | 未测 | 未测 | 只排序：中性。开关只读 required 条目，推断为中性 | RL2 行为测试结果；其余为推断 |
| 对照（RA1c、RA2c、RA3c、RA4、RA4n） | 各列相同 | | | | | 中性；无解场景仍为 0/3 | 对应行，测试结果 |

## 4. 发现表

| 编号 | 严重度 | 归属 | 发现 | 证据 | 建议 |
| --- | --- | --- | --- | --- | --- |
| F1 | 重要 | 实施；设计文档 | 亲和组容量筛选用第一个成员自己的反亲和排除集计算整组可用量。亲和层高于反亲和层、第三个策略先放时误删候选，不重试时丢掉今天能调度的作业 | 静态分析：rel.go:127、144，`scope_repair.go:81`，alloc.go:724、740。未测 | 整组可用量只按作业级可用域计算，或按成员分别计算。§4.7.5 和 §8 第 9 步的"不丢"改成"样本内 0，已知反例"。把反例加进手工场景 |
| F2 | 重要 | 实施；设计文档 | 比较层在候选之下不存在时，required SubGroup 反亲和从"全部拒绝"变成"不生效" | 静态分析：GTA.go:656-668，rel.go:66-82；`hyper_node_info.go:811-818`。未测 | 只在候选下确有该层的域时放宽，其余保持拒绝。§4.7.2 定义"没有该层域的节点"怎么算 |
| F3 | 重要 | 实施 | dev-1.14 的提名节点捷径绕过候选域的节点列表，因此也绕过可用域和子组排除集。抢占只跳过带 `networkTopology` 的作业 | 静态分析：alloc.go:1019-1025（HEAD:926-930），`preempt.go:138-141`。现存漏洞 | 提名节点必须在过滤后的节点列表里。写进 §7 的接触点表 |
| F4 | 重要 | 实施 | 亲和组筛选和节点排除不留任何用户可见的原因；候选被筛光时摘要仍写有可用域 | 静态分析：alloc.go:740-745、976；`unschedule_info.go:111-114、168-235` | 筛选和排除各写一条原因。顺带决定是否修正把 SubGroup 约束标成 `podGroupAntiAffinity` 的现状 |
| F5 | 一般 | 设计文档 | 反亲和放宽也能改变 Job 级的选择；没有硬层上限的子组可以跨到整个 Job 容器。§4.7.5 只把落位变化归因于亲和筛选 | 推断：alloc.go:450-482；GTA.go:132-168。未测 | §4.7.5、§10.3 第 9 项写明这一后果，以及"给子组加硬层上限"的缓解办法 |
| F6 | 重要 | 设计文档 | §4.8.4 说公式和权重含义不变。采用 §4.3.4 第三键后，部分运行的单元上并列加分不再能与 preferred 项相抵。"默认配置下 W=100 有保证"在并列加分触发时今天不成立 | 静态分析：NTA.go:364-369、743-748。数字例子为推断 | §4.8.4 增加第四项变化；§4.8.2 的保证加上前提 |
| F7 | 一般 | 设计文档 | §4.8.2 的例子用了 `hypernode.binpack.memory: 0`，没有说明。默认 b 是 CPU 与内存的平均，不含 GPU。`hypernode.binpack.resources.<名字>` 需要同时列在资源清单里 | 测试结果加静态分析：`zz_probe_weights_test.go:36`；NTA.go:181-200、377-430 | 写明例子的配置；参数表补全 |
| F8 | 一般 | 设计文档 | preferred 项对高于比较层的候选不扣分。dev-1.14 上 preferred SubGroup 条目只要存在就改 Job 级层序，与权重值无关，§4.8.1 的"窄层在前"没有提这个例外 | 静态分析：GTA.go:785、207-258 | §4.8.1、§4.8.2 各补一句 |
| F9 | 建议 | 设计文档 | "节点级权重影响不到选域"只对分数成立，它能通过域内贪心放置改变试放结果 | 推断：alloc.go:1058 前后的节点选择流程 | 改写措辞 |
| F10 | 一般 | 设计文档 | §4.3.9 节点偏好项没有定义归一化和纳入哪些节点评分项 | 推断：`nodeorder.go:136-143` 的默认权重 | 启用前限定为偏好类评分并归一到 0–1 |
| F11 | 一般 | 设计文档 | "开关开和关单测都通过"对放宽是空证据。唯一固定"高于比较层的候选被拒"的单测，夹具没有节点集合，放宽分支进不去。allocate 包没有 required SubGroup 约束的用例 | 静态分析：dev GTA 测试文件 :411-485；`allocate_test.go:3100`；`evidence/round5/dev114-unit-relations-on.log` | §4.7.6 末句加限定。该单测的期望属于契约，改动需单独批准 |
| F12 | 一般 | 设计文档 | 探测没有覆盖：无硬层上限的子组、弹性 gang、部分运行、PodGroup 反亲和并存、required 与 preferred 并存；preferred 场景没有打开开关 | 静态分析：探测源码（第 2 题） | §10.2 的"证据范围"补全 |
| F13 | 一般 | 实施 | dev-1.14 的占用统计不含 Pipelined 任务。子组跨域后，同伴的排除集可能不完整；只剩落位记录时又会把整个父域都算作占用 | 静态分析：tai.go:381、506-527 | 改成与上游一致（带节点名的 Pipelined 任务算占用） |
| F14 | 一般 | 实施 | 放宽在插件、排除在 allocate，默认是放开的结构；原型不看 GTA 是否启用 | 静态分析：rel.go:53-85；GTA.go:656 | 由插件声明子组级可用域，框架只把放宽的候选交给会使用可用域的调用方 |
| F15 | 建议 | 设计文档 | 现存的用户可见问题：dev-1.14 的 webhook 不校验 Update 和层是否存在；`namespaceSelector` 无效；preferred 条目写未知层名会让子组调度失败且没有原因；§4.7.1 没有区分 Pipelined 对端 | 静态分析：见第 1、4 题 | 在 §4.7.1 或 §10.2 记一笔，不必在本次修 |

## 5. 未能核对的事项与剩余风险

- **F1、F2、F3 都没有测试验证。** 按规则我没有运行 Go 测试。F1 的反例可以直接写成关系探测的一个手工场景：1 个 zone、2 个机架、每机架 1 个节点；策略顺序 p2（4 核，1 个 Pod）、p0（4 核，2 个 Pod）、p1（4 核，1 个 Pod），都限定在机架内；亲和 `tier 2 [p0, p1]`，反亲和 `tier 1 [p2, p0]`。
- **没有 Kind 证据。** 回退、重启、抢占路径的结论全部是静态分析。
- **开关组合没有测。** 第五轮开关与可用域开关同时打开、森林路径、部分运行的作业、弹性 gang，都没有任何探测。
- **权重证据范围。** 只有一个首次放置、无 SubGroup 策略、内存权重为 0 的例子。并列加分的两个数字例子和默认内存权重下的数值是我按公式手算的。
- **seed 24 的 Job 级分数**（约 103 对 97）是手算，日志没有打印分数。
- **性能没有评估。** 排除集对每个域做一次集合合并（rel.go:80）；占用统计对每个任务扫描全部域（tai.go:529-555）；放宽分支对每个宽候选克隆节点集合（GTA.go:661）。
- **上游移植。** 上游没有 SubGroup 关系约束。移植时提名路径和 `PurposeEvict` 路径需要重新分析。
- **Kind 安装方式。** dev-1.14 的 `installer/volcano-development.yaml` 里 PodGroup CRD 没有 `topologyAffinity` 字段（Helm chart 的 CRD 有）。用它安装时该字段会被裁掉，Kind 验证要用 Helm 的 CRD。