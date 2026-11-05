> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 013：v1.14 生产版原生 Soft Topology 回移提案

## 审批状态

- 当前阶段：Implemented and verified
- 实现状态：完成
- Proposal：已获用户批准
- 验证用例：已获用户批准并执行
- 工作分支：`fix/013-native-soft-topology-v1.14-backport`
- 源码基线：`b8af4d766`

用户批准后已完成实现、相关回归、构建和 5 节点 Kind preferred/fallback 验证。

## 结论

不直接 cherry-pick release-1.15 的 `a0e218fd0`。在 v1.14 生产基线上移植相同的
hard/soft 边界，但保持旧版 callback 和 action 架构：

```text
hard / required
  -> Applied=true
  -> 参与候选交集

soft / preferred / 不适用
  -> Applied=false
  -> 不参与 hard 候选过滤
  -> 继续由 HyperNodeOrderFn 和 Bug 008 helper 评分/选择

所有 plugin 都 abstain
  -> 从调用 root 构造完整、确定性、低 tier 优先的候选 universe
```

## 为什么不能直接使用 release-1.15 的 012

release-1.15 的 012 基于 `a31ab3dd3`，其中 HyperNode gradient callback 已包含
`SearchPurpose`，preempt/reclaim/evict 也存在额外调用路径。

当前 v1.14 生产基线中：

- gradient 只被 allocate action 调用；
- callback 没有 `SearchPurpose`；
- 没有 `actions/utils.GetCandidateDomains` 等 eviction gradient 路径；
- `session_plugins.go` 仍包含旧版 plugin layer order 保留逻辑。

因此生产 backport 不引入 `SearchPurpose`。只修改 callback 返回值，减少 API 和
调用点范围：

```go
type HyperNodeGradientResult struct {
    Applied   bool
    Gradients [][]*HyperNodeInfo
}

type HyperNodeGradientForJobFn func(
    job *JobInfo,
    root *HyperNodeInfo,
) HyperNodeGradientResult
```

SubJob callback 同理。

## 修改方案

### 1. 保留内部 topology 副本，删除 mode conversion

修改：

- `pkg/scheduler/framework/session.go`
- `pkg/scheduler/api/sub_job_info.go`

行为：

- 保留 #5206 的 `JobInfo.NetworkTopology` 和 `SubJobInfo.NetworkTopology`；
- 保留 `highestTierName -> highestTierAllowed` 翻译；
- 删除 `convertSoftToHardTopology` 调用及函数；
- 删除 `SubJobInfo.ConvertToHardTopology`；
- hard/soft mode 在整个 Session 生命周期中保持用户声明语义；
- 不恢复旧 `updateJobTier`，PodGroup 原始 spec 不被 scheduler 修改。

### 2. 纯 soft workload 显式进入 HyperNode allocate

修改 `JobInfo.RequiresHyperNodeAllocate()`：

```text
ContainsHardTopology()
  -> ContainsNetworkTopology()
```

同时保留：

- `ContainsSubJobPolicy()`；
- required/preferred PodGroup topology；
- required/preferred SubGroup topology。

没有 topology 的普通 Job 继续走普通 Node allocate。

### 3. 增加生产版 Applied/Abstain contract

修改：

- `pkg/scheduler/api/types.go`
- `pkg/scheduler/framework/session_plugins.go`
- 相应测试。

规则：

- `Applied=true + non-empty`：hard constraint 的可用候选；
- `Applied=true + empty`：hard constraint 无可行候选；
- `Applied=false`：soft/preferred/不适用，不参与交集；
- 所有 plugin abstain：返回调用 root 下完整 universe；
- universe 按 tier 升序、同 tier 按 HyperNode name 排序；
- 至少一个 hard plugin applied：只在 hard 结果中求交；
- hard empty 不允许被 all-abstain fallback 覆盖。

保留 v1.14 的 callback 参数，不引入 `SearchPurpose`。

### 4. Network-topology-aware

修改 `pkg/scheduler/plugins/network-topology-aware/`：

- Job hard：Constrain；
- SubJob hard：Constrain；
- Job/SubJob soft：Abstain；
- 未配置 NTA：Abstain；
- hard gradient 仍使用现有 tier、allocated anchor 和 min-resource 行为；
- `HyperNodeOrderFn` 保持原实现，用于 soft preference。

### 5. Group-topology-affinity

修改 `pkg/scheduler/plugins/group-topology-affinity/`：

- required PodGroup affinity/anti-affinity：Constrain；
- required SubGroup affinity/anti-affinity：Constrain；
- preferred-only：Abstain；
- 无适用规则：Abstain；
- hard 构建错误：Constrain(empty)，不得误放宽 required 规则；
- preferred scoring 继续通过 `HyperNodeOrderFn` 执行。

### 6. Bug 008 helper 使用内部 topology

修改 `pkg/scheduler/actions/allocate/allocate.go`：

- `preferJobSoftTopologyCandidates` 读取 `job.NetworkTopology`；
- `preferJobSoftTopologyScoreCandidates` 读取 `job.NetworkTopology`；
- 不再读取或修改 `job.PodGroup.Spec.NetworkTopology`；
- preferred domain 无可行候选时继续返回完整 fallback。

### 7. 与 009 的边界

本分支明确不包含 `473c6538b`。

013 完成后：

- 013 提供 production-compatible hard/soft candidate contract；
- 009 再基于 013 单独移植 multi-root `{B,C}` hard candidate domain；
- 009 的合成 domain key 不参与 LCA；
- 013 的 soft preference 只读取最终实际 Job/SubJob placement。

## 预计源码范围

实现预计修改：

- `pkg/scheduler/api/job_info.go`
- `pkg/scheduler/api/sub_job_info.go`
- `pkg/scheduler/api/types.go`
- `pkg/scheduler/framework/session.go`
- `pkg/scheduler/framework/session_plugins.go`
- `pkg/scheduler/plugins/network-topology-aware/`
- `pkg/scheduler/plugins/group-topology-affinity/`
- `pkg/scheduler/actions/allocate/allocate.go`
- 上述路径对应测试。

不修改：

- staging API、CRD、generated code；
- controller、webhook；
- preempt/reclaim/shuffle；
- recorder 数据结构；
- 009 multi-root allocate 主逻辑。

## 验证用例

### U1：Session 保留 topology mode

- 目的：证明 tier name 被翻译，但 soft/hard mode 不被改写。
- 范围：framework 单测。
- 设置：
  - Job soft + tier name；
  - SubJob soft + tier name；
  - Job hard + numeric tier；
  - 无效 tier name；
  - 无 cluster-top。
- 命令：

  ```bash
  go test ./pkg/scheduler/framework \
    -run 'TestSession_adjustNetworkTopologySpec|TestAdjustNetworkTopologySpec' \
    -count=1
  ```

- 预期：
  - tier name 正确翻译；
  - soft 仍为 soft；
  - hard 仍为 hard；
  - PodGroup 原始 spec 不变。
- 通过标准：全部断言通过。

### U2：Gradient Applied/Abstain

- 目的：证明 soft 不缩小 hard 候选，hard empty 不被放宽。
- 范围：framework 单测。
- 设置：
  1. 所有 plugin abstain；
  2. 一个 hard + 一个 abstain；
  3. 两个 hard 结果求交；
  4. hard empty；
  5. 无注册 plugin；
  6. Job/SubJob callback。
- 命令：

  ```bash
  go test ./pkg/scheduler/framework -run 'Test.*HyperNodeGradient' -count=1
  ```

- 预期：
  - all-abstain 返回完整确定性 universe；
  - abstain 不改变 hard 结果；
  - hard empty 保持 empty；
  - 同 tier 候选顺序稳定。
- 通过标准：成员、顺序和统计全部符合预期。

### U3：NTA/GTA hard-soft 矩阵

- 目的：验证两个 topology plugin 的 contract。
- 范围：plugin 单测。
- 设置：
  - NTA Job/SubJob hard、soft、无 topology；
  - GTA required、preferred-only、无规则；
  - NTA soft + GTA required；
  - NTA hard + GTA preferred。
- 命令：

  ```bash
  go test ./pkg/scheduler/plugins/network-topology-aware \
    ./pkg/scheduler/plugins/group-topology-affinity -count=1
  ```

- 预期：
  - 只有 hard/required 返回 Applied；
  - soft/preferred-only 返回 Abstain；
  - hard error 返回 applied empty；
  - preferred scoring 不被删除。
- 通过标准：矩阵全部通过。

### U4：纯 soft 进入 HyperNode allocate

- 目的：覆盖移除 conversion 后的入口缺口。
- 范围：API 和 allocate 单测。
- 设置：
  1. 只有 Job soft、没有 SubGroupPolicy；
  2. 只有 SubJob soft；
  3. 完全无 topology。
- 命令：

  ```bash
  go test ./pkg/scheduler/api ./pkg/scheduler/actions/allocate \
    -run 'Test.*RequiresHyperNodeAllocate|Test.*Soft.*HyperNode' -count=1
  ```

- 预期：
  - 前两种进入 HyperNode allocate；
  - 普通 Job 不进入。
- 通过标准：入口判断全部符合预期。

### U5：生产基线已复现的 Bug 008 回归

- 目的：将 `b8af4d766` 上已失败的测试固定为完成门禁。
- 范围：allocate 单测。
- 命令：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run '^(TestAllocateJobSoftAffinityAcrossSubJobs|TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates|TestPreferJobSoftTopologyCandidates|TestPreferJobSoftTopologyScoreCandidates)$' \
    -count=1
  ```

- 预期：
  - preferred 域可行时保持配置 LCA；
  - 跨 SubJob 和跨 allocate 调用保持已有 anchor；
  - preferred 域不可行时不删除 fallback。
- 通过标准：四组测试全部通过。

### U6：Mixed hard/soft 与 fallback

- 目的：证明 soft fallback 不放宽 hard tier。
- 范围：allocate 单测。
- 设置：
  - Job hard + SubJob soft；
  - Job soft + SubJob hard；
  - NTA soft + required PodGroup anti-affinity；
  - NTA hard + preferred group affinity；
  - preferred 域资源不足、远端域可用；
  - hard 域不可满足。
- 命令：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'Test.*Soft.*Fallback|Test.*Mixed.*Topology' -count=1
  ```

- 预期：
  - soft 可 fallback；
  - hard 永不越界；
  - required group topology 始终参与过滤。
- 通过标准：所有场景通过。

### U7：相关包回归

- 命令：

  ```bash
  go test ./pkg/scheduler/api ./pkg/scheduler/framework \
    ./pkg/scheduler/actions/allocate \
    ./pkg/scheduler/plugins/network-topology-aware \
    ./pkg/scheduler/plugins/group-topology-affinity -count=1
  ```

- 通过标准：退出码为 0。

### B1：格式、构建和验证

- 命令：

  ```bash
  ./hack/update-gofmt.sh
  make vc-scheduler
  make verify
  ```

- 通过标准：命令退出码均为 0。

### K1：Kind preferred/fallback

- 集群：`volcano-dev-013`；
- 拓扑：1 control-plane + 4 worker，共 5 Node；
- HyperNode：
  - 每个 worker 一个 tier1；
  - 两个 tier1 组成一个 tier2 pair；
  - 两个 tier2 组成 tier3；
- workload：
  - Job soft tier2；
  - 两个 SubJob soft tier1；
  - preferred pair 资源充足场景；
  - preferred pair 资源不足、另一 pair 可用的 fallback 场景。
- 预期：
  - preferred 场景实际 LCA 不超过配置 tier；
  - fallback 场景最终 Running，可越过 soft tier；
  - 日志中不再出现 soft-to-hard conversion；
  - hard/required 规则无违反。
- 通过标准：两种 workload 均成功，placement、LCA 和日志记录完整。

## 基线结果

### 生产基线构造

```text
branch: fix/013-native-soft-topology-v1.14-backport
base without 009 fix: ec7141e5d
#5206 backport: b8af4d766
excluded: 473c6538b
```

### 2026-07-29 聚焦测试

命令：

```bash
go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/actions/allocate -count=1
```

结果：

```text
pkg/scheduler/api          PASS
pkg/scheduler/framework    PASS
pkg/scheduler/actions/allocate FAIL
```

allocate 失败仅记录为 013 的已知基线：

```text
TestAllocateJobSoftAffinityAcrossSubJobs
TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates
TestPreferJobSoftTopologyCandidates
TestPreferJobSoftTopologyScoreCandidates
```

在实现前不弱化或删除这些断言。

## 实施结果

### 生产版 hard/soft contract

已按提案完成：

- 删除 Session 中 Job/SubJob soft-to-hard conversion，保留 topology 内部副本及
  `highestTierName -> highestTierAllowed` 翻译；
- `RequiresHyperNodeAllocate()` 改为识别所有 network topology，包括纯 soft；
- gradient callback 返回 `HyperNodeGradientResult{Applied, Gradients}`；
- all-abstain 从调用 root 构建完整、tier 升序、同 tier 名称排序的候选 universe；
- hard/required 结果参与交集，hard empty 保持不可调度；
- NTA soft 和 GTA preferred-only abstain；
- NTA hard、required PodGroup/SubGroup topology constrain；
- Bug 008 的两个 soft helper 改为读取 `job.NetworkTopology` 内部副本；
- 不引入 release-1.15 `SearchPurpose`、eviction gradient 或 009 multi-root 主逻辑。

### U1-U7

以下门禁全部通过：

```bash
go test ./pkg/scheduler/framework \
  -run 'TestSession_adjustNetworkTopologySpec|TestAdjustNetworkTopologySpec' \
  -count=1

go test ./pkg/scheduler/framework \
  -run 'Test.*HyperNodeGradient' -count=1

go test ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity -count=1

go test ./pkg/scheduler/api ./pkg/scheduler/actions/allocate \
  -run 'Test.*RequiresHyperNodeAllocate|Test.*Soft.*HyperNode' -count=1

go test ./pkg/scheduler/actions/allocate \
  -run '^(TestAllocateJobSoftAffinityAcrossSubJobs|TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates|TestPreferJobSoftTopologyCandidates|TestPreferJobSoftTopologyScoreCandidates)$' \
  -count=1

go test ./pkg/scheduler/actions/allocate \
  -run 'Test.*Soft.*Fallback|Test.*Mixed.*Topology' -count=1

go test ./pkg/scheduler/api ./pkg/scheduler/framework \
  ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity -count=1
```

说明：

- U4 的入口断言位于 `pkg/scheduler/api`，因此同一命令中的 allocate package 显示
  `no tests to run`，但 API 断言确实执行并通过；
- U6 初次执行发现原 v1.14 表测名
  `TestAllocateWithPartitionPolicyNetworkTopology` 不匹配提案正则。未把空匹配视为
  通过；仅将该现有完整表测更名为
  `TestMixedNetworkTopologyAllocationWithPartitionPolicy` 后，使用原 U6 命令重新执行，
  用时约 20 秒并通过；
- 基线失败的四组 Bug 008 回归均已转为 PASS；
- U7 五个相关 package 全部通过，allocate 完整 package 用时约 42 秒。

### B1：格式、构建与 repository verify

```bash
./hack/update-gofmt.sh
make vc-scheduler
GOPATH=/Users/vanderchen/go make verify
git diff --check
```

结果：全部通过。

- 首次直接执行 `make verify` 时，脚本因宿主环境没有导出 `GOPATH` 而在
  `generate-internal-groups.sh` 退出；读取 `go env GOPATH` 得到
  `/Users/vanderchen/go` 后，用上述显式环境重跑通过；
- codegen verify 临时把 `k8s.io/code-generator` 改为 `v0.36.3` 并在仓库根目录
  生成 `volcano.sh/`。验证后已把 `go.mod` 精确恢复为 `v0.34.1`，临时生成目录移至
  `/private/tmp/volcano-013-verify-generated-20260729`，没有 API/generated 变更进入提交；
- `update-gofmt.sh` 发现两个既有 topology 测试夹具字段未对齐，纯格式变更单独提交。

`make unit-test` 也已执行。所有 scheduler（包括 allocate、framework、NTA、GTA）、
controller 和 webhook 路径通过；总目标仍被 macOS/ARM64 既有平台问题阻断：

- Linux-only agent/cgroup package build constraints；
- network-qos/runc 的 Linux syscall/interface build 失败；
- network-qos eBPF 平台测试。

这些失败不经过本次 scheduler 修改路径，因此不宣称完整 `make unit-test` 通过。

## K1：5 节点 Kind preferred/fallback

### 本地镜像

当前 013 工作树交叉编译 Linux/ARM64 二进制：

```bash
GOOS=linux GOARCH=arm64 TAG=dev-013 \
  make -j3 vc-scheduler vc-controller-manager vc-webhook-manager
```

以本机已有的 v1.14.1 ARM64 Volcano 镜像为运行时层，只替换当前分支二进制：

| Image | Local image ID |
| --- | --- |
| `volcanosh/vc-scheduler:dev-013` | `sha256:7e9f6f6f0f66dec39f8962a4950d21ff321bb308597872ae962ac40b5641605a` |
| `volcanosh/vc-controller-manager:dev-013` | `sha256:81b205af795f2782f11a86d532b06463c0adceca62a13ecd1e85b90d9aa5058f` |
| `volcanosh/vc-webhook-manager:dev-013` | `sha256:ab8176eda4698f9f3dfc7f3e9045be471b5238a84d990b37a880f7f07e05ba53` |

镜像仅加载到本地 Kind，没有推送远端。

### 集群与拓扑

- 集群：`volcano-dev-013`；
- Kubernetes：v1.36.1 / ARM64；
- 1 control-plane + 4 worker，共 5 个可见 Node；
- 每个 Node allocatable CPU 为 12；
- tier1：`hn-a/b/c/d`，各包含一个 worker，`nodeCount=1`；
- tier2：`instance-group-ab/cd`，各包含两个 tier1，`nodeCount=2`；
- tier3：`hypercluster-all`，包含两个 tier2，`nodeCount=2`；
- scheduler 同时启用 NTA 和 GTA gradient/order；
- scheduler、controller-manager、admission 均使用 `dev-013` 并保持 Running。

验证清单保存在本任务目录：

- `kind-config.yaml`
- `hypernodes.yaml`
- `scheduler-config.yaml`
- `soft-podgroup.yaml`
- `soft-deployments.yaml`
- `fallback-fillers.yaml`
- `Dockerfile.local-binary`

### K1.1：preferred 可行

按 PodGroup-first 顺序创建 Job soft/tier2、两个 SubJob soft/tier1；每个 SubJob 两个
Pod，每个 Pod 请求 `500m CPU / 200Mi memory`。

最终：

| Role | Pod 数 | Node | HyperNode |
| --- | ---: | --- | --- |
| `role-a` | 2 | `volcano-dev-013-worker` | `hn-a` |
| `role-b` | 2 | `volcano-dev-013-worker` | `hn-a` |

- 四个 Pod 全部 Running；
- PodGroup `phase=Running`、`running=4`；
- API spec 保持 `mode=soft`、`highestTierName=hyperinstance-group`；
- PodGroup 注解 `volcano.sh/job-allocated-hypernode=hn-a`；
- Job 与两个 SubJob 实际 LCA 均为 tier1 `hn-a`；
- Job 初筛日志为 `8/8 hyperNodes available`。

结论：K1.1 PASS。

### K1.2：preferred pair 资源不足后 fallback

清理 K1.1 后，在 hn-a/hn-b 对应 worker 各创建一个直接绑定的 filler Pod。初始
`11500m` 在 hn-a 被 kubelet 判定为 `OutOfcpu`，该尝试未计入验证结果；将两者统一
调整为 `11000m` 后均 Running。结合每个节点已有 `100m` request，A/B 各剩约
`900m`，小于单个 SubJob 的最小资源 `2 * 500m = 1000m`。

随后按 PodGroup-first 顺序创建相同 soft workload。最终：

| Role | Pod 数 | Node | HyperNode |
| --- | ---: | --- | --- |
| `role-a` | 2 | `volcano-dev-013-worker3` | `hn-c` |
| `role-b` | 2 | `volcano-dev-013-worker3` | `hn-c` |

- 四个 Pod 全部 Running；
- PodGroup `phase=Running`、`running=4`、`allocated=hn-c`；
- API spec 仍为 `mode=soft`；
- Job 初筛仍为 `8/8 hyperNodes available`；
- scheduler 先尝试 `hn-a`、`hn-b`，均因 SubJob
  `minResource: cpu 1000.00 ... (1 minResource)` 失败；
- scheduler 随后尝试 `hn-c` 并成功，证明 soft preferred 没有截断 fallback；
- 最近 15 分钟日志中
  `Converting job-level soft|ConvertToHardTopology|convertSoftToHardTopology`
  匹配数为 `0`。

结论：K1.2 PASS。

### Kind 清理

已执行：

```bash
kind delete cluster --name volcano-dev-013
kind get clusters
```

五个任务节点全部删除；原有 `kind` 集群仍存在且未受影响。

## 最终提交

```text
18deb5219 fix(scheduler): preserve native soft topology on v1.14
d3dc2e52a test(scheduler): apply gofmt to topology fixtures
b42c9f426 test(scheduler): include mixed topology matrix in backport gate
```

最终分支：

```text
fix/013-native-soft-topology-v1.14-backport@b42c9f426
```

本分支不包含 `473c6538b`；Volcano 源码工作树清洁。
