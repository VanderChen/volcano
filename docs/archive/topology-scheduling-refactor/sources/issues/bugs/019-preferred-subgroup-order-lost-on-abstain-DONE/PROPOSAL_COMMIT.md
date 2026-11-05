> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 019：preferred SubGroup soft traversal order 修复提案

## 审批状态

- 根因调查：完成；
- 修复方案：用户已批准精简 hotfix；
- 验证用例：用户已批准，并明确要求 Kind 端到端验证；
- 批准日期：2026-08-03；
- 实现：完成；
- Kind 验证：K0、K1、K2 完成；
- 清理：完成；
- 最终状态：DONE。

用户在确认 `Ordered` 单标志精简方案后回复“请开始修复，并在kind中端到端验证”。
以下精简方案取代此前讨论的独立 `PreferredOrder` 矩阵方案。

## 当前证据

### E1：分支与工作区

```text
branch: fix/018-podgroup-antiaffinity-bc-production-containment
HEAD:   f954233d2ba7d3c512f0e1d7c1b999c9e829d14e
status: clean（调查开始时）
```

### E2：真实接线绕过 preferred container helper

`group-topology-affinity.OnSessionOpen` 当前注册：

```text
Job callback -> hyperNodeConstraintForJob
preferred-only Job -> HyperNodeGradientAbstain
```

但现有 `TestHyperNodeGradientForJobPreferredSubGroupContainer` 直接调用
`hyperNodeGradientForJob`，没有经过 `OnSessionOpen -> Session`。因此它能验证 helper
返回 tier2-first，却不能发现生产路径已经不使用该结果。

### E3：现有 helper/scorer 测试通过

调查阶段执行：

```bash
go test ./pkg/scheduler/plugins/group-topology-affinity \
  -run 'TestHyperNodeGradientForJobPreferredSubGroupContainer|TestHyperNodeOrderFnForSubGroupPreferredTerms' \
  -count=1
```

结果：PASS（`1.868s`）。这与现场失败并不矛盾：helper 和 peer-aware scorer 都正确，
丢失的是两者之间的 Job candidate traversal 接线。

## 修复原则

preferred ordering 与 hard membership 必须是两个正交维度：

```text
hard membership:  哪些 HyperNode 可以使用（AND intersection）
soft traversal:   先尝试哪个完整候选层（保留全部 fallback）
peer-aware score: 同层可行候选之间选哪个
```

不能通过把完整 preferred gradient 标记为 `Applied=true` 来临时恢复顺序；即使其
节点全集当前等于 universe，这也会混淆诊断与插件契约，并可能在多 hard-plugin
组合中重新引入 soft-to-hard 回归。

## 拟议设计

### 1. 在现有 gradient 结果上增加一个 `Ordered` 标志

复用 `api.HyperNodeGradientResult.Gradients`，只增加一个 `Ordered` bool：

```text
Applied=false Ordered=false  完全 abstain
Applied=true  Ordered=false  仅 hard membership
Applied=false Ordered=true   仅 soft traversal order
Applied=true  Ordered=true   hard membership + soft traversal order
```

提供明确 constructor/helper，避免插件手工构造含混状态。这是 scheduler 内部 Go API，
不涉及 PodGroup CRD/API codegen，也不增加第二套 gradient 矩阵。

### 2. GTA 同时返回 hard constraint 与 preferred order

重接现有、已验证的 `prioritizePreferredSubGroupJobGradients`：

- preferred-only 当前用例：返回 `Applied=false, Ordered=true`，gradient order 为
  `[tier2(ab,cd), tier3(root), tier1(a,b,c,d)]`；
- 没有有效 preferred term/peer pair：不返回 soft order，保留默认 tier1-first；
- 同时存在 required PodGroup anti-affinity 时：hard gradient 仍负责 membership，
  同一个 gradient 可返回 `Applied=true, Ordered=true`；
- preferred container 被 hard constraint 删除时：保留 hard 结果中的 finer/coarser
  fallback，不把 soft preference变成拒绝原因。

SubJob 的 hard filtering 和 `HyperNodeOrderFn` scorer 保持现有职责，不把评分逻辑搬入
allocate。

### 3. framework 先求 hard 交集，再应用 soft order

`Session.HyperNodeGradientForJobFn` / `intersectHyperNodeGradients` 按以下顺序工作：

1. 从 caller universe 开始；
2. 只对 `Applied=true` 的 hard gradients 做名称交集；
3. 得到最终 eligible set；
4. 取 scheduler 配置顺序中第一个 `Ordered=true` 的 gradient，并过滤到 eligible set；
5. ordered gradient 未覆盖但仍 eligible 的候选，按原 hard/universe 顺序追加为
   fallback；
6. 当前维护分支只有 GTA 产生 Job ordered gradient；多 ordered-provider 合并交给正在
   进行的 topology planner 重构，不在本 hotfix 扩面；
7. `PluginEligibleByTier` 和 `ExcludedByReason` 仍只记录 hard constraint，不把 soft
   reorder 记成排除。

SubJob path 保持兼容同一结果结构，但本修复不要求 GTA 为 SubJob gradient 生成新的
soft order；SubJob sibling 选择继续由现有 HyperNode order score 完成。

### 4. allocate 不增加插件特判

allocate 继续消费 framework 返回的 gradient layers：

- 首先尝试 tier2 container，使后续 SubJobs 能同时看见 `a` 与 `b`（或 `c` 与 `d`）；
- 第一个同角色 shard 可以任选一个 tier1；
- 第二个同角色 shard 的 scorer 同时比较两个 sibling，已占用 sibling 得 0，另一个得
  100；
- preferred affinity 让所有 SubJobs 留在同一个 tier2；
- 如果 tier2 方案不可行，仍能尝试 coarser/finer fallback。

不在 `allocate.go` 中识别 `prefill`、`decode` 或 GTA term。

## 预计源码范围

实现预计只涉及：

```text
pkg/scheduler/api/types.go
pkg/scheduler/framework/session_plugins.go
pkg/scheduler/framework/session_plugins_test.go
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity_test.go
```

若 allocate 端到端回归测试需要，会新增/更新
`pkg/scheduler/actions/allocate/*_test.go`，但不计划修改 allocate 生产代码。

不修改 API staging、CRD、controller、webhook、Helm chart 或 ConfigMap schema。

## 验证用例

### U1：真实 session 接线恢复 tier2-first（当前回归的最小失败测试）

- Purpose：证明测试通过真实 `OnSessionOpen -> Session.HyperNodeGradientForJobFn`
  路径，而不是直接调用死 helper。
- Scope：`group-topology-affinity` + framework focused unit test。
- Setup：tier1 `a/b/c/d`，tier2 `ab/cd`，tier3 root；2 prefill + 2 decode；用户提供的
  preferred terms。
- Command：

  ```bash
  go test ./pkg/scheduler/plugins/group-topology-affinity \
    -run 'TestPreferredSubGroupOrderThroughSession' -count=1
  ```

- Expected：第一层严格为 tier2 `{ab,cd}`，随后保留 tier3 与 tier1 fallback；所有
  HyperNode 的集合与原 universe 完全相同。
- Pass/fail：tier2 不在第一层、丢失候选或 preferred 被标成 hard exclusion 均失败。

### U2：hard membership 与 soft order 正交

- Purpose：ordered gradient 不得逃逸 NTA/GTA hard constraint，也不得把 soft 变 hard。
- Scope：framework unit test。
- Setup：一个 hard plugin 仅允许 `cd` 子树；GTA soft order 原本优先 `{ab,cd}`。
- Command：

  ```bash
  go test ./pkg/scheduler/framework \
    -run 'TestIntersectHyperNodeGradients.*PreferredOrder' -count=1
  ```

- Expected：结果只包含 `cd` hard-eligible candidates，且 `cd` container 优先；被 hard
  删除的 `ab` 不会重新出现；soft plugin 不出现在 hard exclusion reason 中。
- Pass/fail：membership 扩大/缩小、fallback 丢失或非确定性顺序均失败。

### U3：无有效 preferred peer 时保持默认顺序

- Purpose：普通 Job、单 SubJob、非法 weight 或 tier 不受影响。
- Scope：GTA/framework table-driven unit test。
- Expected：没有有效 peer pair 时不产生 preferred order，仍为确定性 tier1-first
  universe；错误 tier 按现有错误/日志契约处理。
- Pass/fail：无关 workload 的 traversal 被改变即失败。

### U4：peer-aware affinity/anti-affinity 分数保持正确

- Purpose：确认现有 scorer 仍是最终 sibling 选择来源。
- Scope：GTA focused unit test。
- Command：

  ```bash
  go test ./pkg/scheduler/plugins/group-topology-affinity \
    -run 'TestHyperNodeOrderFnForSubGroupPreferredTerms' -count=1
  ```

- Expected：affinity peer domain 分数更高；single-policy anti-affinity 下已占用 tier1
  得 0、同一 tier2 的 sibling 得 100（乘插件权重后的等价分数）。
- Pass/fail：同分或方向相反即失败。

### U5：allocate 生产形态布局回归

- Purpose：覆盖 helper、session、gradient traversal、dry-run placement 与 scorer 的完整
  内存调度路径。
- Scope：`pkg/scheduler/actions/allocate` focused test。
- Setup：与用户场景同构，4 个一 Pod SubJobs，节点资源足以全部挤在一个节点。
- Command：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'TestAllocatePreferredSubGroupAffinityAndAntiAffinity' \
    -count=50
  ```

- Expected：每轮 4 Bind；只使用 `ab` 或只使用 `cd`；被选择的两个 tier1 各有
  1 prefill + 1 decode；Job placement 为所选 tier2。
- Pass/fail：跨两个 tier2、同角色同 tier1、四 Pod 单点或任一轮无 4 Bind 均失败。

### U6：native-soft 与 018 candidate-forest 回归门禁

- Purpose：确保没有恢复 soft-to-hard conversion，也不破坏 018。
- Scope：framework、NTA、GTA、allocate focused/package tests。
- Commands：

  ```bash
  go test ./pkg/scheduler/framework \
    ./pkg/scheduler/plugins/network-topology-aware \
    ./pkg/scheduler/plugins/group-topology-affinity -count=1
  go test ./pkg/scheduler/actions/allocate \
    -run 'TestAllocatePodGroupAntiAffinityAcrossCandidateForest|TestPodGroupAntiAffinityCandidateForest' \
    -count=1
  ```

- Expected：NTA soft 继续 abstain + score；hard constraints 继续求交；018 hard/native-soft
  candidate forest 用例全部通过。
- Pass/fail：任一 soft 被标成 hard、A scope 泄漏、B+C forest 回归或已有测试失败均失败。

### B1：格式、包测试、构建与 verify

```bash
./hack/update-gofmt.sh
git diff --check
go test ./pkg/scheduler/api \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/actions/allocate -count=1
make vc-scheduler
make verify
```

全部命令必须通过。若发现与本分支基线无关的既有失败，必须单独复跑、记录证据并由用户
决定是否扩展范围，不能静默豁免。

### K0：当前 HEAD Kind 基线复现

- Purpose：证明当前 scheduler image 能调度 4/4，但 preferred scorer 因单候选不可执行。
- Scope：5-node Kind（1 control-plane + 4 workers，总节点数 5，低于 8 节点上限）。
- Setup：四个 worker 映射到 tier1 `a/b/c/d`；tier2 为 `ab/cd`；PodGroup 先创建，四个
  Deployment 后创建；使用用户给出的 scheduler ConfigMap 相关插件开关。
- Image：从当前 `f954233d2` 构建仅 scheduler 本地镜像并 load 到 Kind。
- Expected：4/4 Running；四 Pod 全部落在一个 tier1；日志显示 Job universe 首层为
  tier1，后续 peer scorer 只收到单 HyperNode candidate。
- Pass/fail：复现单点紧凑布局并取得日志证据为 PASS（表示 bug 已复现）。

### K1：修复后相同 Kind 用例

- Purpose：验证用户期望布局和 soft/hard 边界。
- Scope/Setup：完全复用 K0 cluster、HyperNodes、PodGroup、Deployments 与 scheduler
  ConfigMap，只替换本地 scheduler image 并重建 workload。
- Expected：
  - 4/4 Running；
  - 只使用 `ab` 或只使用 `cd`；
  - 所选 pair 的两个 tier1 各有 1 prefill + 1 decode；
  - PodGroup `status.allocated`（或当前等价 placement 字段）为所选 tier2；
  - 日志显示 preferred container tier 为 tier2；
  - 同角色第二个 shard 的候选日志显示 occupied tier1 低分、sibling 高分；
  - 没有 preferred term 被记录成 hard exclusion。
- Pass/fail：上述条件必须全部满足。

### K2：hard constraint containment control

- Purpose：证明 soft traversal 不会逃出另一个插件的 hard scope。
- Scope：复用 K1 集群，增加一个只允许 `cd`（或只允许 `ab`）的 real-hard Job/SubJob
  network topology control。
- Expected：仍按 1p1d + 1p1d 打散，但只能使用 hard-allowed pair；移除 preferred
  term 后 workload 仍可调度，说明 hard membership 独立。
- Pass/fail：使用 hard scope 外节点、变 Pending 或 preferred 变成 hard filter 均失败。

### Kind 清理

记录镜像 tag、Helm/rollout 命令、Pods→Nodes→HyperNodes 映射、scheduler 日志和每个
case 的 PASS/FAIL。完成后删除 task-specific Kind cluster；若环境无法运行 Kind，任务
保持 IP，不得标记 DONE。

## 完成标准

只有在以下全部满足后才能把 019 标为 DONE：

1. 用户批准本提案及 U1-U6、B1、K0-K2；
2. 实现不把 preferred 标成 hard membership；
3. U1-U6 与 B1 全部通过；
4. K0 复现、K1 修复、K2 containment 全部通过；
5. 验证命令、日志、镜像、对象状态和清理结果回填本文件；
6. `volcano/` 最终工作区状态和提交（如用户要求提交）明确记录。

## 最终实现记录

### 生产代码

```text
pkg/scheduler/api/types.go
pkg/scheduler/framework/session_plugins.go
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go
```

- `HyperNodeGradientResult` 新增 `Ordered`；
- `HyperNodeGradientPrefer` 表示 `Applied=false, Ordered=true`；
- `HyperNodeGradientConstrainAndPrefer` 表示 hard membership 与 soft order 同时存在；
- framework 仍只对 `Applied=true` 做交集；
- 第一个 configured ordered provider 只重排 hard-eligible candidates；
- ordered gradient 未覆盖的 eligible candidates 按原顺序追加为 fallback；
- GTA preferred-only Job 返回完整的 tier2-first order，不再在真实 session 路径完全
  abstain；
- required PodGroup anti-affinity 与 preferred SubGroup term 同时存在时，同一合法
  gradient 可同时携带 hard membership 与 soft order。

### 测试代码

```text
pkg/scheduler/actions/allocate/allocate_test.go
pkg/scheduler/framework/session_plugins_test.go
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity_test.go
```

- framework 覆盖 ordered-only universe、hard intersection containment 和 fallback；
- GTA 测试改走真实生产回调 `hyperNodeConstraintForJob`；
- allocate 使用真实 GTA 插件、2 prefill + 2 decode 和 a/b/c/d 拓扑验证完整布局。

相对 `f954233d2` 共修改 6 个源码/测试文件：309 insertions、15 deletions。
`allocate.go` 无生产 diff。

## 代码验证结果

### 聚焦和包测试

全部通过：

```text
framework preferred-order/intersection focused                 PASS 3.275s
GTA constraint/order/hard-term focused                        PASS 2.714s
allocate preferred affinity+anti-affinity, count=1            PASS 4.380s
allocate preferred affinity+anti-affinity, count=50           PASS 24.764s
api + framework + NTA + GTA packages                          PASS
allocate full package                                         PASS 95.915s
018 candidate-forest focused regressions                      PASS
git diff --check                                              PASS
./hack/update-gofmt.sh                                        PASS
make vc-scheduler                                             PASS
```

`make verify` 第一次在 sandbox 中因网络不可用失败；提升到主机网络后又暴露脚本要求
显式 `GOPATH`。最终命令：

```bash
env GOPATH=/Users/vanderchen/go make verify
```

结果 PASS：生成 deepcopy/client/lister/informer/conversion 后，`pkg up to date`。
脚本会在成功后遗留根目录 `volcano.sh/` 临时生成树并临时升级 `go.mod` 中的
code-generator；两项均已精确清理，最终 `go.mod` 无 diff。

### 全量 unit-test 说明

按工作区约定执行了 `make unit-test`。Darwin 全包目标最终 exit 2，失败集中在与本任务
无关的 Linux-only agent/network-qos/runc 包：

```text
build constraints exclude all Go files (agent QoS handlers)
Linux runc/unix constants unavailable on Darwin
networkqos/tc existing interface mismatch
networkqos eBPF mock expectations
```

同一次全量运行中所有 scheduler 包通过，包括：

```text
pkg/scheduler/actions/allocate                     PASS 100.090s
pkg/scheduler/api                                  PASS
pkg/scheduler/framework                            PASS
pkg/scheduler/plugins/group-topology-affinity      PASS
pkg/scheduler/plugins/network-topology-aware       PASS
其余 pkg/scheduler/...                             PASS
```

因此全量目标的非零结果记录为平台/非 scheduler 基线门禁问题，不改变本任务已批准的
B1 scheduler 权威门禁结果。

## Kind 端到端验证记录

### 环境与镜像

```text
cluster:      volcano-dev-019
topology:     1 control-plane + 4 workers = 5 Nodes
Kubernetes:   v1.36.1
architecture: arm64（5/5 Nodes）
K0 image:     volcanosh/vc-scheduler:dev-019-k0
K0 ID:        sha256:8323161a959b61a850928249600a9ebcfd7c0370aa2eed97ebfe10ff826be5e6
K1 image:     volcanosh/vc-scheduler:dev-019-k1
K1 ID:        sha256:1e77f94a1109128ea517057d873203712af62e3147ff3b0c7afcd0218567287f
controller:   upstream v1.14.1, retagged dev-019-k0, arm64
webhook:      upstream v1.14.1, retagged dev-019-k0, arm64
```

K0 从 detached `f954233d2` worktree 构建；K1 从当前工作区构建。所有镜像由 host
load 到 Kind。Helm 使用本地 chart、`IfNotPresent` 和同一个 scheduler ConfigMap；K1
只滚动替换 scheduler image。

HyperNode 树：

```text
bug019-root (tier3)
  -> bug019-pair-ab (tier2)
       -> bug019-a -> worker
       -> bug019-b -> worker2
  -> bug019-pair-cd (tier2)
       -> bug019-c -> worker3
       -> bug019-d -> worker4
```

### K0：基线复现 — PASS

`f954233d2` scheduler，用户同构 preferred PodGroup，结果：

```text
decode/0   -> worker (a)
decode/1   -> worker (a)
prefill/0  -> worker (a)
prefill/1  -> worker (a)
PodGroup   -> Running, running=4, allocated=bug019-a
```

日志证明 tier1-first 单根 dry-run：

```text
Try Job hyperNode=bug019-a tierLayer=0
hyperNodeCandidates=bug019-a（四个 SubJob 均为 singleton）
随后依次尝试 b/c/d singleton
Allocate Job success hyperNode=bug019-a
```

### K1：修复后用户场景 — PASS

只替换 scheduler 为 `dev-019-k1`，重建同一 PodGroup 和 Deployments：

```text
decode/0   -> worker2 (b)
decode/1   -> worker  (a)
prefill/0  -> worker2 (b)
prefill/1  -> worker  (a)
PodGroup   -> Running, running=4, allocated=bug019-pair-ab
```

日志：

```text
preferredContainerTier=2, tierOrder=[2 3 4 1]
outer candidates: pair-ab, pair-cd
pair-ab SubJob candidates: a,b
第二个 decode:  a=100, b=0 -> a
第二个 prefill: a=100, b=0 -> a
Allocate Job success hyperNode=bug019-pair-ab
```

四 Pod 只使用一个 tier2，且两个 tier1 各 `1 prefill + 1 decode`。

### K2：real-hard NTA containment — PASS

最初尝试仅预置 PodGroup annotation `job-allocated-hypernode=pair-cd`。对没有已分配 Pod
的新 Job，session 会从实际 tasks 重新计算并清空该 annotation，因此这不是有效的
hard anchor；该尝试仍选择 pair-ab，未作为 K2 结果。

修正 setup 后，在同一 PodGroup 内先创建一个固定在 c/worker3 的 Running anchor Pod，
使真实 Job placement=`bug019-c`；Job real-hard network topology 的最高允许层为 tier2。
随后再创建四个目标 Deployments：

```text
anchor      -> worker3 (c)
decode/0    -> worker3 (c)
decode/1    -> worker4 (d)
prefill/0   -> worker3 (c)
prefill/1   -> worker4 (d)
PodGroup    -> Running, running=5, allocated=bug019-pair-cd
```

日志：

```text
preferredContainerTier=2, tierOrder=[2 3 4 1]
HyperNode screening: 3/8 available
pair-ab excluded by networkTopology
only outer candidate: bug019-pair-cd
SubJob candidates: bug019-c,bug019-d
occupied sibling score drops 100 -> 0
Allocate Job success hyperNode=bug019-pair-cd
```

证明 GTA soft order 未重新加入 NTA hard scope 外的 pair-ab。

### 清理

- K0/K1/K2 workload、PodGroups、anchor 与 HyperNodes 已删除；
- 删除前 default namespace 与 HyperNodes 均为 `No resources found`；
- `kind delete cluster --name volcano-dev-019` 成功删除 5 个节点；
- `kind get clusters` 最终只剩用户原有的 `kind`；
- detached baseline worktree 已移除；
- 未推送任何远程镜像；两个本地 scheduler 验证镜像保留供复查。

## 最终 Git 状态

```text
branch: fix/018-podgroup-antiaffinity-bc-production-containment
base:   f954233d2ba7d3c512f0e1d7c1b999c9e829d14e
commit: c0828a799 fix(scheduler): preserve preferred subgroup traversal order
```

上述 6 个预期修改文件均已提交；提交后源码工作区干净。`git diff --check` 通过，
无临时 worktree、生成目录、go.mod 修改或 Kind 集群残留，提交未推送远端。
