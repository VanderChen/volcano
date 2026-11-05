> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 020：preferred SubGroup 反亲和计数评分修复提案

## 审批状态

- 根因调查：完成；
- 修复方案：用户于 2026-08-04 明确批准；
- 验证用例：用户于 2026-08-04 明确批准；
- 实现：完成；
- Kind 验证：完成；
- 清理：完成。

实现分支：`fix/020-preferred-subgroup-antiaffinity-count-scoring`。

## 1. 调查基线

```text
branch: fix/018-podgroup-antiaffinity-bc-production-containment
HEAD:   c0828a799 (fix(scheduler): preserve preferred subgroup traversal order)
status: clean（调查开始时）
```

现存 Kind 集群仅做了只读检查：4 个 arm64 节点，scheduler 镜像为
`volcanosh/vc-scheduler:dev-004-codegen`；`network-topology-aware` 和
`group-topology-affinity` 的 plugin weight 都为 10。未在该长生命周期集群中创建
020 workload 或修改 Node 资源。

调查阶段执行：

```bash
go test ./pkg/scheduler/plugins/group-topology-affinity \
  -run 'TestHyperNodeOrderFnForSubGroupPreferredTerms' -count=1
```

结果：PASS（`1.507s`）。该测试只覆盖一个 peer 占用一个 domain 的二元场景，
不能发现多 peer 数量被集合化的问题。

## 2. 根因

### E1：peer 占用被压缩成集合

`peerSubJobOccupiedHyperNodesAtTier` 遍历匹配的 peer SubJob，调用
`CollectSubJobOccupiedHyperNodesAtTier` 后把 domain 写入 `sets.Set[string]`：

```text
domain A 有 1 个 peer -> A 存在
domain A 有 4 个 peer -> A 仍然只表示“存在”
```

所以 scorer 无法知道 domain 内 peer SubJob 的数量。

相关代码：

```text
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go
  peerSubJobOccupiedHyperNodesAtTier

pkg/scheduler/api/topology_affinity_info.go
  CollectSubJobOccupiedHyperNodesAtTier
```

### E2：preferred 反亲和每条 term 对每个已占用 domain 只扣一次

`scorePreferredSubGroupAntiAffinityTerms` 当前只判断：

```go
if peerHyperNodes.Has(ancestorHyperNode) {
    scores[hyperNode] -= weightFactor * FullScore
}
```

当三个目标 domain 的 decode 数量从 `[1,1,1]` 变成 `[2,1,1]` 后，三者仍都只
命中一次固定 penalty，GTA 分数完全相同。

### E3：同分明确进入随机选择

`allocate.selectBestHyperNodeForSubJob` 调用
`util.SelectBestHyperNodeAndScore`；后者对最高分并列 HyperNode 使用
`rand.Intn`。因此在 GTA 无法区分占用数量以后，结果天然不稳定。

### E4：其他 plugin 可以放大聚集

`PrioritizeHyperNodes` 会累加所有启用的 HyperNodeOrder plugin 分数。
`network-topology-aware` 默认提供 HyperNode binpack 分数；当 GTA 对所有已占用
domain 给出同分时，binpack 或其他 plugin 可以继续偏向更满的 domain。这解释了
为什么现场不仅随机，而且更常出现 decode 聚集。

### E5：非纯数字 shard 值可让 decode 整批先调度

`NewSubJobInfo` 只在 `strconv.Atoi(matchValues[0])` 成功时设置 `MatchIndex`。
`Session.SubJobOrderFn` 在 plugin 没有给出非零顺序后，按 `MatchIndex`、再按 UID 排序。

因此若 shard label 是 `shard-0` 而不是 `0`：

```text
所有 MatchIndex = 0
decode UID < prefill UID
=> 8 个 decode 可连续进入 placement，然后才是 8 个 prefill
```

在这个顺序下，当前布尔 occupancy scorer 在每个 domain 先有 1 个 decode 后失去梯度，
后续随机/binpack 很容易形成 decode `[4,2,2]`，剩余卡容量强制 prefill 为
`[0,4,4]`，即用户报告的 `[dddd][ddpppp][ddpppp]`。

这是复现频率的放大条件，不建议在本 hotfix 中改变 SubJob order：即使顺序交错，
count-blind scorer 仍然错误；改变 order 也会影响所有现有 SubJob workload。

### 结论

用户的核心判断成立：当前 preferred `subGroupAntiAffinity` 只表达“是否冲突”，
没有表达“冲突 peer 有多少”。修复点在 GTA preferred 评分，不在 HyperNode 树解析、
GPU predicate 或 required filtering。

## 3. Volcano 社区历史与 maintainer 观点

调研日期：2026-08-04。范围只包括 `volcano-sh/volcano`，不把 Kubernetes
scheduler 的设计作为本任务的论据。

### 3.1 `#5078` 是通用 spreading/binpack 冲突，不是本 bug 的直接先例

[`#5078 Balancing Inference Spreading vs. Training Binpacking`](https://github.com/volcano-sh/volcano/issues/5078)
讨论的是共享集群中 Deployment/工作负载的 spreading 与训练 binpack 冲突。提议的
`SpreadLimit` 还要表达 `maxPodsPerNode`、`maxSpreadNodes`，并维护 workload 跨节点的
occupancy。maintainer/contributor 的主要意见是：

- 仅提高 preferred anti-affinity 权重只能“在一定程度上”改善；权重过高会扩散到过多
  节点并制造资源碎片；
- 异构共享集群里很难天然切出不会互相影响的 inference/training 资源池；
- 不存在一个对所有 workload 都最优的全局 spread/binpack 策略；
- 讨论后续明确指向 group/sub-group affinity，说明已知关联的同 Job SubGroup 比通用
  Deployment spreading 更适合由 GTA 表达。

本任务不实现 spread domain 上限，也不试图解决 `#5078`。当候选 domain 仍有空域时，
现有 preferred anti-affinity 已会优先空域；020 只修正“所有候选都已占用后，占用数量
不同却完全同分”的信息丢失。

### 3.2 旧 `task-topology` 也是单 Job 内能力，但复杂度来自更大的目标

用户关于“同一个 Job 内关联可控”的方向成立，但“旧方案只处理不同 PodGroup 或
Deployment”并不完整。Volcano 旧
[`task-topology` design`](https://github.com/volcano-sh/volcano/pull/274)
同样在一个 Job 内建立 task 关系。它允许任意 task affinity/anti-affinity 矩阵，构造
bucket，维护 task/bucket/node 的动态状态，并同时参与 TaskOrder、NodeOrder、Allocate
等生命周期。

社区长期反馈也集中在这套模型难理解和状态难维护：早期 review 直接指出 bucket 和
使用方式难以理解；[`#4003`](https://github.com/volcano-sh/volcano/issues/4003)
发现缺少 Deallocate 回滚会让一次失败 statement 的状态污染后续调度；对应
[`#5248`](https://github.com/volcano-sh/volcano/pull/5248) 仍需补完整的反向状态更新。
所以 maintainer 对“以前做过、但很复杂且难用”的判断有充分历史依据，而且不只发生
在跨 PodGroup 场景。

020 与旧 task-topology 的边界不同：

| 维度 | 旧 task-topology | 020 |
| --- | --- | --- |
| 关系模型 | 任意 task 关系矩阵和 bucket | 现有 PodGroup `SubJobs` 与一条现有 term |
| 状态 | 独立可变 JobManager，需 Allocate/Deallocate 对称维护 | 只读取 scheduler 已维护的 task placement |
| 目标 | bucket 构造、task 顺序、资源均衡的组合策略 | preferred scorer 保留 peer multiplicity |
| API | annotation/parser 和专用使用模型 | 无 API/CRD/codegen 变化 |
| 算法边界 | 多阶段启发式布局 | 单次候选 HyperNode 的局部排序 |

也就是说，不能以“同 Job”本身证明修改必然简单；本方案之所以小，是因为不重建布局
模型、不引入新状态，只在现有 peer 遍历中把 `set` 升为 `domain -> count`。

### 3.3 maintainer 反对的是 API/模型膨胀和全局最优承诺

[`#5349`](https://github.com/volcano-sh/volcano/pull/5349) 的 review 起初认为
NetworkTopology、TopologyAffinity、SubGroupTopologyAffinity 多套结构难以理解，随后
建议收敛成一个统一 `TopologyAffinity`，并在其中明确保留同 PodGroup 的
`SubGroupAffinity` / `SubGroupAntiAffinity`。这说明 maintainer 并未否定同 Job
SubGroup 关系，而是要求模型收敛、语义清晰。当前代码已经采用这条简化路径；
[`#4721`](https://github.com/volcano-sh/volcano/pull/4721) 也已把 SubJob 作为一等调度
对象接入分配流程。

[`#3388`](https://github.com/volcano-sh/volcano/pull/3388) 的网络拓扑讨论则给出另一条
清晰边界：当前实现接受局部贪心；若承诺全局最优，需要 DP/backtracking，并会大幅修改
allocate action。这一担忧对“保证任意资源、任意 Pod 顺序、任意 plugin 组合下的全局
最优布局”完全成立。

因此 020 应按下面的局部 invariant 评审，而不是作为新的通用均衡功能：

```text
在同一 applicable preferred anti-affinity term 下，其他条件相同时，
包含更少匹配 peer SubJob 的 domain 应获得更高 GTA score。
```

它不承诺在外部 plugin 权重覆盖、异构资源或其他约束存在时仍得到全局最优布局。
用户的 3×8 卡结果是这个 invariant 的受控回归用例，不是通用调度保证。

### 3.4 结论与建议

maintainer 的历史担忧应拆成两部分：

1. 对通用 spread limit、任意 task 图、全局最优布局的担忧成立，这些功能确实会扩大
   API、状态和 allocate 生命周期；
2. 若把同样的复杂度结论直接套到 020，则范围过宽。020 不增加关联模型，只修复现有
   preferred term 在 `set` 化时丢失数量信息，复杂度与当前 peer 遍历同阶，且 required
   规则和 plugin 组合边界不变。

建议继续以“小型 scorer correctness fix”推进，并避免使用“新增均衡能力”或“保证
理想布局”作为 PR 主张。PR 的核心证据应是 `[2,1,1]` 不应和 `[1,1,1]` 得到相同的
候选相对分数，以及该修复在用户受控 workload 中消除随机退化。

## 4. 修复语义

对某个 applicable preferred anti-affinity term `t` 和候选 HyperNode `h`，定义：

```text
C_t(h) = h 在 term tier 的 ancestor domain 中，匹配该 term 的 peer SubJob 数量
```

计数单位是 **SubJob**，不是 Pod：

- 单策略 term（如 `[decode]`）只统计同 policy 的 peer SubJob；
- 多策略 term 只统计 term 中不同 policy 的 peer SubJob，保持当前 cross-policy 语义；
- 一个 peer SubJob 若实际跨越多个 term-tier domain，则在每个所占 domain 各贡献 1，
  同一 peer 在同一 domain 内不重复计数。

preferred 仍是 soft：计数只改变分数，不删除候选。

## 5. 拟议实现

### 5.1 增加 peer domain 计数 helper

在 `group-topology-affinity` 内增加等价于下述职责的 helper：

```text
peerSubJobOccupiedHyperNodeCountsAtTier(...) map[string]int
```

它复用现有 `CollectSubJobOccupiedHyperNodesAtTier` 的精确 task-placement 路径，
但按匹配 peer SubJob 累加 domain 数量，不提前降为 set。

现有 set helper 保留给：

- required `subGroupAffinity` / `subGroupAntiAffinity`；
- 当前 preferred `subGroupAffinity`。

本 hotfix 不改变 hard membership。

### 5.2 preferred anti-affinity 使用有界计数 penalty

对一条 term，先求候选 domain 中的最大 peer 数 `maxCount`。当 `maxCount > 0`：

```text
penalty(h) = termWeight/100 * FullScore * C_t(h)/maxCount
score(h)   = max(ZeroScore, score(h) - penalty(h))
```

性质：

- `[1,0,0]`：保持当前行为，已占用 domain 扣完整 term penalty；
- `[2,1,1]`：分数比例为 `[0, 0.5, 0.5]`（单条 weight=100 term）；
- `[1,1,1]`：仍然同分，允许在精确均衡状态下由其他 plugin/随机 tie-break 决定；
- 输出继续处于现有 GTA 的有界 score 范围，term weight 和 plugin weight 仍可与其他
  HyperNodeOrder plugin 组合；
- 不采用 `penalty = count * FullScore`，避免 peer 数增加后让 GTA 分数无界并绕过
  plugin weight 的组合语义。

### 5.3 日志

把 preferred anti-affinity V(4) 详情扩展为：

```text
peerCount, maxPeerCount, normalizedPenalty, scoreBefore, scoreAfter
```

不新增 metrics、事件或 API 字段。

## 6. 范围

预计实现文件：

```text
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity_test.go
pkg/scheduler/actions/allocate/allocate_test.go
```

可能只为测试复用增加 `pkg/scheduler/util/test_utils.go` 的 builder 参数；不计划修改
allocate 生产代码。

明确不修改：

- PodGroup API、CRD、codegen、webhook；
- required affinity/anti-affinity filtering；
- preferred `subGroupAffinity`；
- SubJob 顺序、Node predicate、GPU device 分配或 HyperNode binpack；
- 把 preferred 规则升级为 hard，或承诺无视其他 plugin 权重的绝对均衡。
- `#5078` 所讨论的 `maxSpreadNodes` / `maxPodsPerNode` 或通用 workload spreading；
- task-topology 式 bucket、任意 task 关系图或独立可变 occupancy cache；
- DP、backtracking、联合放置或任意调度顺序下的全局最优保证。

## 7. 验证用例

### U1：多 peer domain 计数评分（修复前最小失败用例）

- Purpose：证明 `[2,1,1]` 不再退化为三路同分。
- Scope：`group-topology-affinity` table-driven unit test。
- Setup：3 个 tier-1 HyperNode `a/b/c`；当前 decode SubJob 加上 4 个已分配 decode
  peer，domain 数为 `a=2,b=1,c=1`；单策略 preferred term weight=100。
- Command：

  ```bash
  go test ./pkg/scheduler/plugins/group-topology-affinity \
    -run 'TestHyperNodeOrderFnForSubGroupPreferredAntiAffinityCounts' -count=1
  ```

- Expected：`a=0`，`b=c=50`（乘 plugin weight 前的等价分数）；`b/c > a`。
- Pass/fail：若三者同分、计数按 Pod 重复或候选被过滤则失败。

### U2：边界与既有语义

- Purpose：保持二元行为、soft fallback 和 policy peer 选择规则。
- Scope：GTA table-driven unit test。
- Cases：
  1. 无 peer：所有候选保持 FullScore；
  2. `[1,0,0]`：空 domain 优于已占用 domain；
  3. `[1,1,1]`：所有候选同分且均保留；
  4. 单策略 term 不统计其他 policy；
  5. 多策略 term 只统计不同 policy；
  6. 一个 peer 跨两个 domain 时，每个 domain 计 1；
  7. 非法 tier/weight 继续走现有错误或跳过契约。
- Command：

  ```bash
  go test ./pkg/scheduler/plugins/group-topology-affinity -count=1
  ```

- Pass/fail：任一 candidate membership 改变、peer 范围改变或现有测试失败即失败。

### U3：required 规则不回归

- Purpose：确认 hard anti-affinity 仍是 set-based 拒绝，不受 preferred count scorer 影响。
- Scope：GTA focused unit test。
- Setup：覆盖 single-policy、multi-policy、domain 不足三类既有 required 场景。
- Command：

  ```bash
  go test ./pkg/scheduler/plugins/group-topology-affinity \
    -run 'TestHyperNodeGradientForSubGroupHardTerms' -count=1
  ```

- Expected：hard conflict 继续过滤；无 domain 时保持 Pending/空 gradient。
- Pass/fail：required 规则软化或 peer membership 改变即失败。

### U4：用户 3×8 卡布局的 allocate 回归

- Purpose：覆盖 Session plugin 接线、SubJob 顺序、dry-run 状态、GTA/NTA 分数组合和
  资源 predicate。
- Scope：`pkg/scheduler/actions/allocate` focused in-memory scheduler test。
- Setup：
  - 3 个 tier-1 HyperNode，每个映射 1 个 Node；
  - 每节点自定义卡资源 8；
  - 8 个 `decode` shard（每个 2 卡）和 8 个 `prefill` shard（每个 1 卡）；
  - shard label 使用 `shard-0` 到 `shard-7`，显式覆盖现场可疑的非纯数字顺序；
  - `subGroupSize=1`、两个 `minSubGroups=8`、`minMember=16`；
  - 两条用户提供的 preferred anti-affinity term，weight=100；
  - 启用实际 `gang`、`predicates`、`network-topology-aware`、
    `group-topology-affinity` 接线。
- Command：

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'TestAllocatePreferredSubGroupAntiAffinityBalancesOversubscribedDomains' \
    -count=100
  ```

- Expected：每轮 16 个 Bind；每节点卡使用量为 8；decode 数量排序后为 `[3,3,2]`；
  decode=2 的节点有 4 个 prefill，另外两个节点各有 2 个 prefill（节点名可置换）。
- Pass/fail：任一轮 Pending、资源超配、decode 出现 `[4,2,2]` 或其他非均衡分布即失败。

该精确布局仅作为固定资源、固定 plugin 配置和固定 workload 的回归标准；它不构成
GTA 对其他资源拓扑、task 顺序或 plugin 权重组合的全局均衡承诺。

### U5：评分插件组合边界

- Purpose：确认 GTA count score 仍通过 `PrioritizeHyperNodes` 与 NTA/plugin weight 相加，
  没有变成隐藏的 hard/lexicographic 选择。
- Scope：allocate/framework focused test。
- Setup：构造一个较高权重的测试 HyperNodeOrder plugin 明确覆盖 GTA 偏好。
- Expected：聚合最高分候选胜出；所有候选仍存在；降低竞争 plugin 权重后较少 peer 的
  domain 胜出。
- Pass/fail：preferred 过滤候选、忽略 plugin weight 或 score 无界即失败。

### B1：格式、包测试、构建和 verify

```bash
./hack/update-gofmt.sh
git diff --check
go test ./pkg/scheduler/api \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/actions/allocate -count=1
make vc-scheduler
make verify
```

全部必须通过。若出现与本任务无关的基线失败，单独复跑并记录，不静默豁免。

### K0：当前 HEAD 的独立 Kind 基线复现

- Purpose：在修改前记录真实 scheduler 的布局频率和同分日志。
- Scope：task-specific Kind manual e2e。
- Setup：新建最多 4 个 Kubernetes Node 的 `volcano-dev-020` 集群（1 control-plane +
  3 worker，符合 8 节点上限）；每个 worker 暴露 8 个测试扩展资源；每个 worker 一个
  tier-1 HyperNode，`tierName=volcano.sh/hypernode`；本地当前 HEAD scheduler 镜像；
  独立 namespace。
- Commands（最终以保存的 task artifacts 为准）：

  ```bash
  kind create cluster --name volcano-dev-020 --config issues/bugs/020-*/kind-config.yaml
  TAG=dev-020-baseline BUILDX_OUTPUT_TYPE=docker make images
  kind load docker-image volcanosh/vc-scheduler:dev-020-baseline --name volcano-dev-020
  kind load docker-image volcanosh/vc-controller-manager:dev-020-baseline --name volcano-dev-020
  kind load docker-image volcanosh/vc-webhook-manager:dev-020-baseline --name volcano-dev-020
  helm upgrade --install volcano installer/helm/chart/volcano \
    --namespace volcano-system --create-namespace \
    --set basic.image_tag_version=dev-020-baseline \
    --set basic.image_pull_policy=IfNotPresent

  kubectl patch node volcano-dev-020-worker --subresource=status --type=merge \
    -p '{"status":{"capacity":{"example.com/gpu":"8"}}}'
  kubectl patch node volcano-dev-020-worker2 --subresource=status --type=merge \
    -p '{"status":{"capacity":{"example.com/gpu":"8"}}}'
  kubectl patch node volcano-dev-020-worker3 --subresource=status --type=merge \
    -p '{"status":{"capacity":{"example.com/gpu":"8"}}}'
  kubectl wait --for=jsonpath='{.status.allocatable.example\.com/gpu}'=8 \
    node/volcano-dev-020-worker node/volcano-dev-020-worker2 node/volcano-dev-020-worker3 \
    --timeout=120s

  kubectl apply -f issues/bugs/020-*/hypernodes.yaml
  kubectl apply -f issues/bugs/020-*/podgroup.yaml
  kubectl wait --for=jsonpath='{.status.phase}'=Inqueue podgroup/bug-pg --timeout=120s
  kubectl apply -f issues/bugs/020-*/workloads.yaml
  issues/bugs/020-*/run-layout-repetitions.sh 30 baseline
  ```

  对同一 workload 清理并重建至少 30 次，记录每次 node、role、shard 和卡请求布局，
  同时采集 scheduler V(4) preferred score 日志。
- Expected：至少一次出现 decode 非 `[3,3,2]`，或日志直接证明 `[2,1,1]` 时三候选
  GTA penalty 相同；若 30 次恰好都得到理想布局，日志证据仍可判定基线缺陷。
- Pass/fail：能观测到“占用数量不同但 GTA 同分”即成功复现；若 deployed baseline
  不包含当前源码或 workload 与用户对象不等价则复现无效。

### K1：修复后的 30 次稳定布局

- Purpose：证明修复在真实 Kind 调度路径稳定生效。
- Scope：同 K0，换为修复后本地镜像 `dev-020-fixed`。
- Commands：重新执行 K0 的 image build/load/Helm upgrade，tag 改为
  `dev-020-fixed`，然后运行：

  ```bash
  issues/bugs/020-*/run-layout-repetitions.sh 30 fixed
  ```

- Expected：30/30 次 16 Pod 全部 Running；decode 均为 `[3,3,2]`；卡使用 8/8/8；
  日志中 `[2,1,1]` 的较少 peer domain 分数严格更高。
- Pass/fail：任一轮出现 `[4,2,2]`、Pending、超配或 term 变成 hard 即失败。

这里的 30/30 是用户受控案例的 deterministic regression gate，不外推为通用最优性。

### K2：soft fallback 与重调度

- Purpose：所有 domain 已占用时仍能继续调度，并能读取 session 开始前已有 Pod 的
  精确 domain 计数。
- Scope：Kind manual workload。
- Setup：先形成 decode peer 数 `[2,1,1]`，再创建一个同 PodGroup 的待调度 shard；
  另做 domain 数小于 shard 数的完整 workload。
- Commands：

  ```bash
  kubectl apply -f issues/bugs/020-*/reschedule-baseline.yaml
  kubectl wait --for=condition=Ready pod -l issue=020,case=reschedule-baseline \
    --timeout=180s
  kubectl apply -f issues/bugs/020-*/reschedule-target.yaml
  kubectl wait --for=condition=Ready pod -l issue=020,case=reschedule-target \
    --timeout=180s
  issues/bugs/020-*/assert-reschedule-layout.sh
  ```

- Expected：新 shard 选择 peer 数为 1 的任一可行 domain；domain 全占用后不 Pending；
  删除并重建一个 shard 后仍按当前真实 task placement 计数。
- Pass/fail：选择更拥挤 domain（无更高外部聚合分数时）、fallback 失败或仅靠过期
  `AllocatedHyperNode` LCA 计数即失败。

### K3：清理

```bash
kind delete cluster --name volcano-dev-020
```

保存 K0/K1/K2 的命令、Pod/Node 快照、scheduler 日志摘要和逐轮布局结果到本任务目录，
确认集群删除后才完成清理。

## 8. 完成条件

只有在以下条件全部满足后才能把任务改为 `DONE`：

1. 用户批准本提案和 U1-U5、B1、K0-K3；
2. 修复前最小单测失败且 K0 有有效基线证据；
3. 实现和 focused/package/build/verify 全部通过；
4. K1 30/30 稳定通过，K2 soft fallback/重调度通过；
5. 结果和 artifacts 写回本文件，任务集群已清理。

## 9. 实现结果

### 9.1 实际修改范围

生产代码只有一个文件：

```text
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go
  39 insertions, 8 deletions
```

测试代码：

```text
pkg/scheduler/actions/allocate/allocate_test.go                       +169
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity_test.go +172
pkg/scheduler/util/scheduler_helper_test.go                           +55
```

总 diff：4 files changed，435 insertions，8 deletions；其中生产逻辑净增加 31 行。
未修改 API、CRD、codegen、allocate 生产代码、scheduler framework 或 cache state。

实际实现：

1. 新增只读 helper `peerSubJobOccupiedHyperNodeCountsAtTier`，在现有 peer SubJob
   遍历中保留 `domain -> peer SubJob count`；
2. 原 set helper 从计数 helper 的 key 构造 set，因此 required 和 preferred affinity
   的 membership 语义不变；
3. preferred SubGroup anti-affinity 按候选 domain 的 `peerCount/maxPeerCount` 计算有界
   penalty；
4. V(4) 日志增加 `peerCount`、`maxPeerCount` 和 `normalizedPenalty`。

最终 commit：

```text
8703e8779 fix(scheduler): score preferred subgroup anti-affinity by peer count
```

## 10. Go 与构建验证结果

### 10.1 修复前红灯证据

在生产代码修改前新增并执行 U1：

```bash
go test ./pkg/scheduler/plugins/group-topology-affinity \
  -run '^TestHyperNodeOrderFnForSubGroupPreferredAntiAffinityCounts$' -count=1
```

结果：FAIL，符合根因预期：

```text
score for sn-b: want 50, got 0
score for sn-c: want 50, got 0
```

当 peer 为 `a=2,b=1,c=1` 时，旧实现把三者都扣为 0。

### 10.2 U1-U5 和 focused package

修复后结果：

```text
go test ./pkg/scheduler/plugins/group-topology-affinity -count=1
PASS

go test ./pkg/scheduler/actions/allocate \
  -run '^TestAllocatePreferredSubGroupAntiAffinityBalancesOversubscribedDomains$' \
  -count=100
PASS (48.386s, 100/100)

go test ./pkg/scheduler/util \
  -run '^TestPrioritizeHyperNodesAggregatesPluginScores$' -count=1
PASS
```

U5 证明较高的 competing plugin score 仍能覆盖 GTA 偏好；降低 competing score 后，
较少 peer 的 domain 胜出；无分数 candidate 仍保留。

最终 focused package 门禁：

```bash
go test ./pkg/scheduler/api \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/util \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/actions/allocate -count=1
```

全部 PASS；`pkg/scheduler/actions/allocate` 用时 `96.942s`。

### 10.3 格式和构建

```text
./hack/update-gofmt.sh     PASS
hack/verify-gofmt.sh      PASS
git diff --check          PASS
make vc-scheduler         PASS
```

### 10.4 仓库级基线问题

`make verify` 未通过，失败发生在与 020 无关的 codegen 脚本：

```text
hack/generate-internal-groups.sh: line 80: GOPATH: unbound variable
```

在失败前，旧 `hack/generate-groups.sh` 的无版本 `go get` 还临时把
`k8s.io/code-generator` 从 `v0.34.1` 升到 `v0.36.3` 并生成 repo 根目录下的
`volcano.sh/`。这些命令产生的非任务修改已恢复/移出，最终工作树只剩 4 个 020 源码
和测试文件。

`make unit-test` 在 Darwin 上因 Linux-only agent/network-qos 基线返回失败，包括：

```text
build constraints exclude all Go files (agent QoS handlers)
runc Linux syscall constants undefined on darwin
networkqos/tc existing interface mismatch
networkqos eBPF platform expectation failure
```

同一次门禁中的全部 scheduler actions、api、cache、framework、plugins、util 均 PASS，
包括本任务的 GTA、NTA 和 allocate 包。

## 11. Kind 验证结果

### 11.1 集群、拓扑和镜像

任务集群：`volcano-dev-020`，Kubernetes `v1.36.1`，arm64：

```text
1 control-plane + 3 workers
3 tier-1 HyperNodes (volcano.sh/hypernode)，每个映射一个 worker
1 tier-2 root，nodeCount=3
每个 worker 测试扩展资源 example.com/gpu=8
```

镜像：

```text
volcanosh/vc-scheduler:dev-020-baseline
  sha256:d34901133bbac3ad00ec34e7be2821751901adeb68dcc33d9853272f1d925f96

volcanosh/vc-scheduler:dev-020-fixed
  sha256:e90fd0fb4c6eda3c655e7807b4985843db06f7c1f9cc0e2636b2d8b13cba0f05

volcanosh/vc-controller-manager:dev-020-{baseline,fixed}
  sha256:1606aeab8c9a0523ac0740c616867eb8c5a2bbd46c7ab66dd1b17c8ec71d312f

volcanosh/vc-webhook-manager:dev-020-{baseline,fixed}
  sha256:bcba3f6c0dd4134f017644d7daf99bf4b59e22b1d80288366be0c3a631c7af89
```

baseline 和 fixed scheduler 分别从 `c0828a79966a` 与当前 020 工作树构建；controller
和 webhook 没有 020 差异，因此两轮复用同一重新构建的二进制镜像。

scheduler 配置同时启用 NTA 和 GTA，plugin weight 都为 10；NTA 只对
`example.com/gpu` 进行 HyperNode binpack 评分。

### 11.2 K0：基线 30 次复现

命令：

```bash
./run-layout-repetitions.sh 30 baseline
```

结果：`nonIdeal=30`。30/30 都得到 decode 排序 `[1,3,4]`：

```text
[dddd][dddpp][dpppppp]
GPU: 8/8/8
```

节点名随机置换，但角色数量稳定为上述非理想形态。另一次优化脚本前的 3 轮预跑也全部
为 `[1,3,4]`。这证明 bug 不是资源 predicate 失败：三节点 GPU 均被恰好填满，失败
发生在 preferred domain 排序。

### 11.3 K1：修复后 30 次稳定布局

命令：

```bash
./run-layout-repetitions.sh 30 fixed
```

结果：`nonIdeal=0`。30/30 都得到 decode 排序 `[2,3,3]`：

```text
[dddpp][dddpp][ddpppp]
GPU: 8/8/8
```

V(4) 日志直接显示 count-aware gradient，例如：

```text
peer counts [1,2,1], maxPeerCount=2
normalized penalties [0.50,1.00,0.50]
scores after [0.50,0.00,0.50]

peer counts [2,3,2], maxPeerCount=3
normalized penalties [0.67,1.00,0.67]
scores after [0.33,0.00,0.33]
```

### 11.4 K2：session 前已有 peer 和删除重建

初始 4 个 Running decode 的 domain 数量为 `[2,1,1]`，拥挤节点为
`volcano-dev-020-worker`。新增 target 两次的结果：

```text
attempt=1 targetNode=volcano-dev-020-worker2
attempt=2 targetNode=volcano-dev-020-worker3
```

两次都避开已有 2 个 peer 的节点；两次之间删除并重建 target，证明 scorer 读取当前
task placement，而不是依赖过期的一次性计数状态。所有 domain 已占用时 target 仍能
Running，preferred 没有变成 hard filter。

### 11.5 K3：清理

`kind delete cluster --name volcano-dev-020` 成功，4 个任务节点全部删除。随后
`kind get clusters` 只剩用户原有的 `kind` 集群；基线临时 git worktree 也已移除。

## 12. 保存的验证 artifacts

```text
kind-config.yaml
scheduler.conf
hypernodes.yaml
podgroup.yaml
workloads.yaml
run-layout-repetitions.sh
reschedule-podgroup.yaml
reschedule-baseline.yaml
reschedule-target.yaml
assert-reschedule-layout.sh
```
