> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 010：调查、提案与验证记录

## 阶段

2026-07-28 已根据用户反馈重新打开。前次 R1 在 PodGroup 与两个 SubGroupPolicy 中
额外声明了 soft `networkTopology`，不等同于报告中明确给出的规则。新的严格复现将使用
`podgroup-reported-rules-only.yaml`：它只保留报告所列的 SubGroupPolicy 和 preferred
SubGroup affinity/anti-affinity，同时独立启用 `network-topology-aware` 与
`group-topology-affinity`。R3 已完成，确认“非源码缺陷”的结论；没有实现代码修改或
源码提交。

## 已知边界

- 本次仅分析 `fix/008` 的 `ec7141e5d`；不把 `fix/009` 的 candidate-domain
  修改视为基线行为。
- NPU 资源可假定充足；最小复现不会依赖真实 NPU 设备或扩展资源。

## Kind 复现与对照（2026-07-28）

### 环境

- 分支/提交：`fix/010-preferred-subgroup-affinity-anti-affinity-score-precedence`
  从 `fix/008` 的 `ec7141e5d` 创建。
- 集群：`volcano-dev-010`，1 control-plane + 4 workers（共 5 个可见节点）。
- A/B/C/D 映射：`worker`/`worker2`/`worker3`/`worker4`。
- S0：cordon A (`worker`) 和 C (`worker3`)，仅 B/D 可调度。
- 镜像：`volcanosh/vc-scheduler:dev-010-repro`，由 `ec7141e5d` 本地构建并
  `kind load docker-image`；controller-manager/webhook-manager 使用本机已有
  v1.14.1 镜像添加同一测试 tag 后加载。未推送远程 registry。
- HyperNode：`hostname` tier 1 -> `volcano.sh/hypernode` tier 2 ->
  `volcano.sh/hyperinstance-group` tier 3 -> `volcano.sh/hypercluster` tier 4。
  每个 A/B/C/D hypernode 只有一个 Node 子成员，四者共享一个 hyperinstance-group。
- 清单：`kind-config.yaml`、`hypernodes.yaml`、`podgroup.yaml`、
  `deployments.yaml`。PodGroup 有两个 `prefill` 和两个 `decode`（每个 SubGroup
  大小 1），同用户描述配置两条 100 分 preferred SubGroup anti-affinity 和一条
  100 分 preferred SubGroup affinity。为了使 soft topology 工作路径显式可观测，
  清单额外声明 Job soft `hyperinstance-group` 上界和 SubGroup soft `hypernode`
  上界；不申请 NPU。

### R1：正确 plugin 注册 — 通过，未复现

Scheduler 配置将两个插件作为独立条目启用：

```yaml
- name: network-topology-aware
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
  arguments: {weight: 100}
- name: group-topology-affinity
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
  arguments: {weight: 1000}
```

核心命令：

```bash
cd volcano-010
make vc-scheduler
docker buildx build -t volcanosh/vc-scheduler:dev-010-repro . \
  -f installer/dockerfile/scheduler/Dockerfile --output=type=docker --platform linux/arm64
kind create cluster --name volcano-dev-010 --config ../issues/bugs/010-preferred-subgroup-affinity-anti-affinity-score-precedence-IP/kind-config.yaml
kind load docker-image volcanosh/vc-scheduler:dev-010-repro --name volcano-dev-010
helm upgrade --install volcano installer/helm/chart/volcano --namespace volcano-system \
  --create-namespace --kube-context kind-volcano-dev-010 \
  --set basic.image_tag_version=dev-010-repro \
  --set basic.image_pull_policy=IfNotPresent \
  --set-file custom.scheduler_config_override=../issues/bugs/010-preferred-subgroup-affinity-anti-affinity-score-precedence-IP/scheduler-config.yaml
kubectl --context kind-volcano-dev-010 cordon volcano-dev-010-worker volcano-dev-010-worker3
kubectl --context kind-volcano-dev-010 apply -f ../issues/bugs/010-preferred-subgroup-affinity-anti-affinity-score-precedence-IP/hypernodes.yaml
kubectl --context kind-volcano-dev-010 apply -f ../issues/bugs/010-preferred-subgroup-affinity-anti-affinity-score-precedence-IP/podgroup.yaml
kubectl --context kind-volcano-dev-010 apply -f ../issues/bugs/010-preferred-subgroup-affinity-anti-affinity-score-precedence-IP/deployments.yaml
```

结果：四个 Pod 均 Running，B/D 每个各一个 `prefill` 与一个 `decode`：

```text
decode-0   worker4 (D)
prefill-0  worker4 (D)
decode-1   worker2 (B)
prefill-1  worker2 (B)
```

PodGroup `running=4`，Job allocated HyperNode 是共享的
`bug010-hyperinstance-group`。scheduler 日志中，第二个同 policy SubGroup 的候选分
为 B `100000`、D `0`（或相反），并选择未占用的 hypernode。S4 后 uncordon A/C，四个
Pod 仍 Running 且保持既有 B/D 绑定；Kubernetes/Volcano 不会因节点重新可调度主动迁移
已运行 Pod。

### R2：上报片段等价配置 — 已复现

将 `group-topology-affinity` 从独立 plugin 条目移除，只保留：

```yaml
- name: nodeorder
  arguments:
    weight: 1000
    name: group-topology-affinity
```

重新部署 scheduler、删除并重建同一 PodGroup/Deployment，再 cordon A/C。四个 Pod
全部被绑定到 B (`worker2`)：

```text
decode-0   worker2 (B)
prefill-0  worker2 (B)
decode-1   worker2 (B)
prefill-1  worker2 (B)
```

日志证明此时没有 group-topology-affinity 回调：每个 SubJob 的可用候选仅是一个
hostname，并最终以 `Allocate job to hyperNode success ... bug010-b-hostname` 提交。
这与用户的“同一节点”现象一致。

### R3：用户补充后的严格条件 — 通过，未复现

用户明确要求两个 plugin 同时开启，并指定 nginx、每 Pod `500m CPU` / `200Mb`
资源、忽略 NPU request。为避免将任何未说明的网络拓扑策略带入测试，使用
`podgroup-reported-rules-only.yaml`，它仅包含：

- 两个 `SubGroupPolicy`（`prefill` / `decode`、各 `minSubGroups: 2`、
  `subGroupSize: 1`）；
- `prefill` 与 `decode` 各自 100 分的 preferred SubGroup anti-affinity，tier 为
  `volcano.sh/hypernode`；
- `prefill`/`decode` 100 分 preferred SubGroup affinity，tier 为
  `volcano.sh/hyperinstance-group`；
- 没有 Job 或 SubGroup `networkTopology` 字段。

四个 Deployment 使用 `nginx:1.27-alpine` 和：

```yaml
resources:
  requests:
    cpu: 500m
    memory: 200Mi
```

`scheduler-config.yaml` 已由集群内 `volcano-scheduler-configmap` 原样读回确认，包含
两个独立条目：

```yaml
- name: network-topology-aware
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
- name: group-topology-affinity
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
```

S0 cordon A (`worker`) 与 C (`worker3`) 后，B/D 可用。结果如下：

```text
pg-myq-010-decode-0    Running  volcano-dev-010-worker4  # D
pg-myq-010-prefill-0   Running  volcano-dev-010-worker4  # D
pg-myq-010-decode-1    Running  volcano-dev-010-worker2  # B
pg-myq-010-prefill-1   Running  volcano-dev-010-worker2  # B
PodGroup phase=Running, running=4
```

日志显示第二个同 policy SubGroup 在候选 B/D 上获得 `100000` 对 `0` 的
group-topology-affinity 分数，并选择未占用 hypernode。S4 uncordon A/C 后四个
Pod 保持 Running 和 B/D 绑定；不会因节点恢复可调度而重新调度已有 Pod。

### 结论与处置

在 R3 的严格条件下，结果符合用户预期，故不需要修改 Volcano 源码。R2 保留为配置
排查参考：若生产环境仍出现同节点现象，应先读取实际生效的 scheduler ConfigMap，确认
`group-topology-affinity` 不是被写入 `nodeorder.arguments`。任务专用 Kind 集群会在本
记录完成后删除。
