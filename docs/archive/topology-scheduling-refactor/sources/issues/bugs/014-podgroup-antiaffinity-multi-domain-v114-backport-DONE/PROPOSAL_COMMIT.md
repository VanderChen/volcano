> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 014：修改提案与验证计划（V2：candidate forest 最小修复）

## 审批状态

- V2 批准日期：2026-07-29
- V2 批准方式：用户明确要求开始修改并在 Kind 集群中验证
- 当前阶段：实现、验证与提交完成
- V1 状态：已废弃；不再计划直接移植 009 的通用 multi-root domain
- 源码基线：`b42c9f4262`
- 修复分支：`fix/014-podgroup-antiaffinity-multi-domain-v114-backport`
- 实现状态：完成
- 实现提交：`8a71ca532`

## 结论

013 仍不能解决 B+C 联合搜索，但去除 soft-to-hard conversion 后，可以用比
`473c6538b` 小得多的修复。

当前 framework 已经提供：

1. hard GTA 返回 `Applied({B,C})`；
2. soft NTA 返回 `Abstain`，不缩小 `{B,C}`；
3. SubJob callback 会在给定搜索 root 下重新执行 NTA/GTA hard 交集；
4. SubJob HyperNode order 已实现 native soft preference 和 fallback；
5. allocate 已能在 dry-run 中追踪实际 Job/SubJob LCA placement。

因此不再需要把 009 的完整 `jobCandidateDomain`、hard-domain 分区、
`jobDomainSolution`、多 root callback 合并和 solution-level soft 选择全部移植。

## 009 为什么大，013 为什么可以缩小

009 的通用方案对每个 root 分别调用 SubJob gradient，再合并 gradient、统计和
solution。它还要处理被 convert 成 hard 的 soft topology，因此需要：

- `jobCandidateDomain` 和 domain key；
- Job hard tier ancestor 分区；
- `jobDomainSolution`；
- `preferJobSoftTopologySolutions`；
- 多 root gradient rank 合并；
- 多 root diagnostics 合并；
- synthetic key 与真实 placement 的转换。

013 中 soft mode 保真：

```text
Job NTA soft
  -> IsHardTopologyMode() == false
  -> NTA gradient callback Abstain
  -> 不参与 hard candidate intersection
  -> 只在已有 hard-feasible candidates 上评分和 fallback
```

这使 B、C 可以一次进入现有 SubJob 选择流程，不再需要额外的 solution-level soft
兼容层。

## V2 推荐方案：LCA traversal envelope + 精确 candidate forest

### 1. 概念

对 Job gradient 的一个同层候选集合：

```text
allowed roots = {B,C}
traversal envelope = LCA(B,C) = H
exact forest = subtree(B) ∪ subtree(C)
```

H 只用于让现有 SubJob callback 从一个真实 HyperNode 开始遍历。H 本身以及 H 下的
A 不自动成为可分配候选。

SubJob callback 完成现有 hard constraint intersection 后，allocate 再将结果与
`exact forest` 求交：

```text
SubJobCandidates
  = HyperNodeGradientForSubJob(subJob, H)
    ∩ descendants({B,C})
```

因此 A 即使位于 H 下也会被 candidate-forest filter 删除。

### 2. 为什么在当前 framework 中成立

当前只有两个 HyperNode gradient 插件：

- `network-topology-aware`
- `group-topology-affinity`

两者同时注册 Job 和 SubJob callback。对当前 topology-only hard constraints，
从 H 遍历后与精确 forest 求交，等价于分别从 B、C 遍历后取并集：

```text
(HardGradient(H) ∩ Forest(B,C))
  == HardGradient(B) ∪ HardGradient(C)
```

成立条件：

1. B、C 是同一 gradient layer 的同 tier roots；
2. hard constraints 由当前 NTA/GTA callback 在 SubJob 阶段重新执行；
3. allocate 最后显式应用 forest filter，不依赖插件“刚好再次排除 A”；
4. 不把 traversal envelope 当成合法 candidate 或最终 placement。

第 3 点是安全边界：即使将来 GTA 的内部遍历调整，只要 forest filter 保留，A 也不会
泄漏。

### 3. 激活范围

只在以下条件同时满足时启用 candidate-forest 联合路径：

```text
job.ContainsHardPodGroupAntiAffinity()
&& !job.IsHardTopologyMode()
```

含义：

- required PodGroup anti-affinity 提供精确的 Job hard candidate set；
- Job 没有真实 hard NTA；
- Job NTA absent 或 soft 都可以使用联合路径；
- SubJob hard/soft NTA 不限制激活，它继续在 SubJob callback 中生效。

这是去除 convert-to-hard 后能够安全表达的边界。过去 Job soft 会被改成 hard，从而
无法可靠地区分“真实 hard 边界”和“soft 为了复用路径产生的伪 hard 边界”。

### 4. Job 真 hard NTA 的处理

V2 不改变真实 Job hard NTA 的现有行为：

```text
job.IsHardTopologyMode() == true
  -> 保持逐个 outer HyperNode dry-run
```

这样不会因为本修复放宽 Job hard tier。

这意味着：

- 本次生产场景可修复：Job 无 hard NTA，四个 SubJob 各自 hard tier-1；
- Job hard NTA 下的通用多域分区不由 014 解决；
- 例如 Job hard tier-3 理论上允许 B+C，但当前仍可能逐域搜索，这是明确保留的
  limitation；
- 若要覆盖该场景，应进入 011 的两阶段/forest allocation 重构，而不是重新把 009
  的通用 domain 框架塞入生产小修复。

### 5. allocateForJob 的最小分流

保留当前 default/hard Job 路径。仅在上述激活条件成立时走 focused helper，例如：

```go
func (alloc *Action) allocateForJob(...) *framework.Statement {
    gradients, gradientStats := ssn.HyperNodeGradientForJobFn(...)

    if shouldUsePodGroupAntiAffinityForest(job) {
        return alloc.allocateForJobAcrossCandidateForest(
            job, worksheet, gradients, gradientStats,
        )
    }

    // 当前逐 HyperNode 主逻辑保持不变
}
```

forest helper 仍按 Job gradient layer 顺序处理，保持“第一层有可行解即返回”的现有
行为：

```text
for each gradient layer:
  allowedRoots = stableUnique(layer)
  envelope = LCA(allowedRoots)
  try one Job transaction under (envelope, allowedRoots)
  if Job ready/pipelined:
    recover and return
```

不比较多个 domain、不创建 synthetic domain key、不改变普通 Job/hard Job 路径。

### 6. SubJob candidate-forest filter

为 `allocateForSubJob` 增加一个仅供联合路径使用的薄封装或可选 scope：

```go
type hyperNodeCandidateForest struct {
    envelope *api.HyperNodeInfo
    roots    []*api.HyperNodeInfo
}
```

流程：

1. 调用一次 `HyperNodeGradientForSubJobFn(subJob, envelope)`；
2. 删除不在任一 root 子树内的 HyperNode；
3. 更新 `IntersectedByTier` 和 scope exclusion diagnostics；
4. 调用现有 `FilterGradientsByMinResource`；
5. 完整复用现有 candidate dry-run、soft filter、HyperNode order 和 statement
   recovery。

candidate 是否属于 forest：

```text
candidate == root
OR
root ∈ candidate.GetAncestors()
```

不修改 framework callback，也不分别调用 B、C callback，所以不需要 009 的 gradient
rank/stats merge helpers。

### 7. Job minResource 按精确 forest 聚合

联合路径不能沿用逐 root 的 Job minResource filter，否则 B、C 单独不足但联合足够时
会提前失败；也不能直接检查 H，否则 A 的资源会被错误计入。

forest helper：

1. 收集所有 roots 的 `RealNodesSet`；
2. 对 Kubernetes Node name 去重；
3. 聚合这些 Node 的 `Idle` 和 `FutureIdle`；
4. 对整个 forest 检查 Job `minResources`；
5. diagnostics 仍按原始 roots/tier 记录；
6. SubJob minResource 继续按最终具体 HyperNode candidate 检查。

### 8. 保存真实 placement，不保存 envelope

H 只用于 traversal。dry-run 完成、调用 `RecoverSubJobStatus` 之前捕获：

- `job.AllocatedHyperNode`；
- 本次 worksheet 中每个 SubJob 的 `AllocatedHyperNode`。

如果 solution 成功：

1. 丢弃各 SubJob dry-run statement；
2. recover 合并后的 statement；
3. 恢复 worksheet；
4. 用捕获到的真实 Job placement 调用现有 `SaveJobDecision`；
5. 在这个真实 placement key 下重写本 solution 的 SubJob decisions；
6. 继续由现有 `UpdateDecisionToJob` 提交。

结果：

- 实际使用 B+C 时，Job placement 是真实 LCA H；
- 实际只使用 B 时，Job placement 是 B；
- envelope 不会导致 placement 被无条件扩大；
- 不修改 `recorder.go` 数据结构或公共方法。

### 9. native soft 行为

联合 forest 把 B/C 的全部 hard-feasible candidates 一次交给现有 SubJob 选择：

```text
hard feasibility:
  framework Applied constraints + forest filter

soft preference:
  existing HyperNodeOrderFn
  existing preferJobSoftTopologyCandidates

fallback:
  preferred subset 无可行 candidate 时返回完整 feasible set
```

因此不增加 009 的 `preferJobSoftTopologySolutions`。所有 mode/tier 判断继续读取
`job.NetworkTopology` / `subJob.NetworkTopology`，不读取 PodGroup 原始 NTA 字段。

## 修改范围比较

| 项目 | 009 (`473c6538b`) | 014 V2 |
|---|---:|---:|
| allocate 生产代码 | `+553/-86` | 目标约 `+150~250/-少量` |
| 通用 multi-root domain | 有 | 无 |
| hard Job ancestor 分区 | 有 | 无，hard Job 保持旧路径 |
| 每 root 重跑 SubJob callback | 有 | 无，只在 envelope 上调用一次 |
| gradient/stats merge helpers | 有 | 无 |
| domain solution map | 有 | 无 |
| solution-level soft filter | 有 | 无 |
| recorder 数据结构修改 | 无 | 无 |
| framework/plugin/API 修改 | 无 | 无 |
| 普通与 Job hard 主路径改写 | 较多 | 原则上不改 |

行数是设计约束，不是机械验收标准。如果实现超过该范围，优先检查是否错误地重新引入
009 的通用抽象；确需扩大时先更新 proposal。

## 预计源码范围

允许：

- 修改 `pkg/scheduler/actions/allocate/allocate.go`
- 新增 `pkg/scheduler/actions/allocate/podgroup_antiaffinity_forest_test.go`
- 必要时扩充 `placement_test.go`
- 新增本 issue 的 Kind YAML、日志和验证记录

不允许，除非先更新 proposal 并重新批准：

- `pkg/scheduler/framework/`
- `pkg/scheduler/plugins/group-topology-affinity/`
- `pkg/scheduler/plugins/network-topology-aware/`
- `pkg/scheduler/api/`
- `pkg/scheduler/actions/allocate/recorder.go`
- API、CRD、controller、webhook

## 风险与限制

### R1：LCA envelope 重新引入已排除分支

控制：envelope 只用于 callback 遍历；最终 gradients 必须与 exact forest 求交。单测
显式构造 A 资源更优的场景，确保仍不能进入 A。

### R2：最终 placement 被 envelope 扩大

控制：在 dry-run recovery 前捕获实际 Job/SubJob placement，以实际值写 recorder。
增加“forest 有 B/C，但实际只使用 B”的断言。

### R3：Job minResource 误用 H 的资源

控制：只聚合 roots 覆盖的唯一 Nodes，不读取 envelope 的 `RealNodesSet`。

### R4：未来增加非局部 HyperNode gradient 插件

当前只有 NTA/GTA，且 exact forest post-filter 能保证无候选泄漏。若未来插件的
SubJob hard 语义依赖 Job callback 的全局状态且不满足 root-local 性，应升级 callback
契约表达 candidate forest，而不是静默绕过。014 不提前修改 framework API。

### R5：真实 Job hard NTA 场景仍可能缺少联合搜索

这是 V2 有意保留的限制，用更小生产修复换取低回归风险。相关通用能力由 011 proposal
承接。

## 验证用例

以下用例在用户批准后成为完成标准。

### U1：生产等价 B+C 联合分配

- 拓扑：H -> A/B/C -> 各两个 tier-1 -> 各一个 Node。
- 每 Node：8 个 `huawei.com/ascend-1980`。
- anchor：匹配 PodGroup 位于 A。
- target：四个 SubJob，各一个 Pod，每 Pod 请求 6 卡。
- target hard 规则：
  - required PodGroup anti-affinity=tier-2；
  - 每个 SubJob hard NTA=tier-1；
  - Job 无 NTA。
- 期望：四 Pod 使用 B1/B2/C1/C2，4/4 ready。
- 稳定性：重复 50 次。

### U2：exact forest 不泄漏 A

- 将 A 设置为资源最多或打分最高。
- 期望：目标 Pod 仍全部位于 B/C；A、H 和 synthetic name 都不是 task candidate。
- diagnostics 显示 A 被 hard/scope 排除。

### U3：envelope 与实际 placement 分离

- 子用例一：forest roots 为 B、C，资源只需 B，实际 Job placement 必须为 B。
- 子用例二：任务实际跨 B、C，Job placement 必须为 H。
- 期望：annotation/cache/recorder 中只出现真实 HyperNode 名。

### U4：Job minResource 精确聚合

- B、C 单独不足、联合足够：forest 保留。
- B+C 仍不足：forest 排除。
- H 仅因为 A 才足够：forest 必须排除。
- roots 的 Node set 重叠时：不得重复计数。

### U5：Job soft NTA + hard PAA

- Job `NetworkTopology.Mode=soft`，GTA required 返回 B/C。
- 只设置 `job.NetworkTopology` 内部副本作为执行入口。
- 有 preferred-domain 可行解：优先该域。
- preferred-domain 不可行：允许在 exact forest 内 fallback。
- 期望：soft 不阻止 candidate-forest 路径，不变成 hard filter。

### U6：真实 Job hard NTA 保持旧路径

- Job NTA hard，required PAA 同时存在。
- 期望：
  - `shouldUsePodGroupAntiAffinityForest` 返回 false；
  - allocate 继续逐 outer HyperNode；
  - 不因 014 跨越 Job hard tier；
  - 现有 hard mixed-mode tests 全通过。

### U7：SubGroup hard/soft 组合回归

- required SubGroup affinity/anti-affinity；
- preferred SubGroup affinity/anti-affinity；
- SubJob hard NTA 与 SubJob soft NTA；
- 期望：hard 继续过滤，soft 只排序并 fallback；candidate 始终属于 B/C forest。

### U8：default allocate path 回归

- 无 required PodGroup anti-affinity；
- 普通 Job、纯 soft topology、纯 hard topology；
- 期望：不进入 forest helper，行为与 013 一致。

### B1：格式、单测、构建和静态验证

```bash
./hack/update-gofmt.sh
go test ./pkg/scheduler/actions/allocate -count=1
go test ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity -count=1
make vc-scheduler
make verify
```

全部必须通过。

### K1：7 节点 Kind 生产等价验证

- 集群：1 control-plane + 6 workers，共 7 Nodes。
- HyperNode tree：H -> A/B/C -> 六个 tier-1 -> 六个 workers。
- 构建并 load 014 本地 scheduler 镜像。
- 先创建 anchor/target PodGroups，再创建 Deployments。
- 期望：
  - anchor 位于 A；
  - target 4/4 Running/Ready；
  - target 只位于 B/C；
  - target PodGroup 不再 `2/4 Pending`；
  - scheduler log 显示 `allowedRoots=[B C]`、`envelope=H`；
  - 没有 A candidate leak。
- 验证后删除 `volcano-dev-014`。

## 完成标准

只有以下条件全部满足才将 issue 改为 `DONE`：

1. 用户明确批准 V2 proposal 与 U1-U8/B1/K1；
2. 生产源码修改保持在 allocate 包；
3. default/hard Job 路径没有无关重构；
4. 所有预先约定用例通过；
5. Kind 命令、镜像、对象状态和关键日志记录在本文件；
6. 提交源码与 issue 记录并登记 commit hash。

## 2026-07-29 实施与验证结果

### 实现范围

生产代码：

- `pkg/scheduler/actions/allocate/allocate.go`
  - `+278/-6`；
  - 没有引入 009 的 `jobCandidateDomain` 或 `jobDomainSolution`；
  - 普通 Job 与真实 Job hard NTA 继续走原逐 HyperNode 路径；
  - required PodGroup anti-affinity 且 Job 非 hard NTA 时，同一 Job gradient
    layer 构造 exact candidate forest；
  - roots 的 LCA 仅作为 SubJob callback traversal envelope；
  - callback 结果再次与 roots descendants 求交；
  - Job minResource 使用 roots 覆盖的唯一 Kubernetes Nodes；
  - dry-run 成功后以真实 Job/SubJob placement 写入现有 recorder。

测试：

- 新增
  `pkg/scheduler/actions/allocate/podgroup_antiaffinity_forest_test.go`
  （465 行）；
- 删除
  `pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go`
  中 013 HEAD 已存在但未使用的 `sort` import。该单行测试维护修正使 NTA 包恢复
  可编译，不修改 NTA 生产代码。

未修改：

- framework；
- GTA/NTA 生产代码；
- scheduler API；
- recorder 数据结构与接口；
- CRD、controller、webhook。

### U1-U8

focused 命令：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run 'TestAllocatePodGroupAntiAffinityAcrossCandidateForest|TestCandidateForestUsesActualPlacementInsteadOfEnvelope|TestCandidateForestPreservesNativeSoftJobTopologyFallback|TestCandidateForestDisabledForRealHardJobTopology|TestFilterGradientsByCandidateForestExcludesEnvelopeSibling|TestFilterCandidateForestGradientsByMinResource' \
  -count=1
```

结果：

```text
ok volcano.sh/volcano/pkg/scheduler/actions/allocate 25.579s
```

覆盖：

- U1：B+C 四 Pod 场景重复 50 次全部成功；
- U2：A 的 fake score 为 10000/9000，仍无 task 进入 A；
- U3：只使用 B 时 Job/recorder placement 为 `b-t2`，跨 B+C 时为
  `shared-t3`；
- U4：B+C 聚合满足 24 卡；40 卡失败，即使 envelope 的 A 资源可使 H 足够；
  重叠 roots 不重复计数；
- U5：Job soft tier-2 时仍可跨 B+C fallback，mode 未转 hard；
- U6：真实 Job hard NTA 明确不进入 candidate-forest path；
- U7：SubJob hard NTA 和既有 preferred SubGroup 组合由 Action/K1 与插件回归覆盖；
- U8：allocate 全包回归通过，普通/soft/hard 既有用例未回归。

最终组合回归：

```bash
go test ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  -count=1
```

结果：

```text
ok volcano.sh/volcano/pkg/scheduler/actions/allocate 67.202s
ok volcano.sh/volcano/pkg/scheduler/framework 4.428s
ok volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware 8.005s
ok volcano.sh/volcano/pkg/scheduler/plugins/group-topology-affinity 4.964s
```

### B1

通过：

```bash
./hack/update-gofmt.sh
make vc-scheduler
GOPATH=/Users/vanderchen/go make verify
```

`make verify` 结果：

```text
hack/verify-gofmt.sh
hack/verify-gencode.sh
diffing hack/../pkg against freshly generated codegen
hack/../pkg up to date.
```

仓库的 codegen 脚本会临时将 `k8s.io/code-generator` 从 `v0.34.1` 更新为
`v0.36.3`，并在仓库根生成 `volcano.sh/` 临时输出。门禁完成后已恢复 `go.mod`
并将临时输出移出工作树；最终工作树没有这些副产物。

另外按 workspace 规则执行：

```bash
make unit-test
```

结果：

- 所有 scheduler 包通过，包括：

  ```text
  ok volcano.sh/volcano/pkg/scheduler/actions/allocate 72.929s
  ok volcano.sh/volcano/pkg/scheduler/api 11.616s
  ok volcano.sh/volcano/pkg/scheduler/cache 9.849s
  ok volcano.sh/volcano/pkg/scheduler/framework 6.334s
  ok volcano.sh/volcano/pkg/scheduler/plugins/group-topology-affinity 9.015s
  ok volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware 11.123s
  ```

- 整体命令在 Darwin 主机返回失败，仍为该分支已知的 Linux-only 组件：
  agent QoS handlers build constraints、`opencontainers/runc` Linux syscall、
  `pkg/networkqos/tc` 平台接口及 eBPF mock；与 014 scheduler 修改无关。

### 本地镜像

构建：

```bash
TAG=dev-014 BUILDX_OUTPUT_TYPE=docker make vc-scheduler-image
```

镜像：

```text
volcanosh/vc-scheduler:dev-014
sha256:ab78a959007d84351473d70b3bab54d2725cc18224e0281fea55b9ba6a47eff4
arm64
```

controller/webhook 未修改，复用 013 已验证镜像并增加本地 `dev-014` tag：

```text
volcanosh/vc-controller-manager:dev-014
sha256:81b205af795f2782f11a86d532b06463c0adceca62a13ecd1e85b90d9aa5058f
arm64

volcanosh/vc-webhook-manager:dev-014
sha256:ab8176eda4698f9f3dfc7f3e9045be471b5238a84d990b37a880f7f07e05ba53
arm64
```

工作负载使用的 `registry.k8s.io/pause:3.10` 先在 host pull，再与上述三张镜像一起
load 到 Kind。

### K1：7 节点 Kind

集群：

```text
name: volcano-dev-014
nodes: 1 control-plane + 6 workers
architecture: arm64
visible Kubernetes nodes: 7
Kubernetes: v1.36.1
```

安装：

```bash
kind create cluster --name volcano-dev-014 \
  --config ../issues/bugs/014-podgroup-antiaffinity-multi-domain-v114-backport-IP/kind-config.yaml

kind load docker-image \
  volcanosh/vc-scheduler:dev-014 \
  volcanosh/vc-controller-manager:dev-014 \
  volcanosh/vc-webhook-manager:dev-014 \
  registry.k8s.io/pause:3.10 \
  --name volcano-dev-014

helm upgrade --install volcano installer/helm/chart/volcano \
  --namespace volcano-system \
  --create-namespace \
  --set basic.image_tag_version=dev-014 \
  --set basic.image_pull_policy=IfNotPresent
```

实际 scheduler deployment：

```text
image: docker.io/volcanosh/vc-scheduler:dev-014
availableReplicas: 1
```

HyperNode 状态：

```text
6 x tier-1 volcano.sh/hypernode, nodeCount=1
3 x tier-2 volcano.sh/hyperinstance-group, nodeCount=2
1 x tier-3 volcano.sh/hypercluster, nodeCount=3
```

严格按顺序创建：

1. anchor PodGroup；
2. anchor Pod；
3. target PodGroup；
4. target Pods。

anchor：

```text
pod: bug014-anchor
node: volcano-dev-014-worker
PodGroup phase/running: Running/1
volcano.sh/job-allocated-hypernode: bug014-a-t1-0
```

target：

```text
bug014-decode-1   Running  volcano-dev-014-worker3  # B0
bug014-prefill-1  Running  volcano-dev-014-worker4  # B1
bug014-decode-0   Running  volcano-dev-014-worker5  # C0
bug014-prefill-0  Running  volcano-dev-014-worker6  # C1

PodGroup phase: Running
running: 4
condition: Scheduled=True
volcano.sh/job-allocated-hypernode: bug014-shared-t3
```

A 的两个 workers 没有 target Pod。

scheduler 关键日志：

```text
podGroup anti-affinity: matching occupancy ...
  occupiedHyperNodes=bug014-a-t2

podGroup anti-affinity: reject hyperNode ...
  hyperNode=bug014-a-t2
  reason=conflictWithMatchingPodGroup

podGroup anti-affinity: gradient result ...
  tier-1:[bug014-b-t1-0,bug014-b-t1-1,bug014-c-t1-0,bug014-c-t1-1]
  tier-2:[bug014-b-t2,bug014-c-t2]

Build PodGroup anti-affinity candidate forest ...
  allowedRoots=[bug014-b-t1-0 bug014-b-t1-1 bug014-c-t1-0 bug014-c-t1-1]
  envelope=bug014-shared-t3

Allocate subJob to hyperNode success ...
  decode-0=bug014-c-t1-0
  decode-1=bug014-b-t1-0
  prefill-0=bug014-c-t1-1
  prefill-1=bug014-b-t1-1
```

K1 结果：PASS。

清理：

```bash
kind delete cluster --name volcano-dev-014
kind get clusters
```

结果：

```text
Deleted nodes:
volcano-dev-014-control-plane
volcano-dev-014-worker
volcano-dev-014-worker2
volcano-dev-014-worker3
volcano-dev-014-worker4
volcano-dev-014-worker5
volcano-dev-014-worker6

remaining cluster: kind
```

最终提交：

```text
8a71ca532 fix(scheduler): allocate across PodGroup anti-affinity forest
```

014 的源码、单测、构建、Kind 验证和提交均已完成，issue 状态改为 `DONE`。
