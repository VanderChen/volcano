> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 已废弃记录：错误基线 `8a71ca532`

2026-08-03 第一次定义 Bug 018 时，错误地把生产基线识别为
`dev-1.14@8a71ca532`。基于该错误前提曾完成以下工作：

- 创建 `fix/018-podgroup-antiaffinity-bc-production-containment`；
- 复验 `8a71ca532` 已存在的 candidate-forest 实现；
- focused tests、scheduler 构建和 7 节点 Kind B+C 用例通过；
- 验证镜像为 `volcanosh/vc-scheduler:dev-018`，镜像 ID
  `sha256:9509349edbbbb90562011c5d7a5bafdbc02e8933a68d885dfd1b8b25367f619b`；
- 临时 Kind 集群已删除。

该验证只能证明 `8a71ca532` 的行为，不能证明真实生产基线可直接使用。用户随后明确：

```text
生产基线只到 b8af4d766
feat(network-topology-aware): Network-Topology-Aware scheduling soft mode under job/subjob (#5206)
```

`8a71ca532` 的祖先包含生产不存在的 013 native-soft 改造：删除
`convertSoftToHardTopology`、引入 `HyperNodeGradientResult` 的
`Applied/Abstain` 契约，并修改 framework/NTA/GTA 行为。因此原 018 的“零源码差异”
结论无效，不得作为生产发布依据。

本文件仅保留审计线索。Bug 018 的权威基线、方案和验证门槛以重写后的
`DESCRIPTION.md`、`PROPOSAL_COMMIT.md` 为准。
