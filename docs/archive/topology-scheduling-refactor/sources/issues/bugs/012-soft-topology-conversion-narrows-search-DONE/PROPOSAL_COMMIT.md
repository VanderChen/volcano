> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 012：移除 Soft-to-Hard Topology 转换提案

## 审批状态

- 当前阶段：完成
- 实现状态：完成
- 源码基线：`a31ab3dd3`
- 工作分支：`fix/012-remove-soft-to-hard-topology-conversion`
- Proposal：用户已批准
- 验证用例：用户已批准

用户批准后已按本提案实施，结果记录见文末。

## 结论

012 移除 `convertSoftToHardTopology`，恢复 soft topology 的原始模式和候选
fallback 范围，但继续使用当前逐梯度、first-feasible 的贪心 allocate。

本次只保持如下 hard/soft 边界：

```text
hard / required topology
  决定候选是否可用

soft / preferred topology
  决定当前候选前沿中的尝试和选择顺序
  preferred 不可行时不得删除 fallback

allocate
  返回第一个形成 JobReady/JobPipelined 的 gradient solution
  不搜索后续 gradient 的全局更优 solution
```

## 根因

### 1. Session 打开时擦除了 soft mode

`adjustNetworkTopologySpec` 完成 tier name 翻译后调用
`convertSoftToHardTopology`。转换只修改 scheduler 内部副本，不修改 API 对象，但
allocate 判断读取的正是内部 `JobInfo.NetworkTopology` 和
`SubJobInfo.NetworkTopology`。

因此转换后：

- `JobInfo.IsSoftTopologyMode()` 返回 false；
- Bug 008 的 `preferJobSoftTopologyCandidates` 不启用；
- Bug 008 的 `preferJobSoftTopologyScoreCandidates` 不启用；
- soft 和 hard 在 allocate、preempt、reclaim、evict 等路径中无法可靠区分。

### 2. SubJob soft 被施加了 hard tier 边界

转换策略把 SubJob soft 改为 hard，并设置为 Job 的有效 tier。该 tier 随后进入
`network-topology-aware` 的 `getSearchRoot` 和 gradient eligibility 判断。

soft 的正确语义应是“优先但允许 fallback”；转换后的 hard tier 会在 predicate/
resource dry-run 前删除域外候选，导致搜索范围真实变窄。

### 3. 纯 soft topology 依赖转换进入 HyperNode path

当前 `RequiresHyperNodeAllocate()` 包含 hard topology、SubGroupPolicy 和
PodGroup/SubGroup topology affinity，但没有直接包含所有 network topology。

若只删除转换，只有 Job soft topology、且没有 SubGroupPolicy 的 Job 可能回到普通
Node allocate，从而完全绕过 HyperNode gradient 和 Job soft 锚点维护。

### 4. Plugin gradient 没有“无 hard 意见”语义

framework 当前把所有启用的 HyperNode gradient plugin 结果按名称求交：

- 空 gradient 表示 hard 无可行候选；
- plugin 不想过滤时只能返回一个完整子树；
- soft NTA 与 required group topology 同时启用时，soft 结果可能错误参与 hard
  交集；
- soft SubJob 当前只返回输入 HyperNode，可能与另一个 hard plugin 的 descendant
  候选求交为空。

移除转换后必须显式区分“参与 hard 过滤”和“soft/不适用，放弃过滤”。

## 实现方案

### 1. Session 只翻译 tier

修改 `pkg/scheduler/framework/session.go`：

- 更新 `adjustNetworkTopologySpec` 注释；
- 保留 Job/SubJob 的 `translateHighestTierNameToAllowed`；
- 删除 `convertSoftToHardTopology` 调用和函数；
- 不读取 cluster-top tier 来改写 mode；
- 翻译后仍保留用户配置的 soft/hard mode。

删除不再使用的 `SubJobInfo.ConvertToHardTopology` 及其转换专用测试。

### 2. 显式表示 Constrain 与 Abstain

在 scheduler 内部 HyperNode gradient contract 中增加结果状态，名称可按实现时的
项目风格微调：

```go
type HyperNodeGradientResult struct {
    Applied   bool
    Gradients [][]*HyperNodeInfo
}
```

语义：

- `Applied=true`：plugin 声明了 hard/required 约束，结果参与 hard 交集；
- `Applied=true, Gradients=empty`：hard 约束已证明无候选；
- `Applied=false`：soft/preferred 或规则不适用，不参与 hard 候选交集。

framework 行为：

1. 只对 `Applied=true` 的结果求交；
2. 至少一个 hard plugin applied 时，候选全集严格来自 hard 交集；
3. 所有 plugin 都 abstain 时，从调用方 root 构造确定性的完整 HyperNode gradient，
   按 tier 从低到高排列；
4. 不再用 soft plugin 的返回集合缩小 hard 候选；
5. hard plugin 的空结果不能被“所有 abstain 的 fallback”覆盖。

这只分离候选过滤，不引入完整 solution scoring 或两阶段搜索。

### 3. Network-topology-aware 的模式处理

Job/SubJob hard：

- 返回 `Applied=true`；
- 继续调用现有 `hyperNodeGradientFn`；
- allocate 使用从低 tier 到高 tier 的现有顺序；
- evict 保留 reverse/cap 行为。

Job/SubJob soft 或未配置 network topology：

- 返回 `Applied=false`；
- 不过滤其他 hard plugin 的候选；
- 当没有任何 hard plugin 时，由 framework 的完整确定性 gradient 保持现有
  first-feasible compact search。

### 4. Group-topology-affinity 的模式处理

- required PodGroup/SubGroup affinity/anti-affinity 存在时：
  `Applied=true`，继续提供 hard gradient；
- 只有 preferred term 或完全不适用时：
  `Applied=false`，继续通过现有 `HyperNodeOrderFn` 评分当前候选；
- job/subJob 缺失等真正错误场景保守返回 applied empty，避免误放宽 hard 规则。

012 不改变 group topology 的 scoring comparator 和已知跨 SubJob 贪心边界。

### 5. 纯 soft Job 进入 HyperNode allocate

修改 `JobInfo.RequiresHyperNodeAllocate()`：

- 将 `ContainsNetworkTopology()` 纳入判断；
- hard、soft、Job、SubJob network topology 都进入 HyperNode allocate；
- 完全没有 topology 配置的普通 Job 仍走原路径。

### 6. 保留 Bug 008 的贪心选择

不修改以下基本结构：

- `allocateForJob` 按 gradient 从低到高访问；
- 一个 gradient 有成功 solution 后不访问更宽 gradient；
- `allocateForSubJob` 在第一个有成功 dry-run 的 gradient 中选择；
- `preferJobSoftTopologyCandidates` 在已有 peer/Job anchor 时优先 preferred 域；
- `preferJobSoftTopologyScoreCandidates` 在跨 allocate 调用时保持 Job anchor；
- preferred 集合为空时返回完整候选，保持 fallback。

实现时将 helper 和测试统一读取 scheduler 内部已翻译、但仍为 soft 的
`job.NetworkTopology`，避免混用原始 PodGroup spec。

### 7. 与 Bug 009 / 473c6538b 的关系

本分支不包含 009 的 multi-root domain 实现。

兼容规则固定为：

- soft preference 必须读取最终实际 Job/SubJob placement；
- 未来 candidate domain 的合成 key 不得参与 LCA；
- soft 不得把 `{B,C}` 精确 hard candidate domain 扩成共同父级完整子树；
- 012 不尝试解决 `{B,C}` 中多个 SubJob placement 的组合回溯。

## 预计源码范围

预计修改：

- `pkg/scheduler/api/types.go`
- `pkg/scheduler/api/unschedule_info.go`
- `pkg/scheduler/api/job_info.go`
- `pkg/scheduler/api/sub_job_info.go`
- `pkg/scheduler/framework/session.go`
- `pkg/scheduler/framework/session_plugins.go`
- `pkg/scheduler/plugins/network-topology-aware/`
- `pkg/scheduler/plugins/group-topology-affinity/`
- `pkg/scheduler/actions/allocate/` 的 focused tests/fixture

原则上不修改：

- staging API、CRD、generated code；
- controller、webhook；
- Node predicate/NodeOrder；
- allocate 的 Job/SubJob 全局搜索结构；
- recorder 数据结构；
- preempt/reclaim/evict action 主流程。

如果实现要求修改上述非目标范围，先更新本文件并重新获得批准。

## 验证用例

以下用例在用户批准后成为完成门禁。

### U1：Session 保留模式并翻译 tier

- 目的：证明删除转换后内部对象仍可使用数字 tier，且 mode 不被改写。
- 范围：`pkg/scheduler/framework` 单测。
- 设置：
  - Job soft + `highestTierName`；
  - SubJob soft + `highestTierName`；
  - Job hard + `highestTierAllowed`；
  - 无效 tier name；
  - 无 cluster-top HyperNode。
- 命令：

  ```bash
  go test ./pkg/scheduler/framework -run 'TestSession_adjustNetworkTopologySpec' -count=1
  ```

- 预期：
  - 有效 name 被翻译为 allowed tier；
  - Job/SubJob soft 仍为 soft；
  - hard 仍为 hard；
  - 无效 name 不引发 mode 转换；
  - 不依赖 cluster-top 才能完成翻译。
- 通过标准：上述断言全部通过。

### U2：Gradient Constrain/Abstain

- 目的：证明 soft 不缩小 hard 可行集合，hard empty 不被错误放宽。
- 范围：`pkg/scheduler/framework` 单测。
- 设置：
  1. 所有 plugin abstain；
  2. 一个 hard plugin + 一个 abstain plugin；
  3. 两个 hard plugin 求交；
  4. hard plugin 返回 empty；
  5. Job/SubJob 两套 callback；
  6. allocate/evict purpose 转发。
- 命令：

  ```bash
  go test ./pkg/scheduler/framework -run 'Test.*HyperNodeGradient' -count=1
  ```

- 预期：
  - all-abstain 返回 root 下完整、稳定、低 tier 优先的 gradient；
  - abstain 不改变 hard 结果；
  - hard empty 仍为空；
  - purpose 不丢失。
- 通过标准：候选成员和顺序均符合预期。

### U3：NTA/GTA hard-soft 矩阵

- 目的：证明两个 topology plugin 对 hard 和 soft 使用正确 contract。
- 范围：plugin 单测。
- 设置：
  - NTA Job/SubJob hard、soft、无 topology；
  - GTA required、preferred-only、无规则；
  - soft NTA + required GTA；
  - hard NTA + preferred GTA。
- 命令：

  ```bash
  go test ./pkg/scheduler/plugins/network-topology-aware ./pkg/scheduler/plugins/group-topology-affinity -count=1
  ```

- 预期：
  - 只有 hard/required applied；
  - soft/preferred abstain；
  - NTA hard eviction gradient 顺序和上限不变；
  - GTA preferred scoring 仍可执行。
- 通过标准：矩阵全部通过，现有 plugin tests 无回归。

### U4：纯 soft 进入 HyperNode allocate

- 目的：覆盖原转换移除后的入口缺口。
- 范围：`pkg/scheduler/api` 与 allocate action 单测。
- 设置：
  1. 只有 Job soft、没有 SubGroupPolicy；
  2. Job 无 topology、只有 SubJob soft；
  3. Job/SubJob 都无 topology。
- 命令：

  ```bash
  go test ./pkg/scheduler/api ./pkg/scheduler/actions/allocate -run 'Test.*Soft.*HyperNode|Test.*RequiresHyperNodeAllocate' -count=1
  ```

- 预期：
  - 前两种进入 HyperNode path；
  - 无 topology 的普通 Job 不因本修复进入；
  - pure soft 可从低 tier 到高 tier fallback。
- 通过标准：入口和候选梯度断言全部通过。

### U5：Bug 008 preferred 与跨 SubJob/外层候选

- 目的：证明转换不再让 `cc84f7065`、`a31ab3dd3` 失效。
- 范围：allocate action 单测。
- 命令：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run '^(TestAllocateJobSoftAffinityAcrossSubJobs|TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates|TestPreferJobSoftTopologyCandidates|TestPreferJobSoftTopologyScoreCandidates)$' \
    -count=1
  ```

- 预期：
  - preferred 域有可行候选时 Job LCA 不超过配置 tier；
  - 跨不同 `allocateForJob` 调用仍保持已有 anchor；
  - helper 使用内部 soft topology；
  - hard/no-anchor/no-tier 情况不误过滤。
- 通过标准：四组测试全部通过。

### U6：Soft fallback 与 mixed mode

- 目的：证明 soft 扩大范围只影响 preference，hard 边界不被放宽。
- 范围：allocate action 单测。
- 设置：
  1. Job soft preferred 域资源不足，远端域唯一可行；
  2. SubJob soft preferred leaf 不可行，同一 Job hard 域内 fallback；
  3. Job hard + SubJob soft；
  4. Job soft + SubJob hard；
  5. hard 不可满足时不能通过 soft fallback 越界。
- 命令：

  ```bash
  go test ./pkg/scheduler/actions/allocate -run 'Test.*Soft.*Fallback|Test.*Mixed.*Topology' -count=1
  ```

- 预期：
  - soft 场景能够远端 fallback；
  - hard Job/SubJob tier 始终严格成立；
  - 仍返回第一个成功 gradient，不比较后续全局最优布局。
- 通过标准：五类断言全部通过。

### U7：相关包回归门禁

- 目的：覆盖 callback 签名和 scheduler 主路径的编译/行为回归。
- 范围：相关 package。
- 命令：

  ```bash
  go test ./pkg/scheduler/api ./pkg/scheduler/framework ./pkg/scheduler/actions/allocate \
    ./pkg/scheduler/plugins/network-topology-aware ./pkg/scheduler/plugins/group-topology-affinity \
    -count=1
  ```

- 预期：全部通过。
- 通过标准：命令退出码为 0。

### B1：格式、构建和基础验证

- 目的：确保生产 scheduler 可构建。
- 命令：

  ```bash
  ./hack/update-gofmt.sh
  make vc-scheduler
  make verify
  ```

- 预期：格式无差异，scheduler 构建成功，verify 通过。
- 通过标准：命令退出码均为 0。

### K1：Kind preferred 与 fallback 端到端

- 目的：在真实 scheduler、cache、HyperNode CRD 和 Pod binding 路径中证明移除转换
  后的 preferred 与 fallback。
- 集群：
  - 名称：`volcano-dev-012`；
  - 1 control-plane + 4 workers，共 5 个 Kubernetes Node；
  - tier1：每个 worker 一个 HyperNode；
  - tier2：worker1/2 和 worker3/4 各组成一个 pair；
  - tier3：两个 pair 的共同父级。
- 镜像：
  - tag：`dev-012`；
  - 至少构建并加载本地修改后的 `volcanosh/vc-scheduler:dev-012`；
  - 所有本地 Volcano 镜像使用 `IfNotPresent`，不推送远端。
- 基础命令：

  ```bash
  kind create cluster --name volcano-dev-012 \
    --config ../issues/bugs/012-soft-topology-conversion-narrows-search-IP/kind-config.yaml
  TAG=dev-012 BUILDX_OUTPUT_TYPE=docker make images
  kind load docker-image volcanosh/vc-scheduler:dev-012 --name volcano-dev-012
  helm upgrade --install volcano installer/helm/chart/volcano \
    --namespace volcano-system --create-namespace \
    --set basic.image_tag_version=dev-012 \
    --set basic.image_pull_policy=IfNotPresent
  kubectl get nodes -o wide
  kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.nodeInfo.architecture}{"\n"}{end}'
  ```

- Workload K1.1：preferred
  - Job soft tier2；
  - 两个 SubJob soft tier1；
  - 四个 Pod 均有非零 CPU/memory request；
  - preferred pair 有足够资源。
- 预期：
  - 四 Pod 全部 Running；
  - 每个 SubJob 尽量位于一个 tier1；
  - Job 四 Pod 的实际 LCA 不超过同一个 tier2 pair；
  - scheduler 日志中内部 Job/SubJob mode 为 soft，不出现 converting soft to hard。
- Workload K1.2：fallback
  - 先占满/限制 preferred tier2 pair 的可用资源；
  - 创建同样的 soft Job/SubJob；
  - 远端 tier2 pair 保持可用。
- 预期：
  - 四 Pod 最终全部 Running；
  - scheduler 不因 soft tier 把远端候选过滤；
  - 实际 Job LCA 可以超过 preferred tier，但没有违反任何 hard rule；
  - 日志能看到 preferred 无可行候选后继续访问 fallback gradient。
- PodGroup/Deployment 顺序：
  - 先创建 PodGroup；
  - 确认 PodGroup 存在后再创建 Deployment；
  - 不把两个阶段批量并发 apply。
- 证据：
  - `kubectl get nodes -o wide`；
  - `kubectl get hypernodes -o yaml`；
  - `kubectl get pods -o wide --show-labels`；
  - PodGroup YAML/status；
  - scheduler 日志中 gradient、candidate、mode、最终 placement；
  - 根据 Pod node -> tier1 -> tier2 映射计算实际 LCA；
  - 镜像 ID 和 Helm values。
- 清理：

  ```bash
  kind delete cluster --name volcano-dev-012
  ```

- 通过标准：
  - preferred 和 fallback workload 均通过；
  - 集群总 Node 数不超过 5；
  - 使用本地构建 scheduler 镜像；
  - 完整记录实际状态、日志和清理结果。

## 实施与提交计划

用户批准后：

1. 先增加/调整 U1-U6，使其在旧实现上能够暴露 mode conversion 或 fallback 问题；
2. 实现最小源码修改；
3. 执行 U1-U7、B1；
4. 用 `apply_patch` 增加 012 专用 Kind 配置和 workload；
5. 构建本地镜像并执行 K1；
6. 将所有结果记录到本文件；
7. 检查工作树，只提交 012 范围内的源码、测试、Kind asset 和 issue 记录；
8. 将任务目录改为 `DONE`。

任何预先批准的验证用例若需修改，先在本文件记录原因并重新获得用户批准，不能以更弱
用例替代。

## 2026-07-28：实施与验证结果

### 实现摘要

- 删除 `convertSoftToHardTopology` 和
  `SubJobInfo.ConvertToHardTopology`；Session 只翻译 tier name，Job/SubJob 的
  soft/hard mode 保持不变。
- 新增 scheduler 内部
  `HyperNodeGradientResult{Applied, Gradients}`：
  hard/required 规则返回 constrain，soft/preferred/不适用规则返回 abstain。
- framework 只对 applied hard gradient 求交；全部 abstain 时从调用 root 构造
  完整、稳定、低 tier 优先的候选梯度。
- 保留第一个 hard plugin 的 gradient 层级和候选顺序，后续 hard plugin 只过滤成员，
  避免破坏 allocate 的 ascending 和 eviction 的 reverse/cap 顺序。
- `network-topology-aware` 只对 hard network topology 提供 hard gradient；
  `group-topology-affinity` 只对 required term 提供 hard gradient，preferred-only
  继续由 HyperNodeOrder 评分。
- `RequiresHyperNodeAllocate()` 覆盖 Job/SubJob soft network topology；没有 topology
  的普通 Job 不受影响。
- Bug 008 的两个 Job soft helper 改为读取已翻译但仍保持 soft 的内部
  `job.NetworkTopology`。
- 保持现有逐 gradient、first-feasible 贪心流程，没有引入
  `c41125816` 的全局 layout 枚举或评分。

### U1-U7：单元与相关包回归

最终聚焦命令：

```bash
go test ./pkg/scheduler/framework \
  -run '^(TestAdjustNetworkTopologySpec_.*|TestHyperNodeGradient.*|TestIntersectHyperNodeGradients.*)$' \
  -count=1

go test ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity -count=1

go test ./pkg/scheduler/api \
  -run '^TestRequiresHyperNodeAllocateIncludesSoftNetworkTopology$' -count=1

go test ./pkg/scheduler/actions/allocate \
  -run '^(TestAllocateJobSoftAffinityAcrossSubJobs|TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates|TestPreferJobSoftTopologyCandidates|TestPreferJobSoftTopologyScoreCandidates|TestAllocateWithPartitionPolicyNetworkTopology)$' \
  -count=1
```

结果：全部通过。`TestAllocateWithPartitionPolicyNetworkTopology` 的既有表覆盖
Job hard + SubJob soft、Job soft + SubJob hard、hard 越界不可调度等 mixed-mode
矩阵；soft preferred 资源不足后的跨 tier2 fallback 由 K1.2 的真实 Kind 路径覆盖。

完整相关包命令也通过：

```bash
go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/actions/allocate ./pkg/scheduler/actions/utils \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity -count=1
```

其中 allocate 包耗时约 42 秒，其余相关包均为退出码 0。

### B1：格式、构建和 verify

已执行：

```bash
./hack/update-gofmt.sh
make vc-scheduler
GOPATH=/Users/vanderchen/go make verify
git diff --check
```

结果：全部通过。

- `make verify` 发现基线中
  `pkg/scheduler/api/hyper_node_info_test.go` 和
  `pkg/scheduler/api/topology_affinity_info_test.go` 不符合当前 gofmt；只保留了字段对齐
  的纯格式修正。
- codegen verify 会在本仓库根目录留下临时 `volcano.sh/` 并把
  `k8s.io/code-generator` 临时改为 `v0.36.3`。验证完成后已把 `go.mod` 精确恢复为
  `v0.35.3`，生成目录移至 `/private/tmp`；没有 API/generated 变更进入提交。

`make unit-test` 也已执行。所有 scheduler（包括本次相关包）、controller 和 webhook
路径通过；完整目标仍被 release-1.15 在 macOS/ARM64 的既有环境问题阻断：

- Linux-only agent/cgroup build constraints；
- benchmark `TestMain` 缺少 kubeconfig；
- network-qos eBPF 平台测试；
- `third_party/mindcluster` configmap 既有测试失败。

这些失败不经过本次修改路径，因此不能宣称完整 `make unit-test` 通过。

### 本地 Kind 镜像

标准命令：

```bash
TAG=dev-012 BUILDX_OUTPUT_TYPE=docker make images
```

在读取 `golang:1.25.0` 的 Docker Hub OAuth token 时超时，源码编译未失败。随后使用
宿主 Go 交叉编译当前分支的 Linux/ARM64 静态二进制：

```bash
GOOS=linux GOARCH=arm64 TAG=dev-012 \
  make -j3 vc-scheduler vc-controller-manager vc-webhook-manager
```

以本机已有的 release-1.15 ARM64 Volcano 镜像作为运行时层，只替换当前分支二进制，
得到：

| Image | SHA256 |
| --- | --- |
| `volcanosh/vc-scheduler:dev-012` | `e34f141ded5b62392b77596565f80ac9eb8cbe64902ed5cab0f6565705e73da7` |
| `volcanosh/vc-controller-manager:dev-012` | `96616fb3dd73827676c40f4ea66de72ab4b49e64475ecdc664ffd99f6340c079` |
| `volcanosh/vc-webhook-manager:dev-012` | `e6cf4b539868be448f2a71fe7e8ff877cbd586db7483a45a9a96550c8c1b7044` |

三个镜像均为 ARM64，只加载到本地 Kind，没有推送远端。`nginx:1.27-alpine`
也从宿主本地镜像加载到每个 Kind 节点。

### K1：真实 Kind preferred 与 fallback

#### 集群与安装

- 集群：`volcano-dev-012`。
- Kubernetes：v1.36.1 / ARM64。
- 节点：1 control-plane + 4 worker，共 5 个可见 Kubernetes Node，未超过 8 节点限制。
- 每个节点 allocatable CPU 为 12。
- tier1：`hn-a/b/c/d`，各包含一个 worker，`nodeCount=1`。
- tier2：`instance-group-ab/cd`，各包含两个 tier1，`nodeCount=2`。
- tier3：`hypercluster-all`，包含两个 tier2，`nodeCount=2`。
- Helm 使用 `dev-012`、`IfNotPresent`，scheduler 同时启用 NTA 和 GTA。
- scheduler、controller-manager、admission 全部 Running。

#### K1.1：资源充足时 preferred

先创建 PodGroup，确认存在后再创建两个 Deployment。Job 为 soft/tier2，两个 SubJob
为 soft/tier1；每个 SubJob 两个 Pod，每个 Pod 请求 `500m CPU / 200Mi memory`。

最终：

| Role | Pod 数 | Node | HyperNode |
| --- | ---: | --- | --- |
| `role-a` | 2 | `volcano-dev-012-worker` | `hn-a` |
| `role-b` | 2 | `volcano-dev-012-worker` | `hn-a` |

- 四个 Pod 全部 Running；
- PodGroup 为 Running，`running=4`；
- PodGroup 注解 `volcano.sh/job-allocated-hypernode=hn-a`；
- Job 和两个 SubJob 的实际 LCA 都是 tier1 `hn-a`，满足“不超过同一 tier2”的
  preferred 条件。

结论：K1.1 PASS。

#### K1.2：首选 pair 资源不足时 fallback

删除 K1.1 工作负载后，在 A/B 各直接绑定一个 Running filler Pod，每个请求
`11500m CPU / 50Mi memory`。结合每个 worker 既有 100m request，A/B 各只剩约
400m，不能容纳任一 500m workload Pod；C/D 保持可用。随后仍按 PodGroup-first
顺序创建完全相同的 soft workload。

最终：

| Role | Pod 数 | Node | HyperNode |
| --- | ---: | --- | --- |
| `role-a` | 2 | `volcano-dev-012-worker3` | `hn-c` |
| `role-b` | 2 | `volcano-dev-012-worker3` | `hn-c` |

- 四个 Pod 全部 Running；
- PodGroup 为 Running，`running=4`，API spec 仍为 `mode=soft`；
- PodGroup 注解 `volcano.sh/job-allocated-hypernode=hn-c`；
- scheduler 日志先尝试 `hn-a`、`hn-b`，两者因 SubJob minResource 无候选而失败，
  随后继续尝试 `hn-c` 并成功；
- Job screening 日志为 `8/8 hyperNodes available`，证明 soft 没有在 gradient
  阶段缩小 fallback 搜索范围；
- scheduler 全量日志中没有
  `convertSoftToHardTopology`、`ConvertToHardTopology` 或 converting-soft 记录。

关键日志：

```text
HyperNode screening for job ... job=default/pg-soft-native ... 8/8 hyperNodes available
Try to allocate resource for job in hyperNode ... hyperNode=hn-a ... tierLayer=0
Try to allocate resource for job in hyperNode fail ... hyperNode=hn-a
Try to allocate resource for job in hyperNode ... hyperNode=hn-b ... tierLayer=0
Try to allocate resource for job in hyperNode fail ... hyperNode=hn-b
Try to allocate resource for job in hyperNode ... hyperNode=hn-c ... tierLayer=0
Allocate job to hyperNode success ... job=default/pg-soft-native, hyperNode=hn-c
update allocated hyperNode for subJob ... role-a-a0 ... new=hn-c
update allocated hyperNode for subJob ... role-b-b0 ... new=hn-c
```

结论：K1.2 PASS。

#### 清理

已执行：

```bash
kind delete cluster --name volcano-dev-012
kind get clusters
```

五个任务节点全部删除；原有 `kind` 集群仍存在且未受影响。

### 最终提交

Volcano 源码提交：

1. `a0e218fd0 fix(scheduler): preserve native soft topology search`
   - 20 个源码/测试文件；
   - 删除粗暴转换，增加 Constrain/Abstain contract，保留贪心 soft preference 和
     fallback。
2. `e66f82a82 test(scheduler): apply gofmt to topology fixtures`
   - 2 个既有 topology 测试文件；
   - 仅包含 `make verify` 要求的 gofmt 字段对齐，无行为修改。

最终分支：
`fix/012-remove-soft-to-hard-topology-conversion@e66f82a82`。

Volcano 工作树已清洁，任务目录已从 `IP` 改为 `DONE`。
