> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 009：修改提案与验证计划

## 审批状态

- 当前阶段：实现与验证完成
- 批准日期：2026-07-28
- 批准方式：用户明确要求基于
  `fix/008-hypernode-job-soft-affinity-across-subjobs` 新建 009 分支并修复
- 实现状态：已提交
- 源码基线：`ec7141e5d`
- 修复分支：`fix/009-podgroup-antiaffinity-multi-domain-allocation`
- 实现提交：`473c6538b`

## 设计目标

1. 让 required PodGroup anti-affinity 过滤后同一 Job gradient 中的 B、C
   能作为精确联合候选域，而不是两个互斥的完整 Job 容器。
2. 保持每个 SubJob 自身的 network topology、predicate、资源过滤和 HyperNode
   评分语义。
3. 不把 B、C 的共同父级 H 当成候选域代理，避免把已被过滤的 A 重新引入。
4. 保持 Job 级 hard network topology 的单域上限；它不能因本修复被放宽。
5. 将实现限制在 scheduler allocate 包内，不修改 API、CRD、controller、
   framework callback 或 topology 插件。
6. 不引入全局回溯搜索，控制代码量和运行时复杂度。

## 推荐方案：allocate 内部的多根 Job 候选域

### 1. 增加仅供 allocate 使用的候选域

在 `pkg/scheduler/actions/allocate/allocate.go` 中增加私有结构，名称可在实现时按
项目风格微调：

```go
type jobCandidateDomain struct {
    key   string
    roots []*api.HyperNodeInfo
}

type jobDomainSolution struct {
    stmt                 *framework.Statement
    worksheet            *JobWorksheet
    score                float64
    allocatedHyperNode   string
    subJobPlacements     map[api.SubJobID]string
}
```

`roots` 表示精确集合并集。例如 `{B,C}` 只允许搜索 B、C 的子树，不等价于
它们的 LCA H。`key` 只用于 dry-run 缓存、日志和确定性比较，不写入
`AllocatedHyperNode`。

### 2. 从一个 Job gradient layer 构造候选域

新增私有 helper，例如：

```go
buildJobCandidateDomains(job, hyperNodes)
```

规则：

1. Job 没有 required PodGroup anti-affinity 时，每个 HyperNode 仍生成一个
   singleton domain，完全保持当前行为。
2. Job 有 required PodGroup anti-affinity、且没有 Job 级 hard network
   topology 时，同一个 gradient layer 中的所有候选组成一个联合域。
3. 同时存在 Job 级 hard network topology、最高允许 tier 为 T 时，先按每个候选
   在 tier T 的 ancestor 分区；只有处于同一个 tier-T 容器中的候选才能组成联合域。
   这保证整个 Job 的 LCA 不会越过 T。
4. 无法解析到合法 tier-T ancestor 的候选保守拒绝并记录日志，不回退为跨域联合。
5. domain root 按 HyperNode 名称排序并去重，domain key 稳定，避免 map 遍历造成
   不确定结果。

示例：

```text
PAA 允许候选：B、C

无 Job hard network topology:
  domain-0 = {B,C}

Job hard highest tier = tier2:
  domain-0 = {B}
  domain-1 = {C}

Job hard highest tier = tier3，且 B、C 的 tier3 ancestor 都是 H:
  domain-0 = {B,C}
```

SubGroupPolicy 中的 hard network topology 不参与这里的 Job 域分区；它继续在
每个 SubJob 的 gradient 中生效。

### 3. 在联合域内合并 SubJob 候选

将 `allocateForSubJob` 的“单个 Job root”输入局部扩展为一个
`jobCandidateDomain`，或增加一个薄封装保持 singleton 调用兼容。

对 domain 中每个 root：

1. 调用现有 `HyperNodeGradientForSubJobFn`；
2. 调用现有 SubJob `FilterGradientsByMinResource`；
3. 按返回的 gradient rank 合并候选；
4. 按 HyperNode 名称去重并稳定排序。

随后复用现有逻辑：

- 对合并后的候选逐个执行 task/Node predicate dry-run；
- 保存和丢弃 `framework.Statement`；
- 调用现有 HyperNode order/scoring；
- 选择一个 SubJob placement；
- 更新该 SubJob 与 Job 的 dry-run `AllocatedHyperNode`。

生产案例中，Job domain 为 `{B,C}`，每个 hard tier-1 SubJob 得到：

```text
[B1, B2, C1, C2]
```

四个 SubJob 因而可以在同一个 Job transaction 中分别选择四个叶子节点。A
不属于任何 domain root，不会因为 B、C 的 LCA 是 H 而重新进入候选集。

### 4. 保持顺序式 SubJob 分配，不引入组合回溯

`allocateForJob` 仍按现有 SubJob priority queue 顺序分配。变化只在于每个 SubJob
看到当前 Job domain 中所有合法 root 的候选。

复杂度从单根路径的候选遍历扩展为：

```text
O(SubJob 数 × domain root 数 × 每个 root 下的候选数)
```

不枚举 SubJob placement 的笛卡尔积，不引入指数级状态空间、搜索上限或剪枝框架。
required SubGroup affinity/anti-affinity 因贪心选择而需要回溯的通用问题仍属于 Bug 004
的较大 topology allocation refactor，不在本任务处理。

### 5. 以 domain 联合资源执行 Job minResource 预过滤

当前 `FilterGradientsByMinResource` 在每个 HyperNode 上独立检查 Job
`spec.minResources`。如果 Job 最小资源只能由 B、C 联合满足，先做单根过滤会在
domain 构建前错误删除二者。

因此仅对 Job 级路径增加私有 domain 资源检查：

1. 先由插件 gradient 构造 domain；
2. 对一个 domain 所覆盖的 Kubernetes Node 做集合去重；
3. 聚合这些 Node 的 `Idle` 和 `FutureIdle`；
4. 任一聚合资源满足 Job `minResources` 时保留 domain；
5. singleton domain 的结果必须与现有 `FilterGradientsByMinResource` 一致；
6. SubJob 级资源过滤仍使用现有函数，不改变。

现有 `HyperNodeMinResourceFilterStats` 仍按 root HyperNode 汇总，以保持事件格式兼容；
额外日志打印稳定的 domain root 列表和联合资源判断。若一个 domain 整体失败，其所有
root 记录同一个 domain minResource 原因。

### 6. 保存实际 placement，不让 domain key 泄漏

当前 recorder 使用外层 Job HyperNode 作为 dry-run decision key。联合域没有单一
合法 HyperNode 名称，不能将 `B+C` 或 domain key 写入 recorder。

每个成功 dry-run solution 显式保存：

- dry-run 结束时实际的 `job.AllocatedHyperNode`；
- 本 transaction 中每个已放置 SubJob 的 `AllocatedHyperNode`；
- Statement、worksheet 和累计 score。

选择最终 solution 后：

1. Recover 该 solution 的 Statement；
2. 恢复对应 worksheet；
3. 使用 solution 中的实际 Job placement 作为 `SaveJobDecision` 的 HyperNode；
4. 在同一个实际 placement key 下写入最终 SubJob decisions。

这样无需修改 `recorder.go` 的数据结构或公共方法，最终 annotation/cache 中只出现真实
HyperNode 名称。若四个 SubJob 跨 B、C，Job placement 是它们的真实 LCA H；若实际
只使用 B，Job placement 仍是 B，不会被候选域 envelope 错误扩大为 H。

### 7. 保留 Bug 008 的 Job 级 soft topology 行为

当前工作树包含 Bug 008 对 Job 级 soft topology 跨 SubJob 选择的后续修改。候选域
不能以合成 key 参与 LCA 或 soft preference 判断。

最终 domain solution 比较必须读取 `solution.allocatedHyperNode`：

- soft preferred tier 判断使用实际 placement；
- SubJob 原有候选评分继续使用实际候选 HyperNode；
- 本地无可行 solution 时仍允许 soft fallback；
- hard mode 不经过 soft candidate narrowing。

实现前需先将 Bug 008 工作树整理到明确基线，再在其上添加本任务，不能覆盖现有修改。

## 预计代码范围

推荐实现应控制为：

- 修改：
  `pkg/scheduler/actions/allocate/allocate.go`
- 新增或修改 focused tests：
  `pkg/scheduler/actions/allocate/podgroup_antiaffinity_domain_test.go`
- 新增 Kind 验证清单：
  `issues/bugs/009-podgroup-antiaffinity-multi-domain-allocation-DONE/`

原则上不修改：

- `pkg/scheduler/framework/`
- `pkg/scheduler/plugins/group-topology-affinity/`
- `pkg/scheduler/plugins/network-topology-aware/`
- `pkg/scheduler/api/`
- `pkg/scheduler/actions/allocate/recorder.go`
- API、CRD、controller、webhook

如果实现证明 recorder 必须改动，应先更新本 proposal，说明无法通过 solution
placement 复用现有 recorder 的具体原因，并重新获得用户批准后再扩大范围。

## 不采用的方案

### 方案 A：把共同父级 H 重新放回 PodGroup anti-affinity gradient

这会把 H 的完整子树 A、B、C 暴露给 allocate。虽然 SubJob callback 当前可能再次
过滤 A，但它依赖策略被两个阶段重复执行，并使 Job minResource、诊断和其他插件看到
一个本来被 hard rule 排除的容器。该方案以扩大候选树代替精确集合，不采用。

### 方案 B：在 allocate 中直接用 `LCA(B,C)=H` 作为搜索根

与方案 A 相同，H 表示 `A ∪ B ∪ C`，不是要求的 `B ∪ C`。除非再增加 allowed-set
过滤，否则会重新引入 A；增加过滤后本质上就是本 proposal 的多根 domain。

### 方案 C：修改 framework callback 返回 candidate forest

显式 candidate-set/forest 是更通用的长期模型，但会修改 scheduler API type、
plugin callback、framework intersection 和两个 topology 插件，兼容面明显大于本次
生产缺陷。当前只在 allocate 内部消费已求交的 gradient layer 即可解决。

### 方案 D：复用 `edaaf83a7` 的全局 SubJob 联合搜索

该历史提交涉及 7 个源码/测试文件，净改动超过两千行，处理的是 SubJob placement
组合、回溯、剪枝和统一 solution 比较。它仍以一个外层 HyperNode 为 Job 搜索根，
并不直接表达本问题的 `{B,C}` 精确并集。将其整体引入既不能替代候选域，也会把 Bug
004 的广泛重构带入本修复，不采用。

## 验证用例

以下用例在用户批准后成为实施完成标准。

### U1：生产拓扑等价的 B+C 联合分配

- 目的：直接复现并修复当前 `2/4` Pending。
- 范围：`pkg/scheduler/actions/allocate` 完整 Action 单测。
- 拓扑：
  - 一个 tier-3 H；
  - H 下有 tier-2 A、B、C；
  - 每个 tier-2 下有两个 tier-1；
  - 每个 tier-1 含一个 Node；
  - 每个 Node 有 `huawei.com/ascend-1980: 8`。
- 已有负载：匹配 PodGroup 位于 A 的一个叶子节点。
- 目标负载：
  - 四个 pending Pod；
  - 四个 `MinAvailable=1` 的 SubJob；
  - 每个 Pod 请求 6 个 Ascend；
  - required PodGroup anti-affinity 位于 tier-2；
  - 每个 SubJob hard network topology 位于 tier-1；
  - Job `minMember=4`。
- 修复前预期：最多分配两个，Job 不 ready，无最终 Bind。
- 修复后预期：
  - 四个 Bind；
  - A 的两个叶子均未使用；
  - B、C 的四个叶子各使用一次；
  - 每个 SubJob 的 LCA 是自己的 tier-1；
  - Job 的实际 LCA 是 H。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestAllocatePodGroupAntiAffinityAcrossCandidateDomains$' \
    -count=50
  ```

- 通过标准：50 次均满足以上 placement 与 Bind 断言。

### U2：被 PAA 排除的 A 不得通过 LCA 扩张重新进入

- 目的：证明实现使用精确 `{B,C}`，不是 H 的完整子树。
- 范围：allocate focused unit test。
- 设置：A 保留更多空闲资源并获得更高 HyperNode/Node score，使其在被暴露时必然
  被选择。
- 预期：所有候选、dry-run operations 和最终绑定都不包含 A 下的 Node。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestPodGroupAntiAffinityCandidateDomainDoesNotReintroduceExcludedBranch$' \
    -count=50
  ```

- 通过标准：50 次均无 A 分支 placement。

### U3：Job hard network topology 保持单域边界

- 目的：防止 required PAA 的联合域放宽 Job 自身的 hard topology。
- 范围：候选域 helper 与完整 Action 单测。
- 变体：
  1. Job hard highest tier 为 tier-2：B、C 必须形成两个 singleton domain；
  2. Job hard highest tier 为 tier-3：B、C 可形成同一个 `{B,C}` domain。
- 预期：
  - 变体 1 的 Job 不得通过跨 B、C 满足四 Pod gang；
  - 变体 2 可以跨 B、C 调度；
  - SubJob hard tier-1 在两个变体中都保持不变。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestBuildPodGroupAntiAffinityCandidateDomainsWithHardJobTopology$' \
    -count=50
  ```

- 通过标准：domain 分区和最终 Job LCA 均符合 hard tier。

### U4：Job minResources 可以由联合域满足

- 目的：防止单根 minResource 预过滤在 domain 构造前删除 B、C。
- 范围：allocate domain resource helper 单测。
- 设置：
  - B、C 各有 16 个空闲 Ascend；
  - Job `spec.minResources` 为 24；
  - B、C 任一单独不足，联合资源为 32；
  - 增加联合资源也不足的负向变体。
- 预期：
  - `{B,C}` 在 24 的情形保留；
  - singleton B、singleton C 在 24 的情形不满足；
  - 联合资源不足时整个 domain 被拒绝；
  - 重叠 root 的真实 Node 只聚合一次。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run '^TestFilterJobCandidateDomainsByMinResource$' \
    -count=50
  ```

- 通过标准：资源和诊断断言全部通过。

### U5：dry-run、recorder 与 Bug 008 回归

- 目的：确认 domain key 不污染 placement，且既有 soft Job topology 行为不回退。
- 范围：allocate focused tests。
- 检查：
  - 失败 domain 的 Statement 和 placement 在下一次尝试前全部恢复；
  - 成功 solution 只恢复自己的 operations/worksheet；
  - recorder 中 Job/SubJob decision 使用实际 HyperNode；
  - Job 只实际使用 B 时保存 B，实际跨 B、C 时保存 H；
  - Bug 008 的 local-preferred、remote-fallback 用例继续通过。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate \
    -run '^(TestPodGroupAntiAffinityDomainDryRunIsolation|TestAllocateJobSoftAffinityAcrossSubJobs|TestPreferJobSoftTopology.*)$' \
    -count=50
  ```

- 通过标准：50 次通过且无合成 domain key 出现在 placement/recorder。

### R1：focused package 与 topology plugin 回归

- 目的：确认 singleton 路径、gradient 过滤和现有 topology 语义没有变化。
- 命令：

  ```bash
  cd volcano
  go test ./pkg/scheduler/actions/allocate -count=1
  go test ./pkg/scheduler/plugins/group-topology-affinity -count=1
  go test ./pkg/scheduler/plugins/network-topology-aware -count=1
  ```

- 通过标准：全部通过。

### K1：7 节点 Kind 联合域验证

- 目的：走真实 CRD、scheduler、PodGroup gang 和 Pod bind 路径验证 B+C。
- 集群限制：1 control-plane + 6 workers，共 7 个可见节点，不超过工作区规定的
  8 节点上限。
- 拓扑：
  - 六个 tier-1 HyperNode，每个对应一个 worker；
  - A、B、C 三个 tier-2，每个包含两个 tier-1；
  - 一个 tier-3 H 包含 A、B、C。
- 负载：
  1. 先创建 anchor PodGroup，再创建 anchor Pod，将其固定到 A；
  2. 再创建目标 PodGroup；
  3. 最后创建四个目标 Pod/Deployment；
  4. 目标 Pod 使用 required hostname podAntiAffinity 保证一台 Node 最多一个目标
     Pod，以稳定模拟每个 8 卡节点只能容纳一个 6 卡 Pod；U1 已覆盖真实 scalar
     resource 数值。
- 修复前预期：只能使用 B 或 C 的两个 worker，目标 PodGroup 不 ready。
- 修复后预期：
  - 四个目标 Pod 全部 `PodScheduled`；
  - 分别落在 B、C 的四个 worker；
  - A 无目标 Pod；
  - PodGroup 为 `Running`；
  - scheduler 日志显示一个包含 B、C 的 candidate domain，以及四个 SubJob
    placement。
- 计划命令：

  ```bash
  cd volcano
  TAG=dev-009 BUILDX_OUTPUT_TYPE=docker make images
  kind create cluster --name volcano-dev-009 \
    --config ../issues/bugs/009-podgroup-antiaffinity-multi-domain-allocation-DONE/kind-config.yaml
  kind load docker-image volcanosh/vc-scheduler:dev-009 --name volcano-dev-009
  kind load docker-image volcanosh/vc-controller-manager:dev-009 --name volcano-dev-009
  kind load docker-image volcanosh/vc-webhook-manager:dev-009 --name volcano-dev-009
  helm upgrade --install volcano installer/helm/chart/volcano \
    --namespace volcano-system \
    --create-namespace \
    --set basic.image_tag_version=dev-009 \
    --set basic.image_pull_policy=IfNotPresent
  kubectl apply -f ../issues/bugs/009-podgroup-antiaffinity-multi-domain-allocation-DONE/hypernodes.yaml
  kubectl apply -f ../issues/bugs/009-podgroup-antiaffinity-multi-domain-allocation-DONE/anchor-podgroup.yaml
  kubectl apply -f ../issues/bugs/009-podgroup-antiaffinity-multi-domain-allocation-DONE/anchor-workload.yaml
  kubectl wait pod -l bug009.volcano.sh/role=anchor \
    --for=condition=PodScheduled --timeout=180s
  kubectl apply -f ../issues/bugs/009-podgroup-antiaffinity-multi-domain-allocation-DONE/target-podgroup.yaml
  kubectl apply -f ../issues/bugs/009-podgroup-antiaffinity-multi-domain-allocation-DONE/target-workload.yaml
  kubectl wait pod -l bug009.volcano.sh/role=target \
    --for=condition=PodScheduled --timeout=180s
  kubectl get podgroup,pod -o wide
  kubectl logs -n volcano-system deployment/volcano-scheduler \
    --since=10m
  ```

- 通过标准：四个目标 Pod 仅使用 B、C 的四个 worker，目标 PodGroup Running，
  日志与实际 placement 一致。
- 清理：

  ```bash
  kind delete cluster --name volcano-dev-009
  ```

## 实施后门禁

实现完成后还需运行：

```bash
cd volcano
./hack/update-gofmt.sh
make unit-test
make verify
```

若环境相关的非本任务失败阻断广泛门禁，必须记录完整命令、失败包和原因；focused
allocate/topology tests、U1-U5、R1 与真实 K1 不得跳过或弱化。

## 实施结果

### 代码范围

实际 Git 提交只包含：

- 修改 `pkg/scheduler/actions/allocate/allocate.go`；
- 新增
  `pkg/scheduler/actions/allocate/podgroup_antiaffinity_domain_test.go`。

没有修改 framework、Recorder、topology 插件、scheduler API、CRD、controller
或 webhook。实现新增 allocate 私有的 exact multi-root candidate domain：

- required PodGroup anti-affinity 的同一 Job gradient layer 可形成 `{B,C}`；
- Job hard network topology 按最高允许 tier 的 ancestor 分区；
- Job minResource 按 domain 覆盖的真实 Node 去重并联合计算；
- SubJob 分别从每个合法 root 获取 gradient，再按 rank 合并；
- singleton domain 保留旧 outer-HyperNode recorder 语义；
- multi-root domain 使用实际 dry-run placement，不将合成 domain key 写入 Recorder；
- 继续复用顺序式 SubJob 分配，没有引入全局回溯或 framework callback 变化。

实现提交：

```text
473c6538b fix(scheduler): allocate jobs across anti-affinity domains
```

### U1-U5：50 次聚焦稳定性验证

执行：

```bash
go test ./pkg/scheduler/actions/allocate \
  -run '^(TestAllocatePodGroupAntiAffinityAcrossCandidateDomains|TestPodGroupAntiAffinityCandidateDomainDoesNotReintroduceExcludedBranch|TestPodGroupAntiAffinityDomainDryRunIsolation|TestBuildPodGroupAntiAffinityCandidateDomainsWithHardJobTopology|TestFilterJobCandidateDomainsByMinResource|TestJobCandidateDomainDecisionHyperNode|TestAllocateJobSoftAffinityAcrossSubJobs|TestAllocateJobSoftAffinityAcrossSeparateOuterCandidates|TestPreferJobSoftTopology.*)$' \
  -count=50
```

结果：

```text
ok volcano.sh/volcano/pkg/scheduler/actions/allocate 119.301s
```

覆盖结果：

- `{B,C}` 联合域完成四个 6 卡 Pod 的四次 Bind；
- A 设置为最高分后仍未进入候选或 placement；
- Job hard tier-2 将 B、C 拆为 singleton，两个失败 dry-run 没有泄漏 task、
  Job、SubJob placement 或 Recorder decision；
- Job hard tier-3 允许 `{B,C}`；
- B、C 各 16 卡、Job minResource 24 卡时联合域保留，singleton 被过滤；
- 重叠 root 的真实 Node 不重复计数；
- recorder Job key 为真实 `shared-t3`，没有合成 `B,C` key；
- Bug 008 的 local-preferred 与 remote-fallback 用例保持通过。

### R1：allocate 与 topology plugin 回归

执行并通过：

```text
go test ./pkg/scheduler/actions/allocate -count=1
ok volcano.sh/volcano/pkg/scheduler/actions/allocate 40.186s

go test ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware -count=1
ok volcano.sh/volcano/pkg/scheduler/plugins/group-topology-affinity 1.269s
ok volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware 3.340s

go vet ./pkg/scheduler/actions/allocate
PASS
```

另外，`make unit-test` 中所有 scheduler 包均通过，包括：

```text
ok volcano.sh/volcano/pkg/scheduler/actions/allocate 47.115s
ok volcano.sh/volcano/pkg/scheduler/api 5.801s
ok volcano.sh/volcano/pkg/scheduler/cache 8.558s
ok volcano.sh/volcano/pkg/scheduler/framework 6.834s
ok volcano.sh/volcano/pkg/scheduler/plugins/group-topology-affinity 7.010s
ok volcano.sh/volcano/pkg/scheduler/plugins/network-topology-aware 9.459s
```

### 镜像构建

执行：

```bash
TAG=dev-009 BUILDX_OUTPUT_TYPE=docker make image_bins images
```

本地二进制全部构建完成。包含本任务代码的 scheduler 镜像和 controller 镜像构建
完成：

```text
volcanosh/vc-scheduler:dev-009
sha256:6282d20c197dfddb4824ac0be4e1ba76f44b2e5c92c28f61379e4fc6ac59197f
arm64

volcanosh/vc-controller-manager:dev-009
sha256:d4241b353416872aa0fa98378c99817e3f067d838d3e2e9b2a2233163e76633d
arm64
```

全量 `images` 在未修改的 webhook 镜像构建阶段需要从
`dl.k8s.io` 重新下载 53.25 MiB kubectl，当时下载速度约 80 KiB/s。scheduler
和 controller 已成功后终止该重复下载，复用与本分支基线相同且未修改的 008
webhook 镜像并标记为 `dev-009`：

```text
volcanosh/vc-webhook-manager:dev-009
sha256:d34b141fe48981f64f65d503f52734ddc9d0316a025e53b97f55070d0631d13b
arm64
```

Kind 实际加载并运行了上述三个镜像。

### K1：7 节点 Kind 验证

集群：

```text
name: volcano-dev-009
nodes: 1 control-plane + 6 workers
architecture: arm64
visible Kubernetes nodes: 7
```

HyperNode 状态：

```text
6 x tier-1 volcano.sh/hypernode, nodeCount=1
3 x tier-2 volcano.sh/hyperinstance-group, nodeCount=2
1 x tier-3 volcano.sh/hypercluster, nodeCount=3
```

anchor 先由 Volcano 调度到 A：

```text
pod: bug009-anchor
node: volcano-dev-009-worker
volcano.sh/job-allocated-hypernode: bug009-a-t1-0
```

本地 chart 生成的 PodGroup CRD 将 `podGroupSelector` 生成为 atomic 空对象，
strict decoding 不接受其内部的 `matchLabels`。这属于既有 CRD schema 生成问题，
不在 009 的 allocate 修改范围。K1 使用 `podGroupSelector: {}` 匹配 anchor；
scheduler 仍走真实 group-topology-affinity required 过滤，日志确认匹配的 PodGroup
是 `default/bug009-anchor`，占用域是 `bug009-a-t2`。PodGroup label 精确匹配和
scalar 6/8 卡资源由 U1/U2 覆盖。

目标结果：

```text
bug009-prefill-0  Running  volcano-dev-009-worker3  # B
bug009-decode-0   Running  volcano-dev-009-worker4  # B
bug009-decode-1   Running  volcano-dev-009-worker5  # C
bug009-prefill-1  Running  volcano-dev-009-worker6  # C

PodGroup phase: Running
running: 4
volcano.sh/job-allocated-hypernode: bug009-shared-t3
```

A 的两个 worker 没有目标 Pod。scheduler 关键日志：

```text
podGroup anti-affinity: reject hyperNode ... hyperNode=bug009-a-t2
podGroup anti-affinity: gradient result ...
  tier-2:[bug009-b-t2,bug009-c-t2]
Try to allocate resource for job in HyperNode candidate domain ...
  domain=bug009-b-t2,bug009-c-t2
Allocate job to HyperNode candidate domain success ...
  allocatedHyperNode=bug009-shared-t3
  decode-0:bug009-b-t1-1
  prefill-0:bug009-b-t1-0
  decode-1:bug009-c-t1-0
  prefill-1:bug009-c-t1-1
```

验证完成后执行：

```bash
kind delete cluster --name volcano-dev-009
```

结果：7 个 `volcano-dev-009` 节点均已删除。

### 广泛门禁的既有环境/基线问题

`make unit-test` 整体在 Darwin 主机返回失败。失败集中在 Linux-only 组件：

- agent 的 cpuqos、memoryqos、networkqos handler 被 build constraints 排除；
- `opencontainers/runc` 依赖的 Linux syscall 在 Darwin 不存在；
- `pkg/networkqos/tc` 的平台实现接口不匹配；
- `pkg/networkqos/utils/ebpf` 的 Linux/eBPF mock 断言失败。

这些失败不涉及 scheduler；同一次命令中的全部 scheduler 包均通过，结果已在 R1
列出。

`make verify` 返回失败，仅报告以下 008 基线文件已有的 gofmt 差异：

```text
pkg/scheduler/actions/allocate/dry_run_placement_test.go
pkg/scheduler/api/hyper_node_info_test.go
pkg/scheduler/api/topology_affinity_info_test.go
```

验证：

```bash
git diff --exit-code fix/008-hypernode-job-soft-affinity-across-subjobs -- \
  pkg/scheduler/actions/allocate/dry_run_placement_test.go \
  pkg/scheduler/api/hyper_node_info_test.go \
  pkg/scheduler/api/topology_affinity_info_test.go
```

结果为 exit 0，证明这三处与 009 分支基线完全一致。为遵守修改范围，没有把无关
格式化改动带入 009 提交。009 修改文件自身已由 `gofmt` 处理，`git diff --check`
和 focused `go vet` 均通过。

## 完成标准

只有以下条件全部满足后，任务目录才能从 `IP` 改为 `DONE`：

1. 用户明确批准本 proposal 和 U1-U5、R1、K1；
2. 实现范围未超出批准边界，或扩大范围已再次获批；
3. focused 和回归测试通过；
4. 本地镜像构建并完成真实 Kind 验证；
5. `PROPOSAL_COMMIT.md` 记录实际命令、日志、placement、清理结果和 commit hash。

完成结论：以上条件均已满足。广泛门禁的 Darwin/Linux-only 与 008 基线 gofmt
问题已单独记录，不影响 009 的 scheduler focused、关联插件和真实 Kind 验证。
