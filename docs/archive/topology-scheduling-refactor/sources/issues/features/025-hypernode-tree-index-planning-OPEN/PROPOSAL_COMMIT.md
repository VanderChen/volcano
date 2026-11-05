> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 025 Proposal 与验证计划

## 状态

- 2026-10-23：源码审计与设计完成；proposal/验证用例待批准。
- 需求、范围见 [DESCRIPTION.md](DESCRIPTION.md)；完整设计见 [HYPERNODE_TREE_INDEX_DESIGN_CN.md](HYPERNODE_TREE_INDEX_DESIGN_CN.md)。
- 基线：上游 `master@0854d3a4018c9304f8a25ded7a6f136c539f753c`。
- 专用分支：`feat/025-hypernode-tree-index-planning`；worktree：`/Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025`。
- 无实现修改，无新提交。原 `volcano/` 的 `dev-1.14` 不变。
- 下列测试、fixture、benchmark 和配置为实施时应交付的计划，当前尚不存在；命令未执行，全部用例为 NOT RUN。
- 未将旧 015 原型、既有 020/022/024 Kind 结果计为本方案验证。
- 用户补充后的范围修订：SubGroup required/preferred affinity/anti-affinity 纳入 P3 核心设计与验收；此补充是设计范围确认，不是实施代码或验证计划的批准。

## 实施切分

P1 公共 resolver；P2 controller/CR 发布；P3a scheduler index/资源状态；P3b 联合 planner/缓存/预算；P3c 受限算法实验。各阶段独立提交、可单独批准；本记录在实施和必需 Kind 用例完成前保持 OPEN 或 IP，不提前 DONE。

P1/P2 不引入新的 placement 决策语义。P3b 的评分、软 tier 和关系行为改变必须显式启用。本次固定主线已有 PodGroup anti-affinity；未来 master 的 SubGroup 四类关系从一开始纳入 P3 关系状态、缓存和 planner。API 尚未合入时可用内部模型验证；最终须对齐合入 API 并通过 Kind 用例，不能以接口尚不可用将核心场景标为跳过后完成。

## 验证合同

### 单元与属性用例

每组用例的 setup 是纯内存 HyperNode/Node/Pod fixtures，不创建大集群。属性随机生成器固定 seed 并输出失败种子；oracle 使用简单精确集合和小规模穷举，避免测试复刻生产索引。

| ID / 阶段 | 目的与 setup | 预期和通过标准 |
| --- | --- | --- |
| U1 / P1 | 单根、森林、三层树；exact/regex/label；节点增删、相关/无关标签变化 | 独立集合 oracle 与旧路径在有效兼容输入的 membership 一致；相同语义输入顺序变化输出/hash 不变 |
| U2 / P1 | 环、多父、缺引用、tier 冲突、空/错误 selector、跨子树重叠、同名异 UID 重建 | 无 panic/无无限循环；诊断类别稳定；重叠走兼容去重路径；缺失对象不计入可分配资源 |
| U3 / P1/P2 | declared/resolved/detail 三种视图；tier3 两子域四 Node；详情 UID/spec hash 不匹配 | 显示 direct children=2 和 unique Nodes=4；不误解释旧 nodeCount；stale 明示，REST 调用按 List/页而非每节点增长 |
| U4 / P2 | controller 乱序事件、重复 reconcile、status-only 更新、leader 重启、Node membership 标签变化 | 内容未变不发布新 revision；有效变化最终发布；缺片/混合revision不采用；快照原子替换 |
| U5 / P2 | 大树字节阈值；分片写到一半失败、旧片 GC、consumer 重连、未知 formatVersion | 超限分片或显式拒绝，不能发布不完整 activeRevision；不认识版本 fail closed/明确降级；读者不会混版本 |
| U6 / P3a | Node 资源变化、Allocate/Pipeline/Deallocate/Evict、嵌套 Undo、重叠 membership；拓扑切换时同时资源更新 | 每一步动态域摘要与从 NodeInfo 全量重算一致；撤销后逐字段恢复；一个 Node 在同域只计一次；拓扑与动态基线成对切换，无新树旧资源组合 |
| U7 / P3a | sum 足够但单 Node 放不下；每维 max 足够但无节点同时满足；B+C scope | 粗筛不能错误接受实际不可行 placement；不引入 sibling A；scope 聚合资源不重复计数 |
| U8 / P3b | 缓存命中与依赖变化：Task UID/requests、policy、topology、peer、资源、prefix、cohort | 依赖不变可复用；每种相关变化使证据失效/重验；跨轮计划不直接提交；缓存大小在配置限额内 |
| U9 / P3b | s1 可用 A/B、s2 只可用 A；s1 首选 A；子域失败后父域扩展 | 预算足够时 s1→B、s2→A 成功；窄域失败不污染父域；缓存不冻结 s1；异常恢复全部撤销 |
| U10 / P3b | 两域同 hard 等级；NodeAffinity 与 NTA/Binpack 分项可控；单候选、Skip、评分错误 | 分项均进入共同评价且权重只乘一次；在全量小模型下选择共同目标最好布局；错误不当零分 |
| U11 / P3b | 首个 Pod 偏好 A(100/90)，第二个偏好 B(0/100)，要求同域；固定 cohort 合同 | 首解可为 AA；足够质量预算时找到共同目标更高的 BB；质量比较固定工作集，不少放 Pod 提分 |
| U12 / P3b | 主线 PG anti-affinity namespace/selector/tier、peer 跨域、已运行 peer 删除/重建、重叠 selector | required 无碰撞；preferred 保持主线 term 权重；缺 tier 不放宽；占用更新与 rollback 一致；重叠场景兼容模式与旧 GTA 相同 |
| U13 / P3b | 4 域/4 SubGroup required anti-affinity 与 4 域/5 SubGroup | 4/4 成功；5/4 minMember要求全组时零绑定、暂占完全回滚；与小规模穷举相同 |
| U14 / P3b | 唯一可行域放在首批采样外；多根大小不均；预算边界 | 扩样后可找到；截断时返回 budget/truncated；重试能进展；所有 hard 不违反，不能报全局无解 |
| U15 / P3b | nomination、idle/futureIdle、soft sharding、Reserve 中途失败、Undo失败 | 保留既有服务合同；无半恢复方案提交；不能撤销的插件显式限制额外探索，required 仍执行 |
| U16 / P3c | 同构 slots、费用可加 vs 异构资源、同域 gang、资格不同 task 合流 | 适用子问题 flow/matching/DP 与穷举等价；不适用 capability gate 拒绝接管；放宽界不误剪真实好解 |
| U17 / P3b | 两个单域 SubGroup：tier2 required affinity + tier1 required anti-affinity；反向 tier 组合；三个组仅正亲和成环 | 第一种放同pair不同叶；反向组合证明冲突；纯亲和环有共同支持域时可行，不能仅因环拒绝；全量与穷举一致 |
| U18 / P3b | 同term peer跨多个域且每域多个Pod；preferred affinity/anti-affinity 与 NTA/NodeAffinity 组合；peer多于域 | count-aware语义按peer SubGroup/域去重；soft始终保留hard可行候选；完整收益反映所有来源；权重受控变化产生oracle预期取舍 |
| U19 / P3b | 尚未放置peer、later peer引起跨组回退；有向/any/all term模型；peer位置变但本组资源不变；Undo及同名新UID | 未决关系正确保留，按term匹配语义检查；关系变化使缓存失效；Undo恢复全部状态；无旧UID占用；采样漏掉共同支持域时仅报截断 |

拟议测试入口及精确命令（实现时必须提供这些入口；不得以 `no tests to run` 当通过）：

```bash
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025/staging/src/volcano.sh/apis
go test ./pkg/topology/... -run 'Test(ResolveTopology|TopologyDiagnostics|DetailedTopology)' -count=1
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025
go test ./pkg/controllers/hypernode/... -run 'TestTreeIndex' -count=1
go test ./pkg/scheduler/api ./pkg/scheduler/cache ./pkg/scheduler/framework -run 'Test(TopologySnapshot|TopologyResourceView|TopologyTransaction)' -count=1
go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/plugins/network-topology-aware ./pkg/scheduler/plugins/group-topology-affinity -run 'Test(TopologyPlan|TopologyPlanCache|TopologyDomainBudget|TopologyScoreContract|TopologyRelations)' -count=1
go test ./pkg/scheduler/framework ./pkg/scheduler/actions/allocate ./pkg/scheduler/plugins/group-topology-affinity -run 'TestTopologyRelations(SubGroup|MixedTier|PeerOccupancy|PendingPeer|CacheInvalidation)' -count=1
go test ./pkg/scheduler/framework -run 'TestTopologyRestrictedSolver' -count=1
```

### 性能与规模用例

使用 host Go 和既有缓存。只生成内存数据，不创建 1k/8k Kubernetes 或 KWOK 节点。记录机器、Go版本、GOMAXPROCS、插件配置、seed、基线 SHA、目标 SHA。

| ID | setup / 目的 | 通过标准 |
| --- | --- | --- |
| B1 | 1k/8k/32k 物理 Node；深度 3/5/8；宽树、森林、受控重叠 | 记录解析时间/内存、CR 字节、index build 和复用；纯树 N 翻倍时结构内存不超过约 2.5 倍（除固定成本）；重叠按实际 M 单独对照 |
| B2 | 不变拓扑重复 100 Session；单 Node 资源更新；不同 H 固定 depth | 第二轮起静态 index 构建计数为 0；资源更新访问域数等于 a(v)，不随无关域数线性增长；全量 oracle 一致 |
| B3 | 多 Job/SubGroup 父域回退、warm/cold cache、99%变化导致失效 | 缓存有效时 Predicate/Score/事务重复次数下降；高失效时内存受限、无错误复用；记录命中收益与维护成本 |
| B4 | 唯一可行域、无解、宽松负载、强反亲和、质量优化 | 分别报告首解与质量开销；节点/域访问、真实插件调用和恢复计入预算；小规模 full模式对oracle零hard错误 |
| B5 | 同一拓扑固定资源，SubGroup数8/32/128；稀疏/稠密关系图、跨tier、peer churn；warm/cold cache | 独立报告关系边数、传播/失效/重算次数及字节；不对无关组件失效；精确关系校验不因预算被省略；稠密图成本不冒充树深度成本 |

拟议 benchmark 入口：

```bash
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025/staging/src/volcano.sh/apis
go test ./pkg/topology/... -run '^$' -bench 'BenchmarkTopology' -benchmem -count=5
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025
go test ./pkg/scheduler/api ./pkg/scheduler/framework ./pkg/scheduler/actions/allocate -run '^$' -bench 'BenchmarkTopology' -benchmem -count=5
```

P3a 暂定同语义回放 gate：代表性负载 P95 不劣于基线 10%，小集群常见路径中位延迟不劣于 5%；同时必须达到 B2 的访问/构图次数目标。噪声需多次采样并公开结果，不能挑最好一轮。若 gate 不适用，在实现前提供基线证据并审批修订。

P3b 不用单一“更快”掩盖语义变化：预算为零质量模式衡量首解成本；固定额外质量预算比较 feasible rate 与完整 U。性能模式不要求等于全量 oracle 最优，但 required 违规必须为零，截断/预算原因必须真实；增加预算只有在相同候选顺序、保留 incumbent 的可延续搜索中才要求 U 不下降。

### Kind 用例

统一拓扑：任务集群 `volcano-dev-025`，1 control-plane + 3 workers，共 4 个可调度节点；tier1 每 Node 一个叶域，tier2 两两成对，tier3 两个 tier2 子域。按需临时建立森林、重叠或单 Node tier2 域，case 后恢复。无 KWOK；本机验证可见节点总量不得超过 8。先检查已有集群，不自动删除用户集群。

普通工作负载使用固定 `registry.k8s.io/pause:3.10`（主机先 pull/load），CPU 请求用于真实容量行为；GPU/NUMA/DRA 形状在 unit oracle 中验证，若要宣称设备端到端支持需另批准真实设备用例。controller mode 分别验证 `controller-manager` 与 `standalone`，按顺序升级而非同时运行两种 writer。

| ID / 阶段 | setup / 操作 | 预期和通过标准 |
| --- | --- | --- |
| K1 / P1 | SDK 从 Kind List HyperNode/Node 生成树；同时观测 scheduler 解析结果 | 有效拓扑的 membership 完全相同；三层关系保持；两子域/四物理节点计数明确 |
| K2 / P2 | 创建树、修改 selector 标签、删除/同名重建叶域；两个 controller mode 顺序运行 | 两种部署模式均发布完整新 revision；status-only/Pod churn 不产生结构改版；故障恢复后收敛 |
| K3 / P2/P3a | 注入缺片/旧revision/不兼容版本；重启 scheduler/controller | 不采用混合 index；已知缺失不放宽 required；恢复后新 Session 采用完整版本，日志说明原因 |
| K4 / P3a | 在 pair-a 提交和删除 CPU Job，触发真实 Allocate/Pipeline/回滚 | scheduler 域摘要与 NodeInfo/实际占用一致；试放失败后无资源泄漏；静态 revision 不随 Pod churn 改变 |
| K5 / P3b | hard允许 A/B，Pod preferred NodeAffinity 都偏 B；然后首 Pod 偏 A、全组偏 B | 同一 Node/tier测试；日志显示全部来源参与；质量预算足够时选择已定义 U 更高的布局 |
| K6 / P3b | s1可A/B、s2仅A、A只够一组；预热 s1计划后提交组合工作集 | s1替代到B、s2在A；缓存继承可撤销；Job达到约定 minMember，无多余绑定 |
| K7 / P3b | 已运行 peer 占 A；目标 PG required/preferred anti-affinity，随后删除/同名重建 peer | required从不进入禁域；preferred按term权重取舍；删除重建按UID刷新，旧occupancy不泄漏 |
| K8 / P3b | 同tier四域四组、四域五组；required anti-affinity、minMember要求全组 | 4/4全部Running；5/4全组无绑定且后续独立Job不受残留暂占影响 |
| K9 / P3b | 低域预算使唯一可行域首轮未访问；扩大/轮转后重试 | 首轮报截断/预算而非全局无解；最终能访问并使用合法域；不会因稳定失败缓存永久沉睡 |
| K10 / P3b | 使用未来合入的SubGroup API，两个单域组tier2 required affinity+tier1 required anti-affinity；再创建反向组合Job | 第一Job在同pair不同叶达到minMember；反向组合Job零绑定、正确冲突诊断且无暂占泄漏 |
| K11 / P3b | preferred affinity/anti-affinity分别及API允许的组合，与NodeAffinity竞争；peer多于域、peer多Pod/跨域、删除重建 | soft场景仍可达minMember；分项/主体计数与完整oracle相同；新UID不继承旧peer占用；选优与批准的语义版本一致 |
| K12 / P3b | s1可A/B、s2仅A且required anti-affinity，预热s1缓存；另外构造双方未放置的required affinity | 反亲和场景回退到s1→B/s2→A；亲和场景联合放置到共同支持域；缓存复用/失效与Undo有证据，无永久等待先放peer的死锁 |

未来 fixtures 保存于本目录 `verification/`，验证 harness 命名 `run-kind-case.sh`。它必须生成每例独立 namespace，先创建 PodGroup 并等待存在，再创建 Deployment；读取真实调度、对象及日志后给出非零/零退出码，不可只检查 apply 成功。当前仅定义其合同，未创建或运行 harness。

准确的预期运行命令如下；image tag 为该实施轮唯一值，如重复构建必须更换后缀或 FORCE_REBUILD：

```bash
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025
kind get clusters
kubectl config get-contexts
# 逐个已有本地 Kind context 查看 nodes，确认连同新增集群不超过 8 个可见验证节点。
# kind-4node.yaml 在实施时按上文拓扑生成并纳入评审。
docker pull kindest/node:v1.36.1
kind create cluster --name volcano-dev-025 --config ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/kind-4node.yaml
kubectl --context kind-volcano-dev-025 get nodes -o wide
kubectl --context kind-volcano-dev-025 get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.nodeInfo.architecture}{"\n"}{end}'
kubectl --context kind-volcano-dev-025 taint nodes --all node-role.kubernetes.io/control-plane- --overwrite
TAG=dev-025-v1 BUILDX_OUTPUT_TYPE=docker make images
kind load docker-image volcanosh/vc-scheduler:dev-025-v1 volcanosh/vc-controller-manager:dev-025-v1 volcanosh/vc-hypernode-controller-manager:dev-025-v1 volcanosh/vc-webhook-manager:dev-025-v1 volcanosh/vc-agent:dev-025-v1 volcanosh/vc-agent-scheduler:dev-025-v1 --name volcano-dev-025
docker pull registry.k8s.io/pause:3.10
kind load docker-image registry.k8s.io/pause:3.10 --name volcano-dev-025
helm upgrade --install volcano installer/helm/chart/volcano --kube-context kind-volcano-dev-025 --namespace volcano-system --create-namespace --set basic.image_tag_version=dev-025-v1 --set basic.image_pull_policy=IfNotPresent -f ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/helm-values.yaml
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K1
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K2
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K3
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K4
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K5
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K6
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K7
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K8
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K9
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K10
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K11
bash ../issues/features/025-hypernode-tree-index-planning-OPEN/verification/run-kind-case.sh K12
kubectl --context kind-volcano-dev-025 -n volcano-system get pods -o wide
kubectl --context kind-volcano-dev-025 get hypernodes -o yaml
kubectl --context kind-volcano-dev-025 get hypernodetreeindexes -o yaml
kubectl --context kind-volcano-dev-025 get podgroups,pods -A -o wide
kind delete cluster --name volcano-dev-025
```

`helm-values.yaml` 必须显式启用被测 scheduler 插件和新feature gate、配置固定预算、记录 baseline/target 模式；字段名在对应接口实现前复审。K2 harness 在同集群切换已存在的 `custom.hypernode_controller_mode`。CRD 变化需显式 apply 生成的 CRD；Helm upgrade 不应被当作 CRD 必然升级的证明。

每例留存 fixture、命令、开始结束对象 JSON/YAML、scheduler/controller 日志、revision、访问/预算计数、分项分数、pass/fail 和清理结果。未来 CR 与日志字段的精确名在阶段实施前冻结，不把未生成 fixture 的计划称为可执行完成。

### 必须执行的项目门禁

API 类型变化时先在嵌套 API module 生成，再做仓库级生成；纯 SDK 函数抽取不自动要求 CRD 改动。

```bash
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025/staging/src/volcano.sh/apis
./hack/update-codegen.sh
cd /Users/vanderchen/workspace/dev/volcano-workspace/volcano-hypernode-025
make generate-code
make manifests
make generate-yaml
make generate-charts
./hack/update-gofmt.sh
make unit-test
make verify
make verify-generated-yaml
```

上述全部是未来实施验证，不因设计文档完成而标记通过。若环境无法运行 Kind，实施任务保持 IP 并记录阻塞；若修改用例或降低 gate，先说明证据并批准。

## 本轮已执行的调查

- `git status --short`：原源码树干净；原分支 `dev-1.14`。
- GitHub connector 核实上游默认分支和最新提交；`git fetch upstream master` 更新远端引用。
- 初次沙箱 fetch 无法访问本机代理；按已有工具授权流程使用 host 网络完成 fetch。未修改代理或工具配置。
- 创建独立任务分支与 worktree，避免更改生产基线。
- 并行审计最新 scheduler、既有 015 设计及 flow 一手论文/源码；核实新主线已经包含 GTA、typed gradients、index 与 generation。
- 仅创建本 issue 的三个 Markdown 文档；无代码、CRD、集群、镜像或知识库变更。

## 结果与提交

- 设计：完成，可评审。
- 实现：未开始。
- U/B/K 与项目门禁：NOT RUN。
- 最终实现提交 hash：无。
