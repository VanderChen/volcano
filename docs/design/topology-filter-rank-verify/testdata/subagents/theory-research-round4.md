# 第四轮文献核查报告：外部定理、草稿定理的已知性、新颖性与投稿定位

调研日期 2026-11-12。只做文献检索和一次只打印结果的小规模数值核验，没有改动任何文件，也没有创建 issue、PR 或评论。

标注约定：**〔读原文〕** 表示打开了论文或文档正文；**〔读摘要〕** 表示只看到摘要或元数据；**〔二手〕** 表示来自综述、百科或搜索摘要；**〔推断〕** 是我自己的推理。

---

## 1. 摘要

- **可放心引用的外部结论**：
  - 在线一维装箱的下界 >1.54278 和上界 1.57829（A1）。
  - 在线向量装箱的 Ω(d^{1−ε}) 下界和 First-Fit 的 O(d)（A4）。
  - 固定箱数、最大化接收数的 fair/accommodating 模型（A3）。
  - 嵌套（laminar）处理集的 Glass–Kellerer 与 LFJ（A6）。
  - Oktopus 的“最小子树”贪心（A7）。
  - HiveD 的 buddy 分配安全性定理（A8）。
  - Fagin–Lotem–Naor 的 instance optimality 定义（A9）。
- **需要更正的说法**：
  - Worst-Fit “恰为 1/2”：论文只证了渐近上界 1/(2−1/k)，下界仅 >1/2。
  - “fair 确定性算法不优于 6/7”：已被改进为 <0.809（Azar 等 2002）；6/7 针对任意在线算法。
  - “固定箱数强 NP 完全”：只有箱数是输入时成立；常数箱数是弱 NP 难（Partition）。
  - Bar-Noy–Freund–Naor 的模型是线性（inclusive）层级，不是树或 laminar。
- **草稿定理的已知性**：
  - B2 在本质上是民间结论，也是 LFJ（Least Flexible Job）在 unit job 嵌套集上精确性的变体，可当作“命题”而非“新定理”。
  - B1 是对手论证，与 FLN 的“最短证明”思想和 MPro 的“必要探测”同源。
  - B3 的“移到队首不循环”我没找到现成定理。
  - B4 第一半是平凡反例，第二半未见形式化文献。
- **对 B3 的两处重要更正**（我推导并做了数值核验，见 §3）：
  - 势函数要用 (z_H,…,z_1) 字典序，其中 z_ℓ 是“前面没有更宽限制组的 ℓ 类组数”。按限制序列字典序或逆序对计数都不单调。
  - “重试 ≤ 窄组数”只在两个限制类时成立。≥3 类时我模拟到 4 个组重试了 5 次，通用上界是 ∏(n_ℓ+1)−1，是否紧未研究。
- **新颖性一句话**：理论部分对理论会议太初等，可作系统论文里的命题（类似 HiveD 的 Theorem 1）。可能的新意在于把“有解必须调度成功”当作不变量，用精确 oracle 做差分测试并度量生产调度器的完备性缺口。我检索了 K8s、YARN、Mesos 等调度器的测试和验证文献，没找到这类度量，但这是“未检出”，不是“已证明无”。

---

## 2. Part A：外部结果核查

| 编号 | 结论 | 精确表述与适用条件 | 出处 | 引句或核实方式 |
|---|---|---|---|---|
| **A1** | **支持** | ① 任意在线算法对经典一维装箱的**渐近**竞争比下界 **>1.54278**。② 上界 **≤1.57829**（Advanced Harmonic，渐近）。③ 此前下界 248/161≈1.5403726（BBG 2012）。条件：无限量箱，物品逐个到达，不可重排；Best Fit 的 1.7 是**绝对**比 ⌊1.7·OPT⌋，二者口径不同。 | ① Balogh, Békési, Dósa, Epstein, Levin, *A New Lower Bound for Classic Online Bin Packing*, WAOA 2019 (LNCS 11926:18–28, doi:10.1007/978-3-030-39479-0_2)；Algorithmica 83:2047–2062 (2021), doi:10.1007/s00453-021-00818-7；arXiv:1807.05554。② Balogh 等, *A New and Improved Algorithm for Online Bin Packing*, ESA 2018, LIPIcs 112:5, doi:10.4230/LIPIcs.ESA.2018.5。③ Balogh, Békési, Galambos, TCS 440–441:1–13 (2012), doi:10.1016/j.tcs.2012.04.017。 | 〔读原文〕arXiv 摘要：“We improve the lower bound on the asymptotic competitive ratio of any online algorithm for bin packing to above 1.54278.” 另有一篇单作者 arXiv 2610.32367 声称 1.5782147437，**未见同行评审，不建议引用**。1.5403726 来自文献回顾与 Crossref 元数据〔二手〕。 |
| **A2** | **部分支持** | “Any Fit 的渐近比在 [1.7, 2]”：由 First-Fit=1.7 与 Worst-Fit=2 推出（Johnson 1973/1974）。“至多一个箱 ≤1/2 满”：对 Best Fit 有原文引理；对一般 Any Fit 是两行证明〔推断〕：若两箱都 ≤1/2，后开箱的首件物品必能放进先开箱，违反 Any Fit。 | Johnson, Demers, Ullman, Garey, Graham, SIAM J. Comput. 3(4):299–325 (1974), doi:10.1137/0203025；Dósa & Sgall, *Optimal analysis of Best Fit bin packing*, ICALP 2014, https://iuuk.mff.cuni.cz/~sgall/ps/BF.pdf | 〔读原文〕Dósa–Sgall Lemma 2.1(i)：“there is at most one bin with level at most 1/2.” 〔读摘要〕JDUGG 摘要确认 FF/BF ≤ 17/10·L*+2。Worst-Fit=2 与 R_FF ≤ R_A ≤ R_WF 来自 Wikipedia 引 Johnson 1973〔二手〕。**1974 原文打不开**（UCSD 站点 TLS 失败），未逐字核实。 |
| **A3** | **部分支持（需更正）** | 模型：n 个整数容量 k 的箱，物品大小为整数，最大化接收件数；“fair”指物品只在任何箱都放不下时才可拒绝；“accommodating”指最优算法能装下全部物品的序列。**(a)** 任意 fair 算法在 accommodating 序列上比 ≤6/7（k≥7，BLN Thm 3.9），且 >1/2（Thm 3.12）。**(b)** Worst-Fit 在该类序列上 ≤1/(2−1/k)，k→∞ 时趋于 1/2，**不是“恰为 1/2”**（Thm 3.14）。**(c)** First-Fit 的比 ≥5/8（BLN 2001），并被证明渐近紧（Azar 等）；Best-Fit 同样成立。**(d)** **更正**：fair 确定性算法在 accommodating 序列上 **<0.809**，6/7 针对“任意在线算法”。**(e)** 一般（非 accommodating）序列：所有 fair 确定性算法为 Θ(1/k)；First-Fit 与 Worst-Fit 均为 Θ(1/k)。**(f)** 分离：一般序列上 Worst-Fit 更好，accommodating 序列上 First/Best-Fit（5/8）优于 Worst-Fit（→1/2）。**(g)** 一个 unfair 版 First-Fit 可达 2/3，而标准 First-Fit 上界是 5/8。 | Boyar, Larsen, Nielsen, *The Accommodating Function*, SIAM J. Comput. 31(1):233–258 (2001)；Boyar, Favrholdt, Larsen, Nielsen, *The competitive ratio for on-line dual bin packing with restricted input sequences*, Nordic J. Comput. 8(4) (2001)；Azar, Boyar, Epstein, Favrholdt, Larsen, Nielsen, *Fair versus Unrestricted Bin Packing*, Algorithmica 34:181–196 (2002)（SWAT 2000, LNCS 1851）。PDF：https://imada.sdu.dk/u/kslarsen/Papers/Resources/（BLN01j、BFLN01j、ABEFLN02j 的 paper.pdf） | 〔读原文〕Azar 等摘要：“with an unfair variant of First-Fit we can pack 2/3 of the items for sequences in which the optimal can pack all the items. This is in contrast to the standard First-Fit where we show a tight upper bound of 5/8.” 正文：“any fair deterministic on-line algorithm on accommodating sequences is less than 0.809”。BLN：“All of the results proven in this section for First-Fit also hold for Best-Fit”。**局限**：无离开，物品只进单箱，无 gang 结构，单一资源。它是最接近“固定机架集合、作业在线到达、能放则收”的经典模型，但只是松散类比。 |
| **A4** | **支持** | 在线向量装箱（d 维）：任意确定性在线算法比为 **Ω(d^{1−ε})**；First-Fit 为 O(d)，Garey–Graham–Johnson–Yao 1976 给出 d+0.7；对 B=1 紧。信息论下界，另有随机化版本。 | Azar, Cohen, Kamara, Shepherd, *Tight bounds for online vector bin packing*, STOC 2013, pp. 961–970, doi:10.1145/2488608.2488730, https://bshepherd.ca/img/papers/onlineVBP.pdf；Garey, Graham, Johnson, Yao, JCTA 21:257–298 (1976) | 〔读原文〕摘要：“We settle this by describing a Ω(d^{1−ε}) lower bound”；正文：“tight for B = 1 since there is a (d + .7)-competitive algorithm for VBP [11]”。Garey–Graham–Johnson–Yao 1976 本身未打开，只经 Azar 等转述〔二手〕。 |
| **A5** | **部分支持（需更正）** | ① Goemans–Rothvoss：常数种物品大小的装箱多项式可解（大小和个数用二进制编码，理论多项式但指数巨大〔推断〕）。② Chekuri–Khanna：多背包问题有 PTAS，且 m=2 个相同箱时不存在 FPTAS（除非 Partition 多项式）。③ 3-PARTITION 强 NP 完全。**更正**：“固定箱数”只有在箱数是输入时才强 NP 完全；箱数为常数（如 2）是 Partition，弱 NP 难、伪多项式。 | ① Goemans & Rothvoss, SODA 2014 (doi:10.1137/1.9781611973402.61)；J. ACM 67(6):38 (2020), doi:10.1145/3421750；arXiv:1307.5108。② Chekuri & Khanna, SIAM J. Comput. 35(3):713–728 (2005), doi:10.1137/S0097539700382820。③ Garey & Johnson, *“Strong” NP-completeness results*, JACM 25(3):499–508 (1978), doi:10.1145/322077.322090；《Computers and Intractability》1979 | 〔读摘要〕G&R：“for constant d, solves bin packing in polynomial time. This was an open problem for all d ≥ 3.” 〔读原文〕C&K：“Proposition 1: If MKP with two identical bins has an FPTAS then the Partition problem can be solved in polynomial time.” G&J 1978 只核对了 Crossref 元数据与二手摘要，未读正文〔二手〕。1975 年 Garey–Johnson SICOMP 4(4) 的正文不含 3-PARTITION，**不要引它**。 |
| **A6** | **部分支持（模型需更正）** | **(i) BFN 的层级是线性的**：服务器全序（inclusive），不是树或 laminar。目标是在线负载均衡（最小化最大负载），不是容量装箱。**(ii)** 嵌套（nested = laminar）处理集：Glass & Kellerer 2007 对 P\|Mj(nested)\|Cmax 给出比 2−1/m 的强多项式算法。**(iii)** LFJ（按处理集基数非降排序，放到最空闲的合格机器）对 **P\|Mj(nested), pj=1\|Cmax 最优**（Pinedo 2002），在线含释放时间时 1-竞争（Lee 等 2008）。**(iv)** nested 情形强 NP 难，故无 FPTAS；Muratore–Schwarz–Woeginger 2010 给出 PTAS。**(v)** “laminar 族的 Hall 条件充分”：**未找到可独立引用的原文**（见 B2）。 | Bar-Noy, Freund, Naor, ESA 1999 (LNCS, pp.77–88, doi:10.1007/3-540-48481-7_8)；SIAM J. Comput. 31(2):527–549 (2001), doi:10.1137/S0097539798346135。Leung & Li, *Scheduling with processing set restrictions: a survey*, Int. J. Prod. Econ. 116(2):251–262 (2008), doi:10.1016/j.ijpe.2008.09.003，开放版 https://ira.lib.polyu.edu.hk/bitstream/10397/1849/1/60%20Final-Version.pdf。Glass & Kellerer, Naval Res. Logist. 54(3):250–257, doi:10.1002/nav.20202。Muratore, Schwarz, Woeginger, ORL 38(1):47–50 (2010), doi:10.1016/j.orl.2009.09.010 | 〔读原文〕BFN 摘要：“We consider the linear hierarchy in which the servers are totally ordered in terms of their capabilities.” 〔读原文〕Leung–Li 综述：nested 定义为“either disjoint, Mj ⊆ Mk, or Mk ⊆ Mj”；Theorem 2（Glass–Kellerer）、Theorem 5（LFJ）、Theorem 9（Lee 等）均出自该综述〔二手转述原论文〕。BFN 的 e+1≈3.718 同为综述转述〔二手〕。Muratore 等只读到摘要。Pinedo 2002、Huo–Leung 2010 未读。 |
| **A7** | **支持** | Oktopus 的 Virtual Cluster 分配是**贪心**：先算各子树（机器、机架、pod）可容纳的 VM 数（受空槽数与上行链路剩余带宽约束），再自底向上找“能容纳全部 N 个 VM 的最小子树”；同层有多个子树时选上行链路剩余带宽最少者。它**没有声称最优**，只说在带宽受限边的图上分配是 NP 难（引 [12]）。后续：Rost–Fuerst–Schmid 证明 VC 嵌入问题**并非 NP 难**，并给出多项式最优算法 VC-ACE（目标是资源足迹最小）。 | Ballani, Costa, Karagiannis, Rowstron, *Towards Predictable Datacenter Networks*, SIGCOMM 2011, https://conferences.sigcomm.org/sigcomm/2011/papers/sigcomm/p242.pdf (§4 / Figure 5)。Rost, Fuerst, Schmid, *Beyond the Stars*, ACM CCR 45(3), Jul 2015, https://schmiste.github.io/ccr15emb.pdf | 〔读原文〕Oktopus：“We design a greedy allocation algorithm… the algorithm finds the smallest sub-tree that can fit all tenant VMs.” 又：“the algorithm greedily tries to allocate the tenant VMs to the lowest level possible.” 〔读原文〕Rost 等：“the virtual cluster embedding problem is not NP-hard, and present the fast and optimal embedding algorithm VC-ACE”；树拓扑上运行时间 O(n·N)，“on par with the best known heuristic approaches [3, 16]”。[16] 是 Xie 等（TIVC，SIGCOMM 2012），其标题在提取文本中被截断。**TIVC、Kraken、Fuerst 等后续工作未逐篇核实**。注意 Rost 等的最优性针对**成本最小化**，不是“可行即接收”的完备性。 |
| **A8** | **部分支持（未找到竞争比结果）** | HiveD 对 buddy cell 分配证明了**共享安全性**，不是碎片率或竞争比界。Theorem 1：在“层级均匀可组合性”条件下，若初始 VC 分配可行，则任何合法的 cell 分配请求都能满足；算法复杂度 O(k̂)，k̂ 为层数。其 buddy 分配借自 Knowlton 1965 的内存 buddy 分配器。经典相关结果：Chen & Shin 1987 证明静态分配下 buddy 与 Gray-code 策略都是“每个请求序列总分配到最小子立方体”，即最小子结构优先。 | Zhao 等, *HiveD*, OSDI 2020, https://files.usenix.org/s3fs-public/cdn/osdi20-zhao_hanyu.pdf (§3.3)。Knowlton, *A fast storage allocator*, CACM 1965（HiveD 参考文献 [56]）。Chen & Shin, IEEE Trans. Comput. C-36(12):1396–1407 (1987), doi:10.1109/TC.1987.5009493。Dutt & Hayes, IEEE Trans. Comput. 40(3):341–352 (1991), doi:10.1109/12.76413。Luby, Naor, Orda, SODA 1994 / SIAM J. Discrete Math. 9(1):155–166 (1996)，是 First-Fit 的动态存储分配，不是 buddy。 | 〔读原文〕HiveD：“Theorem 1. Buddy cell allocation algorithm satisfies any legal cell allocation, under the condition of hierarchical uniform composability, if the original VC assignment is feasible.”；“The buddy cell allocation algorithm has the time complexity of O(k̂)”。〔读摘要〕Chen–Shin：“both of these strategies are proved to be optimal in the sense that each incoming request sequence is always assigned to a minimal subcube”；Dutt–Hayes 只看到元数据和摘要首句。**对 buddy 分配或在线子立方体分配的竞争比分析，检索未找到，标 未核实（可能不存在）**。 |
| **A9** | **支持** | **instance optimality**：设 A 为算法类，D 为输入（数据库）类，cost(A,D) 为代价。算法 B 对 A 与 D **instance optimal**，当且仅当 B∈A，且对所有 A∈A、D∈D 有 cost(B,D)=O(cost(A,D))，即存在常数 c、c′ 使 cost(B,D) ≤ c·cost(A,D)+c′；c 称“optimality ratio”。要点：(1) 要求 B 属于被比较的类；(2) 与竞争分析形式相同，但竞争分析不要求 B∈A；(3) 确定性 B 实际上是与最优非确定算法比较，其代价相当于“最短证明”。 | Fagin, Lotem, Naor, *Optimal aggregation algorithms for middleware*, PODS 2001, pp.102–113, doi:10.1145/375551.375567；J. Comput. Syst. Sci. 66(4):614–656 (2003), doi:10.1016/S0022-0000(03)00026-6；arXiv:cs/0204046 | 〔读原文〕arXiv 全文 §1、§5：“B is instance optimal over A and D if B ∈ A and if for every A ∈ A and every D ∈ D we have cost(B, D) = O(cost(A, D))”；“instance optimality is quite strong: the cost of an instance optimal algorithm is essentially the cost of the shortest proof”。**用法提示**：B1 里“恰好调用同样次数”是 c=1、c′=0 的精确版本，比 FLN 的常数因子版更强。需要明确 A（只通过 oracle 取信息且总返回最高分可行候选的确定性算法）、D（候选及分数、oracle 响应的全体）和 cost（oracle 调用次数）。 |
| **A10** | **未找到收敛定理（部分）** | 未找到“失败元素移到队首再贪心重建”的**非循环或收敛**定理。相关结论：(1) SWO 论文自己承认不能保证不陷入小循环，因此在实现中加入随机化和周期性重启。(2) 图着色的 Iterated Greedy：若新排列使上一轮同色类相邻，则颜色数**不增**（“trivial to prove”）。这是“重排再重建”单调性的先例，但机制不同于 move-to-front。 | Joslin & Clements, *“Squeaky Wheel” Optimization*, JAIR 10:353–373 (1999), https://www.cs.cmu.edu/afs/cs/project/jair/pub/volume10/joslin99a-html/swo.html。Culberson, *Iterated Greedy Graph Coloring and the Difficulty Landscape* (1992)；OSTI 记录 35928（CONF-9408161）。RCPSP 优先规则和 Graham 异常方面的检索无直接命中。 | 〔读原文〕SWO：“This does not guarantee that SWO will not become trapped in a small cycle, however. In our implementations we have introduced small amounts of randomness in the basic cycle.” 〔读摘要〕Culberson：“It is trivial to prove that this ensures that the new coloring will use no more colors than the previous coloring.” |

---

## 3. Part B：草稿定理是否已知

| 编号 | 是否已知 | 最接近的出处 | 差别 |
|---|---|---|---|
| **B1** 排名后顺序验证是实例最优 | **部分已知 / 民间结论** | ① FLN 2003 的“最短证明”视角〔读原文〕。② Chang & Hwang, *Minimal probing: Supporting expensive predicates for top-k queries*, SIGMOD 2002 (pp.346–357, doi:10.1145/564691.564731)：MPro 只做“必要探测”，摘要片段称“provably optimal with minimal probe cost”（来自搜索摘要的乱码 OCR，**非全文，未核实**）。③ Marian, Bruno, Gravano, ACM TODS 29(2), 2004 §4.3.2 用 FLN 定义分析 Upper（〔读原文〕）。 | 你的设定是 top-1 加布尔可行性 oracle，且分数事先已知，对手论证（未测的更高分候选可能可行，赢家本身必须验证）几行即可。精确版（c=1）比 FLN 常数因子版强，但形式更平凡。建议写成“命题 + 对手论证”，并引 FLN 的定义与 MPro 作为概念先例。 |
| **B2** 同尺寸子组在树拓扑上的精确性 | **本质已知（民间）** | LFJ：对 P\|Mj(nested), pj=1\|Cmax 最优（Pinedo 2002；Leung–Li 综述 Theorem 5〔二手转述〕）。laminar 拟阵：Fife & Oxley, *Generalized laminar matroids*, Eur. J. Combin. 79:111–122 (2019)。未找到把“每层 Hall 条件充分”写成命题的原文。 | 你的模型是“同尺寸 s、容量 ⌊K(D)/s⌋ 的域槽 + 层级上限”，等价于 unit job 的 LFJ，但此处目标是容量可行性而非 makespan。**我的核验**：必要性是计数；充分性靠归纳，需 ⌊K(D)/s⌋ ≥ Σ⌊K(C)/s⌋（取整超可加）；窄组选哪个域不影响更宽层的总剩余。在 4000 个随机小树（Z≤2，每区≤2 机架，每架≤2 节点，容量≤5，s≤3，组≤4）上，Hall 条件与穷举精确可行性**零不符**（其中 1975 个可行）。“与所选域无关”只在**按限制升序**处理时成立，任意顺序不成立（正是 B3 的动机）。 |
| **B3** 移到队首收敛 | **未找到现成定理** | 仅有 SWO（无保证）与 Culberson（不同机制），见 A10。 | **证明骨架（我的推导）**：Hall 条件成立时，“被阻塞的组前面必有一个限制严格更宽的组”（计数：若前面全是限制 ≤h 的组，则它们占满了所有 tier-h 域的 ⌊K/s⌋ 之和 ≥ n_{≤h}，矛盾）。令 z_ℓ 为“前面没有严格更宽限制组”的 ℓ 类组数。把阻塞组 g（类 h）移到队首：z_h 至少 +1，z_{ℓ>h} 不变，z_{ℓ<h} 可能下降。故 (z_H,…,z_1) 在**字典序（最宽类优先比较）**下严格增大，且有界，所以必终止；终止时无组被阻塞，即成功。重试上界 ∏(n_ℓ+1)−1；两类（窄、宽）时 ≤ 窄组数。**更正**：(1) 限制序列字典序和逆序对计数都**不是**单调势。(2) “重试 ≤ 组数”对 ≥3 类**不成立**：随机对手（域与槽随机选）下，我找到限制为 [0,0,3,1]、s=3 的实例，4 个组重试了 5 次。(3) 在 9875 次模拟中（1975 个可行实例各 5 次），全部成功，无超过 ∏ 界。(4) 上界是否紧，我没研究。 |
| **B4** 排除约束的类型化 | **第一半平凡；第二半未见形式化** | Medea（EuroSys 2018）：约束为 C={subject_tag,{c_tag,cmin,cmax},node_group}，对**每个容器**要求其所在节点组的标签基数落在 [cmin,cmax]；DNF 组合；ILP 求解。 | Medea 自述**无法表达**“对节点集数量施加基数”（如“把所有 spark 容器分散到 3 个机架”），也没有“整组必须落在同一个域内”的原生约束。你的“container（整体放进一个域）与 region（可用节点集）”的类型区分，我**没有找到成文的形式化**；在 ILP/CP 里两者只是并列约束，联合求解天然完备，不完备只出现在“先求域名集合再相交”的分解里〔推断〕。“按域名取交会漏掉 B∪C”的反例是平凡的，可作动机例子。 |

---

## 4. Part C：新颖性与定位

### C1 相近工作（每条一行）

| 工作 | 做了什么 | 与本设计的差别 |
|---|---|---|
| **HiveD**（OSDI 2020）〔读原文〕 | 多级 cell 层级，buddy 分配，证明“共享安全性”（Theorem 1）；真实集群实验。 | 无角色子组、无排除约束、无黑盒谓词、无重试；定理针对配额安全而非调度完备性。 |
| **NVIDIA KAI Scheduler** 文档〔读文档〕 | `kai-scheduler/KAI-Scheduler:docs/topology/README.md:45-52`：域选择“prioritizes domains with **less available resources**”（bin-packing），先过滤放不下整组的域。`docs/topology/multilevel.md`：SubGroup 级 `topologyConstraint`（required / preferred 层级）。 | 与“过滤→排名”结构相近，且已有 SubGroup。文档中未见跨副本排除、完备性论证或失败重试。 |
| **NVIDIA Grove**（技术博客，2025-11-10）〔读摘要〕 | 分层 gang（PodClique / ScalingGroup / Set）、拓扑感知、与 KAI 集成。 | 需求与你的场景几乎一致：“placing related components near each other while spreading replicas for availability”。是产品文档，无完备性分析。 |
| **LeaderWorkerSet exclusive-topology**（LWS 文档）〔读文档〕 | 用 pod affinity/anti-affinity 实现“副本与拓扑域 1:1”，另有 `subgroup-exclusive-topology`；Kueue TAS 可把多个组装进同一域。 | 这是你场景的原生机制，逐 pod 的亲和性语义，不是整组可行性搜索。 |
| **Kueue TAS KEP-2724**〔读原文〕 | `kubernetes-sigs/kueue:keps/2724-topology-aware-scheduling/README.md:1724-1728`：BestFit / LeastFreeCapacity / BalancedPlacement。 | `…/README.md:1591-1594` 明确承认：“Placement is greedy and may miss feasible solutions for `required` topology requests.”（出现在节点替换的语境）。是社区承认贪心不完备的直接证据。 |
| **Slurm topology/block**（SLUG'24 幻灯片，非同行评审）〔读〕 | 块（连续、不重叠、块大小为 2 的幂）与分段；NVL72 场景下的整机架放置。 | 幻灯片称现有拓扑插件“does not implement a hard requirement or guarantees regarding node selection”；是 buddy 式产品先例，无完备性论证。 |
| **Oktopus / Rost 等**〔读原文〕 | “最小子树优先”贪心；后续证明 VC 嵌入可多项式最优求解。 | 目标是带宽成本，不是“可行即调度”；无角色子组、无排除约束。 |
| **Medea**（EuroSys 2018）〔读原文〕 | 带形式语义的放置约束（标签基数 + 节点组）、ILP 与启发式、YARN 400 节点实验。 | 约束是逐容器的；无法原生表达“整组落在一个域”。 |
| **Arnold**（NeurIPS 2025，arXiv:2509.15940）〔读〕 | 字节跳动：让 LLM 预训练通信组与多层网络拓扑对齐；MIP 求解（“bin-packing-like formulation”），基线含 Best-Fit、Random-Fit、GPU-Pack、Topo-Aware。 | 针对训练任务的通信组对齐；用 MIP，无排除约束、无完备性/差分测试。 |
| **OPSche**（arXiv:2608.06987, 2026-08）〔读摘要〕 | K8s 调度框架插件，让外部（CP）求解器驱动全局放置，触发模式含“调度失败时”。 | 把精确求解器当**调度器**使用，不是当测试 oracle；非拓扑/gang。 |
| **Gandiva**（OSDI 2018）〔读摘要〕 | 利用作业内可预测性做时间片、迁移以改善 job-to-GPU 的适配与局部性。 | 面向 DL 集群利用率，非拓扑完备性。 |
| **Themis、Sia、Koordinator、Flux/Fluence、YARN placement、Borg/Omega/Twine、DistServe/Splitwise** | **本轮未逐篇核实**，不下结论。 | 单作者预印本 arXiv:2607.28633 称 DistServe、Splitwise、Mooncake 使用统一 RDMA、忽略拓扑差异；这是该预印本的说法，我没读 DistServe/Splitwise 原文。 |

### C2 方法论类比：把调度正确性当不变量

- **Lozi 等，*The Linux Scheduler: a Decade of Wasted Cores*，EuroSys 2016（doi:10.1145/2901318.2901326）〔读原文〕**：核心不变量“make sure that ready threads are scheduled on available cores”，发现 4 个违反它的 bug，造成 13–24% 的退化；同一摘要还写道：“Conventional testing techniques and debugging tools are ineffective at confirming or understanding this kind of bugs”，并构建了在线检查工具。这与“有合法放置就必须调度成功”几乎同构，是最直接的框架先例。
- **Ipanema，EuroSys 2020（doi:10.1145/3342195.3387544）〔读摘要〕**：指出 Linux CFS 与 FreeBSD ULE 不保证 work conservation，用 DSL 加定理证明器给出可证明性质（concurrent work conservation）。对应“完备性可被证明”的路线。
- **Sieve（OSDI 2022）〔读摘要〕**：比较“扰动前后”的集群状态演化，发现 46 个安全与活性 bug。**Acto（SOSP 2023）〔读摘要〕**：用 oracle 检查系统状态是否符合期望，发现 56 个 bug。**Anvil（OSDI 2024，最佳论文）〔读摘要〕**：用“eventually stable reconciliation”规约对控制器做形式化验证。三者针对**控制器**，不触及放置决策是否完备。
- **缺口**：我对“K8s/YARN/Mesos 调度器测试”“调度 bug 实证研究”“差分/精确求解器对照”做了多轮检索，**没有找到系统度量生产 gang/拓扑调度器相对精确 oracle 的完备性缺口的论文**。这是“检索未命中”，不是“已证明无”，声称“首个”之前建议再查 arXiv cs.DC 与 SoCC/EuroSys/ATC 2024–2026 的论文目录。

### C3 投稿定位

**我的判断（基于以上材料的推断，不是文献事实）**

1. **理论部分单独投理论会议太初等。** B1 是对手论证，B2 是 LFJ/Hall 的变体，B3 是一个小势函数论证。引用的竞争比、不可近似与复杂性结果全是已知的。要在理论场馆被接受，需要新的非平凡结果，例如“带层级上限的分层 gang 放置”的在线竞争比或近似比，或有界重试算法的复杂性刻画。我检索“hierarchical bin packing”只看到 IPL 2015 一篇相关标题（摘要未读，是否同一问题未核实），所以这个组合问题或许确有空白，但那需要另做研究。
2. **较现实的框架是“不变量 + 度量 + 修复”，类似 Lozi 等的论文：**
   - 不变量：存在合法放置则作业必须被调度。
   - 方法：以精确 oracle 做差分测试，发现“可调度却未调度”。
   - 度量：对 Volcano、Kueue TAS、KAI、kube-scheduler+LWS 等量化缺口的频率与成本。
   - 修复：过滤→排名→验证 + 有界重试，并附一个小命题（先例：HiveD 的 Theorem 1）。
3. **场馆层级（从高到低，均为我的建议）**：
   - 系统类全文（EuroSys / ATC / SoCC）：需要下述全部证据。
   - 软件工程测试类（ISSTA / ICSE 等）：把“精确 oracle 差分测试 + 缺陷数”作为主贡献，类比 Sieve/Acto 的形态。
   - 工业或经验类、HPC 作业调度研讨会（如 JSSPP；本轮未核实其征稿范围）、Cluster / ICPP / SC 研讨会：证据门槛较低，更现实。

**可比的已发表论文（3–6 篇）及其被接受的原因**

| 论文 | 被接受的要点（均来自上面读过的摘要或正文） |
|---|---|
| Lozi 等，EuroSys 2016 | 简单而重要的不变量被打破，4 个具体 bug，13–24% 退化，附在线检查工具。 |
| Ipanema，EuroSys 2020 | 形式化证明加与生产调度器相当的性能。 |
| HiveD，OSDI 2020 | 新抽象 + 一个定理 + 真实集群实验；摘要称排队延迟最多可降 1000 分钟。 |
| Medea，EuroSys 2018 | 带形式语义的约束 + ILP 与启发式 + 400 节点集群评估。 |
| Arnold，NeurIPS 2025 | 生产部署（>9600 GPU），端到端提升 10.6%，仿真中通信组跨度最多降 1.67×。 |
| Oktopus，SIGCOMM 2011 | 新抽象加分配算法，摘要称租户成本中位数最多降 74%。 |

**当前工作缺少、评审会要求的证据**

- 生产轨迹或真实事故频率。
- 集群规模评估：数千节点、调度周期延迟、排名开销。
- 基线对比：Kueue TAS、KAI、HiveD、LWS 原生 exclusive-topology、ILP/CP-SAT 精确求解。
- 完备性缺口的量化：多少比例的实例基线失败而存在可行解，并控制 oracle 与调度器的建模差异。
- 消融：排名 vs 不排名、重试次数 N 的影响。
- 对利用率与碎片的影响，不只是可行性。
- 跨调度器的通用性，不只在 Volcano 内。
- 模型有效性：A4 说明向量资源下一维保证会退化；真实谓词也不是同质 unit slot。

---

## 5. 检索范围与未能核实

**检索范围**：本地第三轮报告；Crossref API 与 arXiv API；USENIX、ACM、SIGCOMM 会议页；作者主页的开放 PDF（SDU/Imada、CMU/JAIR、Columbia、Cornell/Upenn 等）；GitHub 文档（Kueue KEP-2724、KAI Scheduler docs、LWS docs）；若干 `web_search`。以 `curl | pdftotext` 流式读取，没有写文件。

**未能核实或仅读到摘要或二手的事项**

- Johnson 1974 / JDUGG 1974（SICOMP）原文：UCSD 站点 TLS 失败，“Worst-Fit=2”和“每个 Any Fit 算法 ≥1.7”未见原文逐字（A2）。
- Garey & Johnson 1978 正文、Garey–Graham–Johnson–Yao 1976（JCTA）正文、Goemans–Rothvoss 正文、Muratore–Schwarz–Woeginger 全文（A5）。
- Bar-Noy–Freund–Naor 原文（ResearchGate 403，只读到摘要片段）及其 e+1≈3.718 的数值（二手转述）；Pinedo 2002、Huo–Leung 2010 未读（A6）。
- TIVC、Kraken、Fuerst 等后续工作（A7）。
- 对 buddy 分配或在线子立方体分配的竞争比分析：检索未命中（A8）。
- Chang & Hwang 2002 全文：只见到乱码摘要片段，“MPro 最优”未核实（B1）。
- A10：RCPSP 优先规则与 Graham 异常方面无直接命中。
- C1：Themis、Sia、Koordinator、Flux/Fluence、Borg/Omega/Twine、DistServe/Splitwise 原文均未读；JSSPP 的征稿范围未核实。
- C2：“生产调度器完备性缺口”只做了有限检索，结论是“未检出”。
- B2/B3 的数值核验：4000 个随机小树（Hall 条件对穷举 0 不符）和 9875 次重试模拟。这只是在**同质单位槽、域与槽随机选**的模型内的核验，不是证明，也不覆盖真实谓词和多维资源。