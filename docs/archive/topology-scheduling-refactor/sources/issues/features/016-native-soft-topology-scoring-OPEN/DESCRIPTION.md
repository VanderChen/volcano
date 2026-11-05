> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Feature 016: Native Soft Topology Scoring

## Local tracking

- Type: Feature / Scheduler framework enhancement
- Status: OPEN
- Created: 2026-08-02
- Upstream repository: `volcano-sh/volcano`
- Upstream base: `master@a4b775ad2c2d456ca85233f35f305e16707126b3`
- Working branch: `feat/016-native-soft-topology-scoring`
- Current phase: Phase 1 proposal and verification cases drafted; awaiting approval
- Related bug: https://github.com/volcano-sh/volcano/issues/5514
- Related fix: https://github.com/volcano-sh/volcano/pull/5539
- Future consumer: https://github.com/volcano-sh/volcano/issues/5347

## Proposed community issue

### Title

`[Enhancement] Make soft network topology honor plugin weights across candidate domains`

### What is the problem you're trying to solve

#5514 identifies a concrete problem when Job-level and SubJob-level soft
network topology are used together: a later SubJob may start without the Job
placement anchor and therefore cannot prefer nodes close to earlier SubJobs.
#5539 proposes propagating the Job anchor inside the same outer HyperNode
dry-run. That is useful and should be preserved, but an anchor passed to Node
scoring still cannot influence candidates that the current allocation scope has
already separated.

The following scenario remains unresolved even if #5539 is merged.

#### Scenario: an elastic Job is allocated in two passes

Consider two independent tier-2 domains:

```text
cluster-root (tier3)
├── supernode-A (tier2)
│   ├── cabinet-A1 (tier1)
│   └── cabinet-A2 (tier1)
└── supernode-B (tier2)
    ├── cabinet-B1 (tier1)
    └── cabinet-B2 (tier1)
```

The workload has Job-level soft network topology and two SubJobs. Each SubJob
also uses soft network topology and contains two Pods. Its Gang readiness allows
the Job to become ready after the first SubJob, while the second SubJob remains
eligible work.

Assume the cluster and scores are arranged as follows:

- the first SubJob is placed in `cabinet-A1` and makes the Job ready;
- `cabinet-A1` is now full;
- the Job is requeued with the second SubJob still pending;
- the second SubJob fits in both `cabinet-A2` and `cabinet-B1`;
- both candidates keep the second SubJob itself inside one tier-1 cabinet;
- `cabinet-A2` is closer to the established Job placement because
  `LCA(A1, A2) = supernode-A`;
- `cabinet-B1` is farther away because `LCA(A1, B1) = cluster-root`, but it has
  a better score from binpack or another enabled Node scoring plugin.

For soft semantics, these are two competing preferences. The selected placement
should depend on configured weights. For example:

```text
NTA weight 10, binpack weight 1  -> cabinet-A2
NTA weight 1,  binpack weight 10 -> cabinet-B1
```

This does not happen in the current allocation flow, including after the anchor
propagation proposed by #5539.

During the second `allocateForJob()` call, `cabinet-A2` and `cabinet-B1` are
evaluated as different outer HyperNode solutions. While evaluating
`cabinet-B1`, the existing Job anchor can be passed to Node scoring, but the
candidate list contains only Nodes from `cabinet-B1`; Nodes from
`cabinet-A2` are not present for comparison. The remote solution therefore
remains feasible regardless of how far it is from the Job anchor.

The outer selection then compares accumulated SubJob HyperNode allocation
scores. It does not attach a Job-level soft-proximity score, based on each
solution's resulting Job LCA, to the same weighted score aggregation used by
the other Node plugins. Consequently, `cabinet-B1` may win because of its
HyperNode binpacking score or a tie even when NTA is configured with a larger
weight. The Job LCA expands from `cabinet-A1` to `cluster-root` although the
closer `cabinet-A2` placement is feasible.

#### The reverse problem occurs before a Job anchor exists

For an initial soft-topology placement, Volcano currently converts `soft` to
`hard`, generates HyperNode gradients from lower tiers to higher tiers, and
returns after the first gradient with a feasible solution. A Node in a later
gradient never participates in score aggregation with a Node in the first
feasible gradient, even if another plugin gives it a much larger weighted
score.

The same soft preference therefore has inconsistent effective weight depending
on the control-flow path:

```text
initial gradient search: soft topology behaves like infinite weight
later outer-solution comparison: Job soft proximity can behave like zero weight
```

This is why propagating an anchor is necessary but not sufficient. The root
problem is that soft topology is partly implemented by candidate enumeration
and early termination instead of being represented as a score over candidates
that actually compete in one weighted decision.

Relevant current code paths at
`master@a4b775ad2c2d456ca85233f35f305e16707126b3`:

- soft topology is converted to hard during Session initialization:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/pkg/scheduler/framework/session.go#L1040-L1124
- `allocateForJob` evaluates outer gradients and returns the first feasible
  gradient's selected solution:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/pkg/scheduler/actions/allocate/allocate.go#L370-L458
- `allocateForSubJob` similarly returns one selected HyperNode solution:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/pkg/scheduler/actions/allocate/allocate.go#L465-L538
- NTA constructs gradients in ascending tier order:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go#L591-L646
- weighted Node scores are aggregated only for the Nodes passed to
  `PrioritizeNodes`:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/pkg/scheduler/util/scheduler_helper.go#L77-L141

### Describe the solution you'd like

Represent soft network topology as a native weighted preference instead of
using hard-gradient control flow to make a soft decision.

The desired behavior is:

1. Preserve the declared Job/SubJob topology mode throughout the scheduler
   Session. Do not convert `soft` policies into `hard` policies.
2. Let only hard topology constraints remove candidates. Soft topology must not
   create a hidden upper tier boundary or finalize a result at the first
   feasible BFS gradient.
3. For each competing hard-feasible Node or placement solution, calculate a
   normalized NTA soft score from the resulting Job/SubJob LCA. Lower resulting
   LCA tiers receive higher scores, while wider-domain candidates remain valid
   fallback choices.
4. Aggregate that score with the existing Node scoring plugins and apply the
   configured NTA weight exactly once. A real placement must be able to change
   when the relative NTA and binpack/other-plugin weights are changed.
5. Apply Job-level soft scoring across explicit SubJobs and across subsequent
   `allocateForJob()` calls, not only between SubJobs processed inside one outer
   HyperNode dry-run.
6. BFS may remain an internal enumeration or performance optimization, but an
   early BFS layer must not prevent candidates required for the weighted
   decision from being compared.
7. Preserve existing hard topology behavior and API compatibility.

Topology state should also distinguish an actual placement from a temporary
scoring hint:

```text
jobPlacement       = actual LCA of all selected Job Pods
subJobPlacement    = actual LCA of only that SubJob's selected Pods
nodeScoringAnchor  = transient anchor used to score the current candidates
```

When a new SubJob has no placement yet, the Job placement may be used as its
`nodeScoringAnchor`, as proposed by #5539. It must not be copied into the
SubJob's actual placement state. Otherwise selecting a Node in a lower cabinet
would still leave the SubJob recorded at the aggregate Job-level domain, losing
the exact placement needed by later scheduling and recovery.

Conceptually, the decision flow should be:

```text
hard topology + predicates
        -> common feasible candidates
        -> NTA soft score + existing plugin scores
        -> configured weight aggregation
        -> selected placement and actual Job/SubJob LCA update
```

### Additional context

Related work:

- #5205 reported an earlier soft NTA placement problem.
- #5206 fixed that problem by routing Job/SubJob soft topology through the hard
  gradient path. This enhancement keeps the placement preference but removes
  the semantic coupling between soft preference and hard candidate control.
- #5514 and #5539 address missing Job-anchor propagation within the current
  dry-run path. This enhancement assumes that behavior is required, while
  extending it to weighted comparison across candidate domains and allocation
  passes.
- #5347 proposes PodGroup/SubGroup group topology affinity. Native hard-filter
  versus soft-score semantics and exact placement state would provide a safer
  framework foundation, but this enhancement does not introduce that API or
  plugin.

Suggested regression scenario:

1. Create the two-supernode topology described above.
2. Let the first SubJob make the Job ready in `cabinet-A1`, leaving the second
   SubJob pending for a later allocation pass.
3. Make both `cabinet-A2` and `cabinet-B1` feasible for the second SubJob.
4. Configure the ordinary scoring plugin to prefer remote `cabinet-B1`.
5. With a larger NTA weight, verify that `cabinet-A2` wins.
6. With a larger ordinary-plugin weight, verify that `cabinet-B1` wins.
7. Remove capacity from `cabinet-A2` and verify that soft topology falls back to
   `cabinet-B1` instead of leaving the workload Pending.
8. Verify that the selected SubJob records its actual cabinet LCA rather than
   inheriting the aggregate Job scoring anchor.

Scope boundaries:

- no `topologyAffinity` API or `group-topology-affinity` plugin;
- no PodGroup/SubGroup occupancy index;
- no retry of an earlier SubJob placement when a later SubJob reaches a dead
  end; bounded joint placement search is a separate enhancement;
- no exhaustive or globally optimal multi-Pod search;
- no preempt/reclaim joint optimization in the first change;
- no CRD change unless design review finds the existing soft fields ambiguous.

### Documentation Updates

- [x] This feature requires design or user documentation changes.
- [x] If documentation changes are required, I will ensure the relevant
  documents are updated and published to the Volcano official website
  (https://volcano.sh) via the
  [volcano-sh/website](https://github.com/volcano-sh/website) repository.
