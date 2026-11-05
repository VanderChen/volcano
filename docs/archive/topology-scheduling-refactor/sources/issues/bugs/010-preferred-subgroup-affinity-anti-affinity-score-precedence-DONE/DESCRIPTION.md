> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 010：preferred SubGroup 亲和与反亲和的评分被 Job 紧凑性覆盖

## 状态

DONE（以用户补充的严格条件完成 Kind 端到端复现；未确认 `fix/008` 的源码缺陷）。

## 来源与基线

- 用户测试上报，2026-07-28。
- 源码基线：`fix/008-hypernode-job-soft-affinity-across-subjobs` 的
  `ec7141e5d`。
- 隔离修复分支：
  `fix/010-preferred-subgroup-affinity-anti-affinity-score-precedence`。
- NPU `requests` 在本次最小复现中按用户要求忽略，使用可调度的 CPU/memory
  request 仅让任务进入 `allocate` 路径。

## 用户场景

1. 四个 A5 节点 A/B/C/D，具有相同 `hypercluster` 和
   `hyperinstance-group`；每个 `volcano.sh/hypernode` 对应一个节点。
2. 初始禁止 A、C 的子节点调度，只留下 B、D；随后恢复 A、C。
3. PodGroup `pg-myq` 有两个 `prefill` 和两个 `decode`、每个 SubGroup 大小为 1。
4. `topologyAffinity` 同时声明：
   - `subGroupAntiAffinity.preferred`：`prefill` 和 `decode` 各自在
     `volcano.sh/hypernode` 级别 `weight: 100`；
   - `subGroupAffinity.preferred`：`prefill`/`decode` 在
     `volcano.sh/hyperinstance-group` 级别 `weight: 100`。
5. 启用 `network-topology-aware` 与 `group-topology-affinity`，且网络拓扑
   层级为 `hypercluster -> hyperinstance-group -> hypernode -> hostname`。

## 期望

在 B、D 两个可调度超节点上，四个 Pod 都运行，B、D 各有一个 `prefill` 和一个
`decode`。恢复 A、C 后，已有 Pod 保持 Running，所在节点可为 A/B 或 C/D，且每个
使用的超节点各有一个 `prefill` 和一个 `decode`。

## 实际现象

四个 Pod 虽可运行，但会有两个 Pod 被调度到同一节点；同 policy 的两个 SubGroup
没有按 `preferred` 反亲和的最大权重分散。

## 调查结论

严格复现确认：在 `ec7141e5d` 上，PodGroup 仅使用问题描述中的两个
`subGroupAntiAffinity.preferred` 规则和一个 `subGroupAffinity.preferred` 规则，且
`network-topology-aware`、`group-topology-affinity` 都作为独立 plugin 启用时，两个
preferred 反亲和规则生效：B、D 各有一个 `prefill` 与一个 `decode`。因此按当前提供
条件不能复现产品缺陷。

此前上报的 scheduler 配置片段把 `group-topology-affinity` 写在 `nodeorder` 的
`arguments.name` 下。该字段不会加载另一个 scheduler plugin；实际运行的插件仍只有
`nodeorder`。反向对照只保留这种等价配置后，四个 Pod 全部被 binpack 到 B，稳定复现
“两个或更多 Pod 同一节点”。日志没有任何 `group_topology_affinity.go` 的 preferred
评分记录。

正确的注册方式必须是独立 plugin 条目，不能嵌入 `nodeorder.arguments`：

```yaml
- name: group-topology-affinity
  enabledHyperNodeGradient: true
  enabledHyperNodeOrder: true
  arguments:
    weight: 1000
```

`network-topology-aware` 也应作为独立条目注册。NPU request 不是本问题的原因；严格
验证使用 `nginx:1.27-alpine`、每 Pod `500m CPU` / `200Mi` memory，仅用于让 Pod
进入 scheduler `allocate` 路径。
