> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 022：Candidate Forest 内存线性化提案与验证计划

## 审批状态

- 当前阶段：实现、门禁、镜像和 Kind 验证全部完成
- Proposal：已批准（2026-08-08，用户回复“请开始修改”）
- 验证用例：已批准（2026-08-08，U1-U6、K1-K3）
- K1 受限拓扑调整：已批准（2026-08-08，用户回复“可以”）；使用 4 节点任务集群，
  保持原 exact-forest 通过标准。
- 规模 UT 交付方式：按用户后续要求（2026-08-08），1k/8k benchmark 与三层压力 harness
  仅保留在报告，不进入最终 commit；CI 只保留轻量语义回归。
- 生产基线分支：`dev-1.14`
- 修复分支：`fix/022-candidate-forest-memory-scaling`
- 源码基线：`71f1d3248`
- 最终提交：`dc4fffbe3e9a234837378e91d68099da69f70d99`

## 调查结论

### 1. 风险由单 Job 的 forest root 数决定

定义：

- R：一个 Job gradient layer 中的 candidate forest root 数；
- N：这些 roots 覆盖的唯一 Kubernetes Node 数；
- L：平均 HyperNode root 名称长度；
- H：LCA traversal envelope 下的 HyperNode 总数。

集群最低层有 8k HyperNode 不等于每个 Job 都有 8k roots。若实际 forest 仍为 `{B,C}`，
R=2，资源检查保持 KB/MB 级；只有 required PodGroup anti-affinity 过滤后仍留下接近 8k
同层 roots 时，当前超线性路径才进入 GB 级。

### 2. 正常路径的 `O(NR)` 分配

当前代码：

```go
nodeNames := sets.New[string]()
for _, root := range roots {
    if nodes, found := ssn.RealNodesSet[root.Name]; found {
        nodeNames = nodeNames.Union(nodes)
    }
}
```

Kubernetes generic Set 的 `Union` 先 clone receiver，再插入另一个 set。同层 roots 在正常
HyperNode tree 中基本互斥；若每个 root 平均覆盖 `N/R` 个 Node，累计 clone entry 数：

```text
(N/R) * (1 + 2 + ... + R) = N * (R + 1) / 2
```

对三层近似 `8192 / 1024 / 128` roots、每层最终都覆盖约 8192 Nodes 的 tree，基于 1,024
实测外推，空资源或资源满足时仅这一阶段就产生约 1.7～1.9GB/Job 的累计 allocation
traffic。旧 map 多数可以被 GC，因此它不是等量常驻内存，但会显著抬高 GC 频率和堆高
水位。

### 3. 失败路径的 `O(R²)` 存活字符串

当前每个失败 root 都执行：

```go
fmt.Sprintf(
    "minResource (%s) in candidateForest %v",
    minResource.String(), candidateForestRootNames(roots),
)
```

每个 reason 包含完整 roots，且所有 reason 在 `resourceStats.ExcludedByReason` 中保持可达，
直到 `FilterCandidateForestGradientsByMinResource` 返回并完成 diagnostics 汇总。近似存活量：

```text
R² * (L + 1)
```

R=8192 时，仅 reason 字符串在 L=16/32/64 下分别约为 1.1/2.1/4.1GiB。最终
`FormatHyperNodeFitSummary` 只消费 tier 计数和 minResource，不需要逐 root 携带完整 forest
列表，因此该放大没有用户可见语义收益。

### 4. 空 Resource 没有 fast path

`JobInfo.GetMinResources()` 在 PodGroup 未配置 `spec.minResources` 时返回 `EmptyResource()`，
不是 nil。当前 helper 只对 nil 提前返回，导致最常见的无 Job minResources 场景仍执行全部
Node union 和 `FutureIdle()` 聚合。

## 推荐实现

### 1. 保留 layer 规范化，增加空资源快速路径

`FilterCandidateForestGradientsByMinResource` 仍对每个 layer 调用
`normalizeCandidateForestRoots`，保持去重、稳定排序以及返回 gradients 的确定性。构建
`FinalByTier` 后，如果 `minResource == nil || minResource.IsEmpty()`：

1. 直接保留所有规范化 layer；
2. 不读取 `ssn.RealNodesSet`；
3. 不读取 `ssn.Nodes`，也不调用 `NodeInfo.FutureIdle()`；
4. 返回与“资源全部满足”等价的统计，不产生 resource exclusion。

`allocatedHyperNode != ""` 的既有提前返回语义保持不变。

### 2. 用单个可变 set 原地聚合 Node 名称

将重复 `Union` 改为对同一个 `sets.Set[string]` 插入：

```go
nodeNames := make(sets.Set[string])
for _, root := range roots {
    for nodeName := range ssn.RealNodesSet[root.Name] {
        nodeNames.Insert(nodeName)
    }
}
```

允许根据 roots 的 set 长度估算初始容量，但必须继续按 Node name 去重，不能用简单资源求和
代替集合语义。这样 map-entry 构建从 `O(NR)` 降为 `O(N)`；遍历 roots 为 `O(R)`，总空间
为 `O(N + R)`。

保留当前 `Idle`/`FutureIdle` 聚合方式，避免在本次内存修复中改变资源计算与日志语义。

### 3. 将失败 reason 改为有界、每 layer 只生成一次

每个失败 layer 只生成一个 compact reason，例如：

```text
minResource (<resource>) in candidateForest (roots=<count>)
```

随后所有失败 root 的 `ExcludedByReason` value 复用同一个 string。reason 不包含完整 root
列表；root 名仍由 map key 表达，tier/count 仍由现有 stats 表达。该设计同时避免：

- 构造 R 次 `[]string`；
- 构造 R 份长度为 O(R*L) 的字符串；
- V(3) 输出整个 `ExcludedByReason` map 时把完整 forest 列表重复 R 次。

失败 stats 的 map entry 数仍为 O(R)，保持现有逐 root exclusion contract。

### 4. 日志采用 count/有界 sample

candidate forest build/invalid 日志不应在大 forest 下无界打印完整 roots。若实现需要调整
现有日志，则使用稳定排序后的 root count 和固定上限 sample；sample 上限作为私有常量，
不改变调度逻辑。

## 不在本次实现的内容

### 1. SubJob 保存所有成功 Statement

`allocateForSubJobInCandidateForest` 当前按一个 gradient layer 保存所有成功 candidate 的
Statement 和 worksheet，再评分选择最佳项；空间上界约为 `O(C * T)`，C 为可行 candidate
数、T 为 SubJob task 数。限制 solution 数量会涉及评分、soft preference、fallback 和稳定
选择语义，属于 `017-bounded-joint-placement-search`，不与本次无语义内存小修复混合。

### 2. candidate ancestor filter CPU 优化

`filterGradientsByCandidateForest` 对每个 candidate 调用 `GetAncestors`，8k synthetic 场景
存在明显 CPU 放大，但内存实测仍为线性且约 459KB/SubJob。直接改为 Parent walk 需要明确
不完整 HyperNode cache 的 fallback contract，留作独立优化。

### 3. API、framework 或 plugin contract

不修改 HyperNode API、CRD、gradient callback、NTA/GTA plugin 或 recorder 数据结构。

## 风险与控制

### R1：空资源 fast path 改变 root 顺序或重复处理

控制：仍统一调用 `normalizeCandidateForestRoots`；测试输入包含乱序、nil 和重复 root，返回
必须稳定排序且去重。

### R2：重叠 roots 重复计算资源

控制：继续通过 Node-name set 去重；保留 B/D roots 覆盖同一 Nodes 的既有测试，并增加
大规模 overlap case。

### R3：compact diagnostics 降低定位能力

控制：map key 仍是被排除 root，reason 保留完整 minResource 和 root count；JobFitErrors
本来只展示 tier/count 聚合。V(3) 日志使用有界 sample，而不是完全丢失 root 线索。

### R4：严格内存门槛受 Go runtime 波动影响

控制：benchmark fixture 在计时前构造，门槛相对优化后预期保留数倍余量；同时比较
1,024 与 8,192 roots 的增长比，验证线性趋势而不是依赖单一绝对值。

## 验证用例

以下用例在用户批准后成为完成标准；若需改变，先更新本文件并重新获得批准。

### U1：空 minResource fast path 与规范化

- Purpose：证明常见的未配置 Job minResources 场景不再构建 Node union，同时保持 roots
  去重、排序和 tier stats。
- Scope：focused unit test。
- Setup：多 layer gradients，包含 nil、重复和乱序 roots；Session 的 `RealNodesSet`/`Nodes`
  设为 nil 或不可用；分别传入 nil 与 `api.EmptyResource()`。
- Commands：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'TestFilterCandidateForestGradientsByEmptyMinResource' -count=100
  ```

- Expected：不访问 Node 数据；返回 roots 稳定排序去重；所有 layer 保留；无 exclusion。
- Pass/fail：100 次全部通过且结果一致。

### U2：联合资源语义与 overlap 去重

- Purpose：证明原地 set 插入不改变 exact forest 资源判断。
- Scope：focused unit test。
- Setup：覆盖以下 table cases：
  1. 两 roots 单独不足、联合足够；
  2. 联合仍不足；
  3. 两 roots 覆盖相同 Nodes，不得重复计数；
  4. roots 含 nil/duplicate；
  5. 某些 Node name 在 Session.Nodes 中缺失，保持现有忽略语义。
- Commands：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'TestFilterCandidateForestGradientsByMinResource' -count=100
  ```

- Expected：结果与当前 exact-union 语义一致。
- Pass/fail：所有 cases 100 次通过。

### U3：失败 diagnostics 有界且共享

- Purpose：证明失败路径不再生成 R 份完整 forest 列表。
- Scope：focused unit test。
- Setup：至少 1,024 个 root，所有 roots 因 minResource 不足被排除，root 名使用 32 字符
  左右的稳定名称。
- Commands：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'TestCandidateForestMinResourceFailureDiagnosticsAreBounded' -count=100
  ```

- Expected：每个 root 仍有 exclusion；所有 value 内容相同，包含 minResource 和
  `roots=1024`；不包含完整 root 列表；单个 reason 长度小于 1KiB。
- Pass/fail：上述断言全部成立。

### U4：8k roots 内存线性门禁

- Purpose：直接验证生产目标量级不再出现 GB 级 allocation traffic。
- Scope：同包 synthetic benchmark；fixture 构建不计入 B/op。
- Setup：分别构造 1,024 和 8,192 roots，每个 root 覆盖一个唯一 Node；覆盖 empty、
  sufficient、insufficient 三种 minResource。
- Commands：

  ```bash
  go test ./pkg/scheduler/actions/allocate -run '^$' \
    -bench '^BenchmarkCandidateForestMinResourceScaling$' \
    -benchmem -benchtime=3x -count=3
  ```

- Expected：
  - 8,192 empty：`< 4 MiB/op`；
  - 8,192 sufficient：`< 8 MiB/op`；
  - 8,192 insufficient：`< 16 MiB/op`；
  - 每种 case 从 1,024 增至 8,192 时，B/op 增长比例 `< 12x`，不再接近 64x 或更高。
- Pass/fail：三次结果均满足绝对门槛和增长比例；记录原始输出。

### U5：candidate forest 调度回归

- Purpose：证明内存优化不改变 B+C 联合分配、exact forest、soft fallback 和 actual
  placement。
- Scope：现有 allocate full-action/focused tests。
- Commands：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'Test(AllocatePodGroupAntiAffinityAcrossCandidateForest|CandidateForestUsesActualPlacementInsteadOfEnvelope|CandidateForestPreservesNativeSoftJobTopologyFallback|CandidateForestSupportsNativeSoftSubGroups|NativeSoftSubGroupsWithoutPAAKeepCoarseFallback|CandidateForestActivationBoundary|FilterGradientsByCandidateForestExcludesEnvelopeSibling|FilterCandidateForestGradientsByMinResource)' \
    -count=50
  ```

- Expected：现有 placement、candidate-path activation 和 exclusion 断言全部不变。
- Pass/fail：50 次全部通过。

### U6：相关包与代码门禁

```bash
./hack/update-gofmt.sh
go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/actions/allocate -count=1
make verify
```

- Expected：命令通过，没有非预期 generated diff。
- Pass/fail：全部通过；若仓库级命令存在确认与本任务无关的平台失败，必须记录原始错误，
  不能用较弱测试替代上述 focused 门禁。

### K1：Kind B+C 联合分配语义

- Purpose：验证真实 scheduler 仍能跨 exact forest 调度且不泄漏 excluded branch。
- Scope（原批准）：5 节点 Kind，1 control-plane + 4 worker。
- 执行前环境检查（2026-08-08）：发现用户已有 4 节点 `kind` 集群；再创建原方案集群会让
  可见节点总数达到 9，超过 workspace 的 8 节点硬上限。不得删除或复用用户集群。
- 已批准调整：使用 4 节点 `volcano-dev-022`（1 control-plane + A/B/C 三个 worker），
  target Pod 各请求 `4500m` CPU；在当前 12 CPU allocatable 下，B 或 C 单独最多容纳 2 个，
  B+C 联合容纳 4 个。该调整保持 exact forest、单 root 不足、联合成功及 A exclusion 的
  原通过标准；用户已于 2026-08-08 明确批准。
- Setup：显式三层 HyperNode tree；A 被已有匹配 PodGroup 占用，target required PodGroup
  anti-affinity 排除 A；B/C 合计满足四个单 Pod SubJob，单个 root 不足。
- Images：使用 `dev-022-memory` 本地 tag 构建并加载 scheduler/controller/webhook；Helm
  `IfNotPresent`。
- Expected：四个 target Pod 全部绑定 B/C；A 无 target Pod；Job placement 是 B/C 的真实
  LCA；scheduler Pod 无 restart/OOM。
- Pass/fail：对象状态、placement、scheduler 日志和 Pod restartCount 同时满足。

### K2：Kind minResource 不足与恢复

- Purpose：验证 compact diagnostics 不改变不可调度语义，且一次失败不会破坏后续调度。
- Scope：复用 K1 集群和三层 tree。
- Setup：先创建 forest 总资源仍不足的 target PodGroup，再创建资源可满足的对照 Job。
- Expected：不足 Job 保持 Pending，事件/JobFitErrors 包含 minResource/tier 聚合；scheduler
  保持 Ready 且 restartCount=0；对照 Job 随后正常绑定。
- Pass/fail：上述状态全部满足，无 excluded branch 泄漏。

### K3：清理

- 删除 task-specific workloads、Helm release 和 `volcano-dev-022` Kind cluster；记录
  `kind get clusters`，不得删除用户原有 cluster。

## 最终实现

提交：

```text
dc4fffbe3e9a234837378e91d68099da69f70d99
fix(allocate): bound candidate forest memory growth
```

生产代码只修改 `pkg/scheduler/actions/allocate/allocate.go`：

- 空或 nil minResource 仍规范化 roots 和生成 tier stats，但不读取 `RealNodesSet`/`Nodes`；
- 唯一 Node 名通过单个可变 set 原地插入，不再重复 clone 累计集合；
- 一个失败 layer 只构造一次 `minResource (...) in candidateForest (roots=N)` reason，所有
  root map value 共享该字符串；
- forest build/invalid 日志只输出 root count 和最多 8 个稳定 sample。

新增 `candidate_forest_memory_test.go`，只保留空资源、缺失/重复 Node 和 16-root 有界失败
diagnostics 等轻量语义回归。根据用户后续要求，1k/8k benchmark、三层生产规模 harness 和
RSS 压力 UT 均不进入最终源码，只在报告中保存方法和结果。没有修改 framework、plugin、
API、CRD、controller、webhook 或 recorder contract。

## Unit、benchmark 与代码门禁结果

### U1-U3

批准的 focused 用例以 `-count=100` 运行，全部通过：

```text
ok  volcano.sh/volcano/pkg/scheduler/actions/allocate  1.058s
```

- nil/empty minResource 在空 Session 上返回稳定排序、去重后的两层 forest；
- unique Node union、重复/nil root、缺失 Node 以及既有 overlap case 保持原语义；
- 验证阶段 1024 个失败 root 的临时规模用例全部满足 exclusion/value 共享/有界断言；
- 最终提交把常驻 CI 语义用例缩小为 16 roots，包含 `roots=16`、不含 root 列表且单个
  reason 小于 1KiB；`-count=100` 复跑 PASS（1.354s）。

### U4：8192-root 内存门禁

环境：Apple M2 Pro、darwin/arm64、Go 1.26.5。批准命令运行三次，结果稳定：

| Case | 1,024 roots B/op | 8,192 roots B/op | 8k MiB/op | 增长比 |
| --- | ---: | ---: | ---: | ---: |
| empty | 82,880 | 633,840 | 0.604 | 7.65x |
| sufficient | 224,408 | 1,769,256 | 1.687 | 7.88x |
| insufficient | 385,578 | 3,078,426–3,078,432 | 2.936 | 7.98x |

全部远低于批准门槛 4/8/16 MiB/op，增长比也低于 12x。对比旧实现 1024-root
insufficient 的约 73.4MB/op，本实现 8192-root insufficient 仍只有约 2.94MiB/op。

### U5：candidate forest 调度回归

原单进程组合命令在 Go 默认 10 分钟超时触发前没有断言失败，但 `-count=50` 会把三个
自身已有 50/50/10 次循环的 full-action test 再整体重复 50 次；现有 informer fixture 在
进程内累积，超时栈的 goroutine ID 已超过 310k，输出约 62MB。该运行记为 harness timeout，
不能作为失败断言。

随后保持每个语义 case 至少 50 次有效执行，但按测试进程隔离：

```text
AllocatePodGroupAntiAffinityAcrossCandidateForest      50 internal iterations  PASS 25.572s
CandidateForestUsesActualPlacementInsteadOfEnvelope   count=50                PASS 25.435s
CandidateForestPreservesNativeSoftJobTopologyFallback count=50                PASS 25.156s
CandidateForestSupportsNativeSoftSubGroups             50 internal iterations  PASS 25.989s
NativeSoftSubGroupsWithoutPAAKeepCoarseFallback         count=5 x 10 internal   PASS 25.045s
Activation/filter/minResource pure tests               count=50 each           PASS 1.308s
```

执行强度未降低；拆分只隔离测试夹具的 informer goroutine 生命周期。

### U6

```text
./hack/update-gofmt.sh                                  PASS
go test ./pkg/scheduler/api                             PASS 0.994s
go test ./pkg/scheduler/framework                       PASS 1.962s
go test ./pkg/scheduler/plugins/group-topology-affinity PASS 2.799s
go test ./pkg/scheduler/actions/allocate                PASS 93.123s
GOPATH=/Users/vanderchen/go make verify                 PASS
```

`make verify` 首次未导出 GOPATH 时在旧脚本的 `${GOPATH}/bin` 访问处退出；使用 `go env
GOPATH` 返回的宿主路径重跑后，gofmt/gencode diff 全部通过。旧生成脚本临时升级根
`go.mod` 的 code-generator 并生成未跟踪 `volcano.sh/`；门禁后已精确恢复 `go.mod` 并删除
该临时目录，业务 diff 未受污染。

## 本地镜像

```text
volcanosh/vc-scheduler:dev-022-memory
  sha256:2a59be03410e7408530d7cacaf72eb7e5eb1e5e3fcedcb3cf21468d02f6755d2
volcanosh/vc-controller-manager:dev-022-memory
  sha256:8a2741d90880e96e8a366b5fa010e33bfaf0f5a7d9b3f1c66e876d3b0073a52c
volcanosh/vc-webhook-manager:dev-022-memory
  sha256:3eb29423d359649d678fff87604fdd1c7ca8cfa14f7e3cea01a102eb0501a76d
```

三镜像与 `registry.k8s.io/pause:3.10` 均已加载到任务集群；Helm 使用
`basic.image_tag_version=dev-022-memory` 和 `IfNotPresent`，三个 Deployment 全部 rollout。

## Kind K1-K3 结果

### 受限拓扑

用户已有 4 节点 `kind` 集群。经用户批准，任务集群改为 4 节点：

```text
volcano-dev-022-control-plane  arm64  12 CPU
volcano-dev-022-worker         arm64  12 CPU  (A)
volcano-dev-022-worker2        arm64  12 CPU  (B)
volcano-dev-022-worker3        arm64  12 CPU  (C)
```

target Pod 各请求 4500m，因此 B/C 单 root 各最多容纳 2 个，B+C 联合可容纳 4 个。

### K1：B+C exact forest

PASS：

```text
bug022-decode-0   Running  volcano-dev-022-worker2
bug022-decode-1   Running  volcano-dev-022-worker3
bug022-prefill-0  Running  volcano-dev-022-worker2
bug022-prefill-1  Running  volcano-dev-022-worker3
bug022-anchor     Running  volcano-dev-022-worker
PodGroup          Running  running=4  placement=bug022-shared-t3
scheduler         Ready=true restartCount=0
```

有界日志：

```text
Build PodGroup anti-affinity candidate forest, job=default/bug022-target,
allowedRootCount=2, allowedRootSample=[bug022-b-t2 bug022-c-t2],
envelope=bug022-shared-t3, tierLayer=0
```

A 没有 target Pod；B/C 各 2 个；实际 placement 是 B/C 的 LCA。

### K2：forest minResource 不足与恢复

初始 100 CPU 会被更早的全局 Job-ready 门禁拦截，因此按当前已观测容量改为 30 CPU：
全体 worker 约 36 CPU 可满足，排除 A 后 B+C 约 24 CPU 不足，精确进入 candidate forest
minResource filter。

不足 Job 保持 Pending/无 node，PodGroup diagnostics：

```text
minResource: cpu 30000.00, memory 0.00
volcano.sh/hyperinstance-group 0/3 (1 podGroupAntiAffinity, 2 minResource)
volcano.sh/hypernode           0/3 (1 podGroupAntiAffinity, 2 minResource)
```

保持该 Job Pending 时重新创建正常 target，PASS：四个 Pod 在 B/C 各 2 个，PodGroup
Running/running=4/placement=bug022-shared-t3；不足 Pod 仍 Pending；scheduler
Ready=true、restartCount=0。

### K3：清理

- 已删除 target、insufficient、anchor 和全部 `bug022-*` HyperNode；
- 已卸载任务 Helm release；
- 已删除 `volcano-dev-022` 四个节点；
- 最终 `kind get clusters` 只返回用户原有 `kind`，未修改或删除该集群。

## 追加生产规模 UT（2026-08-08）

按生产三层假设增加临时同包 UT harness，规模为：

```text
tier1: 8192 roots，每 root 1 Node
tier2: 1024 roots，每 root 聚合 8 Nodes
tier3:  128 roots，每 root 聚合 64 Nodes
唯一 Kubernetes Nodes: 8192
总 forest roots: 9344
```

正确性 UT 运行 10 次全部 PASS：empty/sufficient 保留完整 `8192/1024/128` 三层；
insufficient 排除全部 9344 roots，tier stats 正确，reason 有界且不包含 root 列表。

完整三层 benchmark，`-benchtime=5x -count=3`：

| Case | ns/op 范围 | B/op | MiB/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| empty | 1,221,575–1,489,217 | 726,432–726,435 | 0.693 | 54 |
| sufficient | 5,367,567–5,781,542 | 4,132,680 | 3.941 | 24,867 |
| insufficient | 6,090,567–6,103,250 | 5,442,174–5,442,264 | 5.190 | 24,963–24,964 |

同一测试二进制、`GOGC=100` 的最大 RSS：

| 重复次数 | 结果 | maximum RSS | MiB |
| ---: | --- | ---: | ---: |
| 1 | PASS | 64,421,888 bytes | 61.4 |
| 100 | PASS | 74,465,280 bytes | 71.0 |
| 1,000 | PASS | 75,776,000 bytes | 72.3 |

1,000 轮仅 filter 调用就产生至少约 9.6GiB 累计 allocation traffic，另有每轮 fixture 重建；
但最大 RSS 相比单轮只增加约 10.8MiB，相比 100 轮只增加约 1.3MiB。说明本次路径的
per-Job 临时对象可被 GC 回收，不会随一个 session 内顺序处理的 Job 数线性驻留。该 RSS
只衡量此 UT 进程及 candidate-forest 路径，不代表完整生产 scheduler 的 cache、informer、
Statement 搜索或其他插件内存；多 candidate Statement 保留仍属于 issue 017 的独立风险。

临时规模 harness 与 `/private/tmp` 测试二进制已删除；源码分支保持 clean，最终 commit
最终 amend 后提交为 `dc4fffbe3e9a234837378e91d68099da69f70d99`。

## 实施顺序（已完成）

1. 用户批准本 Proposal 和 U1-U6、K1-K3；
2. 实现 focused helper 修改和单测/benchmark；
3. 运行 U1-U6 并记录结果；
4. 构建本地镜像，执行 K1-K3；
5. 更新本文件的命令、原始数据、镜像 ID、日志摘要和清理结果；
6. 提交到 `fix/022-candidate-forest-memory-scaling`，报告 commit hash；
7. 全部通过后将 issue 目录改为 `DONE`，不合并回 `dev-1.14`。
