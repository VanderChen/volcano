> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 024：required SubGroup 反亲和硬语义验证记录

## 基线

- 源码基线：`dev-1.14@71f1d3248`；
- 验证镜像：`volcanosh/vc-scheduler:dev-022-memory`，来自仅在上述基线上增加
  candidate-forest 内存线性化修复的 `fix/022@dc4fffbe3`；
- 拓扑：1 control-plane + 4 worker；4 个 tier1，两个 tier2，每个 tier2 含两个
  tier1，tier3 包含两个 tier2；
- 任务不修改实现代码。

## V1：可行边界 4 peers / 4 tier1

- Purpose：证明 required tier1 anti-affinity 不会被 outer tier2 first-success 截断，
  且不会违反硬约束。
- Setup：四个 one-Pod `prefill` SubJob，`subGroupSize=1`、`minSubGroups=4`；单策略
  `subGroupAntiAffinity.required` 位于 `volcano.sh/hypernode`。
- Expected：4/4 Running；四个 Pod 的 NodeName 两两不同，每个 tier1 恰好一个；日志中
  finer container 无完整解后继续到 tier3。
- Pass/fail：任意 Pending、重复 tier1 或硬规则被跳过均失败。

## V2：不可行边界 5 peers / 4 tier1

- Purpose：证明当 required all-different 无解时，不会为了满足 gang 而提交违规布局。
- Setup：与 V1 相同，但有五个 one-Pod SubJob，`minMember=5`、`minSubGroups=5`。
- Expected：在观察窗口内 0 Pod 绑定、0 Running，五个 Pod 均无 `spec.nodeName`；日志记录
  required conflict/no solution。
- Pass/fail：任一 Pod 绑定或任意两个 peer 出现在同一 tier1 即失败。

## 结果

### 环境

- Kind 集群：`volcano-dev-024`，1 个 control-plane、4 个 worker，节点总数 5；
- tier1 与 worker 一一对应：`req024-a`、`req024-b`、`req024-c`、`req024-d`；
- tier2：`req024-pair-ab` 和 `req024-pair-cd`；tier3：`req024-root`；
- 调度器镜像：`volcanosh/vc-scheduler:dev-022-memory`
  (`sha256:2a59be...`)；
- 相关路径与生产基线一致：
  `git diff --exit-code dev-1.14 -- pkg/scheduler/actions/backfill/backfill.go
  pkg/scheduler/plugins/group-topology-affinity pkg/scheduler/api/job_info.go` 返回 0。

### V1 结果：PASS

四个 Pod 均为 `Running`，且分别绑定到四个不同 worker：

```text
req024-feasible-0  volcano-dev-024-worker
req024-feasible-1  volcano-dev-024-worker4
req024-feasible-2  volcano-dev-024-worker2
req024-feasible-3  volcano-dev-024-worker3
```

PodGroup 为 `Running`、`running=4`，最终分配 HyperNode 为 `req024-root`。日志显示
tier1 内出现 `conflictWithPeerSubGroup` 后继续搜索；只有覆盖四个 tier1 的
`req024-root` 返回 `jobReady=true` 并提交。这证明搜索不会在只能覆盖两个 tier1 的
tier2 上提前提交，也没有违反 required。

### V2 结果：PASS

五个 Pod 均带有与 V1 相同的资源请求（`cpu=10m`、`memory=8Mi`）。观察 30 秒并经历
多轮调度后：

```text
NAME                            PHASE    NODE
req024-infeasible-requested-0   Pending  <none>
req024-infeasible-requested-1   Pending  <none>
req024-infeasible-requested-2   Pending  <none>
req024-infeasible-requested-3   Pending  <none>
req024-infeasible-requested-4   Pending  <none>
```

PodGroup 保持 `Inqueue`，`volcano.sh/job-allocated-hypernode` 为空。日志显示前四个
SubJob 的试探分配最终均被回滚，第五个 SubJob 在四个 tier1 上全部得到
`conflictWithPeerSubGroup`，顶层候选返回 `job not ready or pipelined`，没有提交任何
绑定。

### 结论

在带资源请求、进入正常组拓扑分配链路的前提下，当前生产基线上的 required
SubGroup anti-affinity 保持硬约束语义：可行时继续扩大搜索范围直到找到合法布局；
不可行时整个 PodGroup 保持未调度，不会提交局部结果或违规布局。

### 主要命令

```bash
kind create cluster --name volcano-dev-024 --config kind-config.yaml
kubectl apply -f hypernodes.yaml
kubectl apply -f pg-feasible.yaml
kubectl apply -f pods-feasible.yaml
kubectl -n req024 wait pod -l role=prefill --for=condition=Ready --timeout=120s
kubectl apply -f pg-infeasible.yaml
kubectl apply -f pods-infeasible.yaml
kubectl -n req024 wait pod/req024-infeasible-requested-0 \
  pod/req024-infeasible-requested-1 pod/req024-infeasible-requested-2 \
  pod/req024-infeasible-requested-3 pod/req024-infeasible-requested-4 \
  --for=condition=PodScheduled --timeout=30s
kubectl -n req024 get pods -l test=req024-infeasible-requested \
  -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName'
```
