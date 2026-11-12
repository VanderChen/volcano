# 社区与产品调研（第三轮）：局部最优 / 全局最优 / 调度效率的文献依据

- 角色：社区与产品调研员（独立 subagent，agent id `08eec13a-6598-48d7-9eae-afb29d1ae0e2`，只读）。
- 范围：协调者给出 21 条拟写入设计文档的论断，要求逐条对照一手来源核实、给出原文引句，并主动寻找反面证据。
- 下文为该 subagent 的原始报告。表中"我的表述"指协调者给出的论断。

---

# 第三轮学术文献核查：21 条论断对照一手来源

## 0. 摘要

- 21 条里，绝大多数的一手来源能打开并核对原文。
- 以下几处只能给出部分核实或 [NV]：
  - 原始论文没打开：Garey & Johnson 1979、Woeginger 1997、Johnson et al. 1974、Azar et al.、Dósa 2007 的 PDF。其中 A1、A2、B5 已用开放教材或后续论文佐证，B4 的 1974 年原文仍为 [NV]。
  - 需要更正的论断：A3、B4、B5、B6、B7、C8、D12、E17、E18、E19。
- 不利于贪心的数字：TetriSched 的 SLO 差距最高 36%，Medea 的启发式约束违反率为 10–40%（ILP 低于 10%），Quincy 的数据传输最多相差 3.9 倍。
- 目前没找到任何论文度量过「贪心逐个候选 + 拓扑层级优先」相对最优解的差距。
- F20 没有找到专门论证「确定性 tie key」的一手学术来源。

## 1. 核对表

标 [NV] 的是一手来源没能打开。正文引句均来自 PDF 全文，除非另注。`pdftotext` 抽取丢了数学符号处，我按上下文还原并在条目里注明。

| 编号 | 我的表述（缩写） | 结论 | 出处 | 原文引句 |
|---|---|---|---|---|
| A1 | 装箱是 NP-hard | **支持**（用教材佐证；Garey & Johnson 1979 本书 [NV]） | Williamson & Shmoys, *The Design of Approximation Algorithms*, CUP 2011, §3.3 p.73，开放 PDF：https://www.designofapproxalgs.com/book.pdf。Dósa 2007（见 B5）引言也写「The problem is NP-hard」，该句来自搜索引擎对 Springer PDF 的文本片段 | "The partition problem is well known to be NP-complete… Theorem 3.8: Unless P = NP, there cannot exist a ρ-approximation algorithm for the bin-packing problem for any ρ < 3/2." |
| A2 | 向量装箱 d≥2 无渐近 PTAS（Woeginger 1997） | **部分核实**。原文 [NV]：IPL 64(6):293–297，DOI 10.1016/S0020-0190(97)00179-8，ScienceDirect 无法取全文。陈述本身被两篇一手论文转述支持。需带「unless P=NP」。「d≥2」由 d=2 推出（高维包含二维） | Bansal, Caprara, Sviridenko, SIAM J. Comput. 39(4):1256–1278, 2009，https://pure.tue.nl/ws/portalfiles/portal/3321242/Metis244840.pdf（引言）。Tetris 论文脚注也引用 Woeginger | "Woeginger [25] ruled out an APTAS even for d = 2." 同文给出可行近似："asymptotic approximation guarantee arbitrarily close to ln d + 1." |
| A3 | 多资源调度 = 多维装箱，APX-hard；Tetris 用什么启发式、收益多少 | **部分支持，更正为：** APX-hard 是 Tetris 作者引用 Woeginger [27]，不是自证。脚注把 APX-hard 解释为「无渐近多项式近似（除非 P=NP）」，措辞偏松。启发式是对齐分数（点积） | Grandl et al., "Multi-Resource Packing for Cluster Schedulers", SIGCOMM 2014，https://www.microsoft.com/en-us/research/wp-content/uploads/2016/02/tetris_sigcomm14.pdf（§2–§4，我未记页码） | "Even though multi-dimensional bin packing is known to be APX-Hard [27]" / "The alignment score is a weighted dot product between the vector of machine's available resources and the task's peak usage of resources." / "improves makespan and job completion by 30% in deployment and up to 40% in simulations over Facebook traces. These gains are 80% of the estimated gains from a simple upper-bound" |
| B4 | First Fit / Best Fit ≤ 1.7·OPT（Johnson 1974；Dósa & Sgall 2013/14） | **支持（用 Dósa & Sgall），Johnson 1974 原文 [NV]（SIAM J. Comput. 3:256–278，付费墙）。更正：** 绝对比是 ⌊1.7·OPT⌋；渐近 1.7 早在 70 年代已证；仅限一维、任意到达顺序 | Dósa & Sgall, "First Fit bin packing: A tight analysis", STACS 2013, LIPIcs 20:538–549, DOI 10.4230/LIPIcs.STACS.2013.538，https://drops.dagstuhl.de/storage/00lipics/lipics-vol020-stacs2013/LIPIcs.STACS.2013.538/LIPIcs.STACS.2013.538.pdf。Best Fit：Dósa & Sgall, "Optimal analysis of Best Fit bin packing", ICALP 2014, LNCS 8572, DOI 10.1007/978-3-662-43948-7_36，https://iuuk.mff.cuni.cz/~sgall/ps/BF.pdf（摘要） | FF：「We prove that also the absolute approximation ratio for FirstFit bin packing is exactly 1.7.」 BF：「if the optimum needs Opt bins, BestFit always uses at most ⌊1.7·OPT⌋ bins.」 |
| B5 | FFD ≤ 11/9·OPT + 6/9（Dósa 2007，tight）；排序把保证从 1.7 提到约 1.22 | **部分支持，更正为：** ① 数值对（渐近 11/9≈1.222，加性常数恰为 2/3）。② Dósa 2007 的 PDF 打不开（Springer 返回空），摘要只见于搜索引擎文本片段，结论转而用 Dósa & Sgall 的一手转述。③ 排序的对象是物品（任务），不是箱子（域）。④ 仅一维。⑤ 绝对比最坏为 4/3（OPT=6 时 8 箱） | Dósa, ESCAPE 2007, LNCS 4614:1–11, DOI 10.1007/978-3-540-74450-4_1。转述见 Dósa & Sgall STACS 2013（同 B4 链接）引言 | 「the additive constant is exactly 2/3 [3]. That is, 11/9 Opt + 2/3 bins are sufficient for First Fit Decreasing, but this number of bins is actually also necessary…」（PDF 抽取把 2/3 排版打乱，据上下文还原）。Dósa 摘要片段："FFD(I) ≤ 11/9 OPT(I) + 6/9, and that this bound is tight." |
| B6 | Borg §3.2：worst fit vs best fit vs 混合，stranded resources；给出测得差异 | **部分支持，更正为：** 实测数字只有「混合模型比 best fit 好 3–5%」。E-PVM（worst fit）一侧**没有给数字**，只有定性描述 | Verma et al., "Large-scale cluster management at Google with Borg", EuroSys 2015, §3.2，https://research.google.com/pubs/archive/43438.pdf | "E-PVM ends up spreading load across all the machines… but at the expense of increased fragmentation, especially for large tasks that need most of the machine; we sometimes call this 'worst fit'." / "It provides about 3–5% better packing efficiency (defined in [78]) than best fit for our workloads." 另：best fit "leaves some machines empty… the tight packing penalizes any mis-estimations" |
| B7a | FGD：packing 类打分减少碎片、多调度作业 | **部分支持，更正为：** 论文证明的是**专为碎片设计的打分**优于 best-fit 等经典 packing，并明确说经典启发式对 GPU 共享不好用。早期集群空旷时 FGD 表现一般，可回退 best-fit | Weng et al., "Beware of Fragmentation…", USENIX ATC 2023，https://www.usenix.org/system/files/atc23-weng.pdf（摘要、§6；6,200 GPU 生产 trace 上做仿真） | "none of the existing packing heuristics, including best-fit…, vector alignment scoring… work well in scheduling GPU sharing workload." / "FGD reduces unallocated GPUs by up to 49%, resulting in the utilization of additional 290 GPUs." |
| B7b | HiveD：从能放下的最低层 cell 分配，必要时才拆高层 | **支持**（保证的是共享安全性，不是全局最优；Theorem 1） | Zhao et al., "HiveD", OSDI 2020，https://files.usenix.org/s3fs-public/cdn/osdi20-zhao_hanyu.pdf（buddy cell 分配小节；评估 §6） | "it first checks whether a free level-k cell is available and allocates one if available. If not, the algorithm will move up level-by-level… The algorithm aims to keep as many higher-level cells available as possible." / "reduces GPU fragmentation and creates opportunities to schedule jobs that require higher-level cells." |
| C8 | kube-scheduler：Filtering→Scoring，最高分胜，平局随机；`percentageOfNodesToScore` 用质量换延迟；默认「50%→5%」 | **部分支持，更正为：** 默认是自适应：100 节点 50%，5000 节点 10%，下限 5%，且至少找 100 个可行节点。不是简单的「50%→5%」。平局随机是文档原话 | https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/（`website` 该文件最近提交 `33dcba8aa6dd…`）。https://kubernetes.io/docs/concepts/scheduling-eviction/scheduler-perf-tuning/（最近提交 `1d3acfcc97786…`）。代码：k/k v1.37.2 `pkg/scheduler/schedule_one.go` L50-62、L866-892（`minFeasibleNodesToFind=100`，`minFeasibleNodesPercentageToFind=5`，`50 - numAllNodes/125`） | "If there is more than one node with equal scores, kube-scheduler selects one of these at random." / "a linear formula that yields 50% for a 100-node cluster and yields 10% for a 5000-node cluster. The lower bound for the automatic value is 5%." / "This would result in a less than ideal placement of the Pod." |
| C9 | Borg：可行性→打分；三项扩展技术；从零调度整个 cell 的耗时 | **支持**（§3.2、§3.4） | 同 B6 的 Borg PDF | "The scheduling algorithm has two parts: feasibility checking… and scoring, which picks one of the feasible machines." / "the scheduler examines machines in a random order until it has found 'enough' feasible machines to score" / "typically took a few hundred seconds, but did not finish after more than 3 days when the above techniques were disabled." |
| C10a | Sparrow：探测少数候选即可接近探测全部 | **部分支持，更正为：** 结论只对**排队长度的负载均衡**成立，不是装箱或拓扑。论文明确说不做装箱、不支持 gang。"12%" 是对理想调度器的差距，且批量采样仍比全知调度器差 1.92 倍 | Ousterhout et al., "Sparrow", SOSP 2013，https://cs.stanford.edu/~matei/papers/2013/sosp_sparrow.pdf（摘要、§3、§6） | "Sparrow performs within 12% of an ideal scheduler." / "place each task on the least loaded of two randomly selected worker machines… improves expected wait time exponentially compared to using random placement" / "does not perform bin packing, and does not support gang scheduling." |
| C10b | 两选择的幂 | **支持** Mitzenmacher；Azar et al. 的 *Balanced Allocations* 原文 [NV] | Mitzenmacher, "The Power of Two Choices in Randomized Load Balancing", IEEE TPDS 12(10), 2001，https://www.eecs.harvard.edu/~michaelm/postscripts/tpds2001.pdf（**仅核对摘要**） | "Having d ≥ 2 choices leads to exponential improvements in the expected time a customer spends in the system over d = 1, whereas having d ≥ 3 choices is only a constant factor better than d = 2." |
| C11 | KEP-5732：生成 placement→检查可行→打分；明确避免复杂回溯 | **支持**（链接已用固定 commit 复核；v1.37.2 与 master 的评估行为不同，见 round-2） | `kubernetes/enhancements` commit `72907deaa85e…`，`keps/sig-scheduling/5732-topology-aware-workload-scheduling/README.md` L212-213、L376-385、L399-403、L929 | "We support only sequential processing of pods within a PodGroup, avoiding complex backtracking or parallel processing." / "Call `ScorePlacement` for all feasible placements. Select the Placement with the highest score." / L929："Pre-filtering of Placements will be implemented to discard clearly infeasible Placements (insufficient total resources)" |
| D12a | Quincy：min-cost flow 在流模型内全局最优 | **支持**。它与贪心对比的结果有利于全局优化（见 G21）。Quincy 称 gang 调度不适用于其场景 | Isard et al., "Quincy", SOSP 2009，https://www.sigops.org/s/conferences/sosp/2009/papers/isard-sosp09.pdf | "a standard solver computes the optimal online schedule according to a global cost model" / "the global solutions found by the min-cost flow algorithm substantially outperform greedy scheduling approaches." / "standard techniques such as gang scheduling [10] are not applicable to our setting." |
| D12b | Firmament：对不适合流模型的约束（co-location、anti-affinity、gang）说了什么；求解延迟与提速技术 | **部分支持，更正为：** Firmament 论文本身没有讨论反亲和、co-location 或 gang（grep 无命中）。「加约束需要指数个顶点」是 **Medea 作者对 Firmament 的评价**，不是 Firmament 原话 | Gog et al., "Firmament", OSDI 2016，https://files.usenix.org/s3fs-public/cdn/osdi16-gog.pdf。Medea 对 Firmament 的评价见 Medea 论文相关工作 | Firmament："it guarantees overall optimal task placements for a given scheduling policy." / Quincy 复现："a median of 64s and a 99th percentile of 83s for the full Google cluster (12,500 machines)" / "Both cost scaling and relaxation misplace thousands of tasks when terminated early". Medea 对 Firmament 的评价："Adding more constraints would require (an exponential number of) additional vertices in the scheduling graph, increasing scheduling latency." |
| D13 | Medea：ILP 长期应用放置、两调度器、延迟对比、ILP 比贪心好多少 | **支持**。适用范围：长运行应用，10 秒调度间隔，每轮 2 个 LRA，不是延迟敏感的 gang 路径 | Garefalakis et al., "Medea", EuroSys 2018，https://www.microsoft.com/en-us/research/wp-content/uploads/2018/01/medea-eurosys2018.pdf（§5.3、§7.4、§7.5） | "This two-scheduler design ensures that the scheduling latency for task-based applications is not impacted, while enabling high-quality placements for LRAs." / "Medea-ILP… leads to almost no constraint violations, even for 90% utilization… our heuristic algorithms result in 10%–20% violations, even for low utilization" / "even with 5000 machines, the average latency is 850 ms" |
| D14 | TetriSched：MILP，有界求解时间 | **支持**。注意 TetriSched-NG 是逐作业 MILP，不是 filter→score 启发式 | Tumanov et al., "TetriSched: global rescheduling with adaptive plan-ahead…", EuroSys 2016, DOI 10.1145/2901318.2901355，https://istc-cc.cmu.edu/publications/papers/2016/tetrisched-tumanov-eurosys16.pdf（§3.2.2、§7.2、§7.3） | "The solver runtime can be bounded, with minor loss of optimality, but we find that it is not necessary even for our 256-node cluster experiments." / "the solver is configured to return 'good enough' solutions within 10% of the optimal after a certain parametrizable period of time" |
| D15 | 实际中贪心与最优的差距（含不利数字） | **只有间接数字，没有直接度量「贪心 vs 最优」的论文。** 数字见下方汇总 | 各条出处见下表 | 见下 |
| E16 | Haralick & Elliott：fail-first 原则 | **支持**（从作者主页的全文 PDF 核对；ScienceDirect 原站打不开）。注意：原则讨论的是回溯树搜索中一致性检查次数的平均开销，不是方案质量 | Haralick & Elliott, "Increasing tree search efficiency for constraint satisfaction problems", Artificial Intelligence 14(3):263–313, 1980, DOI 10.1016/0004-3702(80)90051-X，https://haralick.org/journals/haralick_elliot.pdf（§5, pp.301–302） | "For the unit order choice, the fail first principle states that the next unit to choose should be that one with the fewest possible labels left." / "To succeed, try first where you are most likely to fail." |
| E17 | LDS：好启发式下的失败常常只差少数几个错误决策 | **部分支持，更正为：** 这是作者的**直觉**，不是定理。完整性只在差异上限扩到整树深度（指数级）时成立。截断后的 LDS 没有完整性保证 | Harvey & Ginsberg, "Limited Discrepancy Search", IJCAI 1995，https://ijcai.org/proceedings/1995/0080.pdf（摘要、算法分析节） | "Our intuition is that a failing heuristic might well have succeeded if it were not for a small number of 'wrong turns' along the way." / "Thus the search is guaranteed to find a goal node if one exists and is guaranteed to terminate if there are no goal nodes." |
| E18 | SWO：先贪心构造，找 trouble makers，提前，重复；无最优/完备性保证 | **部分支持，更正为：** 前半支持。后半**论文没有声明**最优或完备性保证，我只找到「不保证不陷入小循环」，不能写成「论文声称无保证」 | Joslin & Clements, "'Squeaky Wheel' Optimization", JAIR 10:353–373, 1999，https://www.jair.org/index.php/jair/article/view/10231（页面抓取失败，全文读自 CMU 镜像 https://www.cs.cmu.edu/afs/cs/project/jair/pub/volume10/joslin99a-html/swo.html） | "The priorities of the trouble makers are then increased, causing the greedy constructor to deal with them sooner on the next iteration." / "This does not guarantee that SWO will not become trapped in a small cycle, however." |
| E19 | 失败后才升级的先例（kube PostFilter、Borg 抢占） | **部分支持，更正为：** ① kube-scheduler PostFilter 严格是失败触发。② **Borg 不是严格失败触发**：可行性检查把可抢占的低优先级资源也算作可用，选定机器后才发现不足再抢占。③ 最强的先例是 **Philly（Microsoft）**：locality 约束在重试固定次数后放宽 | https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/（`website` 最近提交 `e2c0fe17d670…`）。https://kubernetes.io/docs/concepts/scheduling-eviction/pod-priority-preemption/（`64098274d088…`）。Jeon et al., "Analysis of Large-Scale Multi-Tenant GPU Clusters…", USENIX ATC 2019，https://www.usenix.org/system/files/atc19-jeon.pdf（§2、§3.1）。Borg §3.2 | PostFilter："called after the Filter phase, but only when no feasible nodes were found for the pod." / 抢占："Preemption should happen only when a cluster does not have enough resources for a Pod." / Philly："To avoid starvation, the locality constraints are relaxed after a scheduling request has been retried a fixed number of times." |
| F20 | 调度器随机打破平局的理由 vs 确定性的调试价值 | **未找到一手来源直接支持。** 见下方说明 | 见下方 | 见下方 |
| G21 | 反面证据 | 见 G21 小节 | | |

### D15：贪心与最优差距的数字汇总

| 来源 | 数字 | 说明 |
|---|---|---|
| Tetris（SIGCOMM 2014） | 在 Facebook trace 仿真中取得了「简单上界」所估收益的约 80% | 上界本身不是真上界（计算不可行） |
| TetriSched（EuroSys 2016，§7.2） | "Global scheduling accounts for the gap of up to 36% (at 50% over-estimate) between TetriSched and TetriSched-NG" | SLO 达成率，最不利于贪心 |
| Medea（EuroSys 2018，§7.4） | 启发式约束违反 10%–20%，另一设置为 15%–40%；ILP 低于 10% | 长运行应用 |
| Quincy（SOSP 2009） | 跨集群数据传输最多降到 1/3.9，吞吐最多提高 40% | 与贪心排队调度对比 |
| Borg（EuroSys 2015） | 混合打分比 best fit 好 3–5% | 同属启发式之间的差距，不是对最优 |
| Sparrow（SOSP 2013） | 距理想调度器 12%（批量采样 + late binding 后在 5% 内） | 仅限负载均衡 |
| 最坏情况（B4、B5） | FF/BF ≤ ⌊1.7·OPT⌋，FFD ≤ 11/9·OPT + 6/9 | 理论上界，不是实测平均 |

### F20 的说明

- **kube-scheduler**：k/k PR #78009（2019）把 `selectHost` 从轮转改为 reservoir sampling，理由是 "more suitable for selecting one candidate with same possibility from a set, and the code will be cleaner"。动机是「等概率」和「代码更清晰」，没有提到羊群效应。
  - master `dd21ea95` 里 `Randomizer` 字段注释为 "used to provide randomness when randomizing nodes within a common score."
  - 在 `schedule_one.go` L687 里，节点级 `Randomizer = rand.Int()` 只在 extender 分支赋值。
  - placement 级在 `algorithm_podgroup.go` L886 赋值。
- **Sparrow**：只有「并发调度器同时探测同一台空闲机器」的竞态（§3，late binding 动机处），说明多个调度器基于过期状态选同一台机器会冲突。
- **Omega**（EuroSys 2013，https://research.google.com/pubs/archive/41684.pdf）：只在仿真参数表里列了 "randomized first fit"，没有给出理由。
- **Borg**：随机顺序是为了 scalability，不是打破平局。
- **确定性的价值**：没有学术来源；证据只来自仓库内 round-1/2 研究。Kueue 注释写明域 ID 平局是为了 "deterministic messages"；KAI 的 `sortTree` 以 ID 兜底。

### G21：反面证据汇总

1. **Quincy**：全局最优明显胜过贪心；"the global solutions found by the min-cost flow algorithm substantially outperform greedy scheduling approaches." 数字为 3.9 倍和 40%（见 D15）。
2. **TetriSched**：最高 36% 的 SLO 差距；"TetriSched's global scheduling policy is particularly important for bin-packing heterogeneous jobs, as conflicting constraints can be simultaneously evaluated." 同时 "even TetriSched-NG outperforms Rayon/CS… showing that greedy policies using TetriSched's other features are viable options if global scheduling latency rises too high."
3. **Medea**：启发式（含逐个请求处理的 J-Kube）违反约束比 ILP 多得多；"considering only one request at a time makes it difficult to satisfy inter-application constraints."
4. **Firmament 提速的代价**：提前终止全局求解器会错放大量任务（"misplace thousands of tasks when terminated early"）。截断求解器和截断重试不是一回事。
5. **FGD**：经典 best-fit 不够，需要专用碎片打分。
6. **HiveD**：Gandiva 的贪心迁移会为了某租户性能牺牲其他租户的 GPU affinity，出现 "sharing anomaly"；也承认最小化全局碎片可能因增大干扰而降低作业性能。
7. **Tiresias**（NSDI 2019，https://www.usenix.org/system/files/nsdi19-gu.pdf）：反对「窄层级优先」当作绝对规则。"A DDL job will often wait when it cannot be consolidated, even if there are enough spare resources elsewhere in the cluster. Although this constraint was originally set for good performance, it often leads to longer queuing delays and resource under-utilization in practice." 只有部分模型对放置敏感，相对随机放置最多提升 1.67 倍。
8. **Philly**：locality 与排队需要权衡，不宜一刀切。
   - 论文指出碎片很常见："when two thirds of the total GPUs are being used, the fraction of servers that are completely empty is less than 4.5%"。
   - 5–8 GPU 作业里 74.2% 的排队是碎片造成的。
   - 缺少 locality 的代价：8 GPU 作业利用率 56.9%，16 GPU 作业 34.3%。
   - Philly 自己的 rack 排序是「按已分配量**升序**」，即最空的优先，与 bin-packing 的 best-fit 方向相反。

## 2. 对本设计的启示

1. **「先排序、按序取第一个验证通过者」有生产先例，但没有被证明接近最优。**
   - kube-scheduler、Borg、KEP-5732 都是先过滤再打分，Borg 和 kube 还靠采样以质量换延迟（C8、C9、C11）。
   - Philly 的生产调度器在 gang 放置前先对 rack、server 排序（E19）。
   - 对应的最坏情况理论保证只对一维装箱成立（B4）。
2. **一维 best-fit 与「按装满程度排序后取首个能放的」等价（我的推断，未见论文明说），可借用 ⌊1.7·OPT⌋。** 但 FFD 的 11/9 靠的是对物品（作业）排序，而你们排的是候选域。对多维资源，B5 的 1.22 不能照搬，论文写「排序把保证提到 1.22」不成立。
3. **排序键里「打分」一项：packing 类打分有证据，但需带限定。**
   - Borg：减少 stranded resources 的混合打分比 best fit 好 3–5%，纯 best fit 对资源估计误差敏感。
   - FGD：专用碎片打分显著优于通用 best-fit，空旷集群早期收益小。
   - 结论：说「以 packing 为主的现有插件分数」可以；不要说「best-fit 已被证明最优」。
4. **「窄层级优先」与 HiveD 的 buddy 分配（从能放下的最低层 cell 取）同构，这是最直接的学术支持。** 反面证据是 Tiresias 与 Philly：locality 不是对所有作业都值得等；以及碎片会让窄层级很快用尽。所以排序键只管「先试谁」，不要在文档里承诺「窄层级一定更优」。
5. **平局键用 `hash(scope UID, 候选名)`：没有一手学术来源直接支持，文档应说明这是工程取舍。**
   - 主流生产调度器是随机平局（k/k）；Sparrow 说明并发时选同一目标会冲突。
   - 确定性的理由来自仓库内先例（Kueue、KAI）和可解释性需求。
   - 哈希按 (作业, 域) 取值，不同作业会得到不同赢家，可缓解并发选同一域。这是我的设计推断，非文献结论。
6. **「可行优先 + 有界失败路径重试」的学术依据是启发式搜索传统，并非对最优性的保证。**
   - fail-first：Haralick & Elliott 原则；Medea 的 Node-candidates 启发式是系统领域的直接先例（"places the container with the smallest Nc, as that container has the least placement flexibility"）。
   - 重试：LDS 的「少数错误决策」只是直觉，截断后没有完备性（E17）；SWO 是启发式，论文未给最优或完备性保证（E18）。
   - 文档里请写「预算内的有界重试，失败时报告 search incomplete」，不要暗示完备。
7. **失败触发升级有成熟先例，但注意 Borg 不是严格的失败触发。** kube PostFilter 是严格的；Philly 是「重试固定次数后放宽 locality」，与你们的有界重试最接近。
8. **不上全局求解器的理由站得住，但要如实写出代价。**
   - 求解器延迟：Quincy 12,500 机器中位 64 秒；Firmament 通过两个算法并行、增量求解等才降到 200 ms 以内；Medea 的 ILP 在 5000 机器平均 850 ms，但处于 10 秒间隔的非延迟敏感路径。
   - 质量代价：见 D15 与 G21（TetriSched 最高 36%，Medea 10–40% 违反，Quincy 3.9 倍）。
9. **提前终止求解器不是安全的折中。** Firmament 显示提前终止的 MCMF 会错放成千上万任务。你们的「有界重试」是每次重试都完整验证，与截断求解器不同，这一点值得在设计文档里写清楚。
10. **遗留风险：** 提案 §1 自己承认排序不改善失败路径；没有论文度量过「贪心逐个候选 + 窄层级优先」在 gang/拓扑约束下相对最优的差距。建议保留全局求解器作为未来选项，并设计离线对比（同一 trace 下比较首个验证通过与穷举最优）来补这个缺口。

## 3. 检索范围与缺口

**读到 PDF 全文的来源**：Tetris、Borg、FGD、HiveD、Sparrow、Quincy、Firmament、Medea、TetriSched、LDS、Philly、Tiresias、Omega（grep 级别）、Dósa & Sgall（STACS 2013 全文、ICALP 2014 摘要和开头）、Haralick & Elliott（作者主页版全文）、Williamson & Shmoys（教材 §3.3）、Bansal–Caprara–Sviridenko。
**读到源码或文档文本的来源**：kubernetes.io 四篇文档（读的是 `kubernetes/website` 的 markdown 源）、k/k v1.37.2 与 master `dd21ea95` 的相关 Go 文件、`kubernetes/enhancements` 固定 commit 的 KEP-5732、PR #78009。
**只读到摘要或片段的来源**：Mitzenmacher 2001（仅摘要）；Dósa 2007（搜索引擎对 Springer PDF 的文本片段，PDF 本身打不开）。
**未能打开**（均标 [NV]）：
- Garey & Johnson 1979（书）。
- Woeginger 1997（IPL，付费墙）。
- Johnson et al. 1974（SIAM J. Comput.）。
- Azar et al. 的 *Balanced Allocations*。
- JAIR 官方页面（抓取失败，全文改读 CMU 镜像）。

**没做的核对**：
- 没有逐一核对 Tetris、Quincy、Firmament 等论文的章节号或页码，表里有章节的才是我确认过的。
- 没有检索 Gandiva（OSDI 2018）、Themis、Sia（SOSP 2023）的原文数字，G21 没覆盖它们。
- 没找到「FF/FFD 平均情况相对最优」的开放实证文献（Coffman–Garey–Johnson 综述未能打开）。
- Medea 对 Firmament 的评价只核对了 Medea 一侧的措辞。

**注意**：表中所有引句均来自上述 PDF 或源码文本，没有使用二手博客。所有 `pdftotext` 抽取处的数学符号（⌊ ⌋、分数）我按上下文还原，已在相应条目注明。