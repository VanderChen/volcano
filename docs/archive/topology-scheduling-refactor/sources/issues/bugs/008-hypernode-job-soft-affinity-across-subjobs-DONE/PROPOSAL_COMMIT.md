> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 008：提案与验证计划

## 审批

- 2026-07-22：用户已明确批准本提案及 V1/V2/V3 验证用例，进入实施阶段。

## 调查结论（基于 `8fcc0012`）

- `pkg/scheduler/actions/allocate/allocate.go` 的 `allocateForJob` 会在同一个 Job
  HyperNode dry-run 中依次模拟各个 SubJob（约 346-463 行）。第一个成功的 SubJob 已经
  通过 `updateJobAllocatedHyperNodeFromSubJob` 更新 Job 级 LCA。
- `allocateForSubJob` 完成候选 dry-run 后调用
  `selectBestHyperNodeForSubJob`（约 470-614 行）。该函数只将 `subJob` 传给
  `HyperNodeOrderMapFn`。
- network-topology-aware 插件的 `HyperNodeOrderFn` 仅根据该 SubJob 的请求资源和该
  SubJob 已放置任务数打分（`pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go`
  约 349-427 行），未读取 `job.AllocatedHyperNode`。
- 同分候选由 `util.SelectBestHyperNodeAndScore` 随机选取（
  `pkg/scheduler/util/scheduler_helper.go` 约 207-223 行）。因此空闲拓扑下的第二个
  SubJob 没有与第一个 SubJob 保持同一 Job 级分支的偏好。

本次调查没有查看 `edaaf83a`。

## 推荐方案：仅在 SubJob 的最终候选选择处增加贪心 Job 锚点

保持 `allocateForJob` 的候选枚举、Statement dry-run/恢复、资源过滤和提交流程不变。
只在 `pkg/scheduler/actions/allocate/allocate.go` 的 `allocateForSubJob` 中，所有候选
已完成 dry-run、`stmtBackup` 已建立、调用 `selectBestHyperNodeForSubJob` 之前，插入一个
同文件私有 helper：

1. 沿用现有逻辑生成“已成功 dry-run”的候选 HyperNode 及其已有插件分数；不改变
   candidate 枚举、资源 predicate 或 Statement 恢复。
2. 仅当以下条件同时满足时启用贪心偏好：Job 有 SubGroup/partition policy，配置了
   `networkTopology.mode: soft` 且 `highestTierAllowed` 非空，`job.AllocatedHyperNode`
   非空且存在于当前 HyperNode 树，并且至少一个**其他** SubJob 已有
   `AllocatedHyperNode`。不以当前 SubJob 是否已有放置作为 gate，使重调度/部分分配时
   仍能保持 Job 级偏好。
3. 对这些已成功 dry-run 的候选，计算
   `LCA(candidate, job.AllocatedHyperNode)`。若 LCA tier 不高于 Job 的
   `highestTierAllowed`，放入“Job 本地首选集”。
4. 首选集非空时，仅把它交给现有 `PrioritizeHyperNodes` / 插件打分逻辑；首选集为空时，
   把完整成功候选集交给原逻辑。也就是说，Job 的 soft 亲和是“本地可行则优先、本地
   不可行则回退”，而不是硬过滤。
5. 首选集内部完全保持现有插件总分与最终并列随机选择；不增加新的 tier 排序、权重、
   全局回溯、组合搜索或插件接口。

实现形态限定为下列局部伪代码（候选 `stmts` 已经是当前函数成功 dry-run 的结果）：

```go
candidateStatements := preferJobSoftTopologyCandidates(job, subJob, stmts)
bestHN, bestScore, err := selectBestHyperNodeForSubJob(candidateStatements, subJob)
```

`preferJobSoftTopologyCandidates` 只读 Session 中的 HyperNode 树，只新建 map；不会
Allocate、Discard、Recover 或修改任何 `AllocatedHyperNode`。不满足启用条件、或不存在本地
成功候选时，它直接返回原 `stmts`，因此原有代码路径完全不变。

这样，第一个 SubJob 形成 Job 锚点后，第二个 SubJob 会先在同一 tier-2 中选择可行的
tier-1 HyperNode；只有该 tier-2 中没有成功 dry-run 的候选时才跨到 tier-3。该改动仅是
`selectBestHyperNodeForSubJob` 前的一小段候选集选择，不重构 `allocateForJob`。

## 明确的非目标与已知边界

- 这不是全局最优分区：先调度的小 SubJob 可能先占据一个分支，之后更大的 SubJob 才
  发现该 tier-2 容量不足并回退到另一分支。不会回溯或迁移已模拟的前一个 SubJob。
- 若同一 tier-2 内有可行候选但其资源 bin-packing/其他 HyperNode 打分低于远端候选，
  推荐策略仍优先该 Job 本地首选集。这是为了让 Job 级 `soft` 偏好真正跨 SubJob 生效；
  首选集内部才保留既有打分优先级。
- 已经实际跨分支运行的 Pod 不会被该修复重新聚拢；修复作用于后续调度决策。
- 硬拓扑、没有 Job 软拓扑、没有其他 SubJob 锚点，以及没有本地可行候选的场景都保持
  原有调度可行性与回退行为。
- `stmtBackup` 的 key 是本次候选 HyperNode，而不是每次 dry-run 后最终绑定节点；因此
  过滤是按候选 HyperNode 做的保守近似。要按最终落点精确过滤需扩大 Statement/recorder
  的数据流范围，本修复不做该扩张。

## 预先约定的验证用例

### V1：精确 16 节点回归单测

- 目的：稳定重现两个分区 SubJob 的 Job 级软亲和，并证明四个 Pod 的 LCA 不会跨到
  tier-3。
- 范围：`pkg/scheduler/actions/allocate` 的新增
  `TestAllocateJobSoftAffinityAcrossSubJobs`；使用 16 个内存中的 Node 对象，不创建真实
  Kubernetes 节点。
- 拓扑：两个互不为成员的真实 tier-3 根；每个根含两个 tier-2、每个 tier-2 含两个
  tier-1、每个 tier-1 含两个节点（共 14 个 HyperNode、16 个节点）。Session 会自动
  加入虚拟 cluster-top。每个 tier-1 的可用容量恰好容纳一个两 Pod 分区，从而第二个
  分区需选择同 tier-2 的兄弟 tier-1。
- 调度对象：上述 `replicas: 4`、`totalPartitions: 2`、`partitionSize: 2`、
  `minAvailable: 4` 的 Job/PodGroup 等价对象；SubGroupPolicy 使用
  `matchLabelKeys: ["volcano.sh/partition-id"]`，四个 pending Pod 的该标签为
  `0, 0, 1, 1`，确保实际生成两个 `MinAvailable=2` 的 SubJob；Job soft/tier-2，
  SubJob soft/tier-1。
- 断言：两个 SubJob 的各自节点 LCA 均为 tier-1；全部四个节点的 LCA 为一个 tier-2；
  不为 tier-3 或虚拟 cluster-top；并断言 `len(job.SubJobs)==2` 及每个 SubJob 的
  `MinAvailable==2`。测试会从 Session 的 Task `NodeName` 反查 leaf/LCA，不只检查
  bind 数；并注入确定性候选分数，让远端候选的既有分数更高，从而稳定证明 Job 本地
  首选集先于原有分数生效。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate -run '^TestAllocateJobSoftAffinityAcrossSubJobs$' -count=50
  ```

- 通过标准：50 次均满足 LCA 断言；旧行为应可被确定性测试构造为跨 tier-3。

### V2：soft 回退与范围保护单测

- 目的：确认此修复不把 Job 软亲和变为硬限制，也不改变非目标场景。
- 范围：同一 focused package。
- 形式：新增 table-driven `TestPreferJobSoftTopologyCandidates`，直接检查首选候选集；
  再由 V1 覆盖完整 `allocateForSubJob` 调用链。
- 情形：
  1. Job 锚点所在 tier-2 没有可行容量时，第二个 SubJob 可回退至另一 tier-3 分支；
  2. Job 没有 soft networkTopology、没有解析出的 tier、没有其他 SubJob 锚点时，保持
     既有插件分数的选择；
  3. 当前 SubJob 已有部分放置、另一个 SubJob 也已放置时，仍可使用 Job 级首选集。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate -run '^TestPreferJobSoftTopologyCandidates$' -count=50
  ```

- 通过标准：情形 1 成功绑定且跨分支；情形 2、3 的既有预期不变。

### V3：拓扑等价的 Kind 验证（最多 5 个 Kubernetes 节点）

- 目的：在真实 scheduler/controller 路径验证 Job、分区、HyperNode CRD 与绑定结果。
- 说明：仓库规则限制验证拓扑最多 8 个可见 Kubernetes 节点，因此不直接创建原始 16 节点
  Kind 集群。使用 1 control-plane + 4 workers 的缩小等价树：两个 tier-3 分支，各有
  一个 tier-2 和两个 tier-1；每个 tier-1 worker 有足够容量运行一个两 Pod 分区。不能
  复用现有 HyperNode e2e 驱动，因为它固定额外创建 8 个 KWOK 节点，会超出本任务的节点
  上限。
- 设置：构建本地 `dev-008` 镜像，加载 Kind，以 `IfNotPresent` 安装 Helm chart；创建
  HyperNode、Job 和 Namespace 清单。
- 命令：

  ```bash
  cd volcano
  TAG=dev-008 BUILDX_OUTPUT_TYPE=docker make images
  kind create cluster --name volcano-dev-008 --config ../issues/bugs/008-hypernode-job-soft-affinity-across-subjobs-IP/kind-config.yaml
  kind load docker-image volcanosh/vc-scheduler:dev-008 --name volcano-dev-008
  kind load docker-image volcanosh/vc-controller-manager:dev-008 --name volcano-dev-008
  kind load docker-image volcanosh/vc-webhook-manager:dev-008 --name volcano-dev-008
  helm upgrade --install volcano installer/helm/chart/volcano --namespace volcano-system --create-namespace --set basic.image_tag_version=dev-008 --set basic.image_pull_policy=IfNotPresent
  kubectl apply -f ../issues/bugs/008-hypernode-job-soft-affinity-across-subjobs-IP/hypernodes.yaml
  kubectl apply -f ../issues/bugs/008-hypernode-job-soft-affinity-across-subjobs-IP/job.yaml
  kubectl wait --for=condition=Ready pod -l volcano.sh/job-name=test --timeout=180s
  kubectl get pods -l volcano.sh/job-name=test -o wide
  ```

- 通过标准：四个 Pod 均绑定；按清单中的节点到 HyperNode 映射计算，四个 Pod 的 LCA 是同一
  tier-2；每个分区的 LCA 是 tier-1；scheduler 日志中没有因本地容量不足导致的异常回退。

## 实施后的通用回归门禁

```bash
cd volcano
./hack/update-gofmt.sh
make unit-test
make verify
```

实施后还将记录实际镜像标签、Kind 节点列表、Helm 参数、Pod 绑定、LCA 计算、scheduler
日志和清理命令。只有 V1、V2、V3 全部通过才将任务标记为 DONE。

## 实施结果（2026-07-22）

### 隔离基线与修改范围

- 为避免触碰原工作树中已有的大范围拓扑分配改造，创建了独立仓库
  `volcano-008`，从 `8fcc0012` 建立分支
  `fix/008-hypernode-job-soft-affinity-across-subjobs`。全程未查看
  `edaaf83a`。
- 只修改 `pkg/scheduler/actions/allocate/allocate.go` 的
  `allocateForSubJob`：成功 dry-run 候选收集完成后、原有
  `selectBestHyperNodeForSubJob` 前，调用
  `preferJobSoftTopologyCandidates`。
- helper 的启用条件：存在 SubGroup/partition policy、Job 为 soft topology、
  `highestTierAllowed` 非空、有效 Job 锚点存在，并且有另一个已放置 SubJob。
  它仅筛选 `LCA(candidate, job.AllocatedHyperNode)` tier 不高于 Job tier 的
  成功候选；若筛选结果为空，返回原候选集，保持 soft 回退。
- 未改变 `allocateForJob`、gradient、predicate、Statement dry-run/recover、
  recorder 或原有 HyperNode/Node 打分逻辑。

### 回归测试

- 新增 `TestAllocateJobSoftAffinityAcrossSubJobs`：完整 Action 路径、16 个内存
  Node、两个独立 tier-3 根、两个 2-Pod partition。测试插件确定性地让两个
  partition 原本偏向不同根；从实际 `Task.NodeName` 计算 LCA，断言两个
  SubJob 各自为 tier-1、全部为同一 tier-2，并断言每个 SubJob
  `MinAvailable == 2`。
- 新增 table-driven `TestPreferJobSoftTopologyCandidates`：覆盖同 tier-2 首选、
  无本地可行候选时跨 tier-3 回退、非 soft、无 peer、无 tier，以及当前 SubJob
  已部分放置的情形。

### V1 / V2：通过

```bash
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-008
go test ./pkg/scheduler/actions/allocate \
  -run '^(TestAllocateJobSoftAffinityAcrossSubJobs|TestPreferJobSoftTopologyCandidates)$' \
  -count=50
```

结果：通过（50 次，25.525s）。

在仓库声明的 Linux Go 1.24.4 环境中也运行了完整单元测试门禁：

```bash
docker run --rm \
  -v /Users/vanderchen/workspace/dev/volcano-workspace/volcano-008:/src \
  -w /src golang:1.24.4 \
  make unit-test
```

结果：调度相关包均通过，包含
`ok volcano.sh/volcano/pkg/scheduler/actions/allocate 39.446s`；整体命令随后被无关的
network-qos/eBPF 环境问题阻断，详见“通用门禁记录”。

### V3：通过（真实 Kind 验证）

- 集群：`volcano-dev-008`，1 control-plane + 4 worker，共 5 个可见节点；验证后已
  用 `kind delete cluster --name volcano-dev-008` 清理。
- `volcanosh/vc-scheduler:dev-008` 从本分支构建并加载。首次完整 `make images`
  因 Docker registry 拉取 `golang:1.24.0` 超时；使用本机缓存的
  `golang:1.24.4` 临时标记为该 Dockerfile 所需 tag 后，
  `make vc-scheduler-image` 成功。未修改的 controller-manager 与 webhook-manager
  使用本机缓存的 v1.14.1 镜像标记为 `dev-008` 并加载。
- Helm 使用：

  ```bash
  helm upgrade --install volcano installer/helm/chart/volcano \
    --namespace volcano-system --create-namespace \
    --kube-context kind-volcano-dev-008 \
    --set basic.image_tag_version=dev-008 \
    --set basic.image_pull_policy=IfNotPresent
  ```

- 验证清单补齐了 API 要求的 `partitionPolicy.minPartitions: 2`；初次下发被
  admission webhook 正确拒绝后修正。每个测试 Pod 的 CPU request/limit 调整为
  `5`，使单个 12-CPU worker 只能承载一个两 Pod partition，从而必经 sibling
  tier-1 的选择路径。
- 最终 Pod 绑定：

  | partition | Pods | Node | tier-1 |
  | --- | --- | --- | --- |
  | 0 | `test-ps-0`, `test-ps-1` | `volcano-dev-008-worker` | `hn-008-a-tier1-worker1` |
  | 1 | `test-ps-2`, `test-ps-3` | `volcano-dev-008-worker2` | `hn-008-a-tier1-worker2` |

  两个 tier-1 的 LCA 是 `hn-008-a-tier2`（tier 2），所以每个 partition 的 LCA
  为 tier 1，所有 Pod 的 LCA 为同一 tier 2。scheduler 日志同时记录：

  ```text
  Allocate job to hyperNode success ... hyperNode=hn-008-a-tier2
  update allocated hyperNode for subJob ... ps-0 ... hn-008-a-tier1-worker1
  update allocated hyperNode for subJob ... ps-1 ... hn-008-a-tier1-worker2
  ```

### 通用门禁记录

- `./hack/update-gofmt.sh` 已执行；它会重写 3 个与本修复无关的基线测试文件，已只用
  补丁恢复这些无关差异。`git diff --check` 通过，修复涉及的 Go 文件均已格式化。
- 主机 `make unit-test`（macOS / Go 1.26.5）失败于仓库的 Linux-only agent、
  network-qos/eBPF 代码以及既有 PVC fake-volume-binding 用例，均不经过本次 helper。
- Linux Go 1.24.4 容器中的 `make unit-test` 也通过全部 scheduler 包，但被
  `cmd/network-qos/tools` 的 eBPF SIGBUS 和
  `pkg/networkqos/utils/ebpf: TestCreateAndPinArrayMap` 的特权环境假设阻断；
  `pkg/scheduler/actions/allocate` 通过。
- `make verify` 在主机和 Go 1.24.4 容器中均仅因基线已有的 3 个无关 test 文件不符合
  当前 `gofmt -s` 输出而停止；失败输出不包含本次修改的三个文件，因此未运行后续
  gencode 校验。

## 已知边界

- 该修复是顺序相关的贪心策略：先调度的 SubJob 形成 Job tier 锚点；若之后的 SubJob
  在该 tier 内无成功 dry-run 候选，会按 soft 语义回退到更远分支。它不做组合搜索、
  回溯或迁移已运行 Pod，因此不保证全局最优。
- outer helper 按 `subJobsAllocationScores` 的 outer HyperNode key 判断 LCA，而不是解析
  Statement 中每个最终 Pod 的叶子节点。若某个插件只返回高于 Job tier 的聚合 outer
  candidate（它内部可能同时含本地与远端叶子），该 helper 不会把它识别为本地候选，仍保留
  既有 soft fallback；精确拆分这种聚合候选需要扩大 Statement/gradient 数据流，超出本次
  对 `allocate` 的最小修改范围。
- 没有 request 的原始清单仍由 backfill 处理，不经过 `allocateForJob`；本修复不改变
  BestEffort 的拓扑策略。

## 初始修复结论（2026-07-22）

- V1、V2 和真实 Kind 的 V3 均通过；初始任务当时完成。
- 实现已移植到主仓基于 `8fcc0012` 的分支
  `fix/008-hypernode-job-soft-affinity-across-subjobs`，本地提交为
  `3e6b5c950 fix(scheduler): preserve soft topology across subjobs`；尚未推送。

## 2026-07-27 回归复现：任务重新打开

用户提供了 `bug2026052338589-vcjob` 的原始 YAML，要求在资源允许时所有 Pod 留在一个
tier-2。为复现，使用主仓提交 `3e6b5c950` 构建
`volcanosh/vc-scheduler:dev-008-retest`，在 `volcano-dev-008` Kind 集群中创建了两个真实
tier-3 根、每根一个 tier-2、每个 tier-2 两个 tier-1（1 control-plane + 4 worker）。

### R1：用户原始 YAML —— 已复现跨 tier-2，但不经过本次 allocate 修复

- 清单：`user-job.yaml`，逐字段保留用户提交的 Job；没有 resources，也没有
  `minPartitions`。
- 控制器创建的 PodGroup 显示 `minTaskMember.ps: 0`、`minSubGroups: 0`。
- 四个 Pod 的实际绑定为：`ps-0 -> worker4 (B/tier-2)`、
  `ps-1 -> worker2 (A/tier-2)`、`ps-2 -> worker3 (B/tier-2)`、
  `ps-3 -> worker (A/tier-2)`，因此不仅 Job 跨 tier-2，单个 partition 也跨 tier-1。
- scheduler 日志是 `backfill.go: Binding Task`，而不是 `allocateForJob`。原因是无 request
  的 Pod 为 BestEffort，`organizeJobWorksheet` 会跳过它们，backfill 从全 NodeList 逐 Pod
  选择节点。该路径没有 Job anchor，也不会调用
  `preferJobSoftTopologyCandidates`。
- Pod 的 `ErrImagePull`（公开镜像拉取/测试 secret 不可用）不影响 `spec.nodeName`，故不影响
  上述调度结论。

### R2：显式 request、仍省略 minPartitions —— 已复现初始修复遗漏

- 清单：`user-job-with-requests.yaml`；除 CPU/memory request/limit（每 Pod 5 CPU、128Mi）外
  与用户清单一致，故模拟命名空间 LimitRange 注入 request 的现场。`minPartitions` 仍为 0。
- 同一 tier-2 的两个 12-CPU worker 足以放下两组（每组 2 × 5 CPU），因此本地 tier-2
  资源充足。
- 重复运行的第 1 次得到：`ps-0/ps-1 -> worker (A/tier-1)`，
  `ps-2/ps-3 -> worker3 (B/tier-1)`；每组保持 tier-1，但整体跨两个 tier-2，准确复现用户
  描述的现象。
- 根因：第一个 SubJob 提交后，`minPartitions=0` 使第二个 SubJob 在新的
  `allocateForJob` 调用中调度。现有 helper 只过滤单个 outer HyperNode dry-run 内的
  `stmtBackup`；当 outer candidate 本身是远端 leaf 时，本地域候选不在这个 map 中，helper
  按 soft fallback 原样保留远端 leaf。之后 outer
  `selectBestHyperNodeForJob` 仍让本地与远端候选参与评分，同分时 map 迭代可选择远端。

### 为什么 `3e6b5c950` 没有覆盖 R2

`3e6b5c950` 的过滤位置在 `allocateForSubJob`：它只过滤当前 outer Job HyperNode
试探下的内层 `stmtBackup`，再交给 `selectBestHyperNodeForSubJob`。它没有过滤
`allocateForJob` 汇总的 `subJobsAllocationScores`，后者才决定当前 gradient 的 outer Job
HyperNode。

R2 第二次 `allocateForJob` 中，A/tier-2 的 sibling leaf 与 B/tier-2 的 leaf 是分别作为
outer candidate 试探的。试探 B leaf 时，内层 `stmtBackup` 只有 B leaf；helper 因没有 A
候选而按 soft fallback 返回原 map。A 与 B 随后都被写入 outer score map，且原提交在调用
`selectBestHyperNodeForJob` 前没有再按 Job anchor 过滤，故远端仍可能胜出。

初始 V1 测试也未暴露这一点：测试插件把 Job gradient 固定为唯一的
`<cluster-top-hypernode>`，于是 A/B tier-1 候选同时存在于同一次内层 `stmtBackup`，正好是
该 helper 可以覆盖的范围；测试还配置 `minAvailable: 4` 和 `minSubGroups: 2`，两个
SubJob 在同一次 `allocateForJob` dry-run 中完成，不会进入 R2 的跨轮路径。

### 已批准的最小修复方案

2026-07-27，用户明确批准基于 `3e6b5c950` 的设计继续实现本方案。

保留现有 SubJob 内 `preferJobSoftTopologyCandidates`，并在
`allocateForJob` 收集完当前 gradient 的 `stmtBackup` 与 `subJobsAllocationScores` 后、
调用 `selectBestHyperNodeForJob` 前，增加同样的 Job soft/tier 过滤：

- 仅在 Job 有 SubGroup policy、Job 为 soft、Job tier 已设置且已有已放置 SubJob 时启用。
- 优先保留 `LCA(job.AllocatedHyperNode, outerCandidate).Tier() <= highestTierAllowed`
  的 outer candidate；如果当前 gradient 没有本地域成功候选，保留全量以维持 soft fallback。
- 仅裁剪本轮已成功 dry-run 的 outer candidate score map，不改 dry-run、Statement、
  predicate、资源筛选、recorder 或总的 allocate 主循环。

实现时以非空且有效的 `job.AllocatedHyperNode` 作为跨轮 predecessor 的充分证据，不再额外
要求扫描到 peer SubJob。该锚点由前一次提交的 `UpdateDecisionToJob` 写入，并会在本轮
dry-run 的 `SnapshotSubJobStatus` / `RecoverSubJobStatus` 之间被保存和恢复；这正是后一个
SubJob 应读取的前一个 SubJob 信息。

该方案解决 R2（有 request / LimitRange 的用户同款清单）。R1 的原始 BestEffort Job 属于
另一条 backfill 路径；若也要求它遵守 Job/SubJob topology，需要单独设计 BestEffort 的
拓扑分组与 gang-ready 语义，不能作为这次“小范围 allocate 修复”的附带改动。

### 已批准的验证用例

- **V4：outer candidate 单测。** 构造已有 A/tier-1 Job anchor、A/B 两个可行 outer
  candidate，且 B 的打分更高；断言新过滤仍选择 A，且 A 不可行时回退 B。
- **V5：跨轮 Action 回归。** `minPartitions: 0`、两个 2-Pod SubJob、有非零 request；先提交
  A 中第一组，再使第二组的 B 候选分数更高，断言第二组仍选择 A/tier-2。
- **V6：Kind 复验。** 使用 `user-job-with-requests.yaml` 的资源拓扑，重复下发，断言每次
  两个 partition 的 LCA 为 tier-1、所有 Pod 的 LCA 为同一个 tier-2；保留 R1 作为
  BestEffort 路径的边界记录。

### 2026-07-27 outer candidate 修复实施结果

#### 修改范围

- `pkg/scheduler/actions/allocate/allocate.go`：在 `allocateForJob` 的所有 outer
  HyperNode dry-run 完成后、`selectBestHyperNodeForJob` 前，新增
  `preferJobSoftTopologyScoreCandidates`。它只筛选已成功 dry-run 的
  `subJobsAllocationScores`：若已有有效的 Job soft anchor，则保留 LCA tier 不高于
  Job `highestTierAllowed` 的 outer candidate；若没有本地成功候选，则返回原 score map，
  保持 soft fallback。
- 没有修改 `allocate` 主循环、gradient、资源过滤、predicate、Statement、Recorder 或
  内层 `preferJobSoftTopologyCandidates`。
- `pkg/scheduler/actions/allocate/placement_test.go`：新增 V4 table-driven 单测，覆盖
  本地域优先、远端唯一可行时 soft fallback、hard/no-anchor/no-tier/no-subgroup-policy
  的不变行为。
- `pkg/scheduler/actions/allocate/allocate_test.go`：新增 V5 完整 Action 回归。它使用
  非 BestEffort request、`minMember=0` / `minSubGroups=0`，使两个 partition 分别进入
  两次 `allocateForJob`；第二次故意让远端 outer candidate 得分更高，断言仍选择首个
  partition 所在 tier-2 的 sibling tier-1。

#### V4 / V5：通过

```bash
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano
go test ./pkg/scheduler/actions/allocate \
  -run 'TestPreferJobSoftTopology(Candidates|ScoreCandidates)|TestAllocateJobSoftAffinityAcross(SubJobs|SeparateOuterCandidates)' \
  -count=1
go test ./pkg/scheduler/actions/allocate \
  -run '^TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates$' \
  -count=20
go test ./pkg/scheduler/actions/allocate -count=1
go test -race ./pkg/scheduler/actions/allocate -count=1
```

结果：全部通过。V5 连续 20 次通过；其测试插件记录的两次 Job gradient anchor 为
`["", "a-t1-0"]`，证明前一个 SubJob 信息在第二次 `allocateForJob` 中仍保存在
`job.AllocatedHyperNode`，且远端 `100` 分候选不会越过本地 `10` 分候选。

#### V6：真实 Kind 验证通过

- 集群：`volcano-dev-008`，1 control-plane + 4 worker，共 5 个可见节点；验证完成后已
  用 `kind delete cluster --name volcano-dev-008` 清理。
- 镜像：从当前工作树构建 `volcanosh/vc-scheduler:dev-008-outer`；未修改的
  controller-manager / webhook-manager 复用本地 `dev-008-retest` 镜像并添加相同测试
  tag。所有镜像仅加载到本地 Kind，未推送。
- Helm：`basic.image_tag_version=dev-008-outer`、
  `basic.image_pull_policy=IfNotPresent`。
- 清单：使用 `user-job-with-requests.yaml`。每个 Pod request/limit 为 5 CPU，两个
  A/tier-2 或 B/tier-2 worker 各有 12 CPU；PodGroup 实际字段为
  `minTaskMember.ps=0`、`minSubGroups=0`，因此确实覆盖 R2 的跨轮 allocate 路径。
- 最终绑定：

  | partition | Pods | Node | tier-1 |
  | --- | --- | --- | --- |
  | 0 | `ps-0`, `ps-1` | `volcano-dev-008-worker4` | `hn-008-b-tier1-worker4` |
  | 1 | `ps-2`, `ps-3` | `volcano-dev-008-worker3` | `hn-008-b-tier1-worker3` |

  每个 partition 的 LCA 都是 tier-1；两个 tier-1 的 LCA 是
  `hn-008-b-tier2`（tier 2），因此四个 Pod 不跨 tier-2。scheduler 日志也显示第一次
  提交后 Job anchor 为 `hn-008-b-tier1-worker4`，随后继续进行下一次 HyperNode
  allocation。

#### 通用门禁记录

`make unit-test` 已执行，但在 macOS / Go 1.26.5 上被仓库无关的 agent Linux build
constraints、network-qos `TC` 接口不匹配，以及 `cmd/network-qos/tools` 的既有 panic
阻断。该失败发生在 scheduler 测试之前/之外；本次改动所在的
`pkg/scheduler/actions/allocate` 已通过 focused test、完整 package test、Docker 镜像编译
及真实 Kind 验证。

## 2026-07-28：基于 release-1.15 的重新设计

### 新基线与分支

- 已同步 `origin/release-1.15`，基线为
  `bdea8d965551f75ba2ba603da7d4af225d9dacf9`。
- 新分支：
  `fix/008-job-soft-topology-across-subjobs-release-1.15`。
- 独立工作树：
  `/Users/vanderchen/workspace/dev/volcano-workspace/volcano-008-r115`。
- 新分支当前仅包含 release-1.15 基线，没有 cherry-pick `3e6b5c950` 或
  `ec7141e5d`，也没有实现代码修改。

### reviewer 意见确认

release-1.15 已包含 `#5206`（提交 `1733c8853`）。该 PR 解决的问题是：原先 NTA 的
HyperNode gradient 只对 hard topology 生效，纯 soft Job/SubJob 不能完整复用
`allocateForJob` 的分层 HyperNode 搜索。它在 Session 初始化时：

1. 将 `highestTierName` 翻译为内部的 `highestTierAllowed`；
2. 把 `JobInfo.NetworkTopology` 的 soft mode 转换成 hard mode，并把 tier 设为虚拟
   cluster-top；
3. 对 soft SubJob 做等价转换，使其进入 hard gradient 路径，但通过 cluster-top tier
   保留跨域回退能力；
4. 只修改 scheduler 内部的深拷贝，不修改
   `job.PodGroup.Spec.NetworkTopology` 中的用户原始声明。

因此不能简单删除或绕过 `convertSoftToHardTopology`。否则纯 soft Job 可能重新回到普通
Node 分配路径，丢失 NTA 的分层 HyperNode 搜索。

旧的 `3e6b5c950` 和 `ec7141e5d` 与该实现存在直接冲突：

- 两个 helper 都以 `job.IsSoftTopologyMode()` 作为 gate；Session 转换后该值恒为
  `false`，helper 不会执行。
- 转换后的 `job.NetworkTopology.HighestTierAllowed` 是 cluster-top tier。即使去掉
  soft-mode gate，所有候选与 Job anchor 的 LCA 都不会超过 cluster-top，候选仍不会被
  区分。
- 旧 helper 在每个 gradient 内部做“首选集为空则立即回退”。若当前低层 gradient 只有
  远端候选、但后续较高层 gradient 存在本地域可行候选，旧逻辑会过早选择远端候选，尚未
  穷尽本地域就触发 soft fallback。

### 新的修改方案：保留转换，按原始 soft 域重排完整 gradient

本方案保留 `#5206` 的 soft-to-hard effective topology，不再移植两个候选 map 过滤
helper。修改集中在 allocate 的 Job/SubJob HyperNode gradient 消费入口。

#### 1. 区分“用户声明”与“调度执行拓扑”

- `job.NetworkTopology` 继续作为转换后的 effective topology，供现有
  `ContainsHardTopology`、NTA hard gradient 和 HyperNode 调度主路径使用。
- Job 原始 soft 意图从未被转换的
  `job.PodGroup.Spec.NetworkTopology` 读取。
- 新增同文件私有解析 helper，只接受以下配置：
  `mode: soft`，并且能够从 `highestTierAllowed` 或
  `highestTierName + Session.HyperNodeTierNameMap` 得到有效 preferred tier。
- 无 tier、tier name 无法解析、没有 PodGroup、不是 soft mode 时不启用重排，保持现有
  release-1.15 行为。
- 不新增 CRD/API 字段，不执行代码生成，也不修改 PodGroup 对象。

#### 2. 将候选顺序改为两阶段 gradient

在 `allocateForJob` 和 `allocateForSubJob` 获取插件合并后的 gradient 后、开始 dry-run
前，对完整 gradient 做稳定重排：

1. 仅当 Job 有 SubGroupPolicy、存在有效的 `job.AllocatedHyperNode`，并且 Job 原始配置
   是可解析的 soft topology 时启用。
2. 对每个 candidate 计算
   `LCA(candidate, job.AllocatedHyperNode)`。
3. LCA tier 不高于用户原始 preferred tier 的 candidate 归为“域内”；其他 candidate
   归为“域外”。
4. 保持原 gradient 层级顺序和每层 candidate 顺序，先返回所有非空域内 gradient，再
   返回所有非空域外 gradient。

例如原顺序为：

```text
tier-1: [A-local, B-remote]
tier-2: [A-local-parent, B-remote-parent]
tier-3: [root]
```

重排后为：

```text
preferred: [A-local] -> [A-local-parent]
fallback:  [B-remote] -> [B-remote-parent] -> [root]
```

现有 allocate 循环仍然选择第一个有可行解的 gradient，因此会先穷尽本地域的各层候选；
只有本地域所有层级均不可行时，才进入域外 gradient。这样同时保留：

- soft 的域内优先；
- 域内无解时的完整跨域回退；
- 域内和域外各自原有的低 tier 优先；
- 现有 predicate、resource filter、plugin score、Statement 和 recorder 行为。

#### 3. 不再做成功候选 map 的后置裁剪

不移植 `preferJobSoftTopologyCandidates` 和
`preferJobSoftTopologyScoreCandidates`，原因是后置裁剪只能看到当前 gradient 的成功
候选，无法判断后续 gradient 是否存在本地域可行解。新的 gradient 重排在 dry-run 前就把
完整搜索次序表达清楚，可同时覆盖内层 SubJob 候选和外层 Job 候选。

#### 4. 行为边界

- 初次调度时 `job.AllocatedHyperNode` 为空，不重排；继续由 `#5206` 的 ascending
  gradient 选择最低可行 tier。首次提交后形成 Job anchor，后续 SubJob/调度轮次才应用
  域内优先。
- 若已有运行 Pod 已经使 Job anchor 扩大到 preferred tier 以上，本方案不会迁移或重新
  聚拢运行 Pod；无法形成域内候选时保持原顺序。
- BestEffort Pod 仍可能走 backfill，不在本次 allocate gradient 修复范围内。
- 本方案是有序贪心搜索，不做组合回溯，不保证多个尚未调度 SubJob 的全局最优布局。

### 新验证用例（待审批）

#### N1：soft-to-hard 兼容性单测

- 目的：证明保留 `#5206` 的调度执行语义，同时仍能读取用户原始 soft tier。
- 范围：framework/API 现有转换测试加 focused helper 测试。
- 设置：
  1. Job 原始为 `soft + highestTierAllowed: 2`；
  2. Job 原始为 `soft + highestTierName`；
  3. hard、无 tier、未知 tier name。
- 断言：
  - `adjustNetworkTopologySpec` 后 effective topology 仍是
    `hard + cluster-top tier`；
  - PodGroup 原始配置保持 soft；
  - 新 helper 对前两种配置解析出原始 tier，对其他配置返回“不启用”。
- 命令：

  ```bash
  cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-008-r115
  go test ./pkg/scheduler/framework ./pkg/scheduler/actions/allocate \
    -run 'Test.*(AdjustNetworkTopology|DeclaredJobSoftTopology)' -count=20
  ```

#### N2：完整 gradient 两阶段排序单测

- 目的：证明域内所有 gradient 均先于域外 gradient，同时保持每一侧的原层级顺序。
- 范围：`pkg/scheduler/actions/allocate` table-driven 单测。
- 覆盖：
  1. 同一 gradient 同时包含本地和远端；
  2. 低层 gradient 只有远端、较高层 gradient 有本地，断言较高层本地先执行；
  3. 本地域没有任何候选，断言完整保留域外 fallback；
  4. hard/no anchor/no policy/无效 tier 不改变原顺序；
  5. anchor 已扩大至 preferred tier 以上时不错误过滤候选。
- 命令：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestPrioritizeJobSoftTopologyGradients$' -count=50
  ```

#### N3：跨 SubJob/跨轮 Action 回归

- 目的：复现一个 SubJob 已形成 Job anchor、另一个 SubJob 后续调度的场景。
- 范围：`pkg/scheduler/actions/allocate` 完整 Action 路径。
- 设置：两个 2-Pod SubJob；Job 原始 soft/tier-2，SubJob soft/tier-1；A/B 两个
  tier-2 分支。构造远端 tier-1 插件分数高于本地 tier-2，并确保本地 tier-2 资源可行。
- 断言：第二个 SubJob 选择本地较高层 gradient，而不是远端较低层 gradient；本地域完全
  不可行时才回退远端。
- 命令：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestAllocateJobSoftTopologyAcrossSubJobsAfterConversion$' -count=50
  ```

#### N4：原 #5206 行为保护

- 目的：避免修复跨 SubJob 问题时破坏原 PR 解决的 soft Job/SubJob HyperNode 搜索。
- 覆盖：
  1. 只有 Job soft topology、没有 SubGroupPolicy；
  2. 只有 SubJob soft topology；
  3. Job hard + SubJob soft；
  4. Job soft + SubJob hard。
- 断言：effective hard conversion、最高可搜索域、最低可行 tier 优先和 hard 上界均与
  release-1.15 基线一致。
- 命令：

  ```bash
  go test ./pkg/scheduler/framework \
    -run 'Test(ConvertSoftToHardTopology|Session_adjustNetworkTopologySpec)' -count=20
  go test ./pkg/scheduler/plugins/network-topology-aware \
    -run 'Test.*HyperNodeGradient' -count=20
  ```

#### N5：Kind 端到端

- 集群：`volcano-dev-008-r115`，1 control-plane + 4 worker，共 5 个节点。
- 拓扑：A/B 两个 tier-2，每个 tier-2 有两个单节点 tier-1。
- 工作负载：两个 2-Pod partition，显式非零 CPU/memory request；
  Job soft/tier-2、partition soft/tier-1，并显式设置 `minPartitions: 1`，使第一个
  partition 提交形成 anchor 后再调度第二个 partition。
- 断言：每个 partition 的 Pod LCA 为 tier-1；四个 Pod 的 LCA 为同一个 tier-2；将该
  tier-2 资源耗尽的负向用例能够回退至另一个 tier-2。
- 命令框架：

  ```bash
  TAG=dev-008-r115 BUILDX_OUTPUT_TYPE=docker make images
  kind create cluster --name volcano-dev-008-r115 --config <kind-config>
  kind load docker-image volcanosh/vc-scheduler:dev-008-r115 --name volcano-dev-008-r115
  kind load docker-image volcanosh/vc-controller-manager:dev-008-r115 --name volcano-dev-008-r115
  kind load docker-image volcanosh/vc-webhook-manager:dev-008-r115 --name volcano-dev-008-r115
  helm upgrade --install volcano installer/helm/chart/volcano \
    --namespace volcano-system --create-namespace \
    --set basic.image_tag_version=dev-008-r115 \
    --set basic.image_pull_policy=IfNotPresent
  ```

- 验证结束后删除任务专用 Kind 集群。

### 实施门禁

获得新方案及 N1-N5 的明确审批后才开始实现。实现后至少执行：

```bash
./hack/update-gofmt.sh
go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware
make unit-test
make verify
```

所有 focused test 与 Kind N5 通过后，再记录镜像、Pod 绑定、LCA、scheduler 日志和清理
结果。旧分支的通过结果不能替代新基线验证。

## 2026-07-28：最终方向——移除 soft-to-hard 转换

> 本节取代上一节“保留转换，按原始 soft 域重排完整 gradient”的方案及 N1-N5。
> 上一节只作为讨论历史保留，不再作为实施依据。

### 结论

建议移除 `convertSoftToHardTopology`，但不能只删除函数。`#5206` 引入转换的原因是当时
缺少原生的 Job/SubJob soft HyperNode 执行链；如果只删转换，`release-1.15` 上的纯
Job soft 会退出 hard HyperNode 路径，SubJob soft 也只会得到输入 HyperNode，属于明确
回归。

`8fcc0012 + 3e6b5c950 + ec7141e5d` 已具备一部分原生 soft 能力，但仍不足以安全替代
转换：

1. `8fcc0012` 的 NTA 对 soft 返回 cluster-wide gradient，但
   `allocateForJob/allocateForSubJob` 在第一个存在可行解的 gradient 立即返回。不同
   gradient 之间没有统一比较，低层远端可行解可能早于较高层本地优选解被提交。
2. `3e6b5c950`、`ec7141e5d` 只在“当前 gradient 的成功候选”中基于既有 Job anchor
   做过滤；它们看不到后续 gradient，也不比较 dry-run 后 Job/SubJob 的最终 LCA。
3. 两个 helper 仅覆盖 Job soft 跨 SubJob 的局部场景，未覆盖纯 Job soft、只有
   SubJob soft、初次调度无 anchor、hard/soft 两种混合方向以及插件间的 soft 权重竞争。
4. soft 仍通过 gradient 顺序影响选择，而 `group-topology-affinity` 的 preferred
   affinity/anti-affinity 通过 `HyperNodeOrderFn` 打分。两种 soft 表达不在同一个比较
   阶段，配置的 plugin weight 可能被“第一个成功 gradient”短路。
5. `release-1.15` 的 gradient framework 是“第一个启用插件获胜”，`8fcc0012` 改为
   多插件取交集。若不显式区分“约束”和“放弃约束”，soft 插件容易错误缩小 hard
   候选集合，或者为了 pass-through 构造一个带有隐式顺序的 full-subtree gradient。

因此目标不是继续修补转换，而是建立统一语义：

```text
hard topology = 可行性约束，只负责缩小候选集合
soft topology = 可行布局的偏好/分数，不删除 fallback 候选
最终选择       = 先满足所有 hard 约束，再按所有 soft 插件的权重比较真实布局
```

### 新分支与集成方式

- 目标上游基线：
  `origin/release-1.15@bdea8d965551f75ba2ba603da7d4af225d9dacf9`。
- 工作分支：
  `fix/008-job-soft-topology-across-subjobs-release-1.15`。
- 工作树：
  `/Users/vanderchen/workspace/dev/volcano-workspace/volcano-008-r115`。
- 当前工作树为干净的 release-1.15 基线。该基线包含 `#5206`，但不包含
  `8fcc0012` 的 SubGroup topology affinity。
- 获批后将把 `8fcc0012` 所依赖的 HyperNode gradient / group topology affinity
  功能栈按语义移植到 release-1.15。`3e6b5c950`、`ec7141e5d` 不直接作为最终实现
  cherry-pick，只保留其复现用例和问题意图；避免把局部过滤方案与新的统一评分链叠加。

### 修改方案

#### 1. Session 只做 tier 名称规范化，不修改 mode

- 保留 `JobInfo.NetworkTopology` 和 `SubJobInfo.NetworkTopology` 的内部深拷贝。
- `adjustNetworkTopologySpec` 仍把 `highestTierName` 翻译为
  `highestTierAllowed`，但 Job/SubJob 的 `mode: soft` 在整个 Session 中保持 soft。
- 删除：
  - `convertSoftToHardTopology`；
  - `SubJobInfo.ConvertToHardTopology`；
  - 以“soft 最终一定变成 hard”为预期的单测。
- PodGroup 原始对象仍不被修改；hard 配置也不被改变。

#### 2. 明确 gradient 的 `Constrain` / `Abstain` 合约

将 HyperNode gradient 结果显式区分为：

- `Constrain`：插件提供 hard 候选集合，framework 对所有 hard 插件结果做交集；
- `Abstain`：插件当前没有 hard 条件，不参与候选交集。

具体行为：

- NTA 的 Job/SubJob hard network topology 返回 `Constrain`；
- NTA 的 soft 或无 network topology 返回 `Abstain`；
- group-topology-affinity 的 required 规则返回 `Constrain`；
- preferred affinity/anti-affinity 不进入约束集合，只在评分阶段生效；
- 没有任何 hard constraint 时，由 framework 构造完整 HyperNode 候选集合；
- 保留 `SearchPurpose`，allocate/preempt/reclaim/evict/simulate 的 hard 行为不变；
  soft 在这些可行性搜索中不得被当成 hard 过滤条件。

这消除了“soft 为了复用 hard 而返回 cluster-top hard gradient”以及“插件为了
pass-through 手工返回 full subtree”的隐式约定。

#### 3. 所有 topology-aware Job 进入 HyperNode 布局链

统一 `RequiresHyperNodeAllocate` 判定，覆盖：

- Job 或 SubJob 的 hard/soft network topology；
- SubGroupPolicy；
- PodGroup/SubGroup required 或 preferred topology affinity/anti-affinity。

这样纯 Job soft 不再依赖转换成 hard 才能进入 HyperNode 路径。无 topology 配置的普通
Job 保持现有 flat Node 路径。

#### 4. soft 使用统一加权评分，不通过过滤表达

- NTA 对 Job/SubJob soft 布局计算分数：
  - 使用 dry-run 后实际分配节点的 LCA，而不是外层 candidate 名称；
  - 有 `highestTierAllowed` 时，域内布局获得 preferred bonus；
  - 域内、域外均保留，LCA tier 越低越紧凑、分数越高；
  - 无显式 tier 时只使用紧凑度分数；
  - 分数乘以 `network-topology-aware` 的 plugin weight。
- group-topology-affinity 继续对 preferred affinity/anti-affinity 打分，并乘以自身
  plugin weight。
- 同一可行布局的各 soft plugin 分数相加。因此同时启用
  `network-topology-aware(weight=100)` 和
  `group-topology-affinity(weight=1000)` 时，不会再被 NTA gradient 的先返回顺序
  覆盖，权重能够真实影响最终选择。
- 所有 soft preferred 域都没有可行解时，域外候选仍存在并自动成为 fallback。

#### 5. 比较所有相关可行布局，再提交一个 Statement

现有 `allocateForJob` / `allocateForSubJob` 的“第一个成功 gradient 即返回”只适合
hard 的低 tier 优先搜索，不能承载多个 soft preference。调整为：

1. hard constraint 先生成候选全集；
2. 对当前 gang transaction 需要的候选进行 dry-run；
3. 保存每个候选的 Statement、剩余 worksheet、最终 Job/SubJob
   `AllocatedHyperNode` 和 plugin score；
4. 存在 soft topology 时跨 gradient 比较所有成功候选；
5. 依据综合 soft score 选择，最终 LCA tier 和稳定名称只作为同分 tie-break；
6. 恢复获胜 Statement，其余 dry-run 状态全部回滚。

多个 SubJob 之间存在 topology affinity/anti-affinity 或 Job soft 关联时，复用
`edaaf83a7` 已验证过的有界联合搜索/状态剪枝思路，比较当前 transaction 的兼容布局，
避免逐个 SubJob 贪心选择造成前一个选择锁死后续布局。没有跨 SubJob 条件的 Job 保持
快速路径。

#### 6. 放置状态必须来自真实 dry-run 结果

- 每个候选尝试前快照 Job/SubJob `AllocatedHyperNode`；
- 每绑定一个 task 就根据真实 Node 更新 SubJob LCA，再更新 Job LCA；
- 失败或未选中的候选恢复快照；
- recorder 只保存最终获胜布局；
- 已有 Running Pod、scheduler 重启恢复的 anchor 和跨调度轮次继续分配都使用同一套
  LCA 逻辑。

#### 7. 明确边界

- 本 issue 的 workload 使用非零 request，走 allocate action；Kind 清单固定为
  nginx、`500m CPU / 200Mi memory`。
- BestEffort 仍由 backfill action 调度。当前转换同样不能让 BestEffort 进入
  allocate，因此删除转换不会新增该回归；但 backfill 目前也不具备 Job 级联合布局
  选择能力。该边界会保留回归用例并在文档中明确，不把 BestEffort 的现状误称为本次
  已完整支持。
- 本次“完整 soft 处理”指带非零 request 的 topology-aware allocate、以及相关
  preempt/reclaim/evict 可行性链；不修改 CRD 字段或用户 API。

### 新验证用例（待重新审批）

#### S1：Session mode 保真与 tier 翻译

- soft Job、soft SubJob 在 `adjustNetworkTopologySpec` 后仍为 soft；
- `highestTierName` 正确翻译到内部 `highestTierAllowed`；
- PodGroup 原始配置不变；
- hard 和 nil topology 不变；
- 旧 converter 及 `ConvertToHardTopology` 不再存在。

```bash
go test ./pkg/scheduler/framework ./pkg/scheduler/api \
  -run 'Test.*(AdjustNetworkTopology|TopologyMode|TierName)' -count=20
```

#### S2：gradient 合约与双插件交集

- hard NTA + hard group rule：只保留两个 hard 条件的交集；
- soft NTA + preferred group rule：两者均不删除候选；
- hard NTA + preferred group rule：候选只受 hard NTA 限制；
- soft NTA + required group rule：候选只受 required group rule 限制；
- 无 hard 插件时返回完整且确定的候选全集；
- allocate 与 evict purpose 的 hard 顺序保持现有语义。

```bash
go test ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  -run 'Test.*HyperNodeGradient' -count=20
```

#### S3：纯 Job/SubJob soft 与 fallback

表驱动覆盖：

1. 只有 Job soft、没有 SubGroupPolicy；
2. Job 无 topology、只有 SubJob soft；
3. Job soft + SubJob soft；
4. 初次调度没有 anchor；
5. 已有 Running Pod 或恢复出的 anchor；
6. preferred 域有资源时不跨域；
7. preferred 域无资源时可跨域；
8. `highestTierAllowed`、`highestTierName` 和无显式 tier。

断言 mode 始终保持 soft，候选不被过滤，最终布局按实际 LCA 评分。

#### S4：hard/soft 混合矩阵

- Job hard + SubJob soft：Job hard tier 严格限制全集，SubJob soft 只在全集内排序并可在
  该全集内 fallback；
- Job soft + SubJob hard：SubJob hard 必须满足，Job soft 比较所有满足 hard 的布局；
- Job hard + SubJob hard：与 release-1.15 现有行为一致；
- hard 不可满足时不得因 soft fallback 越过 hard 边界。

#### S5：跨 SubJob、跨 gradient、跨轮回归

- 两个 2-Pod SubJob；Job soft/tier-2，SubJob soft/tier-1；
- 构造“远端低层 gradient 可行、本地较高层 gradient 也可行”，验证不会在远端低层
  首次成功后提前返回；
- 同一 transaction 和分两轮 transaction 均验证；
- preferred 域资源耗尽时验证远端 fallback；
- 重复 50 次，绑定和 tie-break 结果稳定。

```bash
go test ./pkg/scheduler/actions/allocate \
  -run 'Test.*(SoftTopology|TopologyLayout|AcrossSubJobs)' -count=50
```

#### S6：四个独立 SubJob 的双插件原始场景

- 4 个独立 SubJob：`prefill-0`、`prefill-1`、`decode-0`、`decode-1`；
- preferred SubGroup anti-affinity：两个 prefill 不在同一 hypernode，两个 decode 不在
  同一 hypernode；
- preferred SubGroup affinity：prefill/decode 位于同一 hyperinstance-group；
- 同时开启：
  - `network-topology-aware weight=100`；
  - `group-topology-affinity weight=1000`；
- 验证加权分数参与跨 gradient/布局选择，而不是被 NTA 首个成功 gradient 短路。

预期四个 Pod 位于 `A+B` 或 `C+D`，且所选 pair 的每个 hypernode 上各有一个
prefill 和一个 decode。

#### S7：preempt/reclaim/evict 与状态恢复

- soft topology 在可行性/victim gradient 中不删除候选；
- hard topology 的 allocate 与 reverse/capped eviction gradient 不变；
- dry-run 失败、候选落选、Statement discard 后 Job/SubJob anchor 完整恢复；
- scheduler restart 从 Running Pod 恢复 LCA 后，下一轮 soft 排序继续生效。

#### S8：Kind 端到端

集群 `volcano-dev-008-r115`：

- 1 control-plane + 4 worker，共 5 个 Kubernetes Node；
- A/B 属于 instance-group-1，C/D 属于 instance-group-2；
- 每个 worker 对应一个单节点 hypernode；
- 所有 Pod 使用 nginx，request/limit 为 `500m CPU / 200Mi memory`。

Kind 至少执行两个 workload：

1. 原始双插件四 SubJob 用例：结果必须为 `A+B` 或 `C+D`，所选 pair 每个节点均为
   `1 prefill + 1 decode`；
2. Job soft/tier-2 + 两个 SubJob soft/tier-1：preferred pair 有资源时四 Pod LCA
   不超过 tier-2；耗尽 preferred pair 后允许 fallback 到另一个 pair。

记录本地镜像 tag、Helm values、Pod→Node、Job/SubJob LCA、两个插件的候选/分数日志和
最终 PASS/FAIL；完成后删除任务专用 Kind 集群。

### 实施门禁

该方案改变了上一轮已经讨论过的实现方向，因此必须重新审批。获得 S1-S8 明确批准后才
进行 feature stack 集成和实现；实施完成后执行 focused tests、相关 package tests、
`make unit-test`、`make verify` 和真实 Kind S8。任何用例若需弱化或更改，先更新本文件并
重新确认。

## 2026-07-28：最终方案实施与验证结果

### 实施范围

- 分支：
  `fix/008-job-soft-topology-across-subjobs-release-1.15`。
- 基线：
  `origin/release-1.15@bdea8d965551f75ba2ba603da7d4af225d9dacf9`。
- 以 squash 方式移植 `8fcc0012` 所需的 SubGroup topology affinity、HyperNode
  gradient、多插件和联合布局功能栈，并按 release-1.15 的 API、cache 和
  `SearchPurpose` 接口完成适配；没有直接移植 `3e6b5c950` /
  `ec7141e5d` 的候选过滤 helper。
- 删除 `convertSoftToHardTopology` 和
  `SubJobInfo.ConvertToHardTopology`。Session 只翻译 tier name，Job/SubJob
  的 soft mode 在整个调度周期保持不变。
- HyperNode gradient 改为显式
  `HyperNodeGradientResult{Applied, Gradients}`：
  hard 规则返回 `Constrain`，soft 或不适用规则返回 `Abstain`。framework
  只对已应用的 hard 结果做交集；全部 abstain 时使用完整候选域。
- NTA soft 和 group-topology-affinity preferred 均不再过滤候选；它们根据
  dry-run 后的真实 Job/SubJob LCA 评分，并分别乘以插件 weight。required 规则仍是
  hard 可行性约束。
- `RequiresHyperNodeAllocate` 覆盖 Job/SubJob hard/soft、SubGroupPolicy 以及全部
  PodGroup/SubGroup topology affinity；不再依赖把 soft 伪装成 hard 才进入
  HyperNode 分配链。
- 对存在跨 SubJob topology 关系的 Job 启用有界联合布局搜索。完整可行布局先比较
  合并后的插件加权分数，实际 LCA 和稳定名称只用于同分 tie-break；所有未获胜
  Statement 和 Job/SubJob LCA 均恢复。
- 第一个 hard gradient 保留原有层级/候选顺序，后续 hard 插件只过滤成员，避免交集
  破坏 NTA 的 ascending allocate 或 reverse/capped eviction 顺序。

### 代码生成与静态门禁

已执行：

```bash
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-008-r115/staging/src/volcano.sh/apis
./hack/update-codegen.sh

cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-008-r115
make manifests generate-yaml
TAG=1.15.0 make generate-charts
GOPATH=/Users/vanderchen/go make verify
git diff --check HEAD
```

结果：全部通过。首次不显式提供 `GOPATH` 的 `make verify` 因本机 shell 中该变量未定义而
在脚本入口停止；按仓库预期提供主机 GOPATH 后完整通过。`generate-charts` 首次使用默认
`latest` 因 chart 要求 semver 失败，改用 release 对应的 `TAG=1.15.0` 后通过。

### S1-S7：单元及 package 回归

下列命令全部通过：

```bash
go test ./pkg/scheduler/framework ./pkg/scheduler/api \
  -run 'Test.*(AdjustNetworkTopology|TopologyMode|TierName)' -count=20

go test ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  -run 'Test.*HyperNodeGradient' -count=20

go test ./pkg/scheduler/actions/allocate \
  -run 'Test.*(SoftTopology|TopologyLayout|AcrossSubJobs)' -count=50

go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/actions/allocate ./pkg/scheduler/actions/utils \
  ./pkg/scheduler/cache ./pkg/webhooks/admission/podgroups/validate
```

- S5/S6 的 50 次稳定性回归耗时 77.461s，全部通过。
- 新增的四独立 SubJob 精确回归
  `TestAllocateWithPreferredSubGroupAffinityAndAntiAffinityAcrossIndependentSubJobs`
  使用 `prefill-0`、`prefill-1`、`decode-0`、`decode-1`，每个 Pod 为
  `500m / 200Mi`，NTA weight=100、GTA weight=1000；断言一个 instance-group
  内两个 hypernode 各有一个 prefill 和一个 decode，已通过。
- framework 新增回归验证：第一个 hard 插件的 gradient 层级顺序在多插件交集后保持
  不变，覆盖 allocate 与 eviction 顺序语义。
- `make unit-test` 也已执行。所有 scheduler、controller 和 webhook 相关 package
  通过；完整命令仍被 release-1.15 在 macOS 环境中的既有问题阻断，包括 Linux-only
  agent/cgroup build constraints、benchmark `TestMain` 的 kubeconfig 前提、
  network-qos eBPF 平台测试以及 third_party/mindcluster configmap 测试。上述失败不经过
  本次修改路径，不能据此宣称完整 `make unit-test` 通过。

### 本地镜像

首次执行默认 image target 时，Docker Hub 获取 `golang:1.25.0` token 遇到连接重置。
随后使用仓库 host build 目标构建 Linux ARM64 静态二进制：

```bash
make -j3 vc-scheduler vc-controller-manager vc-webhook-manager
```

再以本机已有的对应 Volcano runtime image 为运行时层替换二进制，构建并加载：

| Image | SHA256 |
| --- | --- |
| `volcanosh/vc-scheduler:dev-008-r115` | `bd4cb77beb92add77a84e566c87b293bab7fbe931904ff815612ae6ca8f86e4f` |
| `volcanosh/vc-controller-manager:dev-008-r115` | `1b3879cd29a9576f72e816e44095efd72198e089dce0de854094e70964f15100` |
| `volcanosh/vc-webhook-manager:dev-008-r115` | `52804315b479fe2a8d6d9bfe631aba405522800a281e39a91c3d273612d3384a` |

三个镜像均为 ARM64，仅加载到本地 Kind，没有推送远端。

### S8：真实 Kind 端到端

#### 集群与安装

- 集群：`volcano-dev-008-r115`。
- Kubernetes：v1.36.1 / ARM64。
- 拓扑：1 control-plane + 4 worker，共 5 个可见 Kubernetes Node，未超过 8 节点限制。
- worker A/B 属于 `instance-group-ab`，C/D 属于 `instance-group-cd`；每个 worker
  对应一个 tier-1 HyperNode；两个 instance-group 属于 tier-3
  `hypercluster-all`。
- HyperNode status：
  `hn-a/b/c/d nodeCount=1`，
  `instance-group-ab/cd nodeCount=2`，
  `hypercluster-all nodeCount=2`。tier-3 的 nodeCount 按 HyperNode 树的直接子成员计数。
- Helm scheduler 配置同时启用：

  ```yaml
  - name: network-topology-aware
    enableNodeOrder: true
    enabledHyperNodeOrder: true
    enabledHyperNodeGradient: true
    arguments:
      weight: 100
  - name: group-topology-affinity
    enabledHyperNodeOrder: true
    enabledHyperNodeGradient: true
    arguments:
      weight: 1000
  ```

- scheduler、controller-manager 和 admission 均为 Running。

#### S8.1：原始双插件、四独立 SubJob

先创建 PodGroup，再创建四个单副本 Deployment；每个 Pod 使用
`nginx:1.27-alpine`、`500m CPU / 200Mi memory`。

最终绑定：

| Pod role/SubJob | Node | HyperNode |
| --- | --- | --- |
| `prefill-0` | A / `volcano-dev-008-r115-worker` | `hn-a` |
| `decode-0` | A / `volcano-dev-008-r115-worker` | `hn-a` |
| `prefill-1` | B / `volcano-dev-008-r115-worker2` | `hn-b` |
| `decode-1` | B / `volcano-dev-008-r115-worker2` | `hn-b` |

四个 Pod 均为 Running，PodGroup 为 Running（running=4）。结果为允许的 A+B；
两个 hypernode 各有一个 prefill 和一个 decode。调度日志记录：

```text
Topology allocation search completed ... job=default/pg-myq ... exploredStates=341 prunedStates=340
Allocate job to hyperNode success ... hyperNode=instance-group-ab
update allocated hyperNode for subJob ... prefill-p0 ... hn-a
update allocated hyperNode for subJob ... decode-d0 ... hn-a
update allocated hyperNode for subJob ... prefill-p1 ... hn-b
update allocated hyperNode for subJob ... decode-d1 ... hn-b
```

GTA preferred 日志同时记录 `pluginWeight=1000`；ConfigMap 证据确认 NTA weight=100。
结论：S8.1 PASS。

#### S8.2：纯 soft 的四 Pod 首选与回退

清单包含两个独立 SubJob（`role-a`、`role-b`），每个 SubJob 两个 Pod：

- Job：soft / tier-2 `hyperinstance-group`；
- SubJob：soft / tier-1 `hypernode`；
- 共四个 nginx Pod，每个 `500m CPU / 200Mi memory`。

资源充足时四个 Pod 全部调度至 A / `hn-a`，Job LCA 和两个 SubJob LCA 均为 tier-1，
满足“不超过 tier-2”且选择最紧凑可行布局。

随后删除工作负载，在 A、B 各直接绑定一个 Running filler Pod，每个请求 11500m CPU；
两个 12-CPU worker 的已请求量均为 11600m，仅剩 400m，不能容纳任一 500m workload
Pod。重新创建完全相同的四 Pod workload 后：

| Pod | Node | HyperNode |
| --- | --- | --- |
| 2 × `role-a` | C / `volcano-dev-008-r115-worker3` | `hn-c` |
| 2 × `role-b` | C / `volcano-dev-008-r115-worker3` | `hn-c` |

四个 Pod 均为 Running，PodGroup 为 Running（running=4）。日志显示搜索仍保留
`instance-group-ab`、`instance-group-cd`、`hypercluster-all` 和 cluster-top 的可行
候选，最终：

```text
Topology allocation search completed ... hyperNode=instance-group-cd
Topology allocation search completed ... hyperNode=hypercluster-all
Topology allocation search completed ... hyperNode=<cluster-top-hypernode>
Allocate job to hyperNode success ... hyperNode=hn-c
update allocated hyperNode for subJob ... role-a-a0 ... hn-c
update allocated hyperNode for subJob ... role-b-b0 ... hn-c
```

这证明 soft 在 preferred 域资源不足时没有被 hard 过滤，能够回退至另一个
instance-group；S8.2 PASS。

### 最终结论

- S1-S8 全部通过。
- `convertSoftToHardTopology` 可以由本次原生 soft 执行链替代；soft 不再伪装成 hard，
  hard 约束、soft 加权偏好和 fallback 三种语义已分离。
- 原始同时启用 NTA/GTA 的四独立 SubJob 场景符合预期；首选资源耗尽时的纯 soft
  fallback 也通过真实 Kind 验证。
- 已执行 `kind delete cluster --name volcano-dev-008-r115`，5 个任务节点均已删除。
- 原单一提交 `6555947d0` 已按 reviewer 可检视边界拆分为：
  1. `f7bf42289 feat(scheduler): port subgroup topology affinity to release-1.15`
     —— 64 个文件，移植 1.14 PodGroup/SubGroup topology affinity 功能栈，并保留
     release-1.15 的 `convertSoftToHardTopology` 兼容行为；
  2. `e640c0755 fix(scheduler): stop converting soft topology to hard`
     —— 仅修改 5 个文件，删除 `convertSoftToHardTopology` /
     `SubJobInfo.ConvertToHardTopology`，改为 Session mode 保真，并恢复
     Job hard + SubGroup soft 的 mixed-mode allocate 用例。
- 拆分后 `e640c0755^{tree}` 与原 `6555947d0^{tree}` 均为
  `23d3b537a34f665e0e1197ffbfd75350bb461746`，最终源码逐字节一致，因此此前 Kind
  端到端证据仍对应当前最终代码。
- 拆分后额外验证：

  ```bash
  go test ./pkg/scheduler/api ./pkg/scheduler/framework \
    ./pkg/scheduler/plugins/network-topology-aware \
    ./pkg/scheduler/plugins/group-topology-affinity \
    ./pkg/scheduler/actions/allocate

  go test ./pkg/scheduler/framework ./pkg/scheduler/api \
    -run 'Test.*(AdjustNetworkTopology|TopologyMode|TierName|ConvertToHard)' -count=1
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestAllocateWithPartitionPolicyNetworkTopology$' -count=1
  ```

  第一提交保留 converter 时，相关 package 均通过；第二提交的 mode 保真和 mixed-mode
  focused 回归均通过。原提交保留在备份分支
  `backup/fix-008-r115-before-split-20260728`；新提交尚未推送。

## 2026-07-28：提交历史修正（取代上述两提交拆分）

reviewer 指出 `e640c0755` 只删除了 converter，没有体现此前方案中“跨 gradient
完整布局比较、NTA/GTA 联合加权评分、hard/soft 混合和 fallback”等原生 soft
能力。上述 `f7bf42289 + e640c0755` 的两提交拆分因此作废，但保留备份引用
`backup/fix-008-r115-two-commit-split-20260728` 供追溯。

按新的检视边界重新组织历史：

1. 从
   `origin/release-1.15@bdea8d965551f75ba2ba603da7d4af225d9dacf9`
   重新移植原 1.14 topology-affinity 提交栈，保留原提交顺序和提交说明，而不是
   squash 成一个 release-1.15 port 提交。最终 29 个原提交分别落在
   `97280e61a` 至 `d721b668d`；其中 SubGroup topology affinity 功能提交为
   `07015c537`，原生 soft 的通用化及兼容布局评分提交为
   `a400b07bf`、`d721b668d`。
2. 在原提交栈之后新增
   `c41125816 fix(scheduler): complete native soft topology on release-1.15`。
   该提交集中表示“原 1.14 功能栈在 release-1.15 上仍缺少的最终差异”：
   删除 `convertSoftToHardTopology` 和 `SubJobInfo.ConvertToHardTopology`；
   保持 `Applied/Abstain` hard 约束契约；补齐跨 gradient 完整布局搜索、
   NTA/GTA 插件权重联合评分、跨轮锚点及 soft fallback；同时保留
   release-1.15 的 `SearchPurpose`、allocation/eviction gradient 顺序和资源
   预过滤语义，并纳入 release-1.15 所需生成物。
3. 分支
   `fix/008-job-soft-topology-across-subjobs-release-1.15`
   已更新到 `c41125816b44c5ed2d023edc0f0bf90bf8024436`。

最终树校验：

```text
c41125816^{tree} = 23d3b537a34f665e0e1197ffbfd75350bb461746
6555947d0^{tree} = 23d3b537a34f665e0e1197ffbfd75350bb461746
```

因此新历史的最终源码与已完成 S8 Kind 端到端验证的原提交逐字节一致；S8.1
双插件四独立 SubJob 和 S8.2 纯 soft fallback 的验证证据仍然有效。

提交重组后又在 `c41125816` 上执行：

```bash
go test ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/actions/utils \
  ./pkg/scheduler/api
```

结果全部通过：

```text
ok volcano.sh/volcano/pkg/scheduler/framework
ok volcano.sh/volcano/pkg/scheduler/plugins/group-topology-affinity
ok volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware
ok volcano.sh/volcano/pkg/scheduler/actions/allocate
ok volcano.sh/volcano/pkg/scheduler/actions/utils
ok volcano.sh/volcano/pkg/scheduler/api
```
