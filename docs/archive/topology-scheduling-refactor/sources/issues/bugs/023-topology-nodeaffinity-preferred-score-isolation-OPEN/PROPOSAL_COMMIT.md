> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 023 Proposal And Verification Notes

## 当前决定

本任务仅记录生产问题与架构约束，暂不实施补丁。

- 状态：`OPEN`
- 当前阶段：问题确认 / 等待拓扑调度架构重构
- 实现分支：未创建
- 实现提交：无
- Volcano 源码修改：无
- 当前决定：由后续统一拓扑规划框架整体解决，不在 legacy HyperNode allocator
  上增加 plugin-specific bridge

## 调查基线

本次调查对比：

- `548d1f64^`：`e2de98c6086b0f03f64e9fb475c030ee4bdc35db`
- `548d1f64`：v1.14.1 metadata bump
- `b8af4d766f2ac14ddbb8f267462a673adc454823`
- 当前 `dev-1.14` 及其后续 topology bugfix 链

历史起点：

- `ef7aed7824b1b82b2ce9dad62371fb86a1c0d8de`
  `Network topology implements of scheduler`

## 源码结论

### HyperNode 方案选择

`pkg/scheduler/actions/allocate/allocate.go`：

1. `allocateForJob` 遍历 Job HyperNode gradient；
2. 每个 outer HyperNode 下按 SubJob 顺序 dry-run；
3. `allocateForSubJob` 遍历 SubJob HyperNode gradient；
4. 每个 SubJob HyperNode 内调用 `allocateResourcesForTasks`；
5. 只有达到 Ready/Pipelined 的 dry-run 保存为候选；
6. `selectBestHyperNodeForSubJob` 通过 `PrioritizeHyperNodes` 选 SubJob HyperNode；
7. `selectBestHyperNodeForJob` 只比较累计的 SubJob HyperNode score。

### Node 方案选择

`allocateResourcesForTasks`：

1. 在当前 HyperNode 的 physical Nodes 上执行 PrePredicate/Predicate；
2. required NodeAffinity 由 `predicates` plugin 过滤；
3. 对保留 Node 调用 `prioritizeNodes`；
4. preferred NodeAffinity 由 `nodeorder` plugin 贡献 Node score；
5. 只保留一个 `bestNode`，返回 score 被 `_` 丢弃；
6. 单 Node candidate class 不调用 NodeOrder。

### Framework 聚合边界

`pkg/scheduler/framework/session_plugins.go`：

- `HyperNodeOrderMapFn` 只遍历 `EnabledHyperNodeOrder` callback；
- `NodeOrderMapFn` / `BatchNodeOrderFn` 只遍历 `EnabledNodeOrder` callback；
- framework 没有把 NodeOrder contribution 映射到 HyperNode solution score；
- framework 也没有把 topology soft contribution 映射到统一 physical Node score。

### 历史确认

`e2de98c6` 已存在：

```go
bestNode, _ := alloc.prioritizeNodes(...)
```

以及：

```go
hyperNodeScores, err := util.PrioritizeHyperNodes(
    candidateHyperNodeGroups,
    subJob,
    ssn.HyperNodeOrderMapFn,
)
```

`548d1f64` 没有修改 scheduler 源码。`b8af4d76` 相关 NodeOrder 代码不变，
`PrioritizeHyperNodes` 仅有日志格式变化；它新增的 soft-to-hard conversion 扩大了
进入 HyperNode allocator 的 workload 范围，但不是 score isolation 根因。

## 根因

根因不是 NTA 或 GTA 的单 plugin 打分公式，而是 legacy allocator 把一次 Node
placement 决策拆成两个无法共同排序的 score domain：

```text
HyperNode solution utility
  = sum(enabled HyperNodeOrder plugin scores)

Task Node utility
  = sum(enabled NodeOrder/BatchNodeOrder plugin scores)
```

Task Node utility 只用于一个 container 内选择 Node，不进入 HyperNode solution
utility。外层因此无法知道某个成功 HyperNode 方案是否满足更高的 preferred
NodeAffinity utility。

## 为什么不做补丁式修改

### 不在 HyperNodeOrder 中重新实现 NodeAffinity

将 Pod preferred NodeAffinity 聚合成 HyperNode score 会引入未定义语义：

- 一个 HyperNode 包含多个 Node 时取 max、sum、average 还是 capacity-weighted；
- 一个 SubJob 有多个异构 Pod 时如何聚合；
- dry-run 已选择 Node 后是否再次打分；
- 如何避免 NodeAffinity weight 重复应用；
- 如何与普通 resource/image/pod-affinity NodeOrder 保持一致。

### 不把 NodeOrder best score直接累加到 HyperNode score

该方案仍然只评价逐 Task 贪心选出的一个 layout：

- NodeOrder 的早期选择可能阻塞后续 Task；
- 不同 HyperNode candidate 的 task order 和 partial state 不一定可比；
- 第一个成功 gradient layer 仍会截断后续候选；
- 多 SubJob relation 和 fallback 仍缺少统一 placement state；
- `BestNodeFn` 可具有独立选择 contract，不能简单等同于数值最高分。

因此补丁会把当前两级耦合进一步固化，而不是建立清晰的 hard/soft contract。

## 后续重构约束

关联 Feature 015：

`issues/features/015-unified-topology-planning-framework-IP/`

重构应至少提供以下 framework-level 能力：

1. 将 required topology 结果展开成 exact physical Node scope；
2. 在 exact scope 内执行普通 Predicate，包括 required NodeAffinity；
3. 将 topology soft contribution 适配成 per-Node contribution；
4. 与现有 NodeOrder/BatchNodeOrder contribution 在同一 candidate class 内组合；
5. preferred 高分候选失败后保留低分 hard-feasible fallback；
6. 在有界预算内保留 Node alternative，避免只保存一个 `bestNode`；
7. 保持 Statement allocate/deallocate rollback 对称；
8. 对 NTA/GTA 使用相同的 composition contract，不按 plugin name 分支。

## Draft Verification Cases

以下用例是未来重构的初始验证要求。正式实施前仍需根据最终 score composition
方案补充精确权重，并获得用户批准。

### V1：跨 HyperNode preferred NodeAffinity 打破 topology 平分

- Purpose：证明 preferred NodeAffinity 可以参与跨 HyperNode 候选排序。
- Scope：focused allocate/framework unit test + manual Kind workload。
- Setup：两个同层 HyperNode，每个包含一个 Node；两个 HyperNode topology soft score
  相同；Pod 对第二个 Node 配置 preferred NodeAffinity `weight: 100`；两边资源均充足。
- Commands：
  - focused `go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/framework`；
  - Kind 中创建 HyperNode、PodGroup 和带 preferred NodeAffinity 的 workload。
- Expected result：选择 preferred Node 所在 HyperNode/Node。
- Pass/fail：最终 Node 必须为 preferred Node；不能依赖随机或 map iteration。

### V2：NTA soft 与 NodeAffinity preferred 共同打分

- Purpose：证明 NTA topology contribution 与原生 NodeAffinity contribution 使用统一
  composition，而不是两阶段覆盖。
- Scope：focused unit test + manual Kind workload。
- Setup：至少两个 hard-feasible HyperNode；NTA 与 NodeAffinity 分别偏好不同候选；
  使用最终方案批准的显式 plugin/term weights。
- Commands：focused NTA/allocate test 和 Kind workload。
- Expected result：最终选择符合文档化的加权结果。
- Pass/fail：改变显式权重能够以可预测方式改变 winner，且每个权重只应用一次。

### V3：GTA preferred 与 NodeAffinity preferred 共同打分

- Purpose：证明修复不依赖 NTA plugin，PodGroup/SubGroup topology affinity 使用相同
  framework contract。
- Scope：focused GTA/allocate unit test + manual Kind workload。
- Setup：GTA preferred 与 NodeAffinity 指向不同 hard-feasible Node/domain。
- Commands：focused GTA/allocate test 和 Kind workload。
- Expected result：最终选择符合统一 composition。
- Pass/fail：GTA 和 NodeAffinity contribution 都出现在可解释诊断中，结果符合权重。

### V4：preferred 目标不可行时 fallback

- Purpose：证明 preferred NodeAffinity 不会被错误转换为 hard constraint。
- Scope：unit test + manual Kind workload。
- Setup：preferred Node 无资源或被其他 Predicate 拒绝；另一个 topology-allowed Node
  可完成 Job。
- Commands：focused allocate test 和 Kind workload。
- Expected result：Job 在非 preferred Node 成功 Ready/Running。
- Pass/fail：Job 不 Pending，且日志说明 preferred candidate 不可行后使用 fallback。

### V5：required NodeAffinity 仍为 hard Predicate

- Purpose：防止统一 score 重构放宽 required NodeAffinity。
- Scope：focused predicate/allocate unit test + manual Kind workload。
- Setup：topology 允许两个 HyperNode，只有一个 Node 满足 required NodeAffinity。
- Commands：focused predicate/allocate test 和 Kind workload。
- Expected result：只在 required-matching Node 上运行。
- Pass/fail：不匹配 Node 从不进入可提交方案。

### V6：单 Node HyperNode 不绕过 soft composition

- Purpose：覆盖 legacy `len(nodes)==1` 跳过 NodeOrder 的生产形态。
- Scope：focused allocate unit test + manual Kind workload。
- Setup：两个 tier1 HyperNode 各一个 Node，preferred NodeAffinity 只匹配其中一个。
- Commands：focused allocate test 和 Kind workload。
- Expected result：两个物理 Node 在统一候选层比较，preferred Node 胜出。
- Pass/fail：结果不因每个 HyperNode 内只有一个 Node 而丢失 preferred contribution。

### V7：普通非 topology Job 回归

- Purpose：保证 topology refactor 不改变普通 Job 的 NodeOrder contract。
- Scope：focused allocate unit test + manual Kind workload。
- Setup：不配置 NetworkTopology、SubGroup 或 topology affinity，仅配置 preferred
  NodeAffinity。
- Commands：focused allocate test 和 Kind workload。
- Expected result：保持现有普通 NodeOrder 行为。
- Pass/fail：preferred Node 可行时被选择，不进入 topology planner 专用路径。

## 当前记录结果

- 完成 Git 历史与当前源码静态分析；
- 确认问题从初版 HyperNode allocator 即存在；
- 确认 `b8af4d76` 是暴露面扩大因素，不是根因；
- 确认 required NodeAffinity 在 Predicate 阶段仍被检查；
- 未修改 Volcano 实现代码；
- 未执行构建、镜像或 Kind 验证；
- 等待后续拓扑调度架构重构时重新进入 proposal approval 流程。
