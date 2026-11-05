> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 018：18deb5219 基线上的 PodGroup 反亲和 B+C 临时修复

## 状态

- 类型：Bug
- 状态：DONE
- 初次创建日期：2026-08-03
- 第二次基线纠正日期：2026-08-03
- 权威源码基线：`18deb521904e15b426ee596cc14bf3af18ac0de9`
- 基线提交：`fix(scheduler): preserve native soft topology on v1.14`
- 基线父提交：`b8af4d766f2ac14ddbb8f267462a673adc454823`
- 当前源码分支：`fix/018-podgroup-antiaffinity-bc-production-containment`
- 修复提交：`f954233d2ba7d3c512f0e1d7c1b999c9e829d14e`
- 当前阶段：实现、代码门禁、hard/native-soft Kind K0→K1 和清理均已完成

## 基线再次纠正

此前 018 认为生产只到 #5206 的 `b8af4d766`。用户进一步确认生产还包含直接后继
`18deb5219`。

因此：

- `18deb5219` 已删除 #5206 soft→hard convert；
- Job/SubJob soft network topology 保持 native soft；
- NTA soft callback 使用 `HyperNodeGradientAbstain`，只参与 score，不再成为 hard filter；
- framework 已引入 `HyperNodeGradientResult{Applied, Gradients}`；
- GTA/NTA hard constraint 可以正确求交；
- 但 `allocateForJob` 仍把同一个 gradient layer 中的 roots 逐个作为单根事务。

旧 `b8af4d766` 实现和 Kind 结果已归档到
`SUPERSEDED_B8AF4D_VALIDATION.md`，不得作为新生产基线的发布依据。

## 新基线实测结论

在纯净 `18deb5219` 临时工作树中执行同一个 B+C Action 复现：

1. required PodGroup anti-affinity + hard SubGroups：
   - 内部 50 轮全部无法产生 Bind；
2. required PodGroup anti-affinity + native-soft SubGroups：
   - 同样无法产生 Bind；
3. native-soft SubGroups，但没有 required PodGroup anti-affinity：
   - 10/10 次成功；
   - framework universe 保留 coarser root，现有 allocate 可在上层 root 中完成 soft fallback。

所以 `18deb5219` 没有解决 B+C。它解决的是 native soft 的插件/框架语义，而不是
multi-root transaction。

同时，candidate forest 的运行时入口不应泛化成“所有 SubGroup soft”。真正需要
forest 的边界仍是：

- required PodGroup anti-affinity 删除了共同上层 root；
- 一个 gradient layer 只剩多个精确合法 roots（B+C）；
- Job 没有 real hard topology。

PAA + native-soft SubGroup 已满足这个入口；需要新增显式测试和注释，而不是扩大到无
PAA 的 soft workload。

## 生产目标

只修复精确 B+C transaction：

```text
eligible roots = B + C
allowed scope  = subtree(B) union subtree(C)
envelope       = LCA(B, C), only for traversal
```

要求同时支持：

- hard SubGroup network topology；
- native-soft SubGroup network topology；
- native-soft Job topology；
- Job 无 topology；
- required PodGroup anti-affinity；
- 不改变 real-hard Job 和无 PAA native-soft fallback。

详细设计、分支纠正和验证计划见 `PROPOSAL_COMMIT.md`。

## 最终结果

- 生产运行时代码只修改 `pkg/scheduler/actions/allocate/allocate.go`；
- required PAA + hard SubGroups：18deb 基线 4/4 Pending，修复后 4/4 Running；
- required PAA + native-soft SubGroups：18deb 基线 4/4 Pending，修复后 4/4 Running；
- 两个修复后场景都只使用 B/C，A 仍只有 anchor；
- native soft 保持 abstain + score，没有被重新转换成 hard；
- 生产发布只需替换 `volcano-scheduler` 镜像，不需要更新 controller、webhook、
  CRD、Helm 配置或 workload API。
