> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 021：Soft Topology 缺省 Tier 修复提案与验证计划

## 2026-08-06 语义修订（替代下文旧方案）

用户进一步确认原始设计语义：对 soft topology，`highestTierAllowed == nil` 与
`highestTierAllowed == 1` 等价。缺省值不是“根据当前可行候选动态计算最低 LCA tier”，
而是 scheduler 接收 Job/SubJob 后应得到的统一内部表示。用户回复“请重新修改并验证”，
批准本节修订方案和验证用例；下文旧的动态候选推导方案及其历史结果不再作为最终实现和
验收依据。

### 修订后的根因

当前 Session 只把 `highestTierName` 翻译成 `highestTierAllowed`，没有补齐 soft mode 的
缺省 tier。下游 `preferJobSoftTopologyCandidates` 和
`preferJobSoftTopologyScoreCandidates` 因而能观察到两种本应等价的内部状态：`nil` 和
显式 `1`，并对二者采取不同路径。上一版在两个候选 helper 中反复动态解释 `nil`，虽然
恢复了部分近端偏好，却改变了显式 tier1 的既有回退行为，不能满足严格等价性。

### 修订后的实现方案

1. 在 `Session.adjustNetworkTopologySpec` 处理 scheduler 内部 JobInfo/SubJobInfo topology
   副本时统一规范化：
   - 已设置 `highestTierAllowed`：保持原值；
   - 已设置 `highestTierName`：仍按 HyperNode tier-name map 翻译，翻译失败保持原样并告警；
   - soft 且两个 highest-tier 字段均未设置：将内部 `highestTierAllowed` 设置为 `1`；
   - hard 且未设置 highest tier：保持 `nil`。
2. Job 和每个 SubJob 使用同一个规范化 helper；不修改 PodGroup/SubGroupPolicy 原始 API
   对象。
3. 撤销 allocate candidate helper 中对 `nil` 的动态最小 LCA 推导，恢复其只消费明确 tier
   的职责。经正常 Session 入口后，soft nil 与显式 tier1 进入完全相同的下游逻辑。
4. 不修改 API、CRD、controller、webhook 或 network-topology-aware plugin contract。

### 修订后的验证用例

#### N1：Session 入口规范化

- 范围：`pkg/scheduler/framework/session_test.go`。
- 覆盖：Job/SubJob soft nil 均变成 tier1；显式 tier1 保持不变；tier-name 正常翻译；
  未知显式 tier-name 不会误降级为 tier1；hard nil 保持 nil；mode 保持不变。
- 不可变性：断言 PodGroup 和 SubGroupPolicy 中的原始 topology spec 未被修改。
- 命令：

  ```bash
  go test ./pkg/scheduler/framework -run 'TestSession_adjustNetworkTopologySpec|TestAdjustNetworkTopologySpec' -count=50
  ```

#### N2：nil 与显式 tier1 等价

- 范围：framework 规范化测试以及 allocate candidate/full-action 回归。
- 覆盖：分别由 soft nil 和 soft tier1 构造 Session，断言内部 Job/SubJob topology 相等；
  下游 candidate helper 对规范化后的两者返回相同候选；保留显式 tier2 既有完整 allocate
  回归，避免改变原有行为。
- 命令：

  ```bash
  go test ./pkg/scheduler/framework ./pkg/scheduler/actions/allocate \
    -run 'Test(Session_adjustNetworkTopologySpec|AdjustNetworkTopologySpec|PreferJobSoftTopology|AllocateJobSoftAffinity)' -count=50
  ```

#### N3：相关回归与代码门禁

```bash
./hack/update-gofmt.sh
go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/actions/allocate -count=1
make verify
```

通过标准：命令通过且没有非预期 generated diff。仓库级 `make unit-test` 的已知、与本改动
无关的 macOS/Linux-only/eBPF/PVC fake-client 失败继续按历史结果单独记录，不以弱化 focused
门禁代替。

#### N4：Kind 真实等价性与 soft fallback

- 使用 1 control-plane + 4 worker 的显式 tier1/tier2/tier3 HyperNode tree。
- 在完全清理并重置的相同环境中依次运行两份相同 workload：一份 soft 不指定 highest
  tier，一份显式 `highestTierAllowed: 1`；比较 Pod 绑定、Job/SubJob topology anchor 和最终
  LCA，要求结果一致。
- 再运行近端资源不可行的 workload，要求 soft 仍能绑定远端节点，证明 tier1 是偏好而非
  hard 限制。
- 重建并加载包含本次修订的本地 scheduler 镜像；记录镜像 ID、对象状态、scheduler 日志和
  清理结果。

## 2026-08-06 修订实施与验证结果

### 最终修改范围

- Bug 分支：`fix/021-soft-topology-default-tier`；
- 基于生产分支：`dev-1.14`（基线 `71f1d3248`）；
- 最终提交：`ce76e556d fix(scheduler): normalize default soft topology tier`；
- 交付方式：由用户从生产分支手动 cherry-pick，不直接修改或合并 `dev-1.14`。

- `pkg/scheduler/framework/session.go`
  - 新增 scheduler 内部缺省常量 `defaultSoftNetworkTopologyTier = 1`；
  - `adjustNetworkTopologySpec` 对 Job 和 SubJob 的内部 topology copy 调用统一 helper；
  - 显式 allowed tier 保持不变，显式 tier name 优先翻译，soft 真正未指定时补 tier1，
    hard nil 保持 nil；
  - 未知显式 tier name 返回错误并保持原值，不会误降级成 tier1。
- `pkg/scheduler/framework/session_test.go`
  - 覆盖 Job/SubJob soft nil、显式 tier1、hard nil、tier-name 成功/失败、mode 保持和
    PodGroup/SubGroupPolicy 原对象不可变。
- `pkg/scheduler/actions/allocate/placement_test.go`
  - 补充两个 candidate helper 的显式 tier1 行为；保留未经过 Session 规范化的 nil 安全
    返回测试。

上一版 `allocate.go` 中动态计算“当前候选最小 LCA tier”的实现以及相应完整 allocate 测试
改动已全部撤销。最终 `allocate.go`、`allocate_test.go` 相对基线无 diff；候选层继续只消费
明确的 tier，职责边界保持不变。

### N1/N2：focused 等价性，PASS

```bash
go test ./pkg/scheduler/framework ./pkg/scheduler/actions/allocate \
  -run 'Test(Session_adjustNetworkTopologySpec|AdjustNetworkTopologySpec|PreferJobSoftTopology|AllocateJobSoftAffinity)' \
  -count=50
```

结果：

- `pkg/scheduler/framework`：PASS，50 次；
- `pkg/scheduler/actions/allocate`：PASS，50 次；
- soft nil 与显式 tier1 产生相同的 Job/SubJob 内部 spec；
- soft nil 的原始 PodGroup/SubGroupPolicy 字段仍为 nil；
- hard nil、未知显式 tier name 均未被默认转换。

### N3：相关包与代码门禁，PASS

```bash
go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/actions/allocate -count=1
make verify
```

四个相关包全部通过，`make verify` 通过且 generated code 无差异。verify 工具临时把
`k8s.io/code-generator` 从 v0.34.1 改成 v0.36.3，并生成未跟踪的 `volcano.sh/` 客户端目录；
已恢复 `go.mod` 并删除该 232KB 生成目录，最终没有非预期 diff。

额外执行 `make unit-test`。命令整体在 macOS 返回 1，失败仍来自与本修复无关的既有平台
问题：agent handler 的 Linux build tags、`runc` Linux syscall、network-qos TC interface/
工具测试以及 eBPF mock。该次运行中全部 scheduler 包通过，包括：

- `pkg/scheduler/actions/allocate` PASS（99.684s）；
- `pkg/scheduler/framework` PASS（6.873s）；
- `pkg/scheduler/plugins/network-topology-aware` PASS（9.433s）；
- 其余 scheduler actions/api/cache/plugins 均 PASS。

因此仓库级命令失败不是本次 Session/candidate 变更引入的回归；按任务规则仍保留为未关闭的
repository-wide gate 记录。

### N4：本地镜像与 5 节点 Kind，PASS

构建命令：

```bash
TAG=dev-021-normalized BUILDX_OUTPUT_TYPE=docker \
  make vc-scheduler-image vc-controller-manager-image vc-webhook-manager-image
```

| Image | Local image ID | Arch |
| --- | --- | --- |
| `volcanosh/vc-scheduler:dev-021-normalized` | `sha256:000ac2264f4e36a03f6b397b2006179da0e2f43fb0b60727f803ab193187b6bc` | arm64 |
| `volcanosh/vc-controller-manager:dev-021-normalized` | `sha256:bbe123e917cf36e28fdda2f7ce84114985605a8c64962ad8c2f61f385a7783c7` | arm64 |
| `volcanosh/vc-webhook-manager:dev-021-normalized` | `sha256:705e00119b66633241090319d4ae75f2181bce193762557eabe020e62e4834cd` | arm64 |

集群 `volcano-dev-021`：Kubernetes v1.36.1，1 control-plane + 4 workers，共 5 个 ARM64
Node，每个 allocatable CPU=12。HyperNode tree 为 4 个 tier1 leaf、2 个 tier2 group、1 个
tier3 root；各对象 `nodeCount` 与树结构一致。scheduler/controller/admission 均实际运行
`dev-021-normalized`，scheduler ConfigMap 确认启用 NTA/GTA gradient/order 和 NTA
weight=100。

#### nil 与显式 tier1 等价性，PASS

在完整删除第一轮对象并确认测试 Pod 全部消失后，以同名 PodGroup 和完全相同 filler/
Deployment 重跑；两轮唯一差异是第二轮设置 `highestTierAllowed: 1`。

| 输入 | role-a | role-b | PodGroup | 最终 Job anchor/LCA |
| --- | --- | --- | --- | --- |
| soft，highest tier 未指定 | worker / `hn-a` | worker3 / `hn-c` | Running, running=2 | `hypercluster-all` (tier3) |
| soft，`highestTierAllowed: 1` | worker / `hn-a` | worker3 / `hn-c` | Running, running=2 | `hypercluster-all` (tier3) |

nil 轮次 API 输出为 `mode=soft tier=<empty> tierName=<empty>`；显式轮次为
`mode=soft tier=1 tierName=<empty>`。scheduler 日志分别在 02:34 和 02:35 记录相同的提交
序列：先 `hn-a`，再 `hn-c`，Job anchor 从 `hn-a` 更新为 `hypercluster-all`。这直接证明
外部 API 表示不同，但进入 Session 后的调度结果与 soft fallback 行为相同。

#### 本地不可行后的 soft fallback，PASS

K2 使用 soft nil PodGroup：role-a 在 `hn-a` 请求 10500m CPU，filler 在 `hn-b` 请求
10500m CPU，随后 role-b 请求 2000m CPU。scheduler 明确记录 `hn-a`、`hn-b` 均
`no allocatable solution`，然后提交远端 `hn-c`：

| Workload | Node | HyperNode |
| --- | --- | --- |
| role-a | `volcano-dev-021-worker` | `hn-a` |
| local filler | `volcano-dev-021-worker2` | `hn-b` |
| role-b | `volcano-dev-021-worker3` | `hn-c` |

PodGroup 为 Running/running=2，API highest tier 仍为空，最终 Job anchor 为
`hypercluster-all`。证明内部默认 tier1 是 soft preference，不是 hard cap。

#### 清理

执行 `kind delete cluster --name volcano-dev-021`，5 个任务节点全部删除；`kind get clusters`
只剩用户原有的 `kind` 集群。任务镜像未推送远端。

## 审批状态

- 当前阶段：修订实现及 scheduler/Kind 验证完成；repository-wide unit gate 保留既有平台失败
- Proposal：已批准（2026-08-05，用户回复“请开始修改”）
- 验证用例：已批准（2026-08-05，U1-U4、K1-K2）
- 语义修订与 N1-N4：已批准（2026-08-06，用户回复“请重新修改并验证”）
- 生产基线分支：`dev-1.14`
- Bug 分支：`fix/021-soft-topology-default-tier`
- 最终提交：`ce76e556d`
- 源码基线：`71f1d3248`

下文记录的是 2026-08-05 已执行的旧方案与历史结果；最终状态以 2026-08-06 修订为准。

## 调查结论

### 1. API 与原始 soft 语义

`NetworkTopologySpec.HighestTierAllowed` 在 soft mode 下是可选字段。Session 的
`HyperNodesTiers` 按 tier 升序排列，lower tier 代表更优的网络性能域；soft gradient
不施加 hard 过滤，并允许 allocate 从低 tier 向高 tier 搜索。

HyperNode API 允许 `tier >= 0`，因此常见部署的最低层虽然是 tier1，但实现不能假定所有
合法拓扑都从整数 1 开始，也不能假定 tier 连续。

### 2. 回归位置

当前 `pkg/scheduler/actions/allocate/allocate.go` 中：

```text
preferJobSoftTopologyCandidates
  -> HighestTierAllowed == nil 时直接返回全部 statement 候选

preferJobSoftTopologyScoreCandidates
  -> HighestTierAllowed == nil 时直接返回全部 score 候选
```

这两处都跳过了 Job anchor 的 LCA 偏好。现有 table-driven 测试还把
`does not constrain without a job tier` 固化为旧预期，因此基线 focused 测试通过并不
代表语义正确。

基线命令：

```bash
cd volcano
go test ./pkg/scheduler/actions/allocate \
  -run 'TestPreferJobSoftTopology(Candidates|ScoreCandidates)$' -count=1
```

基线结果：PASS；代码和测试均确认 `nil` 当前会保留所有候选。

### 3. 为什么不直接把字段写成 `tier1`

直接执行 `job.NetworkTopology.HighestTierAllowed = ptr.To(1)` 有三个问题：

1. 修改 Session 内部 topology spec，混淆“用户显式 tier1”和“自动选择最低 tier”；
2. 合法 HyperNode topology 可以从 tier0 开始或存在 tier 间断；
3. 现有 helper 在 tier1 首选集为空时会一次性回退全部候选，不能保证先试 tier2、再试
   tier3。

因此只在候选选择 helper 内解释 `nil`，不改写 API/spec。

## 推荐修改方案

### 1. 统一解析 Job soft preference

在 `allocate.go` 增加一个同文件私有 helper，为已经通过 dry-run 的候选解析本轮优先
LCA tier：

```text
输入：Job topology spec、Job AllocatedHyperNode、候选 HyperNode 名称

显式 HighestTierAllowed：
  preferredTier = explicit tier

缺省 HighestTierAllowed：
  计算每个有效候选与 Job anchor 的 LCA tier
  preferredTier = 所有可行候选中的最小 LCA tier
```

“取最小可行 LCA tier”等价于从 Session 最低 tier 开始逐层放宽，直到遇到第一个非空
候选层；同时自然支持 tier0 和非连续 tier。

如果 Job anchor 或候选 LCA 无法在当前 HyperNode tree 中解析，沿用 soft 的安全行为：
不把异常解析变成 hard rejection；没有任何有效 preferred 候选时返回原候选集合。

### 2. 修复 SubJob statement 候选路径

`preferJobSoftTopologyCandidates`：

- 删除 `HighestTierAllowed == nil` 的提前返回；
- 保留现有启用条件：SubGroup policy、Job soft mode、有效 Job anchor、存在已放置 peer
  SubJob；
- 显式 tier 使用现有 `LCA tier <= highestTierAllowed` 规则；
- 缺省 tier 只保留最小可行 LCA tier 的 statement 候选；
- preferred 集为空时仍返回完整 `stmts`。

### 3. 修复 Job 外层 score 候选路径

`preferJobSoftTopologyScoreCandidates` 使用完全相同的 tier 解析规则：

- 删除 `HighestTierAllowed == nil` 的提前返回；
- 缺省 tier 在所有成功 outer candidates 中保留最小可行 LCA tier；
- preferred 集为空时返回完整 `scores`；
- 候选内部原 score 和后续选择逻辑不变。

### 4. 语义边界

- 这是候选偏好，不改变 predicate、resource feasibility 或 Statement recover/commit；
- 显式 tier 行为保持不变；
- 缺省 tier 不设置 hard cap，最接近的候选不可行时仍能选择更高 LCA tier；
- 不增加跨调度周期回溯，也不迁移已经运行的 Pod；
- 不改变 SubJob 自身 soft topology gradient；本修复只补齐 Job anchor 跨 SubJob/外层候选
  的缺省 tier 语义。

## 预先约定的验证用例

### U1：两个 candidate helper 的缺省 tier 单测

- 目的：直接证明 `nil` 不再 bypass，并且 statement/score 两条路径语义一致。
- 范围：`pkg/scheduler/actions/allocate/placement_test.go`，table-driven 单测。
- 设置：构造 tier1/tier2/tier3（含 cluster top 关系）的内存 HyperNode tree，以及已有
  Job anchor 和 peer SubJob。
- 情形：
  1. soft + 缺省 tier，同时存在同一 tier1、同一 tier2 和远端候选：只保留最低 LCA
     tier 候选；
  2. 同一 tier1 不可行但同一 tier2 与远端均可行：选择同一 tier2，证明逐级回退；
  3. 只有远端候选可行：保留远端候选，证明 soft 不变成 hard；
  4. soft + 显式 tier2：保持现有阈值语义；
  5. hard、无 peer、无 anchor、无 SubGroup policy：保持原输入；
  6. 最低合法 tier 为 0：自动选择 tier0，证明没有写死整数 1。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run 'TestPreferJobSoftTopology(Candidates|ScoreCandidates)$' -count=50
  ```

- 通过标准：50 次全部通过；两个 helper 对等价候选返回相同的 HyperNode key 集合。

### U2：同一次 Job dry-run 的完整 allocate 回归

- 目的：证明 Job soft 未指定最高 tier 时，后续 SubJob 会先使用最低可行 LCA 层，而不是
  被远端更高插件分数带走。
- 范围：已有 `TestAllocateJobSoftAffinityAcrossSubJobs` 的完整 Action 路径。
- 设置：两个 tier3 分支，每个分支含多个 tier2/tier1；两个分区 SubJob；第一个 SubJob
  建立 anchor；同 leaf 容量不足，近端 sibling tier1 的 LCA 为 tier2，远端候选 LCA 更高，
  且测试插件故意给远端更高分。
- 变更：增加 soft + `HighestTierAllowed=nil` 场景；显式 tier2 场景继续保留为回归。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestAllocateJobSoftAffinityAcrossSubJobs$' -count=50
  ```

- 预期：缺省 tier 场景中两个 SubJob 的总 LCA 为近端 tier2；所有 Pod 均绑定。
- 通过标准：50 次无跨远端 tier3/cluster-top，显式 tier2 场景也继续通过。

### U3：跨 `allocateForJob` 调用的完整回归

- 目的：覆盖 `preferJobSoftTopologyScoreCandidates` 的真实调用点。
- 范围：已有 `TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates`。
- 设置：两个 SubJob 分两次 `allocateForJob` 调用提交；第二次远端 outer candidate 分数更高。
- 变更：增加 soft + `HighestTierAllowed=nil` 场景。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates$' -count=50
  ```

- 预期：第二次调用先选与已有 anchor 的最小可行 LCA tier；资源不足时仍可回退远端。
- 通过标准：50 次均满足预期 Job LCA 和 bind 数。

### U4：相关 package 与仓库回归门禁

- 目的：确认 API mode、framework gradient、NTA hard/soft 及其他 allocate 路径未回归。
- 命令：

  ```bash
  cd volcano
  ./hack/update-gofmt.sh
  go test ./pkg/scheduler/api ./pkg/scheduler/framework \
    ./pkg/scheduler/plugins/network-topology-aware \
    ./pkg/scheduler/actions/allocate -count=1
  make unit-test
  make verify
  ```

- 通过标准：全部命令通过；不产生非预期 generated diff。

### K1：Kind 缺省 tier 近端优先

- 目的：通过真实 scheduler/controller、PodGroup、HyperNode CRD 和 bind 路径验证 soft
  未指定最高 tier 的自动亲和。
- 拓扑：任务专用 1 control-plane + 4 workers Kind 集群（共 5 个 Kubernetes 节点）；
  每个 worker 对应一个 tier1 HyperNode，两个 tier1 组成一个 tier2，两个 tier2 组成
  tier3。符合最多 8 节点限制。
- workload：Job-level `networkTopology.mode: soft`，不设置 `highestTierAllowed/name`；两个
  分区 SubJob；required hostname anti-affinity 使第二个分区不能复用第一个 worker，验证
  它优先落到同一 tier2 的 sibling tier1。
- 镜像：使用唯一 `dev-021` 本地 tag，构建并加载 scheduler、controller-manager、
  webhook-manager 等 chart 所需本地镜像；Helm 使用 `IfNotPresent`。
- 核心命令：

  ```bash
  cd volcano
  TAG=dev-021 BUILDX_OUTPUT_TYPE=docker make images
  kind create cluster --name volcano-dev-021 --config ../issues/bugs/021-soft-topology-default-lowest-tier-IP/kind-config.yaml
  kind load docker-image volcanosh/vc-scheduler:dev-021 --name volcano-dev-021
  kind load docker-image volcanosh/vc-controller-manager:dev-021 --name volcano-dev-021
  kind load docker-image volcanosh/vc-webhook-manager:dev-021 --name volcano-dev-021
  helm upgrade --install volcano installer/helm/chart/volcano \
    --namespace volcano-system --create-namespace \
    --set basic.image_tag_version=dev-021 \
    --set basic.image_pull_policy=IfNotPresent
  kubectl apply -f ../issues/bugs/021-soft-topology-default-lowest-tier-IP/hypernodes.yaml
  kubectl apply -f ../issues/bugs/021-soft-topology-default-lowest-tier-IP/job-default-tier.yaml
  ```

- 观测：记录节点架构、Pod 绑定、PodGroup/Job 状态、HyperNode tree、两分区 leaf 与 LCA、
  scheduler 相关日志。
- 预期：两个分区均成功绑定到不同 worker，但位于同一 tier2；Job LCA 不上升到 tier3。
- 通过标准：真实对象状态、绑定和 LCA 全部满足预期，无异常 scheduler/controller 日志。

### K2：Kind soft fallback

- 目的：证明缺省 tier 的近端偏好不会成为硬限制。
- 设置：在同一集群中使用单独 workload，通过 node affinity/资源占用使 anchor 所在 tier2
  没有可行 sibling，远端 tier2 保持可用。
- 预期：第二个分区回退至远端 tier2 并成功绑定，Job LCA 上升到 tier3。
- 通过标准：workload 完成绑定；scheduler 日志和最终 LCA 明确显示远端 fallback。

### 清理

验证完成后删除任务专用 workload，并删除 `volcano-dev-021` Kind 集群；除非用户要求保留
用于调试。所有实际命令、输出摘要、镜像 ID、日志位置和清理结果回填本文件。

## 实施结果

### 修改范围

实现只修改 Volcano scheduler allocate 路径及其测试：

- `pkg/scheduler/actions/allocate/allocate.go`
- `pkg/scheduler/actions/allocate/placement_test.go`
- `pkg/scheduler/actions/allocate/allocate_test.go`

具体行为：

1. 两个 candidate helper 不再因 `HighestTierAllowed == nil` 提前返回；
2. 新增私有 `preferredJobSoftTopologyTier`：
   - 显式 tier 原样返回，保持既有阈值语义；
   - 缺省 tier 计算所有成功候选与 Job anchor 的 LCA，选择最小可行 LCA tier；
   - 无法解析任何有效 LCA 时返回原候选，保持 soft 安全回退；
3. statement 和 score 两条路径使用同一解析规则；
4. 未修改 API、CRD、controller、webhook、plugin gradient contract 或用户 spec。

### 测试改动

- 直接 helper 测试新增：
  - 缺省 tier 的同 leaf 优先；
  - 同 leaf 不可行时选择近端下一 LCA tier；
  - 仅远端可行时 soft fallback；
  - tier0 为合法最低层；
  - statement/score 两条路径等价；
- `TestAllocateJobSoftAffinityAcrossSubJobs` 改为显式 tier2 与缺省 tier 双场景；
- `TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates` 同样改为双场景；
- 两个完整测试都故意让远端候选分数更高，稳定证明 topology preference 先于普通评分。

## 单元测试与门禁结果

### U1-U3：PASS

命令：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^(TestPreferJobSoftTopology(Candidates|ScoreCandidates)|TestAllocateJobSoftAffinityAcrossSubJobs|TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates)$' \
  -count=50
```

结果：PASS，耗时 `96.503s`。四组测试（含两个完整路径的双场景）重复 50 次均通过。

### U4 focused scheduler packages：PASS

命令：

```bash
go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/actions/allocate -count=1
```

结果：全部 PASS：

- scheduler/api：`1.415s`
- scheduler/framework：`1.434s`
- network-topology-aware：`4.495s`
- scheduler/actions/allocate：`96.670s`

### gofmt / diff：PASS

```bash
./hack/update-gofmt.sh
git diff --check
```

结果：PASS。

### make verify：PASS

首次执行时当前 shell 未导出 `GOPATH`，在 `generate-internal-groups.sh` 因
`GOPATH: unbound variable` 退出。按该生产线已验证过的主机环境显式执行：

```bash
GOPATH=/Users/vanderchen/go make verify
```

结果：PASS，输出包括：

```text
diffing hack/../pkg against freshly generated codegen
hack/../pkg up to date.
```

verify 脚本会临时把 `k8s.io/code-generator` 从 `v0.34.1` 改为 `v0.36.3`，并在
仓库根目录生成 232KB、53 个文件的 `volcano.sh/` 中间目录。验证完成后已把 `go.mod`
精确恢复为 `v0.34.1` 并删除该非跟踪目录；没有 generated/API 差异保留。

### make unit-test：已执行，repository-wide gate 未全绿

`make unit-test` 在 macOS/ARM64 主机上失败，失败项不经过本次 scheduler 修改路径：

1. Linux-only agent/cgroup/metriccollect/network-qos 包在 `GOOS=darwin` 下被全量枚举，
   出现 build constraints、runc Linux syscall 和 TC interface 编译失败；
2. network-qos eBPF 平台测试期望 `new map failed`，实际因主机目录权限先返回
   `mkdir failed`；
3. 既有 `TestAllocateWithPVC/static_pv_matched_and_node_with_enough_resources` 出现
   PreBind fake client 找不到 node `n2`，绑定数为 0；单独重复时可稳定复现相同的
   `node n2 not found`，与本次 topology candidate 逻辑无调用关系。

同一次全量运行中 scheduler api/framework/NTA/GTA 等相关包通过；本任务 focused
allocate package 和 U1-U3 均独立通过。由于预先约定的 U4 写明 repository-wide
`make unit-test` 应全绿，当前按规则不把任务目录改为 DONE，等待用户确认是否接受上述
平台/既有失败并以 scoped scheduler gate 作为本 Bug 的权威完成门禁。

## 本地镜像

执行：

```bash
TAG=dev-021 BUILDX_OUTPUT_TYPE=docker make images
```

Kind 所需镜像全部成功构建：

| Image | Local image ID | Arch |
| --- | --- | --- |
| `volcanosh/vc-scheduler:dev-021` | `sha256:0285c52ea0546af9bad3e11e9fe77e2c840d5be21baaa6b9d887fa4db5ac810e` | arm64 |
| `volcanosh/vc-controller-manager:dev-021` | `sha256:bd0aa484edf60c75c94b51d3f3ba6f64ac2f95469a0696978203a492cf0dbd4a` | arm64 |
| `volcanosh/vc-webhook-manager:dev-021` | `sha256:88505caf2b19ee63d8210eb4948e6b6bd45e48956203c5f6d8048fa9d814a41b` | arm64 |

完整 `make images` 随后在默认禁用且本验证不需要的 `vc-agent` 阶段，因 Docker Hub
OpenEuler metadata `DeadlineExceeded` 退出。Helm 默认
`agent_scheduler_enable: false`，且没有启用 agent；scheduler/controller/webhook 三个
本地镜像均已加载并用于 Kind。镜像未推送远端。

## K1/K2：5 节点 Kind 验证

### 集群、组件与拓扑

- 集群：`volcano-dev-021`
- Kubernetes：v1.36.1 / ARM64
- 节点：1 control-plane + 4 workers，共 5 个可见 Kubernetes Node
- 每个 Node allocatable CPU：12
- tier1：`hn-a/b/c/d`，各一个 worker，`nodeCount=1`
- tier2：`instance-group-ab/cd`，各两个 tier1 child，`nodeCount=2`
- tier3：`hypercluster-all`，两个 tier2 child，`nodeCount=2`
- Session 虚拟 cluster top 为 tier4，总计 8 个 HyperNode candidate
- scheduler/controller/admission 均使用 `dev-021` 且 Running
- scheduler 同时启用 NTA/GTA gradient/order；NTA weight=100

加载镜像：三个 `dev-021` 核心镜像及主机已有的 `nginx:1.27-alpine`。

任务目录保留了以下验证清单：

- `kind-config.yaml`
- `hypernodes.yaml`
- `scheduler-config.yaml`
- `k1-podgroup.yaml`、`k1-filler.yaml`、`k1-role-a.yaml`、`k1-role-b.yaml`
- `k2-podgroup.yaml`、`k2-filler.yaml`、`k2-role-a.yaml`、`k2-role-b.yaml`

### K1：缺省 tier 的下一近端 LCA 优先，PASS

PodGroup：

```yaml
networkTopology:
  mode: soft
```

API 中 `highestTierAllowed` 和 `highestTierName` 始终为空。

步骤：

1. 在 `hn-c` 对应 worker3 直接绑定 8000m CPU filler，使远端 candidate 的 NTA
   binpack score 高于空闲 `hn-b`；
2. role-a 请求 10500m CPU 并通过 nodeSelector 固定到 worker/`hn-a`，建立 Job anchor；
3. role-b 请求 2000m CPU，`hn-a` 剩余 1400m 不可行；`hn-b`、`hn-c`、`hn-d`
   均可行；
4. scheduler 对这些 outer candidates 都完成 dry-run，最终提交近端 `hn-b`。

最终绑定：

| Workload | Node | HyperNode |
| --- | --- | --- |
| role-a | `volcano-dev-021-worker` | `hn-a` |
| role-b | `volcano-dev-021-worker2` | `hn-b` |
| high-score filler | `volcano-dev-021-worker3` | `hn-c` |

PodGroup 最终 `phase=Running`、`running=2`，Job annotation 为
`volcano.sh/job-allocated-hypernode=instance-group-ab`。role-a/role-b 的 LCA 是 tier2，
没有被远端更高 binpack 分数带到 tier3。

关键 scheduler 证据：

```text
job=default/pg-soft-default-k1, allocatedHyperNode=hn-a
... role-b ... hyperNode=hn-b ... success
... role-b ... hyperNode=hn-c ... success
Allocate job to hyperNode success ... hyperNode=hn-b
update allocated hyperNode for job ... old=hn-a, new=instance-group-ab
```

### K2：本地 pair 不可行后的 soft fallback，PASS

步骤：

1. 清理 K1 workload；
2. role-a 再次固定在 `hn-a`，请求 10500m CPU；
3. 在 `hn-b` 对应 worker2 直接绑定 10500m CPU filler；
4. role-b 请求 2000m，`hn-a/hn-b` 均不可行，远端 `hn-c/hn-d` 可行；
5. role-b 最终绑定 worker4/`hn-d`。

最终绑定：

| Workload | Node | HyperNode |
| --- | --- | --- |
| role-a | `volcano-dev-021-worker` | `hn-a` |
| local filler | `volcano-dev-021-worker2` | `hn-b` |
| role-b | `volcano-dev-021-worker4` | `hn-d` |

PodGroup 最终 `phase=Running`、`running=2`；API highest tier 字段仍为空；Job annotation
从 `hn-a` 更新为 tier3 `hypercluster-all`。

关键 scheduler 证据：

```text
job=default/pg-soft-default-k2, allocatedHyperNode=hn-a
hn-a ... no allocatable solution
hn-b ... no allocatable solution
Allocate job to hyperNode success ... hyperNode=hn-d
update allocated hyperNode for job ... old=hn-a, new=hypercluster-all
```

结论：缺省 tier preference 能逐层选择最近可行 LCA，同时没有形成 hard cap。

### 清理

K1 workload 已在进入 K2 前删除。完成 K2 后执行：

```bash
kind delete cluster --name volcano-dev-021
kind get clusters
```

任务专用的 5 个节点全部删除；用户原有 `kind` 集群仍存在且未受影响。

## 旧方案待确认项（已由 2026-08-06 修订结果替代）

本节是上一版动态候选方案执行后的历史记录。最终代码和验证结果以文首的 2026-08-06
修订章节为准；repository-wide `make unit-test` 的平台失败继续记录，因此任务目录暂不改为
`DONE`。
