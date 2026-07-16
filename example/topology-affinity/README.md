# Topology Affinity Examples

这些样例通过手写 PodGroup 和 Deployment，直接验证
`PodGroup.spec.topologyAffinity`，不经过 VCJob。

## 内容

- `README.md`：测试前提、执行方式和预期行为。
- `cases/`：六组 PodGroup、SubGroup 以及 network topology 组合场景。

## 前提

- 集群已经安装包含本功能的 Volcano API、controller、webhook 和 scheduler。
- scheduler 已启用 `network-topology-aware` 和
  `group-topology-affinity` 的 HyperNode gradient/order 回调：

  ```yaml
  - name: network-topology-aware
    enabledHyperNodeGradient: true
    enabledHyperNodeOrder: true
  - name: group-topology-affinity
    enabledHyperNodeGradient: true
    enabledHyperNodeOrder: true
  ```

- 集群已经创建 `tier3 -> tier2 -> tier1 -> Node` 的 HyperNode 树，tier name
  分别为 `tier1`、`tier2` 和 `tier3`。
- Case 6 使用节点标签 `topology.volcano.sh/tier2=kind-pair-a` 将 anchor
  固定到 pair-a；使用其他节点名或标签时需要相应调整该 case。
- 集群存在可用的 `default` queue；否则需要在 PodGroup 中设置 `spec.queue`。
- Kind 节点已经具备 `busybox:1.36` 镜像。例如：

  ```bash
  docker pull busybox:1.36
  kind load docker-image busybox:1.36 --name kind
  ```

更完整的安装与 HyperNode 配置说明参见：

- [`docs/user-guide/how_to_use_topology_affinity.md`](../../docs/user-guide/how_to_use_topology_affinity.md)
- [`docs/user-guide/how_to_use_network_topology_aware_scheduling.md`](../../docs/user-guide/how_to_use_network_topology_aware_scheduling.md)

## 标准四节点拓扑

这些 cases 按以下 Kind 拓扑设计：

| Tier | HyperNode | Members |
| --- | --- | --- |
| `tier1` | `kind-tier1-control-plane` | `kind-control-plane` |
| `tier1` | `kind-tier1-worker` | `kind-worker` |
| `tier1` | `kind-tier1-worker2` | `kind-worker2` |
| `tier1` | `kind-tier1-worker3` | `kind-worker3` |
| `tier2` | `kind-tier2-pair-a` | `kind-tier1-control-plane`, `kind-tier1-worker` |
| `tier2` | `kind-tier2-pair-b` | `kind-tier1-worker2`, `kind-tier1-worker3` |
| `tier3` | `kind-tier3-all` | `kind-tier2-pair-a`, `kind-tier2-pair-b` |

其中 `tier3` 的 members 是两个 `tier2` HyperNode，而不是四个 Kubernetes
Node。可以使用以下命令检查实际拓扑：

```bash
kubectl get hypernodes.topology.volcano.sh \
  -o 'custom-columns=NAME:.metadata.name,TIER:.spec.tier,TIER_NAME:.spec.tierName,MEMBER_TYPE:.spec.members[*].type,MEMBER_NAME:.spec.members[*].selector.exactMatch.name'
```

## 执行规则

手写 PodGroup 与 Deployment 必须分阶段创建：

1. 先创建 PodGroup，并确认对象已经存在。
2. 再创建引用该 PodGroup 的 Deployment。
3. 多 PodGroup 场景按照各 case 的顺序等待前一个 workload 运行后再继续。
4. 每个 case 独立运行，结束后先删除 Deployment，再删除 PodGroup。

Deployment 创建的 Pod 通过 `scheduling.k8s.io/group-name` annotation 关联到
手写 PodGroup。不要把 PodGroup 和 Deployment 合并为一次 apply，否则
Deployment controller 可能先创建 Pod，使 Pod 没有关联到目标 PodGroup。

通用检查命令：

```bash
kubectl get pg,pod,deploy -n default -o wide
```

## Case 1：hard PodGroup anti-affinity

两个 PodGroup 都通过 required anti-affinity 要求避开对方所在的 `tier2`。
`podGroupSelector: {}` 会匹配 namespace 中的其他 PodGroup，因此该 case
必须隔离运行。

```bash
kubectl apply -f cases/case-01-pg-hard-podgroups.yaml
kubectl apply -f cases/case-01-pg-hard-deploy-a.yaml
kubectl wait --for=condition=Available deployment/ta-deploy-pg-hard-a -n default --timeout=2m
kubectl apply -f cases/case-01-pg-hard-deploy-b.yaml
kubectl wait --for=condition=Available deployment/ta-deploy-pg-hard-b -n default --timeout=2m
kubectl get pg,pod -n default -o wide
kubectl delete -f cases/case-01-pg-hard-deploy-b.yaml
kubectl delete -f cases/case-01-pg-hard-deploy-a.yaml
kubectl delete -f cases/case-01-pg-hard-podgroups.yaml
```

预期：两个 PodGroup 分别位于 pair-a 和 pair-b。

## Case 2：soft PodGroup anti-affinity

两个 PodGroup 优先使用不同 `tier2`，但 preferred 规则在没有更好候选时不会
阻塞调度。该 case 同样使用空 selector，必须隔离运行。

```bash
kubectl apply -f cases/case-02-pg-soft-podgroups.yaml
kubectl apply -f cases/case-02-pg-soft-deploy-a.yaml
kubectl wait --for=condition=Available deployment/ta-deploy-pg-soft-a -n default --timeout=2m
kubectl apply -f cases/case-02-pg-soft-deploy-b.yaml
kubectl wait --for=condition=Available deployment/ta-deploy-pg-soft-b -n default --timeout=2m
kubectl get pg,pod -n default -o wide
kubectl delete -f cases/case-02-pg-soft-deploy-b.yaml
kubectl delete -f cases/case-02-pg-soft-deploy-a.yaml
kubectl delete -f cases/case-02-pg-soft-podgroups.yaml
```

预期：资源允许时，两个 PodGroup 分别位于 pair-a 和 pair-b。

## Case 3：hard SubGroup affinity/anti-affinity

`prefill` 和 `decode` 必须共享 `tier3`；同 role 以及跨 role 的 shard 都不能
共享 `tier1`。

```bash
kubectl apply -f cases/case-03-sg-hard-podgroup.yaml
kubectl apply -f cases/case-03-sg-hard-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/ta-deploy-sg-hard-prefill-0 \
  deployment/ta-deploy-sg-hard-prefill-1 \
  deployment/ta-deploy-sg-hard-decode-0 \
  deployment/ta-deploy-sg-hard-decode-1 \
  -n default --timeout=2m
kubectl get pg,pod -n default -o wide
kubectl delete -f cases/case-03-sg-hard-deployments.yaml
kubectl delete -f cases/case-03-sg-hard-podgroup.yaml
```

预期：四个 shard 使用四个不同 `tier1`，PodGroup 分配域为 `tier3`。

## Case 4：soft SubGroup affinity/anti-affinity

`prefill` 和 `decode` 优先共享 `tier2`；同 role 的 shard 优先分散到不同
`tier1`，跨 role 也配置了较低权重的 preferred anti-affinity。

```bash
kubectl apply -f cases/case-04-sg-soft-podgroup.yaml
kubectl apply -f cases/case-04-sg-soft-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/ta-deploy-sg-soft-prefill-0 \
  deployment/ta-deploy-sg-soft-prefill-1 \
  deployment/ta-deploy-sg-soft-decode-0 \
  deployment/ta-deploy-sg-soft-decode-1 \
  -n default --timeout=2m
kubectl get pg,pod -n default -o wide
kubectl delete -f cases/case-04-sg-soft-deployments.yaml
kubectl delete -f cases/case-04-sg-soft-podgroup.yaml
```

预期：四个 Pod 位于同一 `tier2`，两个 `prefill` shard 和两个 `decode`
shard 分别使用该 `tier2` 下的两个 `tier1`。

## Case 5：network topology 与 group topology 组合

PodGroup network topology 限制整体 `tier3`，SubGroup network topology 限制
每个 shard 的 `tier1`；topology affinity 再要求跨 role 共用 `tier2`、同 role
分散 `tier1`。

```bash
kubectl apply -f cases/case-05-topo-combo-podgroup.yaml
kubectl apply -f cases/case-05-topo-combo-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/ta-deploy-topo-prefill-0 \
  deployment/ta-deploy-topo-prefill-1 \
  deployment/ta-deploy-topo-decode-0 \
  deployment/ta-deploy-topo-decode-1 \
  -n default --timeout=2m
kubectl get pg,pod -n default -o wide
kubectl delete -f cases/case-05-topo-combo-deployments.yaml
kubectl delete -f cases/case-05-topo-combo-podgroup.yaml
```

预期：两个 topology 插件的 hard 候选取交集，同时保留 preferred 评分。

## Case 6：PodGroup anti-affinity 与 SubGroup 规则组合

先把 anchor 固定到 pair-a；combo 通过 hard PodGroup anti-affinity 使用 pair-b，
同时要求自己的 `prefill`/`decode` 共享 `tier2` 并优先分散 `tier1`。

```bash
kubectl apply -f cases/case-06-pg-and-sg-podgroups.yaml
kubectl apply -f cases/case-06-pg-and-sg-anchor-deployment.yaml
kubectl wait --for=condition=Available deployment/ta-deploy-pg-sg-anchor -n default --timeout=2m
kubectl apply -f cases/case-06-pg-and-sg-combo-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/ta-deploy-pg-sg-combo-prefill \
  deployment/ta-deploy-pg-sg-combo-decode \
  -n default --timeout=2m
kubectl get pg,pod -n default -o wide
kubectl delete -f cases/case-06-pg-and-sg-combo-deployments.yaml
kubectl delete -f cases/case-06-pg-and-sg-anchor-deployment.yaml
kubectl delete -f cases/case-06-pg-and-sg-podgroups.yaml
```

预期：anchor 位于 pair-a；combo 位于 pair-b，其两个 SubGroup 分别使用
`kind-worker2` 和 `kind-worker3`。

## 排障

如果 hard SubGroup case 没有候选，先确认 term 使用的
`topologyTierName` 与 HyperNode `spec.tierName` 完全一致：

```bash
kubectl get hypernodes.topology.volcano.sh \
  -o custom-columns=NAME:.metadata.name,TIER:.spec.tier,TIER_NAME:.spec.tierName
```

如果 Pod 已创建但没有进入预期 PodGroup，检查 PodGroup 是否先于 Deployment
创建，以及 Pod 上的 `scheduling.k8s.io/group-name` annotation 是否正确。
