> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 社区 PR 材料：SubGroup HyperNode Topology Affinity

## 提交范围

- **目标基线分支：** `wangyang0616/task2-podgroup-antiaffinity-v1.14.1`（`a42372bdc`）
- **提交分支：** `VanderChen:dev/podgroup-antiaffinity`
- **待发布提交：**
  1. `8fcc0012c feat(scheduler): support SubGroup topology affinity`
  2. `edaaf83a7 fix(scheduler): search SubJob topology placements jointly`
  3. `c49caabf9 docs: document HyperNode topology-affinity scheduling`
  4. `47e691146 docs: add topology affinity examples`

此 PR 必须以以上基线分支为目标，而不是 `master`；基线分支已经包含其依赖的
PodGroup HyperNode anti-affinity 工作。

## 建议 PR 标题

```text
feat(scheduler): support SubGroup topology affinity
```

## 建议 PR 内容（中文版）

```markdown
## 本 PR 的改动

本 PR 为 `PodGroup` 增加可选的 `topologyAffinity` 规则，使具备
HyperNode 感知能力的调度可以在两个层次表达拓扑放置关系：

- **PodGroup 层：** 与匹配 PodGroup 的 required/preferred anti-affinity。
- **SubGroup 层：** `subGroupPolicy` 定义的 SubGroup 间的
  required/preferred affinity 与 anti-affinity。

required 规则仍是硬可行性约束。preferred 规则和 soft network topology
仍是偏好：当无法满足偏好布局时，调度器会保留可行的 fallback。

### API 与 admission

- 在 scheduling API 和 `PodGroupSpec` 中增加 `TopologyAffinitySpec`、
  PodGroup anti-affinity term、SubGroup affinity/anti-affinity term。
- admission 校验 term 结构、权重、引用的 SubGroup policy、tier 字段，以及
  互相冲突的重叠 hard SubGroup 规则。
- 包含新可选 API 所需的 generated CRD 与 apply-configuration 更新。

### 调度器行为

- HyperNode gradient callback 现在区分“插件对当前对象没有 hard 约束意见”与
  “插件返回空的 hard 可行候选集”。framework 仅交集实际生效的 hard 约束，
  因此 neutral 插件不会意外移除其他插件的候选节点。
- 扩展 `group-topology-affinity`：required PodGroup/SubGroup 规则用于筛选
  候选，preferred 规则用于给候选打分。
- 将 soft network-topology 偏好从 gradient intersection 移到 HyperNode
  score。这样 soft fallback 始终保留，并能与 topology-affinity 正确组合。
- 在选择 Job HyperNode 前，先比较完整的 Job/SubGroup dry-run 放置方案；
  比较同时考虑插件得分与 Job 级 soft placement，并采用稳定的 tie-breaker。
- 对 required SubGroup 关系，仅搜索当前 gang transaction 所需的 SubGroup，
  缓存被支配的 placement state；当 minimum 为 0 或小于总 SubGroup 数量时，
  仍保证可以取得调度进展。

### Reviewer 建议路径

1. 先查看 API 与校验：
   `staging/src/volcano.sh/apis/pkg/apis/scheduling/` 和
   `pkg/webhooks/admission/podgroups/validate/`。
2. 查看 hard constraint 组合逻辑：
   `pkg/scheduler/framework/session_plugins.go`。
3. 查看 required/preferred 规则计算：
   `pkg/scheduler/plugins/group-topology-affinity/`；以及 soft topology
   score：`pkg/scheduler/plugins/network-topology-aware/`。
4. 查看方案选择与有界 SubGroup 搜索：
   `pkg/scheduler/actions/allocate/allocate.go`。
5. CRD、Helm 与 development-installer 的变更是可选 API 和插件启用所需的
   generated/deployment 配套，不包含独立的调度逻辑。

### 兼容性

- `topologyAffinity` 为可选字段；未配置它的已有工作负载继续沿用当前调度路径。
- required term 会拒绝不可行 placement；preferred term 和 soft network topology
  仅影响排序，并保留可行 fallback。

### 验证

已在最终提交树执行：

```bash
git diff --check a42372bdc..HEAD
go test -count=1 ./pkg/scheduler/actions/allocate ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity ./pkg/scheduler/api \
  ./pkg/webhooks/admission/podgroups/validate
make vc-scheduler
```

以上检查均通过。

Kind 验证使用标准四节点拓扑，并启用两个 HyperNode 插件。已通过六个 focused
topology-affinity 场景、生产形态的 `weight: 100` preferred SubGroup
anti-affinity 回归，以及 Llama-70B 形态的 hard PodGroup/SubGroup affinity
场景。

### 已验证的 Kind 场景：精确 selector 与 hard SubGroup 放置

在四节点 Kind 集群应用本仓库生成的 PodGroup CRD 后，使用原始精确
`podGroupSelector.matchLabels` selector，运行了一个缩减版 Llama-70B 形态的
工作负载：

- 一个 anchor PodGroup 固定在 `kind-tier2-pair-a`（`supernode-a`）。
- target PodGroup 包含两个单 Pod 的 `prefill` shard 和两个单 Pod 的 `decode`
  shard（`minMember: 4`；每个 role 的 `subGroupSize: 1`、`minSubGroups: 2`）。
- 它的 hard 规则要求：PodGroup 在 `supernode` 层分离；target SubGroup 在
  `supernode` 层 affinity；同 role shard 在 `rack` 层 anti-affinity。

精确 selector 被 admission 接受且没有被裁剪。target 被放置在
`kind-tier2-pair-b`；`prefill/p0` 与 `decode/d0` 运行在
`kind-tier1-worker2`，`prefill/p1` 与 `decode/d1` 运行在
`kind-tier1-worker3`。这证明了三项 hard 规则均生效：与 anchor 分离、target
共享同一 supernode、同 role 的 shard 位于不同 rack。跨 role 的 rack 共享由该配置
有意允许。

未将 `make unit-test` 标为通过：当前 macOS checkout 在 agent QoS 的 build
constraint 与 `pkg/networkqos/tc` 上存在既有、与本改动无关的问题。上述 focused
scheduler、API、admission、build 和 Kind 覆盖均已通过。
```

## Reviewer 意见第 2 点答复

这里理解的“放在 allocate 中”是指是否应该修改 allocate action 的分配控制流，
而不是代码物理上位于 `allocate.go` 还是同 package 的另一个文件。因此没有通过拆分
文件来回应这条意见，candidate 与搜索逻辑仍放在 `allocate.go`；仅将
`subJobAllocationOption` 重命名为语义更明确的 `subJobPlacementCandidate`。

这部分执行逻辑仍不能整体迁移到 `group-topology-affinity` 插件或 `SubJobInfo`。
原因不是只看当前结构体引用了哪些字段，而是 Bug 002 已经验证过该问题的源点在
allocation transaction 的控制流。

Bug 002（提交 `6d2abfe071b43ed5113bd78c6c15fe103c091363`）修复前，
`minMember=0`、`minSubGroups=0` 的 Job 在尚未模拟任何 SubJob 时就可能满足
`JobReady`。allocate 因此直接结束循环或回溯搜索，后续 peer SubJob 根本不会进入
插件回调。此时即使 `group-topology-affinity` 内部的 required/preferred 规则计算
完全正确，也没有机会产生作用：插件只能对“调用方交给它的当前状态”做候选过滤和
打分，无法让已经结束的 allocate 循环继续执行。

required SubGroup affinity/anti-affinity 还是一个跨 SubJob 的组合搜索问题。调度器
需要按以下顺序处理：

1. 为 SubJob A 选择一个候选 HyperNode，并恢复其 dry-run `Statement`，形成暂态
   资源占用；
2. 更新 A/Job 的暂态 `AllocatedHyperNode`，再让插件基于这个状态计算 SubJob B
   的候选；
3. 如果 B 无解，丢弃暂态操作并回溯到 A 的下一个候选；如果组合可行，再统一恢复
   并提交最终 `Statement`。

preferred 规则虽然不拒绝候选，也要求 allocate 在普通 gang ready 之后继续模拟
被 term 引用的 peer SubJob，否则后续 SubJob 不会参与评分，偏好规则会等同于没有
生效。上述循环终止条件、`JobWorksheet` 克隆、`Statement` 的 recover/discard、
资源暂占与最终提交边界都属于 allocate action 的职责，并且要与其他 predicate、
资源检查及调度插件共同组成同一个事务。

如果把这些过程放进 `group-topology-affinity`，插件就必须取得 task queue、worksheet、
`Statement`、`JobReady/JobPipelined` 和提交/回滚控制权，实质上会在插件里再实现一套
allocator，也难以与 `network-topology-aware` 及后续其他拓扑插件组合。如果放进
`SubJobInfo`，则会让 API 状态对象承担 scheduler/framework 的执行职责，并形成错误的
依赖方向。因此当前边界是：

- `group-topology-affinity` 负责给定暂态下的 required 候选计算和 preferred 打分；
- allocate action 负责 SubJob 枚举、暂态资源事务、跨 SubJob 回溯和最终提交。

这并不是说架构上只有这一种实现。长期可以新增一个通用的 topology planning
扩展点，由插件声明本次 transaction 必须参与的 SubJob、是否需要回溯及完成条件，
仍由 allocator 执行资源事务；也可以抽象独立的 scheduler-internal placement
planner。两者都需要新的组合语义和兼容性验证。当前 PR 不新增这类接口，因此在保持
现有 callback 契约的前提下，修复 Bug 002 的最小正确源点仍是 allocate action。

补充说明：当前 candidate 结构的原型最早来自初版 SubGroup topology affinity，
并非 Bug 002 才新增；Bug 002 提供的是“为什么周边搜索与终止条件必须由 allocate
持有”的直接回归证据。其修复只改动了 `allocate.go` 和 allocate 单元测试：hard
路径在仍有目标 SubJob 时不再因 `JobReady` 提前返回，preferred 路径则在相关 peer
完成 dry-run 前不提前结束。

## 历史整理

原始 feature 与后续修复（`ee838ba2d` 到 `fba432356`）已整理为以上四个可审阅提交：
第一个提交实现 API、admission、framework 组合语义和 topology plugins，不修改
allocate action；第二个提交单独引入 Bug 002 所需的跨 SubJob dry-run、working set、
回溯及完整方案选择；第三个提交包含设计与用户文档；第四个提交仅包含
topology-affinity README 和六组可运行 cases。

其中第一个提交对 `network-topology-aware` 的修改仅用于插件组合：当 SubJob 没有
hard network topology 约束时返回全量候选，使其不会在 framework 取交集时误删
`group-topology-affinity` 的候选。跨 SubJob soft network topology 的 LCA 评分、
`SoftTopologyPreferredTier` helper 和相应测试均放在第二个联合分配提交中。
合并前 tip 暂保留在本地分支
`backup/dev-podgroup-antiaffinity-before-pr-cleanup`，直至 PR 合并。

发布改写后的分支时执行：

```bash
git -C volcano push --force-with-lease origin dev/podgroup-antiaffinity
```
