# Topology Affinity 用户指南

## 概览

`topologyAffinity` 是 `scheduling.volcano.sh/v1beta1` `PodGroup` 的可选字段。它以 HyperNode 树为拓扑事实来源，描述**组与组之间**应位于同一或不同拓扑域的关系；它不改变 Pod 的普通 Kubernetes `podAffinity` / `podAntiAffinity` 语义。

该特性有两个使用层级：

| 目标 | 使用的字段 | 范围 |
| --- | --- | --- |
| 将同类业务实例隔离到不同故障域 | `topologyAffinity.podGroupAntiAffinity` | 当前 PodGroup 与其他已调度的 PodGroup |
| 让一个业务实例内的多个角色位于同一域 | `topologyAffinity.subGroupAffinity` | 同一个 PodGroup 的 SubJob |
| 将一个角色的分片分散，或隔离不同角色 | `topologyAffinity.subGroupAntiAffinity` | 同一个 PodGroup 的 SubJob |
| 限制一个 PodGroup 或 SubGroup 可以跨越的最大拓扑范围 | `networkTopology` | 聚合/边界约束，不是本特性的替代品 |

推荐使用 `topologyTierName`，让 YAML 与 HyperNode 的 `spec.tierName` 对齐。也可以使用数字 `topologyTier`，但同一个 term 中只能设置二者之一；数字越大，代表越靠近 HyperNode 树根的粗粒度域。

## 使用前提

1. 集群已经安装包含 `PodGroup.spec.topologyAffinity` 的 Volcano CRD，且 scheduler 和 admission webhook 与该 CRD 版本匹配。
2. 集群中已经有完整的 HyperNode 树。每个工作节点都必须能沿树解析到配置中引用的 tier；tier 名称必须与 `HyperNode.spec.tierName` 完全一致。
3. scheduler 配置同时启用 `network-topology-aware` 和 `group-topology-affinity` 的 HyperNode gradient 与 order 回调。
4. 每个待调度 Pod 都属于目标 PodGroup，并使用 Volcano 调度器。手写 PodGroup 时，先创建 PodGroup，确认其存在后再创建 Pod/Deployment；Pod 模板需要 `scheduling.k8s.io/group-name: <podgroup-name>` 和 `schedulerName: volcano`。

检查集群拓扑：

```bash
kubectl get hypernodes.topology.volcano.sh \
  -o custom-columns=NAME:.metadata.name,TIER:.spec.tier,TIER_NAME:.spec.tierName
kubectl get nodes -o wide
```

在 scheduler ConfigMap 的调度 tier 中加入（保留现有插件和参数）：

```yaml
- name: network-topology-aware
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
  arguments:
    weight: 10
- name: group-topology-affinity
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
  arguments:
    weight: 10
```

修改后重启并等待 scheduler：

```bash
kubectl rollout restart deployment/volcano-scheduler -n volcano-system
kubectl rollout status deployment/volcano-scheduler -n volcano-system
```

HyperNode 的创建和 `networkTopology` 的基本配置请参考[网络拓扑感知调度指南](./how_to_use_network_topology_aware_scheduling.md)。仓库中提供了可直接运行的[4 节点拓扑与用例](../../example/topology-affinity/README.md)。

## API 规则

所有三个块都以 `required` 或 `preferred` 表达 term：

- `required` 是硬约束，不能设置 `weight`。没有满足条件的 HyperNode 时，PodGroup 保持 Pending/Unschedulable。
- `preferred` 是软约束，必须设置 `weight: 1` 到 `100`。它影响候选 HyperNode 的排序，资源或其他硬约束无法满足时允许退化。
- 同一块中的多个 `required` term 取交集，必须同时满足。
- 每个 term 都必须设置 `topologyTierName` 或 `topologyTier`，不能同时设置。

### PodGroup 层：跨实例反亲和

`podGroupAntiAffinity` 将当前 PodGroup 与 `podGroupSelector` 选中的**其他** PodGroup 比较。调度器自动排除自身；未设置 `namespaceSelector` 时只比较同一 namespace 的 PodGroup。

当前交付只支持跨 PodGroup 的**反亲和**，不支持 `podGroupAffinity`（跨 PodGroup 共域）。它比较已分配工作负载所在的 HyperNode 祖先域，所以应先使第一个实例稳定运行，再提交第二个实例以获得可观察、可重复的隔离结果。

下面的 term 要求相同模型组的实例不能共享 `supernode`：

```yaml
spec:
  topologyAffinity:
    podGroupAntiAffinity:
      required:
      - podGroupSelector:
          matchLabels:
            topology.volcano.sh/model-group: llama-70b-prod
        topologyTierName: supernode
```

两个实例都应带有同一个标签：

```yaml
metadata:
  labels:
    topology.volcano.sh/model-group: llama-70b-prod
```

> **CRD 兼容性提示：** `podGroupSelector` 的目标形态是 Kubernetes `LabelSelector`。如果已安装 CRD 将它显示为 atomic object，严格字段校验会拒绝 `matchLabels` / `matchExpressions`。这种集群中只能在隔离 namespace 的测试里使用 `podGroupSelector: {}`（表示同 namespace 的所有其他 PodGroup），不能用于共享生产 namespace 的精确选择；应先升级到支持完整 `LabelSelector` schema 的特性 CRD。

`namespaceSelector` 目前不会扩展匹配范围：实现仍只匹配当前 PodGroup 的 namespace。因此不要将跨 namespace 隔离作为此版本的能力。

### SubGroup 层：单个实例内部的角色与分片

`subGroupAffinity` 和 `subGroupAntiAffinity` 只作用于**一个** PodGroup。term 中的 `subGroups` 填的是 `spec.subGroupPolicy[].name`，例如 `prefill`、`decode`，而不是 Pod label，也不是某一个 Pod 名称。

`subGroupPolicy` 通过 `labelSelector` 找到 Pods，并可使用 `matchLabelKeys` 将它们拆成多个 SubJob。下面的 `shard` 使每个 `(role, shard)` 组合成为一个 SubJob：

```yaml
subGroupPolicy:
- name: prefill
  labelSelector:
    matchLabels:
      role: prefill
  matchLabelKeys: [shard]
  subGroupSize: 1
  minSubGroups: 2
- name: decode
  labelSelector:
    matchLabels:
      role: decode
  matchLabelKeys: [shard]
  subGroupSize: 1
  minSubGroups: 2
```

子组 term 的语义如下：

| 配置 | 含义 |
| --- | --- |
| `subGroupAffinity`，`subGroups: [prefill, decode]` | 两个策略下的全部 SubJob 必须位于 term tier 的同一个拓扑域。至少要列出两个不同策略。 |
| `subGroupAntiAffinity`，`subGroups: [prefill]` | `prefill` 的各个 SubJob 两两不能共享该 tier 的域，即同角色分片展开。该策略必须能产生至少两个 SubJob：设置 `matchLabelKeys`，或设置 `minSubGroups >= 2`。 |
| `subGroupAntiAffinity`，`subGroups: [prefill, decode]` | `prefill` 与 `decode` 的 SubJob 不能共享域；同一角色的 SubJob 仍可共域。若还要同角色展开，分别再添加 `[prefill]`、`[decode]` term。 |

当硬 `subGroupAffinity` 与硬 `subGroupAntiAffinity` 包含同一个策略时，亲和 tier 必须与反亲和 tier 相同或更粗。例如“Prefill/Decode 同一个 supernode，同时在不同 rack 展开”是可行组合；反过来要求同一个 rack、又在不同 supernode 则冲突。

## 完整 PodGroup 示例

这个示例演示常见的 Prefill/Decode 推理实例。它同时表达：

- 与匹配的其他实例在 `supernode` 隔离；
- 当前实例的 Prefill 与 Decode 在同一个 `supernode`；
- 每个角色的分片在不同 `rack`；
- 两个角色的分片也不共享 `rack`。

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: llama-70b-instance-0
  namespace: default
  labels:
    topology.volcano.sh/model-group: llama-70b-prod
spec:
  minMember: 4
  queue: default
  subGroupPolicy:
  - name: prefill
    labelSelector:
      matchLabels:
        role: prefill
    matchLabelKeys: [shard]
    subGroupSize: 1
    minSubGroups: 2
  - name: decode
    labelSelector:
      matchLabels:
        role: decode
    matchLabelKeys: [shard]
    subGroupSize: 1
    minSubGroups: 2
  topologyAffinity:
    podGroupAntiAffinity:
      required:
      - podGroupSelector:
          matchLabels:
            topology.volcano.sh/model-group: llama-70b-prod
        topologyTierName: supernode
    subGroupAffinity:
      required:
      - subGroups: [prefill, decode]
        topologyTierName: supernode
    subGroupAntiAffinity:
      required:
      - subGroups: [prefill]
        topologyTierName: rack
      - subGroups: [decode]
        topologyTierName: rack
      - subGroups: [prefill, decode]
        topologyTierName: rack
```

所有属于这个 PodGroup 的 Pod 都需要指向它，并带上能匹配子组策略的 label。例如 `prefill` 的 `p0` 分片可以使用下面的模板；`p1`、`decode/d0`、`decode/d1` 应分别使用不同的 `shard` 值。

```yaml
metadata:
  labels:
    role: prefill
    shard: p0
  annotations:
    scheduling.k8s.io/group-name: llama-70b-instance-0
spec:
  schedulerName: volcano
  containers:
  - name: worker
    image: busybox:1.36
    command: ["sh", "-c", "sleep 360000"]
```

若用 Deployment 创建固定分片，建议每个分片使用一个 Deployment（或确保其 Pod template 的 `matchLabelKeys` 值不同）。一个 `replicas: 4` 且所有 Pod label 相同的 Deployment 只会形成一个 SubJob，不能验证同角色分片反亲和。

## 与 `networkTopology` 组合

`networkTopology` 管理“允许工作负载跨到多大的域”以及每个 SubJob 的拓扑聚合；`topologyAffinity` 管理候选域相对其他组的关系。二者可以同时存在，两个插件产生的 HyperNode 候选会取交集。

典型组合如下：

```yaml
spec:
  networkTopology:
    mode: hard
    highestTierName: supernode
  subGroupPolicy:
  - name: prefill
    # 省略 labelSelector / matchLabelKeys
    networkTopology:
      mode: hard
      highestTierName: rack
  topologyAffinity:
    subGroupAntiAffinity:
      preferred:
      - subGroups: [prefill]
        weight: 100
        topologyTierName: rack
```

上述配置先把整个 PodGroup 限制在一个 `supernode`，再要求每个 Prefill SubJob 在 `rack` 级别 gang；最后尽量让各分片在不同 rack。若任一硬约束没有候选域，PodGroup 不会调度。不要把软反亲和当作容量保证。

## 验证路径

仓库样例覆盖了 PodGroup 层、SubGroup 层以及二者组合。进入示例目录，按其 README 完成 Volcano、HyperNode 和 scheduler 配置后，运行组合用例：

```bash
cd example/topology-affinity
kubectl apply -f cases/case-06-pg-and-sg-podgroups.yaml
kubectl apply -f cases/case-06-pg-and-sg-anchor-deployment.yaml
kubectl wait --for=condition=Available deployment/ta-deploy-pg-sg-anchor \
  -n default --timeout=2m
kubectl apply -f cases/case-06-pg-and-sg-combo-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/ta-deploy-pg-sg-combo-prefill \
  deployment/ta-deploy-pg-sg-combo-decode \
  -n default --timeout=2m
kubectl get pg,pod -n default -o wide
```

该用例先将 anchor PodGroup 固定到 `tier2` 的 `kind-pair-a`，再创建包含 PodGroup 反亲和与 SubGroup 规则的 combo PodGroup。combo 必须选择另一个 `tier2` 域；其 `prefill` / `decode` 必须在同一个 `tier2`。`tier1` 的分散规则是 soft preference，因此资源紧张时可以退化。

完成后清理：

```bash
kubectl delete -f cases/case-06-pg-and-sg-combo-deployments.yaml
kubectl delete -f cases/case-06-pg-and-sg-anchor-deployment.yaml
kubectl delete -f cases/case-06-pg-and-sg-podgroups.yaml
```

## 排障与边界

| 现象 | 首先检查 |
| --- | --- |
| PodGroup 一直 Pending | `group-topology-affinity` 和 `network-topology-aware` 是否已启用；term 的 tier 名称是否存在；硬反亲和所需的不同域数量是否足够。 |
| 子组规则没有生效 | Pod 是否带正确的 `scheduling.k8s.io/group-name`、`schedulerName: volcano`、`role` 与 `shard` label；PodGroup 是否在工作负载之前创建。 |
| Admission 拒绝 PodGroup | 每个 term 是否只写了一个 tier 字段；`preferred` 是否有 1–100 的 `weight`；`required` 是否未设置 `weight`；`subGroups` 是否是已有且不重复的策略名称。 |
| 两类规则无共同候选 | 检查 `networkTopology` 的硬边界是否与 `topologyAffinity` 的硬关系冲突，并使用 scheduler 日志定位被过滤的 HyperNode。 |

本版本的明确边界是：

- 不支持跨 PodGroup 的亲和；跨 PodGroup 仅支持 `podGroupAntiAffinity`。
- 不支持跨 PodGroup 或跨 namespace 的 SubGroup 亲和/反亲和；所有 `subGroups` 都属于当前 PodGroup。
- `namespaceSelector` 目前不实现跨 namespace 匹配。
- HyperNode 树和可用资源是硬约束的前提。特性不会创建拓扑域，也不会在域数量或资源不足时强行打破 `required` 规则。
