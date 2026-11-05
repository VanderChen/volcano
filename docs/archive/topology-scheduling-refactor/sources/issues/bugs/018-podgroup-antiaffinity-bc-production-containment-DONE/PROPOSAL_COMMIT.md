> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 018：18deb5219 基线 B+C candidate forest 修订提案

## 审批状态

- candidate-forest 总体方案：用户已同意；
- 新增要求：重新基于 `18deb5219`，显式考虑 SubGroup native soft；
- 新基线诊断：完成；
- 修订后的运行时入口和验证用例：用户已回复“请开始完成修复”；
- 批准日期：2026-08-03；
- 实现代码、代码门禁、hard/native-soft Kind K0→K1 与清理：全部完成。

## 结论

`18deb5219` 没有解决 B+C。

它使 soft topology 正确保持 native soft：

```text
soft NTA -> Abstain from hard gradient + participate in scoring
```

但 outer allocate 仍然是：

```text
for each gradient layer:
  for each single root in layer:
    allocate the whole Job under only this root
```

当 required PAA 删除 A/H、gradient layer 为 `[B,C]` 时，hard SubGroup 和
native-soft SubGroup 都仍无法在一个 gang transaction 中联合使用 B+C。

## 新基线代码证据

### 1. framework 已正确区分 hard 与 soft

`18deb5219` 的 `HyperNodeGradientResult`：

- `Applied=true`：hard constraint，参与交集；
- `Applied=false`：abstain，不缩小 universe。

NTA 对 Job/SubJob soft 返回 `HyperNodeGradientAbstain()`。GTA 对 required PAA
返回 hard gradient。最终 framework 能得到合法 B+C，但不会把一个 layer 变成一个
transaction。

### 2. 根因仍在 allocate

`allocateForJob` 对 `[B,C]` 仍分别运行完整 Job dry-run。B、C 单独各只有两个
可用 leaves，minMember=4，因此两次 dry-run 都失败。

### 3. SubGroup native soft 的边界

实测：

- PAA + native-soft SubGroups：失败，因为 PAA 删除了共同上层 H；
- 无 PAA + native-soft SubGroups：10/10 成功，因为 universe 中仍有 H，现有
  coarser-root fallback 最终可调度。

所以不能把所有 soft SubGroup 都切到 forest；那会无必要地改变已工作的 fallback。
正确做法是保留 PAA 边界，并对 PAA+soft 添加显式覆盖。

## 修订设计

### 1. 运行时入口

保留语义窄、但按新基线重新解释的入口：

```go
job.ContainsHardPodGroupAntiAffinity() && !job.IsHardTopologyMode()
```

它覆盖：

- PAA + Job 无 topology + hard SubGroups；
- PAA + Job 无 topology + native-soft SubGroups；
- PAA + native-soft Job；
- PAA + hard/soft 混合 SubGroups。

它排除：

- Job real hard topology；
- 无 required PAA 的 native-soft Job/SubGroup；
- 普通 Job。

新增注释和用例明确：SubGroup soft 不会让 Job 变成 hard，因此 PAA+SubGroup soft
已经进入 forest；不需要额外扩大 predicate。

### 2. gradient layer 是精确 candidate forest

对一个 layer 稳定去重并按名称排序：

```text
allowedRoots = stableUnique(layer)
envelope     = LCA(allowedRoots)
forest       = union(descendants(root) for root in allowedRoots)
```

envelope 只用于调用现有 SubJob gradient callback，不成为允许 scope。

### 3. 保留 native-soft 语义

在 envelope 下调用一次现有 `HyperNodeGradientForSubJobFn`：

- hard SubGroup NTA/GTA 继续返回 Applied constraint；
- soft SubGroup NTA 返回 Abstain，framework 返回完整 universe；
- allocate 最后再与 exact forest 求交，保证 A 不因 envelope 重新进入；
- soft 的 HyperNodeOrderFn 继续在 B+C 内评分和 fallback。

不修改 framework、NTA、GTA 生产代码。

### 4. forest minResource 与 placement

沿用已验证的最小闭包：

- Job minResource 按 allowed roots 覆盖的唯一 Kubernetes Nodes 聚合；
- A 资源不得计入；
- overlapping roots 不得重复计数；
- dry-run recovery 前捕获实际 Job/SubJob placement；
- recorder 保存实际 placement，不无条件保存 envelope。

### 5. 源码范围

生产代码：

- `pkg/scheduler/actions/allocate/allocate.go`

功能测试：

- 新增 `pkg/scheduler/actions/allocate/podgroup_antiaffinity_forest_test.go`

新基线测试卫生：

- 删除 `network_topology_aware_test.go` 中 `18deb5219` 遗留的 unused `sort` import；
- 对 `hyper_node_info_test.go`、`topology_affinity_info_test.go` 只做 gofmt；
- 不带入 `b42c9f426` 的测试重命名；
- 不修改 framework/NTA/GTA 生产代码、API、CRD、controller 或 webhook。

测试卫生修改不进入 scheduler 运行时行为，只用于让新基线门禁可执行。

## 分支纠正

批准后：

1. 将当前已验证但基线错误的分支重命名为
   `backup/fix-018-b8af4d-verified`；
2. 从 `18deb5219` 创建新的
   `fix/018-podgroup-antiaffinity-bc-production-containment`；
3. 新提交必须以 `18deb5219` 为直接祖先；
4. 新分支不得包含 `d3dc2e52a`、`b42c9f426`、`8a71ca532`；
5. candidate forest 代码在新基线上重新应用，不能把旧 b8 raw-gradient 版本直接当成
   最终补丁。

## 验证用例

### G1：基线与 ancestry

通过标准：

- `18deb5219` 是 HEAD ancestor；
- `8a71ca532` 不是 HEAD ancestor；
- runtime diff 只有 allocate；
- framework/NTA/GTA runtime 与 `18deb5219` 完全一致。

### R1：hard SubGroup 修复前失败

在纯净 `18deb5219`：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^TestAllocatePodGroupAntiAffinityAcrossCandidateForest$' \
  -count=1 -timeout=10m
```

已观测：内部 50/50 次无 Bind。

### R2：native-soft SubGroup 修复前失败

同一 B+C/PAA 拓扑，把 SubGroup networkTopology 改成 soft。

已观测：纯净 `18deb5219` 无 Bind。

修复后标准：4/4 Bind，只使用 B/C；soft callback 仍 Abstain，不被改成 hard。

### C1：无 PAA native-soft control

无 required PAA，native-soft SubGroups，资源需要多个 leaves。

已观测：纯净 `18deb5219` 10/10 成功。

修复后必须继续成功，并证明 `shouldUsePodGroupAntiAffinityCandidateForest=false`。
该 control 防止入口被错误扩大成“所有 SubGroup soft”。

### U1：hard B+C forest

- 内部 50 轮全部 4 Bind；
- B/C 四个 leaves 各一个；
- A 零使用；
- Job placement=H。

### U2：native-soft SubGroup B+C forest

- PAA + 四个 native-soft SubGroups；
- 50 轮全部 4 Bind；
- NTA soft 只评分、不 hard-filter；
- B/C 可跨 root fallback；
- A 零使用。

### U3：native-soft Job 与 mixed SubGroups

覆盖：

- Job native soft + PAA；
- hard/soft 混合 SubGroups + PAA；
- real-hard Job + PAA 保持 legacy single-root；
- 无 PAA soft control 不进入 forest。

### U4：scope、资源与 placement

分别验证：

- envelope 下 A 必须被 post-filter 删除；
- B+C 联合满足 minResource 时保留；
- 只有加入 A 才满足时拒绝；
- overlapping roots 不双计；
- 实际只用 B 时 placement=B，不是 H；
- 实际跨 B+C 时 placement=H。

### B1：构建与门禁

```bash
./hack/update-gofmt.sh
go test ./pkg/scheduler/actions/allocate -count=1
go test ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity -count=1
make vc-scheduler
make verify
```

新基线当前已知状态：

- NTA test 有一个 unused `sort` import；
- 两个 API fixture test 需要 gofmt；
- allocate 的 PVC case曾出现一次环境性失败，需单独复跑确认稳定性。

本次 proposal 已显式允许最小测试卫生修改，使 B1 不再依赖“基线差异豁免”。

### K0：18deb5219 基线 Kind

7 Nodes：1 control-plane + 6 workers。

执行两个 target：

1. required PAA + hard SubGroups；
2. required PAA + native-soft SubGroups。

期望两个 target 都不能 4/4，anchor 固定在 A。

### K1：scheduler-only 修复

同一集群，只替换 scheduler 镜像，controller/webhook/CRD/配置不变，分别重建两个
target。

通过标准：

- hard target 4/4 Running；
- native-soft target 4/4 Running；
- 两者都只使用 B/C；
- A 只有 anchor；
- PodGroup placement=H；
- 日志显示 stable allowedRoots=[B,C]、envelope=H；
- native-soft callback 仍为 Abstain/fallback；
- 回滚只恢复旧 scheduler image。

## 生产最小发布方式

若以上通过：

1. 从新 `18deb5219` 基线的 fix/018 构建 scheduler image；
2. 只滚动 `deployment/volcano-scheduler`；
3. 不更新 controller、webhook、CRD；
4. 不修改 workload API；
5. 回滚只恢复原 scheduler image。

## 最终实现

### 分支与提交

- 分支：`fix/018-podgroup-antiaffinity-bc-production-containment`
- 基线：`18deb521904e15b426ee596cc14bf3af18ac0de9`
- 修复：`f954233d2ba7d3c512f0e1d7c1b999c9e829d14e`
- 标题：`fix(allocate): schedule anti-affinity jobs across candidate forest`
- `18deb5219` 是修复提交 ancestor；`8a71ca532` 不是 ancestor。

相对基线的生产运行时差异只有：

```text
pkg/scheduler/actions/allocate/allocate.go
```

其余差异为新增 allocate 回归测试和新基线测试卫生；framework、NTA、GTA 生产代码
保持 `18deb5219` 原样。

### 实现闭包

- 对每个 Job gradient layer 构造稳定去重、按名称排序的 `allowedRoots`；
- LCA 只作为 traversal envelope；
- SubJob gradient 最后与 exact forest 求交，A 不会经 envelope 泄漏；
- minResource 按 forest 中唯一 Kubernetes Nodes 聚合，排除 A 并避免重叠 root 双计；
- dry-run recovery 前保存实际 Job/SubJob placement；
- 入口保持 `ContainsHardPodGroupAntiAffinity() && !IsHardTopologyMode()`；
- PAA + subgroup native soft 进入 forest；无 PAA soft 继续原 coarser-root fallback。

## 代码验证结果

全部通过：

```text
focused hard/native-soft/control/activation matrix       PASS 54.024s
A exclusion + forest minResource, count=50              PASS 1.190s
actual placement, count=50                              PASS 25.217s
go test ./pkg/scheduler/actions/allocate -count=1        PASS 92.917s
go test ./pkg/scheduler/framework -count=1               PASS 1.274s
go test ./pkg/scheduler/plugins/network-topology-aware   PASS 5.302s
go test ./pkg/scheduler/plugins/group-topology-affinity  PASS 2.072s
make vc-scheduler                                        PASS
GOPATH=/Users/vanderchen/go make verify                  PASS
```

`make verify` 的旧脚本会临时升级 code-generator 并生成根目录 `volcano.sh/`；门禁完成后
已精确恢复临时 `go.mod` 漂移并删除该临时生成目录，最终源码工作树 clean。

## 本地镜像

```text
volcanosh/vc-scheduler:dev-018-18deb-base
  sha256:b62e1ff0add910e1a7fdc2b6bca0ac2cbb4e28d4c568628b4c51e21ba93d5020
volcanosh/vc-scheduler:dev-018-18deb-fixed
  sha256:7357c88f46f0f557b0dbcaf9e92eccf6dc91aee999935ea92d5e447f24c892c6
volcanosh/vc-controller-manager:dev-018-18deb-base
  sha256:bac622c480d9ad95ccba35f79fc8a90f376663f44eb7a867a3fcd7a17d638a2a
volcanosh/vc-webhook-manager:dev-018-18deb-base
  sha256:e5e56d6c40ff027467e883a9a343664748ded60e6c33f5b5f1aa9a868f136bb4
```

controller/webhook 在 K0 与 K1 使用完全相同的镜像；K1 只修改 scheduler Deployment。

## Kind 验证结果

### 受限拓扑调整

验证前已有一个承载 dev-004 与 kthena 的 4 节点 `kind` 集群，不能安全删除。为遵守
工作区总计最多 8 个可见节点的硬限制，任务集群 `volcano-dev-018` 使用 4 节点：

```text
control-plane + A worker + B worker + C worker
```

每个 target Pod 请求 `4500m` CPU。节点 allocatable 为 12 CPU，因此 B 或 C 单独最多
容纳 2 个 target，B+C 联合可容纳 4 个。这个受限拓扑保持原用例的关键判据：单 root
无法满足 gang、candidate forest 联合可以满足；没有触碰原有 `kind` 集群。

对应工件：

- `kind-config-bounded.yaml`
- `hypernodes-bounded.yaml`
- `target-workload-bounded.yaml`

### K0：18deb scheduler

hard SubGroups：FAIL-AS-EXPECTED。

- 30 秒后 4 个 target 全部 Pending、无 node；
- PodGroup 为 `Inqueue/Unschedulable`；
- scheduler 分别在 `bug018-b-t2` 与 `bug018-c-t2` 中试放；
- 每个 root 只能放下 2 个，随后整 Job statement recovery；
- 没有 4/4 transaction。

native-soft SubGroups：FAIL-AS-EXPECTED。

- 30 秒后 4 个 target 全部 Pending、无 node；
- PodGroup spec 明确为 `mode: soft`；
- fit 状态没有 `networkTopology` hard-filter，说明 native soft 仍 abstain；
- outer allocate 仍逐个 B/C root 尝试并回滚。

### K1：只替换 scheduler

执行：

```bash
kubectl --context kind-volcano-dev-018 -n volcano-system set image \
  deployment/volcano-scheduler \
  volcano-scheduler=volcanosh/vc-scheduler:dev-018-18deb-fixed
```

替换后组件状态：

```text
volcano-admission    vc-webhook-manager:dev-018-18deb-base
volcano-controllers vc-controller-manager:dev-018-18deb-base
volcano-scheduler   vc-scheduler:dev-018-18deb-fixed
```

hard SubGroups：PASS。

```text
bug018-decode-0   Running  volcano-dev-018-worker3
bug018-decode-1   Running  volcano-dev-018-worker2
bug018-prefill-0  Running  volcano-dev-018-worker3
bug018-prefill-1  Running  volcano-dev-018-worker2
PodGroup          Running running=4 placement=bug018-shared-t3
```

native-soft SubGroups：PASS。

```text
prefill mode=soft
decode  mode=soft
bug018-decode-0   Running  volcano-dev-018-worker2
bug018-decode-1   Running  volcano-dev-018-worker3
bug018-prefill-0  Running  volcano-dev-018-worker2
bug018-prefill-1  Running  volcano-dev-018-worker3
PodGroup          Running running=4 placement=bug018-shared-t3
```

两套场景共同证据：

```text
Build PodGroup anti-affinity candidate forest,
allowedRoots=[bug018-b-t1 bug018-c-t1], envelope=bug018-shared-t3
```

soft 场景还记录到 NTA 对 B/C 的 `networkTopologyAware hyperNode score`，证明 soft 仍参与
score/fallback，没有被 candidate forest 转成 hard constraint。A 上只有 Running anchor，
target 零使用。

### 清理

- 已删除任务集群 `volcano-dev-018`；
- 已删除临时基线 worktree `/private/tmp/volcano-018-18deb-base`；
- 原有 `kind` 集群保持存在且未修改；
- Volcano 源码工作树 clean。

## 发布与回滚

生产最小发布只滚动 scheduler：

```bash
kubectl -n volcano-system set image deployment/volcano-scheduler \
  volcano-scheduler=<registry>/vc-scheduler:<f954233d2-build-tag>
kubectl -n volcano-system rollout status deployment/volcano-scheduler
```

不更新 controller、webhook、CRD、Helm 配置或业务 PodGroup。回滚也只恢复原 scheduler：

```bash
kubectl -n volcano-system set image deployment/volcano-scheduler \
  volcano-scheduler=<current-production-scheduler-image>
kubectl -n volcano-system rollout status deployment/volcano-scheduler
```
