# 客户行为与回归分析（第二轮）

- 角色：客户行为与回归分析员（独立 subagent，agent id `0d575786-3d36-4ea0-a1c9-196c387959de`）。没有参与设计与原型编写。
- 分析对象：`evidence/round2/section15-draft-for-review.md` 的 S7–S11、第二轮原型补丁与探测，两条基线。
- 它运行的探测源码与日志归档在 `subagents/review-behavior-round2/`。
- 下文为该 subagent 的原始报告。各发现的处置见 `PROPOSAL_COMMIT.md` §16。

---

# 任务 004 第二轮：客户行为与回归分析报告

本阶段没有 Kind 集群，以下全部是读码结论或单测级实测，不代表集群行为。

**运行方式与指示有两处偏差：**
- 本环境不允许我向 `/tmp` 写文件，所以没有在 `/tmp/vc-probe-dev114` 内跑测试。我把 `/tmp/vc-probe-dev114` 和 `/tmp/vc-probe-master2` 复制成临时副本（排除 `.git`，`diff -rq` 无差异），在副本上运行，`TMPDIR`/`GOTMPDIR` 也指向副本目录。
- 副本曾短暂放在工作区根目录 `.review-behavior-scratch/`，现已删除。任务目录、`volcano/`、`kthena/` 和各 worktree 的 `git status --short` 前后一致。

探测源码（12 个文件，1829 行）和日志保存在会话目录 `subagents/review-behavior-round2/`。下文"up"指上游 d423e427c 加原型，"dev"指 dev-1.14 7947e8756 加原型；RBn / Un 是我的探测编号，定义见第 4 节。

## 1. 结论

1. **没有发现硬约束被破坏。** 所有探测里没有 Pod 落进被排除的域；上游提名快速路径在 480 个组合里没有一次接受含被排除节点的提名。开关全关时新分支不可达或为空操作（读码，并在副本上重跑相关包通过）。
2. **"不影响今天能调度的作业"在会话级不成立，且只开可用域（S7）就会发生。** 同一会话里两个互相反亲和的作业，J1 今天放不下、J2 今天 2/2；开启后 J1 4/4、J2 0/2（两条基线实测）。R6 的生成器避开了这一类。这正是用户点名的风险，需要明确决策。
3. **失败后重试（S8）在重试失败时会让状态变得乐观且错误。** 一个确实无解的作业，条件从 `2/3 tasks in gang unschedulable` 变成 `0/3 … Pending: 3 Schedulable`，"预算用完"不进任何用户可见字段。默认开启前必须修。
4. **dev-1.14 的森林去留（S11）：** 选项 a "落位零变化"得到确认；选项 b "26 个不同、丢失 0"得到确认，且 26 个全部更集中。但选项 b 在弹性 gang 上有一例少放 Pod，SubJob 记录出错，成功路径的任务试放次数约翻倍。
5. **成本：** S7 让无解的反亲和作业每个会话都试放（0 次变 10–15 次）；上游的组合代码随 HyperNode 数呈平方增长。S9 在预算 2 和 4 时比单用 S8 少调度作业。

## 2. 行为差异表

| 编号 | 步骤 | 基线 | 维度 | 变更前 | 变更后 | 归类 | 证据类型 | 证据 |
|---|---|---|---|---|---|---|---|---|
| D1 | S7 | 两条 | API/插件合同 | `HyperNodeGradientResult`、`HyperNodePluginGradient`、`HyperNodeGradientStats` 无可用域字段 | 各加一个字段（`Scope`、`Scope`、`ScopeHyperNodes`），零值即旧行为；GTA 有 required 反亲和时恒置 `Scope=true`，由框架按开关屏蔽 | 中性（关）/ 预期变更（开） | 读码 | up `session_plugins.go:1175,1197`；dev `:1075,1112`；GTA up `:156`，dev `:123-129` |
| D2 | S7 | up | 哪些调用方看到新候选集 | allocate、抢占类、提名快速路径共用按名求交 | 只有 allocate（`allocate.go:501,682`）看到"容器 ∩ 可用域"；`PurposeEvict`（`utils/util.go:89`、`simulate.go:93`）和提名快速路径不变；backfill、enqueue 不调用 gradient | 预期变更 / 其余中性 | 读码 | `session_plugins.go:1175` 的 `purpose == api.PurposeAllocate`；`hyper_node_candidate.go:54-67` 直接调插件回调，不带 `Scope` |
| D3 | S7 | dev | 同上 | 只有 allocate 调用 gradient（`allocate.go:389,712`） | 同左；但 GTA 的 SubJob 级在开关开时不再按反亲和过滤（GTA `:139-143`），节点级排除完全依赖 allocate 的 `scopeNodes` | 预期变更，隐含耦合 | 读码 + 实测 | RB2–RB10 泄漏 0 |
| D4 | S7/S10 | up | 提名快速路径 | 与完整 gradient 等价（单测断言） | 480 组合里 80 处 fast=false、full=true，0 处接受含被排除节点的提名 | 中性（安全）；单测参照需改写 | 实测 | U7，见 F12 |
| D5 | S7–S9 | 两条 | 开关全关 | — | `setScope` 在 stats 为空时返回；`nodesInScope` 返回原切片；`blocked`/`repairable` 只是追加；`repairJob` 在预算 ≤0 时返回；组合函数无 `Scope` 时走原函数；dev 的 `allocateForJob` 包装直接转调 | 中性 | 读码 + 实测 | dev `scope_repair.go:43-46,75-77,220`、`probe_scope.go:53-55`；up `scope_repair.go:224`、`session_plugins.go:1223-1227`；副本重跑见第 4 节 |
| D6 | S7 | 两条 | 状态：新变可调度（HB2） | `4/4 tasks in gang unschedulable … subJob …: 0/11 hyperNodes available` | 4 Binding；摘要 `8/11 …: tier-4 1/1 (1 podGroupAntiAffinity); tier-3 1/1 (1 podGroupAntiAffinity)` | 预期变更；摘要自相矛盾属回归 | 实测 | RB2、U2 |
| D7 | S7 | 两条 | 状态：仍不可调度（反亲和 5 Pod） | `5/5 … 5 Unschedulable`，0 次试放 | `1/5 … 1 Unschedulable, 4 Schedulable … Node: myq-worker-4: 0/4 nodes are unavailable`；任务试放 15（dev）/ 10（up） | 文本为预期变更；成本为回归 | 实测 | RB2、RB8、U2 |
| D8 | S7 | 两条 | 状态：可用域为空、与容器不相交 | `0/11 hyperNodes available: … (3 podGroupAntiAffinity) …` | 逐字相同，没有新增"可用域"记录 | 中性 | 实测 | RB2 (iv-a)(iv-b)、U2 |
| D9 | S8 | 两条 | 状态：重试成功（017 例） | 0/2 | 预算 4：2/2；重试记录只在 trace | 预期变更 | 实测 | RB1、U1 |
| D10 | S8 | 两条 | 状态：重试失败或预算用完 | `2/3 … 1 Schedulable, 2 Unschedulable` | 预算 1：`1/3 … 2 Schedulable`；预算 4：`0/3 … 3 Schedulable`；`JobFitErrors` 不变；预算用完只在 trace | 回归 | 实测 | RB1c、U1 |
| D11 | S7 | 两条 | 记录 | up 的反亲和作业记录不超过比较层；dev 森林记实际最近公共祖先 | 记容器名：HB1–HB4 为 `dc`，注解同值；无真实公共父域时为 `<cluster-top-hypernode>` | 预期变更 | 实测 | RB3、RB7、U2 |
| D12 | S7 | 两条 | 下一会话/重启（2 个运行在 B1、C1，2 个待调度） | HB 类今天放不下 | 2/2 放到 B2、C2，不进 A；`AllocatedHyperNode` 由任务恢复为 `dc` | 预期变更 | 实测 | RB4、U3 |
| D13 | S7 | 两条 | 排除集后来变化（同类 PodGroup 进入 B） | 新 Pod 只进 C（1/2） | 相同 | 中性 | 实测 | RB4 第 4 个场景、U3 |
| D14 | S7 | dev | 同类 PodGroup 自身扩容 | A 里的同类能在 A 加 Pod | 相同（占用按任务位置算，不受 `dc` 记录影响） | 中性 | 实测 | RB5 |
| D15 | S7 | 两条 | 回滚、混跑 | — | D12 的状态在开关关时 0/2（dev：HB2、HB4 类；up：HB1、HB2、HB4 类）；运行中的 Pod 不受影响 | 中性（等同旧缺陷），需写进升级说明 | 实测 | RB4、U3 的 off 行 |
| D16 | S7 | dev | 可用性：单作业严格 gang | 464/600 | a：467，新增 3，丢失 0，落位变化 0；b：467（加重试 8 为 475），丢失 0，落位变化 26 | 预期变更 | 实测 | RB9 |
| D17 | S7 | 两条 | 可用性：同会话互斥反亲和 | J1 0/4，J2 2/2 | J1 排前：J1 4/4，J2 0/2（`2/2 tasks in gang unschedulable`）；J2 排前：不变 | 预期后果，需用户决策 | 实测 | RB6、U4 |
| D18 | S11-b | dev | 可用性：未覆盖类别 | — | 弹性 150：少放 1、落位变化 13；软模式 150：新增 2、变化 11；部分运行 146：变化 2（加重试 8 再新增 8）；整个作业丢失 0，泄漏 0 | 回归（少放 1）+ 预期 | 实测 | RB10 |
| D19 | S11-a | dev | 同上 | — | 弹性 0 变化；软模式新增 2、0 变化；部分运行 0 变化 | 中性/预期 | 实测 | RB10 |
| D20 | S11-b | dev | 落位形状 | 森林把作业摊到多个域 | 26 个不同的落位全部更集中；没有变宽的层，没有增加的域或机架 | 预期变更 | 实测 | RB9 |
| D21 | S11-b | dev | 成功路径成本 | HB1：任务试放 10、Allocate 事件 18 | 20、36；HB5 6→10；2 Pod 对照 7→10；选项 a 不变；无反亲和作业不变 | 回归（成本） | 实测 | RB8 |
| D22 | S7 | 两条 | 性能：gradient 组合 | up 近线性；dev 已有平方项 | 平方增长 | 回归 | 实测 | RB13、U5，数字见 F5 |
| D23 | S8 | 两条 | 性能：失败路径 | 无解例 12 次任务试放 | 预算 1：16；预算 4：28；事件 10→26 | 预期（有上限） | 实测 | RB1、U1 |
| D24 | S9 | up | 可用性 | — | 同预算下比 S8 少调度：预算 4 少 2，预算 2 少 4/3，预算 8 相同 | 回归（相对 S8） | 实测 | U6 |
| D25 | S10 | up | 抢占 | `PurposeEvict` 按名求交 | 未改；需要跨域的作业在抢占路径仍提名不出 | 未知（无原型） | 读码 | `utils/util.go:89`、`simulate.go:93` |
| D26 | S8 | up | 拒绝缓存 | 成功路径记录胜者的拒绝（`allocate.go:575-578,634-636`） | 重试成功路径不记录（`scope_repair.go:248-270`） | 未知 | 读码 | 未测 |
| D27 | S7 | 两条 | 第三方插件 | — | 旧 gradient 插件零改动；`HyperNodeOrderFn` 会收到被部分排除的父域和未过滤的节点列表（up `allocate.go:863`） | 中性/未知 | 读码 | — |

## 3. 发现表

| 编号 | 严重度 | 发现 | 证据 | 建议处置 |
|---|---|---|---|---|
| F1 | 阻断（S8 默认开启前） | 重试失败后状态误报。gang 用任务的 `LastTransaction` 计数，任何一次试放放下过的任务都算 Schedulable；重试换顺序后每个任务都被放下过一次。无解作业显示 `0/3 tasks in gang unschedulable … 3 Schedulable`。"预算用完"只写 trace。 | 实测 RB1c、U1；dev `gang.go:224-238`、`statement.go:396`；up `gang.go:241-255`、`statement.go:381`；dev `scope_repair.go:274-276` | 重试失败后恢复正常搜索结束时的任务事务状态和 `NodesFitErrors`，或让重试不写 `LastTransaction`；把"搜索未完成"写进 `JobFitErrors`。 |
| F2 | 重要 | S7 单独就会让今天能调度的作业落空（D17）。§15.1 第 3 条"不影响今天能调度的作业"需要限定为单作业；R6 的"可用域：落空 0"是因为生成器避开了互斥反亲和。dev 上 HB1 类今天已有此现象，新增的是 HB2–HB4 类。 | 实测 RB6（dev）、U4（up） | 改 §15.1、§15.4.5 的措辞；在 §15.12 加一个决策项；补单测和 Kind 场景。 |
| F3 | 重要（仅选项 b） | 弹性 gang 加反亲和加 SubGroup 的作业回到通用路径后，本会话少放 Pod（种子 195：4→3），hard tier-1 的 SubJob 被记成 `dc` 或 `z2`。去掉反亲和、开关全关时通用路径有同样现象，是现存缺陷；森林路径用 `saveCandidateForestDecision` 规避了它。 | 实测 RB10、RB10d、RB11；dev `recorder.go:60-86`、`allocate.go:1117-1132` | 选 b 之前先修通用路径的记录；§15.6 第 8 项注明"丢失 0、都更集中"只在严格 gang 上测过。 |
| F4 | 重要 | S7 使 minResource 预过滤对反亲和作业失效：被部分排除的父域带着被排除域的资源通过预过滤，无解作业每个会话都试放（D7）。§15.8 把"按交集计算"列在 S7 范围内，原型没做。 | 实测 RB8、U2；dev `allocate.go:1373-1427`；up `:1418,1455` | 预过滤按交集求和与 S7 同批交付，并断言这类作业试放 0 次。 |
| F5 | 重要（up）/ 一般（dev） | 组合代码对每个允许的 HyperNode 调 `GetAncestors`，后者到根时总会全表扫描 `getParent`。2022 个 HyperNode 时：up Job 级 gradient 1.1–2.0 ms → 24.9–30.4 ms，SubJob 级同量级，且每个（容器，SubJob）调一次；dev 26.8–27.1 ms → 51.4–52.4 ms。`setScope` 只有 0.6–0.9 ms。 | 实测 RB13、U5（各 3 次，机器有其他负载）；up `probe_scope.go:72-76`、`hyper_node_info.go:884-905,961`；dev `probe_scope.go:67-73` | 正式实现用 `HyperNodeIndex` 或父指针；加基准。 |
| F6 | 一般 | S9 按层重试与最终重试共用预算，且按层调用不为最宽容器保留（up `allocate.go:608-614` 传 `false`）。预算 4：R5 409→407，R4 977→975；预算 2：少 4 和 3；预算 8 相同。"按层 + 保留"无效。对现行丢失 0。 | 实测 U6 | §15.4.4 补预算 2、4 的行；按层重试也保留 1 次，或单独计预算。 |
| F7 | 一般 | 摘要把被部分排除的父域同时记为可用和被排除（D6）。dev 的 SubJob 级摘要丢掉反亲和计数。不可调度作业的条件文本和计数变化（D7）可能影响依赖 reason 的自动化。 | 实测 RB2、U2 | 摘要区分"整体可用""部分可用""被排除"；D7 的文本变化列入 §15.8 的 S7 行。 |
| F8 | 一般 | 重试有浪费：节点集相同的嵌套容器（`z0` 与 `<cluster-top-hypernode>`）各重试一次同样必败的顺序。dev 没有"留 1 次给最宽"，017 例预算 1 仍 0/2。预算按 `allocateForJob` 调用计，不是 §15.4.3 写的每作业每会话。失败重试后 `NodesFitErrors` 条目变少。 | 实测 RB1、U1；dev `scope_repair.go:224`；up `:234` | 节点集相同的容器去重；dev 补"保留"；统一预算口径。 |
| F9 | 一般 | 回滚和混跑：经 S7 跨域放置的作业，在关掉开关或没有该特性的调度器上无法补 Pod（D15）。 | 实测 RB4、U3 | 写进升级说明；Kind 加回滚场景。 |
| F10 | 一般（仅选项 b） | 需要摊开的森林类作业，成功路径试放翻倍（D21）；2022 个 HyperNode 的例子整会话多 37%–60%。§15.6 第 8 项没列成本。 | 实测 RB8、RB13 | 在 a/b 对比表里加成本列。 |
| F11 | 一般 | dev "可用域 + 全量单测 46 包通过"对森林是空证据：森林单测用不声明可用域的假插件，森林路径照常启用。我用声明可用域的假插件重放 4 个森林用例：选项 b 下节点和 `AllocatedHyperNode` 断言成立，`recorded SubJob decision key` 断言失败。 | 实测 RB12；dev-1.14 原版 `podgroup_antiaffinity_forest_test.go:126,143,154,169,308-320` | §15.5 注明；选 b 时这些用例和 `:343,390,420` 及 `candidate_forest_memory_test.go:30,69,102` 需改写或删除。 |
| F12 | 一般 | `TestNominationFastPathMatchesPluginGradients` 失败的确切原因：80 处不一致里 72 处是参照的问题（参照用容器的全部节点判断"包含"，`root` 含被排除的 node-a），8 处是快速路径更严（提名域在可用域内，但只有被部分排除的父域能当容器）。§15.6 第 1 项"保持安全"成立。`TestNominationCandidateValidationMatchesFullGradients` 通过是因为假插件不声明可用域。 | 实测 U7；up `constraints_test.go:296-357`、`framework/hyper_node_candidate_test.go:31,124`、`hyper_node_candidate.go:32-85`、GTA `:67-91,262-272` | S10 改写参照为"容器 ∩ 可用域"；给后一个单测加声明可用域的假插件；明确那 8 种情形是否应走快速路径。 |
| F13 | 一般 | 上游重试成功路径不记录拒绝（D26），拒绝缓存开启时弹性作业的剩余任务没有唤醒线索。 | 读码，未测 | T31 增加成功分支。 |
| F14 | 建议 | 记录应是实际落位的最近公共祖先：S8 带"保留"顺序、预算 1 时 017 例在 `<cluster-top-hypernode>` 成功并记录它，而 `z0` 就够；无真实公共父域时该虚拟名写进 `volcano.sh/job-allocated-hypernode`（上游反亲和作业为新增）。 | 实测 U1、U2、RB7 | 按 §15.6 第 5 项实现并加断言。 |
| F15 | 建议 | 协调者命令里的 `grep -v "^I0\|^W0\|^E0"` 滤不掉 11 月的 klog 行（`E1111 …`）。 | 实测 | 改用 `grep -v "^[IWE][0-9]"`。 |

上游其他按旧合同写的测试和文档（读码，未运行）：
- `allocate_test.go:6085`
- `actions/utils/simulate_test.go:236`
- `docs/design/group-topology-affinity.md:406`
- e2e `test/e2e/hypernode/podgroup_anti_affinity_test.go:312,336,388`

## 4. 我实际运行的测试

**完整源码没有贴在这里**（1829 行），原样保存在上面的会话目录：
- `probes/review/`：dev-1.14，8 个文件。
- `probes/review-up/`：上游，3 个文件。
- `probes/review-up-gta/`：上游 GTA 包内 1 个文件。
- `logs/`：过滤掉 klog 后的全部输出。

**复跑命令：**

```bash
# 把 review/ 或 review-up/ 放到任务目录 probes/ 的同级副本里
run_probe.sh <dev-1.14 原型树> history,prototype,baseline-dra,prototype2,prototype2-dev114,review -run 'TestReviewRB' -v
run_probe.sh <上游原型树> history,prototype,prototype2,prototype2-ext,review-up -run 'TestReviewUp' -v
# 提名探测：把 zz_review_behavior_nomination_test.go 放进 pkg/scheduler/plugins/group-topology-affinity/
go test ./pkg/scheduler/plugins/group-topology-affinity/ -count=1 -run TestReviewNominationSoundness -v
VC_PROBE_SCOPE_UNION=1 go test ./pkg/scheduler/plugins/group-topology-affinity/ -count=1 -run TestNominationFastPathMatchesPluginGradients
```

**核心代码（运行器和两个最关键的探测）：**

```go
type rvCfg struct { name string; mode protoMode; scope, fallback bool; budget int }
var (
    rvOffDet = rvCfg{name: "off+detTies", mode: modeExhaustiveDet}
    rvA      = rvCfg{name: "S7a forest-first", mode: modeExhaustiveDet, scope: true, fallback: true}
    rvB      = rvCfg{name: "S7b region-replaces-forest", mode: modeExhaustiveDet, scope: true}
)
func rvRun(t testing.TB, tc *uthelper.TestCommonStruct, cfg rvCfg) *rvOut {
    previous := framework.ProbeScopeFallback
    framework.ProbeScopeFallback = cfg.fallback
    defer func() { framework.ProbeScopeFallback = previous }()
    out := &rvOut{jobs: map[api.JobID]*api.JobInfo{}, ahnAtOpen: map[api.JobID]string{}}
    out.res, out.action = runRound2(t, tc, cfg.mode, round2{scope: cfg.scope, budget: cfg.budget}, nil,
        func(ssn *framework.Session) {
            for id, job := range ssn.Jobs { out.jobs[id] = job; out.ahnAtOpen[id] = job.AllocatedHyperNode }
        })
    return out
}
// rvCondition 读 gang 在会话关闭时写的 Unschedulable 条件
// rvContention(freeA, freeB, pinned)：017 的形状，机架 a/b 各剩 freeA/freeB 个 CPU，
// 1 个 flexible SubJob 在前，后面 pinned 个只能用机架 a 的 SubJob，每个 Pod 1 CPU

func TestReviewRB1cConditionAfterFailedRepair(t *testing.T) {
    for _, budget := range []int{0, 1, 4} {
        cfg := rvOffDet
        if budget > 0 { cfg = rvOffDet.repair(budget) }
        out := rvRun(t, rvContention(1, 2, 2), cfg)
        t.Logf("budget=%d placed=%s condition=%q", budget, rvPlaced(out.res, "job-"), rvCondition(out.jobs["c1/job"]))
    }
}

func TestReviewRB6MutualAntiAffinityInOneSession(t *testing.T) {
    for _, small := range []string{"pg-small", "pg-a-small"} { // 名字决定作业顺序
        for _, cfg := range []rvCfg{rvOffDet, rvA, rvB} {
            tc := buildBCScenario(bcVariants[1]) // HB2：4 个 Pod，需要 B 和 C
            rvAddSmallJob(tc, small, 2)          // 同标签、同 required 反亲和，2 个 6 卡 Pod
            out := rvRun(t, tc, cfg)
            t.Logf("[%s] J1 placed=%s | J2 placed=%s", cfg.name, rvPlaced(out.res, "myq-"), rvPlaced(out.res, "small-"))
        }
    }
}
```

**探测清单与关键输出：**

| 探测 | 基线 | 内容 | 关键输出 |
|---|---|---|---|
| RB1 / RB1c | dev | 重试失败后的状态（4 个争用场景 × 预算 0、1、4） | `budget=0 … "NotEnoughResources: 2/3 tasks in gang unschedulable: … Pending: 1 Schedulable, 2 Unschedulable."`；`budget=1 … 1/3 … 1 Unschedulable, 2 Schedulable.`；`budget=4 … 0/3 … Pending: 3 Schedulable.`（三者 placed=0）；017 例 `budget=1 placed=0`，重试记录 `in z0-r01: retry 1 … failed` |
| RB2 | dev | 可用域相关状态 | 见 D6–D8 |
| RB3 | dev | 首次放置后的记录 | HB2 在 a、b 下：`job.AllocatedHyperNode="dc" annotation="dc" subJobs=[c1/pg-myq=dc]` |
| RB4 | dev | 第二会话 | HB2 类：`[off+detTies] … newly placed=0`；`[S7a]`、`[S7b]`：`newly placed=2 [myq-worker-2@z1-r01-n0 myq-worker-3@z2-r01-n0]` |
| RB5 | dev | 同类 PodGroup 在 A 扩容 | 三种配置都是 `peer newly placed=1 [anchor-1@z0-r00-n0]` |
| RB6 | dev | 互斥反亲和 | `[off+detTies] J1 placed=0 [] \| J2 placed=2`；`[S7a]`、`[S7b]`：`J1 placed=4 … \| J2 placed=0 []`，`J2 FitError: … Pending: 2 Unschedulable` |
| RB7 | dev | 弹性、软模式、无真实公共父域 | 无 `dc` 的 HB2：a、b 下 4/4，`annotation="<cluster-top-hypernode>"` |
| RB8 | dev | 计数器 | 见 D7、D21 |
| RB9 | dev | 600 个种子，today / a / b / b+重试 8，加落位形状 | `S7a … 467 \| 3 \| 0 \| 0 \| 0 …`；`S7b … 467 \| 3 \| 0 \| 0 \| 26 \| 26 \| 0 \| 0 \| 0 \| 0`（后五列：更集中、形状相同、更分散、层更宽、非法） |
| RB10 / RB10d | dev | 450 个种子三类；种子 195 细查 | 表见 D18、D19；种子 195 选项 b：`placed=3 …`，`subJobs=[… c1/job/p1-2=dc …]`；去掉反亲和、开关全关时结果相同 |
| RB11 | dev | 弹性 SubGroup 作业的 SubJob 记录 | `noPAA=false [S7b] … MISMATCH subJob c1/pg-myq/prefill-0 (hard tier 1): pod on z1-r01-n0, recorded AllocatedHyperNode="dc"`；`noPAA=true [off+detTies]` 同样 MISMATCH |
| RB12 | dev | 森林用例重放 | 四个用例在选项 b 下：`FAILS: recorded SubJob decision keys=[b-t2 c-t2 shared-t3] want only "shared-t3"`（2 Pod 用例为 `[b-t2 c-t2]`）；其余模式 `all assertions hold` |
| RB13 | dev | 规模成本 | `20x100x2 \| 2022 \| 4000 \| 27.12ms \| 51.427ms \| 686µs \| 3.574s … \| 3.522s … \| 4.885s …` |
| U1 | up | 同 RB1/RB1c，加"保留"顺序 | 条件三行与 dev 相同；`budget=1 order="reserve" placed=2`，重试在 `<cluster-top-hypernode>` 成功 |
| U2 | up | 状态和记录 | HB1 off：`2/4 tasks in gang unschedulable … 2 Schedulable, 2 Unschedulable`（与用户报告一致）；S7：4/4，`annotation="dc"`；2 Pod 对照开关前后计数器相同（10/10/20/18） |
| U3 | up | 第二会话 | HB1、HB2、HB4 类 off：`newly placed=0`；S7：`newly placed=2` |
| U4 | up | 互斥反亲和（J1 为 HB1） | S7：`J1 placed=4 … \| J2 placed=0 []`，`J2 condition: "… 2/2 tasks in gang unschedulable …"` |
| U5 | up | 规模成本 | `20x100x2 \| 2022 \| 4000 \| 1.086ms \| 24.875ms \| 1.044ms \| 24.958ms \| 658µs \| 3.91s … \| 3.988s …` |
| U6 | up | S9 对 S8，同预算（R5 生成器 1000 个、R4 生成器 1200 个） | `S8 budget 4, reserve \| 409 \| 0 \| 0 \| 0`；`S9 budget 4, per layer \| 407 \| 0 \| 2 \| 0`；R4：977 对 975 |
| U7 | up | 提名快速路径 | 开关关：`fast path agrees with both oracles everywhere`；开关开：80 处 `fast=false full=true`（72 + 8），无 UNSOUND 行；原单测开关开时失败、关时通过 |
| 回归套件 | 副本 | 开关全关 | up：`actions/...`、`framework`、GTA、`api` 全部 ok；dev：`allocate`（91.99 s）、`framework`、GTA、`api` ok；dev 加 `VC_PROBE_SCOPE_UNION=1 VC_PROBE_REPAIR_BUDGET=8` 同样 ok |

## 5. 建议补充的回归覆盖

对照 §15.9，缺少以下单测：

- **重试失败后的条件文本（F1）：** N/M 和 Schedulable/Unschedulable 的分布不因重试变乐观，条件里出现"搜索未完成"。T31 只要求"记录"，没有断言条件。
- **同会话互斥反亲和（F2）：** 两种作业顺序，期望在用户决策后写死。T28 是观测指标，不覆盖这一类。
- **弹性 gang 加反亲和加 SubGroup（F3）：** 放下的 Pod 数不少于现行，SubJob 的 `AllocatedHyperNode` 等于实际落位。
- **软模式作业和已部分运行的作业：** T30 只覆盖追加 Pod，需要再加"同一状态在开关关时"的断言（F9）。
- **没有真实公共父域的拓扑：** HB1、HB2 加注解值（F14）。
- **排除集后来变化（D13）和同类 PodGroup 自身扩容（D14）；** 再加占用回退窗口的用例（任务还没同步，只有 `AllocatedHyperNode`）。
- **无解反亲和作业的试放次数为 0（F4）：** T34 加计数断言。
- **gradient 组合耗时基准（F5）：** 2 千和 1 万个 HyperNode。
- **S9 不得少于同预算的 S8（F6）。**
- **提名的第三种情形（F12）：** 提名域在可用域内，但容器是被部分排除的父域。T32 没有覆盖。
- **森林用例改用声明可用域的假插件（F11）；摘要文本（F7）；重试成功加拒绝缓存（F13）。**
- **Pipelined/Releasing 资源下的可用域与重试；required SubGroup（反）亲和与重排。** T35 只写了"同时存在"。

Kind 场景缺少：

- 互斥反亲和的两个作业，两种顺序。
- 回滚：用修改后的构建放下 HB1，换回未修改构建，删一个 Pod；预期新 Pod Pending，其余 Pod 不被驱逐。
- 重试失败的作业的 PodGroup 条件和事件文本。
- 没有公共父 HyperNode 的拓扑。
- 一个长期无解的反亲和作业存在时，调度周期耗时的前后对比。
- 上游 `test/e2e/hypernode/podgroup_anti_affinity_test.go` 在开关开时重跑（需要你明确要求跑 e2e）。

## 6. 未验证的风险

- **Kind：** 没有做，所有结论止于单测。
- **Pipelined/Releasing 资源和 `JobPipelined` 成功分支：** 没有探测。重试成功用 `Merge` 保留 statement，读码看与正常路径等价，没有实测。
- **多队列、队列超用：** 只读码（`ssn.Allocatable` 在逐任务处判断，与可用域无关），没有探测。
- **preferred 反亲和项与被部分排除的父域的打分：** 没测（D27）。
- **dev-1.14 的 required SubGroup（反）亲和：** 与可用域、与重试重排的交互都没测；重排会改变由谁先建立锚点。
- **占用回退的误伤窗口：** 任务路径为空时按 `AllocatedHyperNode` 展开（dev 原版 `topology_affinity_info.go:494` 的注释承认会过度排除）。S7 之后反亲和作业自己也会有跨域记录，重启或缓存未同步时可能把无关域算作被占。没有复现。
- **上游 e2e：** `:312,336,388` 三个期望 Unschedulable 的用例没有运行；读码看 Pod 被钉在被占域的节点上，应不受影响。
- **拒绝缓存与重试、可用域的联动：** 没测。
- **通用路径上弹性 SubGroup 作业的记录缺陷在上游是否存在：** 没查。
- **S9 和"保留"顺序：** dev-1.14 没有原型。
- **耗时数字：** 来自有其他负载的共享机器，各 3 次；量级可信，百分比不精确。
- **规模：** 随机场景不超过 20 个节点；RB13、U5 的拓扑是规则树，作业只有 4 个 Pod。大作业到达最宽容器时 SubJob 级组合的总开销没有实测。
- **第三方插件：** 用"列出允许域"表达排除的第三方 gradient 插件行为不变，但与 GTA 的可用域组合后的结果没有测试。