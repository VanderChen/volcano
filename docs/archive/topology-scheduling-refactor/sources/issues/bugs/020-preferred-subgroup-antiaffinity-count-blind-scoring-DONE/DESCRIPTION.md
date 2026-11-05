> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 020：preferred SubGroup 反亲和评分丢失域内数量

## 用户报告

一个 PodGroup 包含 8 个 `prefill` SubJob 和 8 个 `decode` SubJob；每个
SubJob 只有一个 Pod：

- `prefill` Pod 请求 1 张卡；
- `decode` Pod 请求 2 张卡；
- `subGroupPolicy[].matchLabelKeys: [shard]`，`subGroupSize: 1`；
- 两个 policy 都要求 `minSubGroups: 8`；
- 每个 policy 各有一条 weight=100 的 preferred 单策略
  `subGroupAntiAffinity`，比较 `volcano.sh/hypernode`；
- 3 个目标 HyperNode 各对应 1 个 Kubernetes Node，每个 Node 有 8 张卡。

总请求恰好为 `8*1 + 8*2 = 24` 张卡，等于集群 `3*8 = 24` 张卡。

用户多次下发后观察到：

```text
理想（节点可置换）: [dddpp] [dddpp] [ddpppp]
常见非理想结果:   [dddd]  [ddpppp] [ddpppp]
```

其中 `d` 表示一个 2 卡 decode Pod，`p` 表示一个 1 卡 prefill Pod。

用户初步分析：decode 在每个节点各分布一个以后，三个节点的反亲和分数相同，
当前逻辑没有继续为均衡分布打分。

## 规范化后的关键 PodGroup 结构

原始消息的 YAML 缩进在聊天中丢失；调查按下述等价结构理解，
`topologyAffinity` 位于 `PodGroup.spec`：

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: bug-pg
  namespace: default
spec:
  minMember: 16
  subGroupPolicy:
  - name: prefill
    labelSelector:
      matchLabels:
        role: prefill
    matchLabelKeys:
    - shard
    subGroupSize: 1
    minSubGroups: 8
  - name: decode
    labelSelector:
      matchLabels:
        role: decode
    matchLabelKeys:
    - shard
    subGroupSize: 1
    minSubGroups: 8
  topologyAffinity:
    subGroupAntiAffinity:
      preferred:
      - subGroups: [prefill]
        topologyTierName: volcano.sh/hypernode
        weight: 100
      - subGroups: [decode]
        topologyTierName: volcano.sh/hypernode
        weight: 100
```

如果用户的原始对象还包含 `networkTopology`、额外 scheduler plugin 权重或不同的
卡资源名，基线复现时应原样补入；这不会改变当前已定位的计数丢失问题，但可能影响
不同布局出现的频率。

`shard` 的具体值也影响复现频率：当前 `SubJobInfo.MatchIndex` 只把纯整数字符串解析为
顺序。若值是 `shard-0`、`shard-a` 等非纯数字，所有 SubJob 的 `MatchIndex` 都为 0，
UID fallback 可让 decode SubJob 整批排在 prefill 之前；这与用户给出的常见布局完全
一致，但它只是暴露计数评分缺陷的顺序条件，不是本 bug 的根因。

## 期望行为

preferred 单策略 `subGroupAntiAffinity` 在可用 domain 少于 SubJob 数时仍保持 soft：

- 不因所有 domain 已占用而拒绝候选；
- 在其他聚合分数相同或不足以覆盖该 term 的权重时，优先选择同策略 peer SubJob
  数更少的 domain；
- 对本例，decode 数量应稳定为 `[3,3,2]`（节点可置换）；在 24 卡必须全部用满的
  约束下，对应 prefill 数量为 `[2,2,4]`。

## 当前状态

- 状态：`DONE`；
- 分支：`fix/020-preferred-subgroup-antiaffinity-count-scoring`；
- 实现：preferred anti-affinity 从 domain occupancy set 改为有界 peer SubJob
  count penalty；生产代码净增加 31 行，无 API/CRD/cache 变化；
- 单测：修复前最小用例红灯，修复后 focused packages 和 100 次 allocate 回归通过；
- Kind：独立 1 control-plane + 3 worker 集群中，基线 30/30 非理想，修复后
  30/30 得到 `[dddpp][dddpp][ddpppp]`；已有 peer 和删除重建用例通过；
- 清理：任务 Kind 集群和基线临时 worktree 已删除。

完整命令、diff 规模、日志、镜像摘要和验证结果见 `PROPOSAL_COMMIT.md`。
