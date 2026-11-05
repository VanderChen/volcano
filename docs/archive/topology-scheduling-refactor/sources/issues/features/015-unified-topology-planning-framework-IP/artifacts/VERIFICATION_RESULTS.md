> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Feature 015 实际验证结果

验证日期：2026-07-30  
源码分支：`feat/015-unified-topology-placement`  
源码基线：`8a71ca5323fdd7e48eb2eebe6ed7169d3d586633`  
最终实现提交：`c4c6c3643efed8a17f1ef31e8991d9ce1e25f891`  
备份分支：`feat/015-unified-topology-placement-backup`
（固定在 amend 前的 `7c0eb9d4f8f78294b23907492d8dda77278942b2`）  
状态：实现和验证完成；当前分支已删除 legacy/observe 运行路径，默认使用统一
topology planner 和 `maxExtraPlacementAttempts=8`，等待 reviewer 审阅。

## 1. 聚焦回归

最终实现通过：

```bash
go test ./pkg/scheduler/actions/allocate

go test ./pkg/scheduler/framework \
  ./pkg/scheduler/api \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/task-topology \
  ./pkg/scheduler/plugins/extender \
  ./pkg/scheduler/actions/preempt \
  ./pkg/scheduler/actions/reclaim

go test ./pkg/scheduler/actions/allocate \
  -run '^(TestTopologyScopeCacheRevisionDistinguishesEquivalentLCABranches|TestUnifiedTopologyOrderedPlacementBacktracksFitFirst|TestUnifiedTopologyMaintainsActualSubGroupAffinityRelation|TestUnifiedTopologyComposesActualNTAAndGTAExactScope)$' \
  -count=10
```

还执行了 Statement 和 unified planner 的 race 回归，均通过：

```bash
go test -race ./pkg/scheduler/framework -run '^TestStatement' -count=5
go test -race ./pkg/scheduler/actions/allocate \
  -run '^TestUnifiedTopology(OrderedPlacementBacktracksFitFirst|MaintainsActualSubGroupAffinityRelation|ComposesActualNTAAndGTAExactScope)$' \
  -count=3
```

`go vet` 覆盖本次修改包，`git diff --check` 通过。

### 已知测试环境波动

把九个包放在一个 `go test` 命令中并发运行时，
`TestAllocateWithPVC/static_pv_matched_and_node_with_enough_resources`
两次在约 0.45 秒后得到 0 个 Binding。该用例单独 `count=10` 全部通过，
`pkg/scheduler/actions/allocate` 单包全量两次通过（约 71–73 秒），其余八个包在同次
并发命令中也全部通过。该现象按测试依赖异步 informer 且在多 test binary 并发压力
下超时记录，不归因于 topology planner；没有修改或放宽该用例。

## 2. Repository 级命令

- `make vc-scheduler`：通过。
- `make unit-test`：scheduler action/plugin 测试通过；总命令被 macOS 上既有的
  Linux-only agent/network-QoS/runc package 阻断。
- `make verify`：`verify-gofmt` 通过，随后 generator 因环境未设置 `GOPATH`
  失败。命令临时产生的 `go.mod` code-generator 变化和 `volcano.sh/` 目录已恢复/
  清理；本 Feature 没有 API/CRD 源码变化。
- 首次 `make images`：scheduler/controller/webhook/agent-scheduler 镜像成功；
  最后的 agent 镜像因 Docker Hub 获取 openEuler token 时连接重置失败。agent 在本
  Kind 场景中禁用且不参与验证。

## 3. 最终本地镜像

初始实现 scheduler runtime：

```text
volcanosh/vc-scheduler:dev-015
sha256:2c072571af6bf85b95396f7568485b75875e8b1da7757da9f699c72b2e7827c9
linux/arm64
```

配套未改组件镜像：

```text
volcanosh/vc-controller-manager:dev-015
sha256:fb21d901f9fd38e7dd996d699a7e842df60b811d7e8bd232417373800b2350bf

volcanosh/vc-webhook-manager:dev-015
sha256:c0ebb592aa0bc245f4a2074eb56bf76ac639f60346e720eb15638ee43edd7ffb
```

镜像在实现提交前由同一份 dirty source 构建，因此镜像内版本字符串仍显示基线 SHA；
以上 image ID 与本文件记录的是该工作区源码的本地验证结果，不能作为已发布 commit
镜像的证明。相同源码随后提交为
`7c0eb9d4f8f78294b23907492d8dda77278942b2`。

## 4. P1：第一次分支成功

命令：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^$' \
  -bench '^Benchmark(UnifiedTopologyFirstBranch|HyperNodeGradientFnPerformance)$' \
  -benchtime=1x -benchmem -count=5
```

环境：Apple M2 Pro，darwin/arm64，1000 Pod / 100 SubGroup synthetic case。

| 模式 | 平均 ns/op | 平均 B/op | 平均 allocs/op |
| --- | ---: | ---: | ---: |
| legacy | 5,883,991,342 | 3,351,137,037 | 43,868,168 |
| unified | 1,559,847,083 | 1,687,390,813 | 5,417,385 |

本 workload 中 unified 约快 3.77 倍，bytes/op 约减少 49.6%，allocs/op 约减少
87.7%；每个 frontier task 只走首选分支，`extraAttempts=0`、`backtracks=0`。
这是 synthetic common-path 结果，不单独决定生产默认。

## 5. P2：失败回退与 paired extender

命令：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^$' \
  -bench '^BenchmarkUnifiedTopologyBacktrack$' \
  -benchtime=1x -benchmem -count=5
```

benchmark 使用真实 `httptest` HTTP endpoint 配置 paired extender
allocate/deallocate verb，并同时用本地 EventHandler 交叉计数。

| 场景 | attempts | extra | backtracks | allocate callbacks | deallocate callbacks | ns/op 范围 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 第 1 个 alternative 成功 | 2 | 0 | 0 | 2 | 0 | 104.1–114.3M |
| 第 2 个 alternative 成功 | 3 | 2 | 1 | 3 | 1 | 104.4–106.3M |
| 第 4 个 alternative 成功 | 5 | 4 | 3 | 5 | 3 | 106.2–109.2M |
| 第 8 个 alternative 成功 | 9 | 8 | 7 | 9 | 7 | 107.1–110.3M |
| budget=8 后停止 | 9 | 8 | 9 | 9 | 9 | 109.7–112.2M |

paired extender 与本地 EventHandler 的调用次数逐次相等。budget case 的所有 Task
恢复 Pending、Node task 集合为空；尝试次数满足
`workingSetSize + maxExtraPlacementAttempts` 上界。

## 6. 初始实现的 Kind 环境

- cluster：`volcano-dev-015`
- Node：1 control-plane + 3 worker，共 4 个 arm64 Node
- topology：4 个 tier1 leaf，3 个 tier2 domain，1 个 tier3 root
- Helm：
  - `topologyPlanningMode: unified`
  - `maxExtraPlacementAttempts: 128`
  - NTA、GTA、task-topology 与普通 priority/gang/predicates/nodeorder 等共同启用
- 初始实现 scheduler 容器运行本地导入镜像：
  `import-2026-07-30@sha256:344ea3c494f985d10b8a2c21e4b40271b2d38b748151654a5cae15aaa29eaed3`

### 临时 CRD workaround

当前安装 CRD 把 `metav1.LabelSelector` 生成为不保留未知字段的空 atomic object，
server-side dry-run 会拒绝合法的 `matchLabels`。仅在任务专用 Kind cluster 中应用
`k0-podgroup-selector-crd-patch.json`，给 PodGroup/SubGroup relation selector
schema 增加 `x-kubernetes-preserve-unknown-fields: true`。仓库 API/CRD 未修改。

首次 K2 清单还缺少 `matchLabelKeys`，按现有 `getSubJobMatchValues` 语义两个 Pod
进入默认 SubJob。清单修正后 server dry-run 通过，再重新执行 K1/K2/K3；失败的首次
结果没有计为通过。

## 7. K1–K4 最终结果

### K1：B+C exact scope

- occupied Pod 固定在 tier2 `domain-a` 的 worker。
- required PodGroup anti-affinity 将 target exact scope 限制为
  `[worker2, worker3]`。
- `worker-worker` SubJob 同时具有 soft topology；4 个 5-CPU Pod 最终在
  worker2/worker3 上 2+2 分布。
- trace：`Found(Ready), attempts=4, extraAttempts=0, backtracks=0`。
- excluded worker 从未进入候选。
- 在初始实现 scheduler 镜像上完整创建/等待/核对/清理 20 次，20/20 均为
  `worker2=2, worker3=2, excluded A=0`。

### K2：真实跨 SubJob required affinity

- Session 构建出独立的 `decode-decode` 和 `prefill-prefill`。
- 第一个 SubJob 放到 worker3 后，第二个 SubJob 的 exact scope 从 4 Node 动态收缩
  为 `[worker3]`。
- 两个 Pod 均 Running 于 worker3。
- trace：`Found(Ready), attempts=2, extraAttempts=0, backtracks=0`。

### K3：fit-first dead end 回退

- task-a 首先按偏好尝试 worker。
- task-b 只允许 worker，第一分支失败。
- planner 回滚 task-a，改放 worker2，再把 task-b 放 worker。
- 两个 Pod 均 Running。
- trace：`Found(Ready), attempts=3, extraAttempts=2, backtracks=1`。

### K4：普通 Job 与 plugin mix

- 无 topology/SubGroup/relation 的普通 PodGroup 保持现有 allocate 路径。
- 两个 Pod 均 Running。
- 日志中没有 `feature-015/ordinary` 的 unified planner result。
- 最终 K1–K4 日志中未发现 panic、fatal、负计数、rollback failure 或
  allocate/deallocate error。

## 8. 删除 legacy 后的最终复验

当前分支删除 Job/SubJob/forest legacy allocator、observe mode 和 mode runtime
分流后，重新执行：

```bash
go test ./pkg/scheduler/actions/allocate
go test ./pkg/scheduler/framework ./pkg/scheduler/api
go test ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/task-topology \
  ./pkg/scheduler/plugins/extender
go test ./pkg/scheduler/actions/preempt ./pkg/scheduler/actions/reclaim
go vet ./pkg/scheduler/actions/allocate ./pkg/scheduler/conf
make vc-scheduler
```

全部通过。删除旧路径后的 P1 独立运行结果为：

| 指标 | 5 次范围 | 平均 |
| --- | ---: | ---: |
| ns/op | 1,777,326,125–1,790,181,083 | 1,783,895,825 |
| B/op | 1,939,952,584–1,941,774,016 | 1,940,494,752 |
| allocs/op | 5,484,691–5,491,290 | 5,486,333 |

P2 的 attempts/extra/backtracks/callback 数保持不变。最终本地 scheduler 镜像：

```text
volcanosh/vc-scheduler:dev-015-nolegacy
sha256:bbf8c47e4c08af1b4db466d8424e240b0cdb0284bcc8a5ac696851d4e1603d1b
linux/arm64
```

重新创建 `volcano-dev-015-nolegacy`（1 control-plane + 3 worker），Helm 配置中
不再设置 `topologyPlanningMode`，也不显式设置 `maxExtraPlacementAttempts`，
直接验证默认路径：

- K1：20/20，每次 worker2=2、worker3=2、excluded worker1=0；
- K2：prefill/decode 都在 worker3；
- K3：task-a=worker2、task-b=worker，trace 为
  `attempts=3, extraAttempts=2, backtracks=1`；
- K4：2 个普通 Pod Running，日志中无该 Job 的 topology planner result；
- scheduler 日志无 panic、fatal、rollback failure 或 allocate/deallocate error。

scheduler 容器实际 image ID：

```text
docker.io/library/import-2026-07-30@sha256:1e539082cceb94faab63d183238d2cf599f49624146afe2d187392b66348f9bc
```

任务专用集群已删除，`kind get clusters` 只剩用户原有 `kind` 集群。

## 9. 结论

- 013/014 的修复不依赖本 Feature；对应回归在统一路径继续通过。
- 统一 planner 自然支持 exact B+C、hard+soft 组合、真实跨 SubJob relation 和
  fit-first 回退。
- 普通 Job 不进入新 planner；NTA/GTA 复用现有 callback adapter；task-topology
  补齐精确 inverse；extender 配置契约未改变。
- topology Job 只有统一运行路径，默认 extra-attempt budget 为 `8`；
  `topologyPlanningMode` 仅作为旧配置兼容键保留。
- 旧实现由固定备份分支和 Git history 保存，不再增加当前源码的维护和测试矩阵。
