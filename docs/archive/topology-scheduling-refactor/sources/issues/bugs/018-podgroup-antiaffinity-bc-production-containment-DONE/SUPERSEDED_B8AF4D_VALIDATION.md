> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 已废弃：b8af4d766 基线上的 018 实现与验证

> 2026-08-03 用户确认生产还包含 `18deb5219 fix(scheduler): preserve native soft topology on v1.14`。
> 因此以下内容只保留为审计记录，不再是 018 的权威基线、实现或发布依据。

# 018：#5206 基线 B+C 生产临时修复提案与验证计划

## 审批状态

- 批准日期：2026-08-03
- 批准方式：用户明确回复“好的，开始修复”
- 当前阶段：实现进行中
- 权威基线：`b8af4d766f2ac14ddbb8f267462a673adc454823`
- 基线说明：包含 #5206 soft→hard convert，不包含后续 013/014
- 预计正确分支：`fix/018-podgroup-antiaffinity-bc-production-containment`
- 实现状态：源码提交和 K0/K1 Kind 验证已完成；因两项生产基线既有门禁失败，issue 暂保留 IP

## 结论

推荐在 `b8af4d766` 上局部回移 candidate-forest 的必要闭包，但不 cherry-pick
`8a71ca532` 的后续基线，也不引入任何去 convert 修改。

源码范围严格限制为：

- `pkg/scheduler/actions/allocate/allocate.go`
- 新增 `pkg/scheduler/actions/allocate/podgroup_antiaffinity_forest_test.go`

不修改 framework、NTA、GTA、API、CRD、controller、webhook。

## 分支纠正方案

当前同名 018 分支错误地指向 `8a71ca532`。为保留审计线索，不强制 reset：

1. 将错误分支重命名为
   `backup/fix-018-wrong-base-8a71ca532`；
2. 从 `b8af4d766` 重新创建
   `fix/018-podgroup-antiaffinity-bc-production-containment`；
3. 正确分支的第一个修复提交必须以 `b8af4d766` 为直接祖先；
4. 最终用 `git log` 和 ancestry 检查证明没有包含 `18deb5219`、`b42c9f426`、
   `8a71ca532`。

## 推荐设计：#5206 兼容的 exact candidate forest

### 1. 严格激活条件

增加 allocate-private helper：

```go
job.ContainsHardPodGroupAntiAffinity() && !job.IsHardTopologyMode()
```

在 `b8af4d766` 上该条件具有额外保护：

- Job 无 NTA：`IsHardTopologyMode=false`，进入生产 B+C 路径；
- Job real hard：不进入；
- Job soft 经 #5206 convert 后变成 hard：不进入。

因此临时补丁不会改变 #5206 的 soft 行为，只覆盖当前生产 Job 无 NTA 的场景。

### 2. 一个 gradient layer 构成精确 roots

对 Job gradient layer：

```text
allowed roots = stableUnique(layer)
envelope      = LCA(allowed roots)
exact forest  = union(subtree(root) for root in allowed roots)
```

生产示例中可能先得到四个合法 tier-1 roots：

```text
B1, B2, C1, C2
```

它们的 envelope 是 H，但 H 只作为现有 SubJob callback 的遍历入口，不是合法
candidate scope。

roots 必须按名称排序、去重，保证日志、dry-run 和测试确定性。

### 3. SubJob callback 后应用 exact forest

一次调用现有：

```text
HyperNodeGradientForSubJobFn(subJob, envelope)
```

随后由 allocate 将返回结果与 exact forest 求交：

```text
final candidates = callback result ∩ descendants(allowed roots)
```

这样：

- NTA 继续执行 SubJob hard/converted-hard tier 过滤；
- GTA 继续执行 required PodGroup/SubGroup hard terms；
- 即使 envelope H 下包含 A，allocate 的最终 scope 仍只允许 B+C；
- 不依赖 GTA 当前实现“碰巧再次过滤 A”作为唯一安全边界。

### 4. Job minResource 按 roots 覆盖的唯一 Nodes 聚合

联合路径不使用现有逐 root Job minResource filter。新增 allocate-private forest
resource check：

1. 收集每个 root 的 `RealNodesSet`；
2. 按 Kubernetes Node name 去重；
3. 聚合 `Idle` 和 `FutureIdle`；
4. B+C 联合满足时保留 layer；
5. 只因 A 资源才满足时仍拒绝；
6. roots 重叠时不得双计数。

SubJob 级 `FilterGradientsByMinResource` 保持不变。

### 5. 保存实际 placement，不保存 envelope

在每次 dry-run recovery 前捕获：

- 实际 `job.AllocatedHyperNode`；
- 本 solution 中每个 SubJob 的 `AllocatedHyperNode`。

成功 solution 恢复后使用实际 placement 重写现有 recorder decision：

- 只使用 B：Job placement=B；
- 跨 B+C：Job placement=H；
- 不把 traversal envelope 无条件记为 Job placement；
- 不修改 `recorder.go` 的结构或公共接口。

### 6. 保持现有贪心和 first-feasible 行为

联合 forest 内仍按现有 SubJob priority queue 顺序分配，仍使用现有
HyperNodeOrderFn、predicate、statement、gang ready/pipelined 逻辑。

本次不增加：

- placement 笛卡尔积；
- 回溯；
- beam/bounded search；
- 新 framework callback；
- 新配置项或 feature gate。

复杂度只从单 root 扩展为一次 envelope callback + forest 过滤，仍为有界贪心。

## 预计代码规模

目标：

- allocate 生产代码约 `+250~330` 行，少量现有调用调整；
- focused test 约 `+400~500` 行；
- 生产代码只在 `allocate.go`；
- 不出现 013 的 `HyperNodeGradientResult`、`Applied`、`Abstain`；
- 不删除或修改 `convertSoftToHardTopology`。

若实现需要超出该范围，必须先更新本提案并重新批准。

## 不采用方案

### A. 直接 cherry-pick `473c6538b`（Bug 009）

不采用。该提交的生产实现约 `+639/-86`，增加通用 multi-root domain、solution
比较和多 root gradient 合并，修复面大于生产 B+C 所需。

### B. 直接 cherry-pick `8a71ca532` 及其祖先

不采用。其祖先包含 013 去 convert、callback 类型和 framework/plugin 语义变化，
生产明确不存在这些 commit。

实现时可以人工参考 `8a71ca532` 的 allocate-local forest 思路，但必须在
`b8af4d766` 上重新编写/适配，并保持 raw gradient API。

### C. 仅把 B+C 替换成 LCA H

不采用。H 的完整子树是 A+B+C，会产生 A 泄漏、A 资源误计和 placement 过宽。

### D. 跳过 Job minResource filter

不采用。最终 predicate/gang 虽能防止超卖，但会损失明确诊断和性能，也不能解决
A scope 与 recorder 问题。

### E. 删除 convert

不采用。它属于长期社区演进，不进入生产临时修复。

## 验证用例

以下用例在用户批准后成为权威完成标准。

### G1：正确基线与分支 ancestry

```bash
git show -s --oneline HEAD
git merge-base --is-ancestor b8af4d766 HEAD
git merge-base --is-ancestor 18deb5219 HEAD
git merge-base --is-ancestor 8a71ca532 HEAD
```

通过标准：

- `b8af4d766` ancestry 返回 0；
- `18deb5219`、`8a71ca532` ancestry 返回非 0；
- diff 中不包含 framework/NTA/GTA/API/CRD 改动。

### R1：修改前复现 B+C 失败

- 范围：完整 allocate Action test。
- 拓扑：H -> A/B/C -> 各两个 tier-1 -> 各一个 Node。
- 每 Node：8 个 `huawei.com/ascend-1980`。
- anchor：同标签 PodGroup 位于 A。
- target：4 个 SubJobs，各 1 Pod、每 Pod 6 卡、minMember=4。
- target hard rules：PAA=tier-2；SubJob NTA=tier-1；Job 无 NTA。
- 修改前期望：target 无法 4/4 Bind，典型结果为单根最多 2 个。
- 修改后期望：四个 Bind，使用 B1/B2/C1/C2。

测试必须先在 `b8af4d766` 运行并记录 pre-fix failure，再应用修复。

### U1：B+C 联合分配稳定性

测试函数内部执行 50 轮，命令不再额外重复 50 次：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^TestAllocatePodGroupAntiAffinityAcrossCandidateForest$' \
  -count=1 -timeout=10m
```

通过标准：50 轮均 4 Bind；B/C 四个 leaves 各使用一次；A 零使用；Job placement=H。

### U2：A 不得通过 H 重新进入

- A 的资源/score 设置为最高；
- callback 从 H 遍历时会看到 A；
- forest post-filter 必须删除 A。

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^TestFilterGradientsByCandidateForestExcludesEnvelopeSibling$' \
  -count=50
```

### U3：联合 minResource 精确计算

- B/C 单独 16、联合 32、minResource=24：保留；
- B+C=32、minResource=40，只有加 A 才足够：拒绝；
- overlapping roots：Nodes 只计算一次。

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^TestFilterCandidateForestGradientsByMinResource$' \
  -count=50
```

### U4：实际 placement 与 envelope 分离

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^TestCandidateForestUsesActualPlacementInsteadOfEnvelope$' \
  -count=50
```

通过标准：实际只使用 B 时，Job/recorder placement=B，不是 H。

### U5：#5206 convert 与 Job hard 边界不变

检查：

- Job 无 NTA + required PAA：forest=true；
- Job real hard：forest=false；
- Job soft 转换成 hard 后：forest=false；
- 现有 `convertSoftToHardTopology` tests 全部通过；
- 不新增 native-soft `Applied/Abstain` 行为。

### U6：普通 allocate 路径回归

- 无 required PAA；
- 普通 Job；
- real hard Job；
- #5206 converted soft Job/SubJob。

通过标准：不进入 forest helper，行为与 `b8af4d766` 一致。

### B1：格式、回归、构建与 verify

```bash
./hack/update-gofmt.sh
go test ./pkg/scheduler/actions/allocate -count=1
go test ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity -count=1
make vc-scheduler
make verify
```

全部必须通过。若 Darwin 的仓库级门禁遇到已知平台限制，记录原始错误并证明与本次
allocate 修改无关；不得用较弱测试替代 focused scheduler gates。

### K0：修复前 7 节点 Kind 复现

- 集群：1 control-plane + 6 workers，共 7 Nodes；
- scheduler 镜像：从 `b8af4d766` 构建；
- 顺序：HyperNodes -> anchor PG -> anchor workload -> target PG -> target workload；
- 预期：anchor 在 A；target 不能 4/4，日志显示逐 B/C 单根 transaction。

### K1：只升级 scheduler 后修复

保持同一集群、controller、webhook、CRD、HyperNodes 和配置，只替换 scheduler
镜像为修复分支镜像；清理并重建 target PG/workload。

通过标准：

- anchor 仍在 A；
- target 4/4 Running/Ready；
- target 只使用 B/C 四个 leaves；
- A 无 target；
- PodGroup placement=H；
- scheduler 日志包含 stable allowedRoots、envelope 和实际 SubJob placements；
- 无 `2/4 Pending`。

K0→K1 的原地 scheduler-only 升级同时证明最小生产发布方式。

## 生产发布边界

若验证通过，生产只需要：

1. 从正确 fix/018 分支构建并推送 scheduler 镜像；
2. 只更新 `deployment/volcano-scheduler` 的 image；
3. 不更新 controller/webhook/CRD；
4. 不修改现有 #5206 scheduler config 或 workload API；
5. 回滚时只恢复旧 scheduler image。

## 批准后执行顺序

1. 备份错误分支并从 `b8af4d766` 重建正确 018 分支；
2. 添加 focused regression test，在 pre-fix 基线上证明失败；
3. 实现 allocate-local forest；
4. 完成 U1-U6/B1；
5. 构建 baseline/fixed 两个 scheduler 镜像；
6. 完成 K0→K1 scheduler-only Kind 验证；
7. 记录结果、提交源码、删除临时集群并将 issue 改为 DONE。

## 实施结果

### 分支与提交

- 分支：`fix/018-podgroup-antiaffinity-bc-production-containment`
- 基线：`b8af4d766f2ac14ddbb8f267462a673adc454823`
- 修复提交：`6aa1f614c88137ae7861200429cb9e1450d707e2`
- 提交标题：`fix(allocate): schedule anti-affinity jobs across candidate forest`
- 修改源码仅包含：
  - `pkg/scheduler/actions/allocate/allocate.go`
  - `pkg/scheduler/actions/allocate/podgroup_antiaffinity_forest_test.go`
- `git merge-base --is-ancestor b8af4d766 HEAD`：返回 `0`；
- `git merge-base --is-ancestor 18deb5219 HEAD`：返回 `1`；
- `git merge-base --is-ancestor 8a71ca532 HEAD`：返回 `1`。

错误基线分支已保留为：

```text
backup/fix-018-wrong-base-8a71ca532
```

### R1：修复前失败

在纯净 `b8af4d766` 上先加入同一 Action regression，未加入生产修复时执行：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^TestAllocatePodGroupAntiAffinityAcrossCandidateForest$' \
  -count=1 -timeout=10m
```

结果：`FAIL`，内部 iteration `00` 至 `49` 全部报告：

```text
failed to get Bind request in case 0
```

即修复前 50/50 次均无法形成 4 Pod gang 的 Bind 请求。

### U1-U6 与相关包回归

修复提交前后最终源码执行结果：

| 用例 | 命令摘要 | 结果 |
| --- | --- | --- |
| U1 | B+C Action test，内部 50 轮 | PASS，`25.521s` |
| U2/U3 | A scope 隔离 + forest minResource，各 50 次 | PASS，`1.180s` |
| U4 | actual placement 与 envelope 分离，50 次 | PASS，`24.793s` |
| U5/U6 | activation boundary，50 次 | PASS，`1.161s` |
| #5206 convert | 三个 `convertSoftToHardTopology` focused tests | PASS，`1.165s` |
| framework/NTA/GTA | 三个相关 package 全量 test | PASS，分别 `1.206s`、`4.817s`、`1.584s` |
| build | `make vc-scheduler` | PASS |
| format | 两个改动文件 `gofmt` + `git diff --check` | PASS |

### B1：生产基线既有门禁差异

以下两项没有被本补丁修复，因为它们在纯净 `b8af4d766` 上以完全相同方式失败，
且修改它们会超出本次只改 allocate B+C 的批准范围。

1. `go test ./pkg/scheduler/actions/allocate -count=1`：
   - 修复分支只失败于
     `TestAllocateJobSoftAffinityAcrossSubJobs`、
     `TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates`、
     `TestPreferJobSoftTopologyCandidates`、
     `TestPreferJobSoftTopologyScoreCandidates`；
   - 在独立纯净工作树 `/private/tmp/volcano-018-base-test@b8af4d766` 只执行上述
     四个 test，得到同样失败文本；
   - 这些 test 是 #5206 soft→hard convert 基线自身与旧 soft 预期之间的既有冲突，
     与 candidate forest 激活路径无关。
2. `make verify`：
   - 修复分支和纯净基线均在 `hack/verify-gofmt.sh` 失败；
   - 两边均只报告以下三个既有文件需要 gofmt：
     `dry_run_placement_test.go`、`hyper_node_info_test.go`、
     `topology_affinity_info_test.go`；
   - 本次没有修改这三个文件。

根据工作区规则，未获得用户对“用纯净基线差异证明代替 B1 全绿”的明确批准前，
018 保持 `IP`，不把这些既有失败伪装成 PASS，也不顺手扩大临时补丁范围。

## Kind K0/K1 验证结果

### 集群与镜像

- 集群：`volcano-dev-018`
- 架构：`linux/arm64`
- 拓扑：1 control-plane + 6 workers，共 7 Nodes；
- HyperNode：`shared-t3 -> A/B/C-t2 -> 各两个 t1 -> 各一个 worker`；
- baseline scheduler：
  `volcanosh/vc-scheduler:dev-018-base`，image ID
  `sha256:e73263155b01af8dc69700677fb03aebca0295c7a5c5f946d5f04fd7e1ecbd60`；
- fixed scheduler：
  `volcanosh/vc-scheduler:dev-018-fixed`，image ID
  `sha256:5b6bd6d7dd8861dcd3837dfa3e0618165d033990c04405f7a25882ff9acbbd57`；
- controller：
  `volcanosh/vc-controller-manager:dev-018-base`，image ID
  `sha256:1d29b225e28f562b83af09f3b4b3c9920a916c314d499f03205b04fb80848223`；
- webhook：
  `volcanosh/vc-webhook-manager:dev-018-base`，image ID
  `sha256:eb78bfd13760721ba0b589047fbe73f810c929f6ff7d03c5d7b40dff8a775106`。

Helm 安装命令：

```bash
helm upgrade --install volcano installer/helm/chart/volcano \
  --kube-context kind-volcano-dev-018 \
  --namespace volcano-system --create-namespace \
  --set basic.image_tag_version=dev-018-base \
  --set basic.image_pull_policy=IfNotPresent \
  --wait --timeout 5m
```

验证清单保存在当前 issue 目录：

- `kind-config.yaml`
- `hypernodes.yaml`
- `anchor-podgroup.yaml`
- `anchor-workload.yaml`
- `target-podgroup.yaml`
- `target-workload.yaml`

### K0：基线失败

1. anchor 运行在 `volcano-dev-018-worker`，对应 `bug018-a-t1-0`；
2. target 创建后等待 30 秒，四个 Pod 全部 `0/1 Pending`；
3. PodGroup `Unschedulable` message 明确显示：

```text
2/4 tasks in gang unschedulable
4 Pending, 4 minAvailable
Pending: 2 Schedulable, 2 Unschedulable
```

4. 日志显示基线对 B/C 各叶节点逐个执行整个 Job transaction；单根最多形成两个
   可行 placement，无法达到 minMember=4，符合 Bug 009 根因。

结论：K0 PASS（成功复现修复前失败）。

### K1：只更新 scheduler 后成功

先只删除 target Pod/PodGroup，保留 anchor、HyperNodes、controller、webhook、CRD 和
Helm 配置。唯一组件变更：

```bash
kubectl --context kind-volcano-dev-018 -n volcano-system set image \
  deployment/volcano-scheduler \
  volcano-scheduler=volcanosh/vc-scheduler:dev-018-fixed
```

rollout 后组件镜像为：

```text
volcano-admission   vc-webhook-manager:dev-018-base
volcano-controllers vc-controller-manager:dev-018-base
volcano-scheduler   vc-scheduler:dev-018-fixed
```

重建相同 target 后约 2 秒内四个 Pod 全部 Ready：

| Pod | Node | HyperNode/域 |
| --- | --- | --- |
| `bug018-prefill-1` | `worker3` | `bug018-b-t1-0` / B |
| `bug018-decode-1` | `worker4` | `bug018-b-t1-1` / B |
| `bug018-prefill-0` | `worker5` | `bug018-c-t1-0` / C |
| `bug018-decode-0` | `worker6` | `bug018-c-t1-1` / C |

- target：`Running=4`；
- anchor：仍在 A 且 Running；
- A：没有 target；
- target annotation：
  `volcano.sh/job-allocated-hypernode=bug018-shared-t3`；
- 关键 scheduler 日志：

```text
Build PodGroup anti-affinity candidate forest,
allowedRoots=[bug018-b-t2 bug018-c-t2],
envelope=bug018-shared-t3

update allocated hyperNode for subJob ... new=bug018-b-t1-0
update allocated hyperNode for subJob ... new=bug018-b-t1-1
update allocated hyperNode for subJob ... new=bug018-c-t1-0
update allocated hyperNode for subJob ... new=bug018-c-t1-1
```

结论：K1 PASS，且 K0→K1 证明生产可以只滚动 scheduler 镜像。

## 当前收尾状态

- 源码提交：完成；
- focused/相关 package 回归：完成；
- scheduler build：完成；
- K0/K1：完成；
- 临时集群：已删除，`kind get clusters` 只剩任务前已有的 `kind`；
- 临时纯净基线 worktree：已通过 `git worktree remove` 删除；
- issue：因 B1 的两项纯净基线既有失败保持 `IP`，等待用户确认接受差异门禁证据。
