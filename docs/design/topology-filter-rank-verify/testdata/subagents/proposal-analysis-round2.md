# 方案分析（第二轮）：对 §15 草稿的独立复核

- 角色：方案分析员（独立 subagent，agent id `39547571-eb34-4930-8264-0035d2db8d43`）。没有参与设计与原型编写。
- 复核对象：`evidence/round2/section15-draft-for-review.md`、第二轮原型补丁与探测。
- 它运行的探测源码与日志归档在 `subagents/review-analyst-round2/`。
- 下文为该 subagent 的原始报告。各发现的处置见 `PROPOSAL_COMMIT.md` §16。

---

# 004 第二轮提案分析报告（§15 草稿的独立复核）

**环境说明**
- 本环境禁止向 `/tmp` 写文件，所以没有创建 `/tmp/review-analyst`，也没有往 worktree 拷文件。我改用 `go test -overlay` 把探测文件注入 `/tmp/vc-probe-master2/pkg/scheduler/actions/allocate/`，文件名为 `zz_review_analyst_*`。
- `git -C /tmp/vc-probe-master2 status --short` 前后一致（8 行）。workspace 没有改动。
- 源码、日志、运行脚本在 `S=subagents/review-analyst-round2/`（136K，无二进制）。
- "unmodified" 配置是在 master2 上把原型开关全关跑的，没有在 pristine worktree 上跑。dev-1.14 我只读了代码。

## 1. 结论

1. **G1 相对"现行穷举 + 确定性并列"成立。** 我没有构造出反例；在更宽的场景空间（2000 + 593 个）里复测，丢失 0、落位变化 0、非法 0。
2. **但这个参照不是用户说的"现在"，§15.1 第 1 条因此不成立。** 现行并列是随机的，相当于跨周期的隐式重启；确定性并列（S1，排序路径强制启用）把"几个周期内能调度"变成"永远调度不了"。
   - SubJob 级并列：16 个作业名里 8 个永不调度，重试能救回。
   - 节点级并列：16 个里 8 个永不调度，重试救不回。
3. **"可用域"和"失败后重试"的方向可取。** 实测重试优于"最受限优先"的静态顺序；后者当正常顺序用还会丢掉今天能调度的作业。
4. **重试有四个 §15 没写的问题。** 预算规则三处不自洽；重试后的失败信息变差；弹性 gang 上有状态泄漏；G3 只覆盖预算截断这一种情况。
5. **几个数字偏乐观。** "39 → 4" 需要预算 8，建议默认值下是 5；"比现行穷举少 34%" 只在 R5 成立，R4 上是多 12%。
6. **约束分型的合同不完整。** 可用域按 HyperNode 名字求交，并且只在 Job 级设一次；遇到多个可用域来源或 SubJob 级排除时不成立。

## 2. 发现表

行号指 `/tmp/vc-probe-master2/pkg/scheduler/...`，另有说明的除外。

| 编号 | 严重度 | 发现 | 证据类型 | 证据 | 建议处置 |
|---|---|---|---|---|---|
| F1 | 阻断（针对 §15.1 第 1 条、G1 的表述、S1/S3 的上线顺序；不阻断 S7/S8） | 确定性并列把"几个周期内能调度"变成"永远调度不了"。对 C 类（单 SubJob 内任务争节点），重试不介入，救不回。 | 实测 + 读码 | A1、A9（§3.1）。`actions/allocate/rank_verify.go:140-142`：`deterministic()` 在 `rankThenVerify` 打开时恒为真。并列点在 `allocate.go:829、850-860、1342-1346`。 | ① G1 改成对"现行真实行为、跨周期"陈述。② S1/S3 不能先于缓解措施上线，§15.8 "S7、S8 可以先做"应改为"必须先做"。③ 并列键加一个随周期变化、写进决策记录的盐；或者把 C 类纳入范围。④ 增加多周期并列用例。 |
| F2 | 重要 | G3 表述过强。"搜索未完成"只在预算用完时标记。C 类漏解不标记。重试因"顺序重复"结束时也不标记。 | 实测 + 读码 + 推理 | A6：R4 各配置下都有 4 个"有合法放置、未调度、未标记"。A3 预算 16：4 次后因重复停止，未标记（该例确实无解）。`scope_repair.go:275-283、289-291`。"重试跑过、有解、未标记"在 2200 个场景里是 0 例，这一条只是推理。 | G3 改为"除非有可靠上界证明，否则不可调度消息一律写'未找到'并附覆盖范围"。把 C 类明确排除在 G3 之外。 |
| F3 | 重要 | 重试失败后，用户看到的失败信息变差。 | 实测 + 读码 | A3：预算 0 时是 `1 Schedulable, 2 Unschedulable`，带 2 条任务级原因；预算 4 或 16 时是 `3 Schedulable`，只剩 1 条，而该作业无解。`scope_repair.go:248-252` 每次重试都重置 FitErrors，最后留下的是末次重试的。 | 重试失败后恢复正常搜索的 FitErrors。把它列入 S8 的行为变化（§15.8 现在只写了成本）。加用例。 |
| F4 | 重要 | 预算规则三处不自洽：(a) 早退的尝试也扣 1 次；(b) 虚拟根与真实根 `dc` 节点集相同，却各试一遍；(c) 预算是按 `allocateForJob` 调用计，不是"每作业每会话"。 | 实测 + 读码 | (a) A6：没有任何 SubJob 被挡住就失败的尝试占 141/380（R4）、431/918（R5，4 留 1）、347/604（R5，2 留 1）。(b) A8：R5 预算 4 时 918 次里 128 次落在 `<cluster-top-hypernode>`，全部是 `dc` 里试过的顺序，成功 0；"留 1 次"时 181 次里 159 次重复，成功 2；A3 的第 3、4 次重复第 1、2 次。(c) `allocate.go:505` 是局部变量，`:436-438` 会把作业放回队列。 | 候选按"可用域内的节点集"去重。早退不扣预算，或先单独验证被挡住的 SubJob。预算定义为每作业每周期。§15.4.3 "最宽的容器包含其余容器"改掉：它只是列表最后一个。 |
| F5 | 重要 | 标题数字和成本读数偏乐观。 | 实测 | A6：建议默认值（4，留 1）在 R4 是 977 调度、5 漏解；草稿 R4 表里没有这一行。失败路径任务试放 7042，现行 6287（多 12%），排序 4675（多 51%）。39 个漏解里 14 个只在拟议语义下才算合法（上游 `docs/design/group-topology-affinity.md:406`）。 | §15.1 第 3 条写明预算。§15.5 成本读数同时给 R4。"漏解率 4.0% / 4.9%"注明是合成的紧场景、按拟议语义。 |
| F6 | 重要（缺陷本身影响有限，重要的是测量缺口） | 状态泄漏：重试在容器 X 成功后，提交时把"正常试放失败那一次"在 X 里记下的 SubJob 决策也应用了。没放下任何 Pod 的 SubJob 被记上 `AllocatedHyperNode`，本会话内它的 Pod 放不到别处。 | 实测 + 读码 | A2：重试后 2/3，对照 3/3；`worker-0 AllocatedHyperNode="z0-r00"`，任务仍 Pending；日志 `no intersection between hyperNodeAvailable z0-r01 and hyperNodeHighestAllowed z0-r00`。`recorder.go:55-63、79-91`，`scope_repair.go:264-269`。下一会话应被 `framework/session.go:291-296、382-395` 纠正，这一点只读了代码。R3–R6 的生成器全是严格 gang（`probes/prototype2/zz_probe_round2_test.go:250`，`probes/prototype2-ext/zz_probe_round2_oracle_test.go:386、561`），看不到它。 | 每次 Job 级试放前清空该容器的 SubJob 决策，或只应用获胜那次的。T26、T27 的生成器加入弹性 gang。 |
| F7 | 一般 | "新增候选只出现在现行候选之上的层"是 GTA 的性质，不是框架保证的。§15.4.1 "只在现行搜索结果为否时介入"对可用域不准确：它对所有带反亲和的作业都改候选表。 | 读码 + 推理 | `plugins/group-topology-affinity/group_topology_affinity.go:262-272`：允许集是"最小 term 层及以下"，所以被部分排除的祖先都更高。`framework/probe_scope.go:71-91` 只按祖先关系保留。如果某个可用域插件只列最上层的允许域，或允许集里有高于部分排除域的层，落位相同就不成立。 | 把这条写成合同里的不变量，框架里断言，并加单测。 |
| F8 | 重要（合同） | 可用域的表示和作用范围不够通用。 | 读码 + 推理 | ① `probe_scope.go:66-69` 按名字求交；两个来源粒度不同时（一个只列 tier-2，一个只列 tier-1），名字交集为空而节点交集非空。② `allocate.go:502-503` 每个 Job 只设一次；dev-1.14 的 required SubGroup 反亲和若归为可用域，SubJob 的任务会看到对它已排除的节点，有非法放置风险（无原型，推理）。③ `rank_verify.go:203-219` 和 `FilterGradientsByMinResource` 给打分插件的是未过滤的节点。④ GTA 一个结果里同时带"已有落位的搜索根"和"排除"（`group_topology_affinity.go:143-157、217-222`），与 §15.4.2 把"已有 Pod 所在的域"归为容器不一致。 | 可用域定义为节点集，按单元（Job、SubJob）分层生效，组合用集合求交。容器保留条件改为"至少有 1 个域内节点"。打分回调收到域内节点。 |
| F9 | 一般 | 门槛和早退的可靠性边界。 | 读码 + 推理 | 门槛（`scope_repair.go:299-315`）：`FutureIdle()` 可以为负（`api/node_info.go:115-117`，`api/resource_info.go:309-330`），求和后可能跳过可行容器；需求用 `InitResreq` 求和，`InitResreq > Resreq` 时高估。早退（`scope_repair.go:185-196`）：对资源和反亲和可靠；SubJob 之间有正向依赖（required Pod 亲和）时不可靠，但只损失重试机会。dev-1.14 的 SubGroup 亲和对称、无同伴时不约束（`/tmp/vc-probe-dev114/pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go:617-668`），重排后仍合法。 | 门槛按节点截断负值，需求按 `Resreq` 之和加一次差额。在 §15.4.3 写明早退的前提。 |
| F10 | 一般 | §15.4.1 和 §15.3 的表述。 | 推理 + 实测数据 | ① 预算就是第 5 项对第 2 项的上限（R5 预算 4 漏 1 个，预算 64 为 0），"不会为了第 5 项牺牲第 2 项"只对第一轮的提前停止成立。② 第 3 项是贪心，并且先于重试。③ 弹性 gang 缺"超出 minMember 的 Pod 数"这一维。④ §15.3 第 2 行"结果相同"不适用于 SubGroup 作业的 Job 级（§5.2 保持穷举），也有 §5.3 的并列加分例外。⑤ §15.6 第 7 项"重试是确定的"依赖 S1。 | 改成"有界的字典序"。在 §15.3 每行标明精确、贪心还是有界。 |
| F11 | 一般（补证，正面） | §15.4.5 里"落空的作业一定排在某个新调度成功的作业之后，没有逐例核对"，我逐例核对了。 | 实测 | A5：6 个落空作业，4 个直接确认；2 个在新配置下没有任何任务试放（被上界剪掉），新增作业记录在前；反例 0。 | 可以去掉"没有逐例核对"，引用本结果。 |
| F12 | 建议 | 测量方法复核：合法性检查和求解器对已生成的约束类型没有发现错误；局限在生成器。 | 读码 + 实测 | 语义核对：SubGroup 同域、作业 hard 层为单域、反亲和排除锚点所在 zone、nodeSelector、容量（只有 CPU，内存不构成约束）。"调度成功"定义为放下任意 Pod（`zz_probe_round2_test.go:413`）；A6 里我加了完整性断言，0 例部分放置。生成器从不产生：无真实根、多 term、tier 1 或 3 的 term、多锚点、soft、hard tier 1、弹性 gang、作业已部分运行。B1 补了这些，结果一致。 | 采用更宽的生成器。在 §15.5 写明"0 丢失"只对该分布成立。 |
| F13 | 建议 | §15.9、§15.11 的缺项。 | — | 见 §2.1。 | 补入。 |

没有发现问题的三处：
- **(ii) `nodesInScope`**（读码）：过滤保持顺序；队列节点缓存存的是未过滤列表（`allocate.go:1055-1063`）；抽样目标按未过滤长度算（`:1051`）。完全在域内的候选，节点列表不变。
- **(iii) 重试的位置**（读码）：`repairPerLayer` 关闭时只在层循环之后调用（`allocate.go:645-650`）；循环内的提前返回不经过重试，与现行相同。
- **(i) 候选组合**：除 F7 的前提外，没有构造出"现行保留、修订后丢掉或换序"的例子。

### 2.1 §15.9 和 §15.11 应补的项

- 并列加争用的多周期用例（F1），SubJob 级和节点级各一个。
- 弹性 gang 下的重试：提交后未落位的 SubJob 的 `AllocatedHyperNode` 为空；可选 Pod 同一会话内能放下（F6）。
- 重试失败后的状态文本断言（F3）。
- 预算口径与去重：同一节点集的容器只试一次；每作业每周期的次数上限（F4）。
- 没有真实根的拓扑。用户描述的拓扑没有 tier-3，K14 应按用户集群的实际形态各做一遍。B2 显示此时 `job.AllocatedHyperNode="<cluster-top-hypernode>"`，T30 需要先定义这种情况下什么算"正确"。
- 两个可用域来源、粒度不同；SubJob 级可用域（F8，T35 的前置条件）。
- 第二轮没有性能基准：可用域过滤的耗时；失败路径在大作业、大拓扑上的耗时；长期调度不了的作业每个周期重复相同重试的稳态开销。
- 关闭开关后的回滚：已经跨 B、C 落位的作业在旧语义下扩容。
- 域内超过 100 个节点时的节点抽样：全局游标 `lastProcessedNodeIndex` 会被额外的试放推进，不能断言落位相同。
- §15.11 应新增的风险行：F1、F3、F4、F6、F8，以及"G1 的参照是确定性并列变体"。

## 3. 我运行的探测

运行方式，`run.sh` 等价于 `cd /tmp/vc-probe-master2 && go test -overlay=<json> ./pkg/scheduler/actions/allocate/ -count=1 <args>`：

```bash
S=subagents/review-analyst-round2
T=history,prototype,baseline-dra,prototype2,prototype2-ext
$S/run.sh $T targeted          -run 'TestReviewA1|TestReviewA2|TestReviewA3' -v   # logs/a123.log
$S/run.sh $T targeted          -run 'TestReviewA5|TestReviewA6' -v                # logs/a56.log
$S/run.sh $T targeted,fuzz     -run 'TestReviewB2' -v                             # logs/b2.log
$S/run.sh $T targeted,fuzz     -run 'TestReviewB1' -v -timeout 30m                # logs/b1.log
$S/run.sh $T targeted,fuzz,alt -run 'TestReviewA7' -v -timeout 30m                # logs/a7.log
$S/run.sh $T targeted,fuzz,alt -run 'TestReviewA8' -v                             # logs/a8.log
$S/run.sh $T targeted,fuzz,alt -run 'TestReviewA9' -v                             # logs/a9.log
```

完整源码（归档请直接拷贝文件）：

| 文件 | 行数 | sha256 前 16 位 | 内容 |
|---|---|---|---|
| `$S/probes/targeted/targeted_test.go` | 484 | `74e48096e817944e` | A1、A2、A3、A5、A6 |
| `$S/probes/fuzz/wide_fuzz_test.go` | 695 | `8a6162f98aefabbb` | B1、B2，含独立的合法性检查 |
| `$S/probes/alt/alternatives_test.go` | 275 | `03ada0cf33425120` | A7、A8、A9 |
| `$S/run.sh` | — | `515e65aecf4bf519` | overlay 运行脚本 |

如果改用任务目录的 `run_probe.sh`（拷贝方式），把三个文件改名为 `zz_review_analyst_*.go` 放进一个探测目录即可。

### 3.1 反例探测的源码（A1、A2、A3、A9，与文件一致）

```go
// raRun is runProbe plus a hook that sees the session after the action ran
// and before it is closed.
func raRun(tb testing.TB, tc *uthelper.TestCommonStruct, tiers []conf.Tier, extra map[string]framework.PluginBuilder,
	action *Action, inspect func(ssn *framework.Session)) *probeResult {
	tb.Helper()
	res := &probeResult{
		counters:       &probeCounters{},
		placed:         map[string]string{},
		jobFitErrors:   map[api.JobID]string{},
		fitError:       map[api.JobID]string{},
		nodesFitErrors: map[api.JobID]map[string]string{},
	}
	tc.Plugins = probePluginBuilders(res.counters, extra)
	ssn := tc.RegisterSession(tiers, nil)
	defer tc.Close()
	tc.Run([]framework.Action{action})
	for jobID, job := range ssn.Jobs {
		for _, task := range job.Tasks {
			if task.Pod == nil || task.Pod.Status.Phase != v1.PodPending {
				continue
			}
			if task.Status == api.Pending || task.NodeName == "" {
				continue
			}
			res.placed[task.Name] = task.NodeName
		}
		res.jobFitErrors[jobID] = job.JobFitErrors
		res.fitError[jobID] = job.FitError()
		perTask := map[string]string{}
		for taskID, fitErrors := range job.NodesFitErrors {
			if fitErrors == nil {
				continue
			}
			name := string(taskID)
			if task := job.Tasks[taskID]; task != nil {
				name = task.Name
			}
			perTask[name] = fitErrors.Error()
		}
		res.nodesFitErrors[jobID] = perTask
	}
	if inspect != nil {
		inspect(ssn)
	}
	return res
}

// unmodifiedAction is the allocate action with every prototype switch off.
func unmodifiedAction() *Action {
	a := New()
	a.rankThenVerify, a.fitBound, a.deterministicTies = false, false, false
	a.repairBudget, a.repairPerLayer, a.repairOrder = 0, false, ""
	return a
}

func subGroupWorkerPolicy(minGroups int32) schedulingv1.SubGroupPolicySpec {
	size, tier := int32(1), 1
	return schedulingv1.SubGroupPolicySpec{
		Name: "worker", MatchLabelKeys: []string{"idx"}, SubGroupSize: &size, MinSubGroups: &minGroups,
		NetworkTopology: &schedulingv1.NetworkTopologySpec{Mode: schedulingv1.HardNetworkTopologyMode, HighestTierAllowed: &tier},
	}
}

// ---- A1 -------------------------------------------------------------------
// Racks a and b are equally used (2 of 4 CPU free each). The flexible SubJob
// (handled first) fits either; the pinned SubJob only fits rack a and needs
// all that is free there. The flexible SubJob's choice between a and b is a
// tie.
func tieContentionScenario(job string) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0), nodeName(0, 1, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup(job, "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{subGroupWorkerPolicy(2)}
	flexible := util.BuildPod("c1", job+"-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil)
	pinned := util.BuildPod("c1", job+"-pinned", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "1"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "tie-contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// A1: today's random tie-break makes this Job schedulable in a fraction of
// the cycles (so it schedules after a few cycles). With deterministic ties
// and no repair the outcome is fixed per Job name.
func TestReviewA1RandomTieIsAnImplicitRestart(t *testing.T) {
	const names, repeats = 16, 10
	type cfg struct {
		name   string
		action func() *Action
	}
	configs := []cfg{
		{"unmodified (random ties)", unmodifiedAction},
		{"exhaustive + deterministic ties", func() *Action { return modeExhaustiveDet.action() }},
		{"ranked + fitBound (round 1)", func() *Action { return modeRankedFit.action() }},
		{"ranked + fitBound + repair(4)", func() *Action { a := modeRankedFit.action(); a.repairBudget = 4; return a }},
	}
	t.Logf("| configuration | job names always scheduled | never scheduled | sometimes | sessions scheduled / total |")
	t.Logf("|---|---|---|---|---|")
	for _, c := range configs {
		always, never, sometimes, ok := 0, 0, 0, 0
		var neverNames []string
		for j := 0; j < names; j++ {
			job := fmt.Sprintf("job%02d", j)
			succ := 0
			for rep := 0; rep < repeats; rep++ {
				res := runProbe(t, tieContentionScenario(job), probeTiers(), nil, c.action(), nil)
				switch got := res.count(job + "-"); got {
				case 2:
					succ++
				case 0:
				default:
					t.Errorf("%s %s: partial gang placed (%d/2)", c.name, job, got)
				}
			}
			ok += succ
			switch succ {
			case repeats:
				always++
			case 0:
				never++
				neverNames = append(neverNames, job)
			default:
				sometimes++
			}
		}
		t.Logf("| %s | %d | %d | %d | %d/%d |", c.name, always, never, sometimes, ok, names*repeats)
		if len(neverNames) > 0 {
			t.Logf("    never scheduled: %s", strings.Join(neverNames, " "))
		}
	}
}

// ---- A2 -------------------------------------------------------------------
// Elastic gang: minMember 2 of 3 single-pod SubGroups. Rack a has 2 CPU free,
// rack b 4. The flexible SubJob needs 2 CPU anywhere, the two pinned SubJobs
// need 1 CPU each in rack a.
func elasticContentionScenario(flexibleFirst bool) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup("job", "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{subGroupWorkerPolicy(2)}
	idx := []string{"0", "1", "2"} // flexible, pinned1, pinned2
	if !flexibleFirst {
		idx = []string{"2", "0", "1"}
	}
	flexible := util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": idx[0]}, nil)
	pinned1 := util.BuildPod("c1", "job-pinned1", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": idx[1]}, map[string]string{"only": "a"})
	pinned2 := util.BuildPod("c1", "job-pinned2", "", v1.PodPending, api.BuildResourceList("1", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": idx[2]}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "elastic-contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned1, pinned2}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// A2: does a SubJob decision recorded by the failed normal dry-run leak into
// the committed state when repair succeeds in the same container?
func TestReviewA2StaleSubJobDecisionAfterRepair(t *testing.T) {
	type run struct {
		name          string
		flexibleFirst bool
		budget        int
	}
	for _, r := range []run{
		{"flexible first, repair off", true, 0},
		{"flexible first, repair(4)", true, 4},
		{"pinned first (control: normal search succeeds), repair(4)", false, 4},
	} {
		for _, mode := range []protoMode{modeExhaustiveDet, modeRankedFit} {
			action := mode.action()
			action.repairBudget = r.budget
			var state []string
			res := raRun(t, elasticContentionScenario(r.flexibleFirst), probeTiers(), nil, action, func(ssn *framework.Session) {
				job := ssn.Jobs["c1/job"]
				if job == nil {
					return
				}
				state = append(state, fmt.Sprintf("job.AllocatedHyperNode=%q", job.AllocatedHyperNode))
				ids := make([]string, 0, len(job.SubJobs))
				for id := range job.SubJobs {
					ids = append(ids, string(id))
				}
				sort.Strings(ids)
				for _, id := range ids {
					subJob := job.SubJobs[api.SubJobID(id)]
					var tasks []string
					for _, task := range subJob.Tasks {
						tasks = append(tasks, fmt.Sprintf("%s:%s@%s", task.Name, task.Status, task.NodeName))
					}
					sort.Strings(tasks)
					state = append(state, fmt.Sprintf("%s AllocatedHyperNode=%q tasks=%v", id, subJob.AllocatedHyperNode, tasks))
				}
			})
			t.Logf("%s / %s: placed %d/3 %v", r.name, mode.name, res.count("job-"), res.placed)
			for _, repair := range action.trace.Repairs {
				t.Logf("    %s", repair)
			}
			for _, line := range state {
				t.Logf("    %s", line)
			}
			t.Logf("    fitError: %s", shortFitError(res.fitError["c1/job"]))
		}
	}
}

// ---- A3 -------------------------------------------------------------------
// Strict gang of three that has no legal placement: two pinned SubJobs need
// 2 CPU each in rack a, which has 2 CPU free.
func infeasibleContentionScenario() *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 2, 1, "4", "16Gi", func(_, rack, _ int, n *v1.Node) {
		if rack == 0 {
			n.Labels["only"] = "a"
		}
	})
	fillerPG, fillers := fillerPods([]string{nodeName(0, 0, 0)}, "2", "1Gi")
	pg := util.BuildPodGroup("job", "c1", "q1", 3, nil, schedulingv1.PodGroupInqueue)
	pg.Spec.SubGroupPolicy = []schedulingv1.SubGroupPolicySpec{subGroupWorkerPolicy(3)}
	flexible := util.BuildPod("c1", "job-flexible", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "flexible", "idx": "0"}, nil)
	pinned1 := util.BuildPod("c1", "job-pinned1", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "1"}, map[string]string{"only": "a"})
	pinned2 := util.BuildPod("c1", "job-pinned2", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), "job",
		map[string]string{"volcano.sh/task-spec": "pinned", "idx": "2"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "infeasible-contention", PodGroups: []*schedulingv1.PodGroup{pg, fillerPG},
		Pods: append([]*v1.Pod{flexible, pinned1, pinned2}, fillers...), Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

// A3: what a user sees for a Job that is unschedulable with and without repair.
func TestReviewA3FailureDiagnosticsWithRepair(t *testing.T) {
	for _, budget := range []int{0, 4, 16} {
		action := modeRankedFit.action()
		action.repairBudget = budget
		res := runProbe(t, infeasibleContentionScenario(), probeTiers(), nil, action, nil)
		t.Logf("repairBudget=%d placed %d/3; repair attempts=%d; flagged search-incomplete=%v", budget, res.count("job-"),
			len(action.trace.Repairs), action.trace.RepairBudgetExhausted)
		t.Logf("    fitError: %s", shortFitError(res.fitError["c1/job"]))
		tasks := make([]string, 0)
		for task, msg := range res.nodesFitErrors["c1/job"] {
			tasks = append(tasks, task+": "+msg)
		}
		sort.Strings(tasks)
		for _, line := range tasks {
			t.Logf("    task fit error: %s", line)
		}
		for _, repair := range action.trace.Repairs {
			t.Logf("    %s", repair)
		}
	}
}

// A9: task-level contention with a node tie. One rack, two identical nodes
// with 4 CPU. The small task (2 CPU, handled first) fits either node; the big
// task (4 CPU) is pinned to node 0. Which node the small task takes is a tie.
func nodeTieScenario(job string, hardTier int) *uthelper.TestCommonStruct {
	topo := buildProbeTopo(1, 1, 2, "4", "16Gi", func(_, _, node int, n *v1.Node) {
		if node == 0 {
			n.Labels["only"] = "a"
		}
	})
	var pg *schedulingv1.PodGroup
	if hardTier > 0 {
		pg = util.BuildPodGroupWithNetWorkTopologies(job, "c1", "", "q1", 2, nil, schedulingv1.PodGroupInqueue, "hard", hardTier)
	} else {
		pg = util.BuildPodGroup(job, "c1", "q1", 2, nil, schedulingv1.PodGroupInqueue)
	}
	small := util.BuildPod("c1", job+"-a-small", "", v1.PodPending, api.BuildResourceList("2", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "small"}, nil)
	big := util.BuildPod("c1", job+"-b-big", "", v1.PodPending, api.BuildResourceList("4", "1Gi"), job,
		map[string]string{"volcano.sh/task-spec": "big"}, map[string]string{"only": "a"})
	tc := &uthelper.TestCommonStruct{Name: "node-tie", PodGroups: []*schedulingv1.PodGroup{pg}, Pods: []*v1.Pod{small, big}, Queues: probeQueue()}
	topo.apply(tc)
	return tc
}

func TestReviewA9NodeTieIsAnImplicitRestart(t *testing.T) {
	const names, repeats = 16, 10
	type cfg struct {
		name   string
		action func() *Action
	}
	configs := []cfg{
		{"unmodified (random ties)", unmodifiedAction},
		{"exhaustive + deterministic ties", func() *Action { return modeExhaustiveDet.action() }},
		{"ranked + fitBound (round 1)", func() *Action { return modeRankedFit.action() }},
		{"ranked + fitBound + repair(8)", func() *Action { a := modeRankedFit.action(); a.repairBudget = 8; return a }},
	}
	for _, hardTier := range []int{0, 1} {
		t.Logf("job network topology: hard tier %d (0 = none)", hardTier)
		t.Logf("| configuration | job names always scheduled | never scheduled | sometimes | sessions scheduled / total |")
		t.Logf("|---|---|---|---|---|")
		for _, c := range configs {
			always, never, sometimes, ok := 0, 0, 0, 0
			for j := 0; j < names; j++ {
				job := fmt.Sprintf("job%02d", j)
				succ := 0
				for rep := 0; rep < repeats; rep++ {
					res := runProbe(t, nodeTieScenario(job, hardTier), probeTiers(), nil, c.action(), nil)
					switch got := res.count(job + "-"); got {
					case 2:
						succ++
					case 0:
					default:
						t.Errorf("%s %s: partial gang placed (%d/2) %v", c.name, job, got, res.placed)
					}
				}
				ok += succ
				switch succ {
				case repeats:
					always++
				case 0:
					never++
				default:
					sometimes++
				}
			}
			t.Logf("| %s | %d | %d | %d | %d/%d |", c.name, always, never, sometimes, ok, names*repeats)
		}
	}
}
```

### 3.2 输出

**A1：SubJob 级并列**（16 个作业名，每个 10 次会话）

```text
| configuration | job names always scheduled | never scheduled | sometimes | sessions scheduled / total |
| unmodified (random ties) | 1 | 0 | 15 | 123/160 |
| exhaustive + deterministic ties | 8 | 8 | 0 | 80/160 |
    never scheduled: job00 job02 job04 job06 job08 job11 job13 job15
| ranked + fitBound (round 1) | 8 | 8 | 0 | 80/160 |
| ranked + fitBound + repair(4) | 16 | 0 | 0 | 160/160 |
```

**A9：节点级并列，C 类**

```text
job network topology: hard tier 0 (0 = none)
| unmodified (random ties) | 0 | 0 | 16 | 79/160 |
| exhaustive + deterministic ties | 8 | 8 | 0 | 80/160 |
| ranked + fitBound (round 1) | 8 | 8 | 0 | 80/160 |
| ranked + fitBound + repair(8) | 8 | 8 | 0 | 80/160 |
job network topology: hard tier 1 (0 = none)
| unmodified (random ties) | 0 | 0 | 16 | 83/160 |
| exhaustive + deterministic ties | 8 | 8 | 0 | 80/160 |
| ranked + fitBound (round 1) | 8 | 8 | 0 | 80/160 |
| ranked + fitBound + repair(8) | 8 | 8 | 0 | 80/160 |
```

**A2：状态泄漏**（节选，两种模式结果相同）

```text
flexible first, repair off / ranked+fitBound: placed 0/3 map[]
E… network_topology_aware.go:342] "build HyperNode gradient failed" err="getSearchRoot failed: there is no intersection between hyperNodeAvailable z0-r01 and hyperNodeHighestAllowed z0-r00" subJob="c1/job/worker-0" hyperNode="z0-r01" highestAllowedTier=1 allocatedHyperNode="z0-r00"
flexible first, repair(4) / ranked+fitBound: placed 2/3 map[job-pinned1:z0-r00-n0 job-pinned2:z0-r00-n0]
    job c1/job in z0-r00: retry 1 with [c1/job/worker-1, c1/job/worker-2] first succeeded
    job.AllocatedHyperNode="z0-r00"
    c1/job/worker-0 AllocatedHyperNode="z0-r00" tasks=[job-flexible:Pending@]
    fitError: pod group is ready, 1 Pending, 2 Binding, 2 minAvailable; Pending: 1 Schedulable.
pinned first (control: normal search succeeds), repair(4) / ranked+fitBound: placed 3/3 map[job-flexible:z0-r01-n0 job-pinned1:z0-r00-n0 job-pinned2:z0-r00-n0]
    c1/job/worker-2 AllocatedHyperNode="z0-r01" tasks=[job-flexible:Binding@z0-r01-n0]
```

**A3：失败信息**

```text
repairBudget=0 placed 0/3; repair attempts=0; flagged search-incomplete=[]
    fitError: pod group is not ready, 3 Pending, 3 minAvailable; Pending: 1 Schedulable, 2 Unschedulable.
    task fit error: job-pinned1: In hyperNode z0-r01: 0/1 nodes are unavailable: 1 node(s) didn't match Pod's node affinity/selector.
    task fit error: job-pinned2: In hyperNode z0-r01: 0/1 nodes are unavailable: 1 node(s) didn't match Pod's node affinity/selector.
repairBudget=4 placed 0/3; repair attempts=4; flagged search-incomplete=[c1/job]
    fitError: pod group is not ready, 3 Pending, 3 minAvailable; Pending: 3 Schedulable.
    task fit error: job-pinned1: In hyperNode z0-r01: …
    job c1/job in z0: retry 1 with [c1/job/worker-1, c1/job/worker-2] first failed; still blocked: [c1/job/worker-2]
    job c1/job in z0: retry 2 with [c1/job/worker-2, c1/job/worker-1] first failed; still blocked: [c1/job/worker-1]
    job c1/job in <cluster-top-hypernode>: retry 3 with [c1/job/worker-1, c1/job/worker-2] first failed; still blocked: [c1/job/worker-2]
    job c1/job in <cluster-top-hypernode>: retry 4 with [c1/job/worker-2, c1/job/worker-1] first failed; still blocked: [c1/job/worker-1]
repairBudget=16 placed 0/3; repair attempts=4; flagged search-incomplete=[]
    fitError: pod group is not ready, 3 Pending, 3 minAvailable; Pending: 3 Schedulable.
```

**A5：R6 逐例核对**

```text
seed 52: displaced job jc has no recorded task attempt in the new run (order [c1/ja])
seed 380: displaced job jc has no recorded task attempt in the new run (order [c1/ja c1/jb])
600 sessions: 6 displaced jobs; 4 handled after a newly scheduled job; 0 not explained; 2 without recorded order
```

**A6：建议默认值下的结果**。第二行与草稿 R4 表的"重试 4 次"一致（975 / 7 / 6973），说明我的运行方式复现了草稿的数字。

| R4（1200 个，982 个可行） | 调度 | 漏解 | 漏解且已标记 | 漏解、跑过重试、未标记 | 漏解、没跑重试、未标记 | 重试次数 | 无 SubJob 被挡住的失败尝试 | 预算用完 | 任务试放（现行调度不了） |
|---|---|---|---|---|---|---|---|---|---|
| 现行 | 943 | 39 | 0 | 0 | 39 | 0 | 0 | 0 | 6287 |
| 排序 + 可用域 + 重试 4 | 975 | 7 | 3 | 0 | 4 | 380 | 140 | 72 | 6973 |
| 排序 + 可用域 + 重试 4，留 1（建议默认） | 977 | 5 | 1 | 0 | 4 | 380 | 141 | 70 | 7042 |
| 排序 + 可用域 + 重试 8，留 1 | 978 | 4 | 0 | 0 | 4 | 547 | 160 | 26 | 8078 |

| R5（1000 个，410 个可行） | 调度 | 漏解 | 已标记 | 跑过重试未标记 | 没跑重试未标记 | 重试次数 | 无 SubJob 被挡住的失败尝试 | 预算用完 | 任务试放 |
|---|---|---|---|---|---|---|---|---|---|
| 现行 | 390 | 20 | 0 | 0 | 20 | 0 | 0 | 0 | 31959 |
| 排序 + 重试 2，留 1 | 407 | 3 | 3 | 0 | 0 | 604 | 347 | 271 | 19171 |
| 排序 + 重试 4，留 1 | 409 | 1 | 1 | 0 | 0 | 918 | 431 | 131 | 21063 |

**A8：虚拟根上的重复尝试**（R5）

```text
budget 4 order "":        327 jobs entered repair; 918 attempts; 128 in <cluster-top-hypernode>, of which 128 repeat an order already tried in dc; successes: 0 in <cluster-top-hypernode>, 17 elsewhere
budget 4 order "reserve": 918 attempts; 181 in <cluster-top-hypernode>, of which 159 repeat …; successes: 2 in <cluster-top-hypernode>, 17 elsewhere
budget 8 order "":        1235 attempts; 234 in <cluster-top-hypernode>, of which 234 repeat …; successes: 0 …, 19 elsewhere
budget 8 order "reserve": 1235 attempts; 249 in <cluster-top-hypernode>, of which 249 repeat …; successes: 0 …, 19 elsewhere
```

**B1：更宽的单调性随机测试**（2000 个场景，参照为"穷举 + 确定性并列"）
- 覆盖：无真实根 678；required 反亲和 972（两个 term 287；term 在 tier 1、2、3 分别 286、795、178）；hard tier 1、2、3 分别 208、511、186；soft 317；弹性 gang 664；三个 SubGroup 策略 496。
- 第 2 阶段：把现行放下的作业设为运行中再扩容，共 593 个。

| 第 1 阶段（现行放下 1135；非法 0；不完整 0） | 放下 | 新增 | 丢失 | 落位变化 | Pod 变少 | Pod 变多 | 非法 | 不完整 |
|---|---|---|---|---|---|---|---|---|
| 排序 + 上界 | 1135 | 0 | 0 | 0 | 0 | 0 | 0 | 0 |
| 排序 + 可用域 | 1187 | 52 | 0 | 0 | 0 | 0 | 0 | 0 |
| 排序 + 可用域 + 重试 4，留 1 | 1199 | 64 | 0 | 0 | 0 | 0 | 0 | 0 |
| 穷举 + 可用域 + 重试 4，留 1 | 1199 | 64 | 0 | 0 | 0 | 0 | 0 | 0 |
| 排序 + 可用域 + 重试 8 | 1198 | 63 | 0 | 0 | 0 | 0 | 0 | 0 |

第 2 阶段：五个配置都是放下 504，各项差异为 0。

**B2：用户场景，但没有真实的 tier-3 根**

```text
current search: placed 0/4 …; Pending: 1 Unschedulable, 3 Schedulable.
ranked + region + repair(4, reserve): placed 4/4 map[job-decode-g2-p0:z1-r01-n0 job-decode-g3-p0:z2-r00-n0 job-prefill-g0-p0:z2-r02-n0 job-prefill-g1-p0:z2-r01-n0]; job.AllocatedHyperNode="<cluster-top-hypernode>"; illegal="" incomplete=""
    usable region of c1/job: [z1 z2]
    scale-up by one SubGroup, current search: newly placed map[job-decode-g23-p0:z1-r02-n0]; illegal=""
    scale-up by one SubGroup, ranked + region + repair(4, reserve): newly placed map[job-decode-g23-p0:z1-r02-n0]; illegal=""
```

## 4. 备选方案比较

A7 实测，全部在"排序 + 上界"之上，R4 另开可用域。"任务试放"只统计正常搜索失败的场景；R4 这一列的口径与草稿"现行调度不了"那一栏不同。

| 方案 | R5 漏解（410 可行） | R4 漏解（982 可行） | 任务试放 R5 / R4 | 对今天能调度的作业 | 评价 |
|---|---|---|---|---|---|
| 只有正常搜索 | 20 | 25 | 16790 / 5022 | — | 基线 |
| 被挡住的先放（4，留 1），即草稿方案 | 1 | 5 | 21063 / 6763 | 无影响 | 覆盖最好、成本最低。问题见 F3、F4、F6。 |
| 失败后按"最受限优先"整轮重跑一遍 | 5 | 6 | 34603 / 10245 | 无影响 | 覆盖更差，成本约翻倍。静态键（有 nodeSelector、层更低、请求更大）看不到实际争用。 |
| "最受限优先"作为正常顺序 | 8 | 8 | — | R5：390 个里 258 个落位变化、3 个丢失。R4：957 个里 168 个变化、2 个丢失。 | 违反 G1，不可取。 |
| 最宽容器优先（草稿数据，预算 1） | 7（最窄优先 9） | 7（最窄优先 12） | — | 无影响 | 小预算下更好，但放弃层更窄。去重（F4）之后，"留 1 次"才真正留给了不同的节点集。 |
| 对 SubJob 选域做有限偏差搜索 | 未测 | 未测 | 每个容器至少多 k 次整作业试放 | 无影响 | 针对的那一类在 2200 个场景里是 0 例，现在没有数据支持。需要把 `forcedChoice` 变成正式机制。 |
| 并列键加周期盐，或失败后保留随机并列 | 未测 | 未测 | 0 | 针对 F1 | 现行随机并列下，A1、A9 里"永不调度"都是 0 个。它是 F1 的候选解，不能替代重试。 |

## 5. 未能核实的事项

- **基线**：没有在 pristine upstream 上跑 A1、A9，依据的是"开关全关时不进入原型分支"。dev-1.14 没有运行任何测试，F9 关于 SubGroup 亲和的结论只是读码。
- **F6**：过期的 SubJob `AllocatedHyperNode` 在下一会话是否确实被纠正，只读了 `session.go` 和 `cache/cache.go:1888-1906`，没有做多会话测试。
- **F2**：重试因顺序重复而结束、作业其实有解却未标记，没有观察到实例。
- **F8**：两个可用域来源的名字交集问题、SubJob 级可用域泄漏，都是推理；上游只有 GTA 一个来源，dev-1.14 的 SubGroup 反亲和没有原型。
- **F9**：负的 `FutureIdle`、`InitResreq > Resreq`、SubJob 间的 required Pod 亲和，都没有构造场景。
- **节点抽样**：域内超过 100 个节点时游标的影响没有测；所有探测的域都小于 100 个节点。
- **其他路径**：不可调度作业缓存、提名快速路径、`PurposeEvict` 与可用域或重试的联动没有测。成员重叠的 HyperNode、不在任何 HyperNode 下的节点，只做了读码推理。
- **F1 的缓解**：周期盐没有做原型。生产上"分数精确并列加争用"的频率未知。
- **Kind**：没有做。所有结论都是单测级，场景不超过 18 个节点、12 个 Pod。
- **`/tmp` 里的残留**：我的第一次读取超出输出上限，工具运行时自动生成了 `/tmp/1794420324426-copilot-tool-output-1043943-6acc4a23-fa87-4470-933c-4ed50d095d11.txt`，我没有删除。`/tmp` 下其余新增条目不是我产生的。