> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Feature 017: Bounded Joint Placement Search

## Local tracking

- Type: Feature / Scheduler framework enhancement
- Status: OPEN
- Created: 2026-08-02
- Upstream repository: `volcano-sh/volcano`
- Upstream base: `master@a4b775ad2c2d456ca85233f35f305e16707126b3`
- Working branch: not created
- Current phase: community issue drafted; detailed proposal not started
- Related community issue: https://github.com/volcano-sh/volcano/issues/5347
- Foundation: Feature 016 native soft topology scoring

## Proposed community issue

### Title

`[Enhancement] Add bounded joint placement search for multi-SubJob scheduling`

### What is the problem you're trying to solve

The current two-level topology allocation path schedules SubJobs sequentially.
For each SubJob, `allocateForSubJob` evaluates the current HyperNode candidates,
selects one best solution, recovers only that `Statement`, and returns it to
`allocateForJob`. If a later SubJob then has no feasible placement, the whole
Job dry-run is discarded. The scheduler does not retain or retry an earlier
SubJob's second-best placement.

This can produce a scheduling false negative even when all topology policies are
hard and a feasible joint placement exists. For example:

```text
tier1-hn-a -> node-a: 4 CPU allocatable, 3 CPU already used
tier1-hn-b -> node-b: 4 CPU allocatable, idle

SubJob A: one 1-CPU task; can run on node-a or node-b
SubJob B: one 1-CPU task; can run only on node-a
Both SubJobs: hard network topology at tier1, minAvailable=1
Job: minMember=2
```

With HyperNode binpacking, SubJob A prefers `tier1-hn-a` and consumes its last
CPU. SubJob B then has no candidate. The Job attempt is rejected even though the
joint placement below is valid:

```text
SubJob A -> node-b
SubJob B -> node-a
```

The problem is not score calibration. Plugin scores can order the alternatives
for the current SubJob, but the framework keeps only one local winner. Once a
later SubJob reaches a dead end, no earlier alternative remains for the
scheduler to explore. Removing the current soft-to-hard conversion alone does
not solve this all-hard case.

Relevant current code paths at
`master@a4b775ad2c2d456ca85233f35f305e16707126b3`:

- sequential SubJob allocation in `allocateForJob`:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/pkg/scheduler/actions/allocate/allocate.go#L370-L431
- selection and return of one SubJob solution:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/pkg/scheduler/actions/allocate/allocate.go#L465-L538
- the design document's current first-feasible two-level flow:
  https://github.com/volcano-sh/volcano/blob/a4b775ad2c2d456ca85233f35f305e16707126b3/docs/design/Network%20Topology%20Aware%20Scheduling.md

This limitation already affects current multi-SubJob network-topology-aware
scheduling. It also becomes more common when multiple topology relationships
are introduced: #5347 proposes SubGroup affinity and anti-affinity, which can
make a later SubJob's feasible scope depend on earlier placements.

### Describe the solution you'd like

Add a bounded joint placement search that preserves the current greedy result
as its first branch, but retains ordered alternatives and can roll back an
earlier placement when a later SubJob reaches a dead end.

The desired behavior is:

1. Keep the existing `SubJobOrder`, `TaskOrder`, predicates, and weighted Node or
   HyperNode scores as the deterministic ordering of the first branch.
2. Retain the next feasible alternative at each placement decision instead of
   discarding every non-winning solution immediately.
3. Introduce a `Statement` mark/rollback mechanism, or an equivalent
   transaction boundary, so a failed branch reverses allocations, pipelines,
   plugin callbacks, worksheet progress, and Job/SubJob topology anchors before
   trying the next alternative.
4. Re-evaluate `SubJobReady`, `SubJobPipelined`, `JobReady`, and `JobPipelined`
   against each partial placement. Accept a solution only when the existing
   readiness contracts and all hard topology constraints pass.
5. Bound extra attempts/backtracks with a configurable or well-defined internal
   search budget. Budget exhaustion must leave the Session state clean and be
   distinguishable from an internal error.
6. Preserve the current fast path: when every first choice succeeds, no
   backtracking is performed.
7. Keep the search deterministic for deterministic plugin ordering and expose
   enough logs or metrics to diagnose attempts, backtracks, and budget
   exhaustion.

Conceptually:

```text
choose A's best candidate
  -> choose B's best candidate
     -> dead end
  -> rollback A and B to A's choice point
  -> choose A's next candidate
     -> choose B's candidate
        -> JobReady / JobPipelined
```

This enhancement does not require exhaustive or globally optimal scheduling.
It does not introduce the `topologyAffinity` API or the
`group-topology-affinity` plugin. It should first prove the planner and rollback
contract with existing multi-SubJob NTA behavior.

### Additional context

Related work:

- #5347 proposes PodGroup/SubGroup group topology affinity. Bounded placement
  search is a scheduler foundation that the future feature can reuse, but this
  issue is independently reproducible with existing hard NTA policies.
- #5362 fixes missing cleanup when `RecoverOperations` returns an error. That is
  required error-path hygiene, but it does not retain alternatives or roll back
  to a choice point after a normal no-candidate dead end.
- #4899 optimized resource pre-filtering and early termination in the same
  sequential two-level allocation flow. It reduced failed work but did not retry
  an earlier SubJob's alternative placement.
- #5205 and #5206 concern soft NTA behavior. The example above intentionally
  uses only hard topology so this issue remains separate from native soft
  scoring.
- Companion enhancement: preserve native soft topology as a weighted score over
  the common hard-feasible candidate set. Add its issue number here after the
  two issues are created.

Scope boundaries:

- no new CRD/API fields;
- no group-topology-affinity plugin or occupancy index;
- no unbounded enumeration or global optimum guarantee;
- no redesign of preempt/reclaim in the first change;
- no weakening of existing predicates, Gang readiness, or hard topology
  validation;
- the first implementation should remain independently reviewable and
  mergeable before #5347.

Suggested validation includes an all-hard two-SubJob regression for the example
above, rollback/state-isolation unit tests, a search-budget exhaustion test, and
a Kind case proving that the feasible joint placement is found while the
first-branch-success path performs zero backtracks.

### Documentation Updates

- [x] This feature requires design or user documentation changes.
- [x] If documentation changes are required, I will ensure the relevant
  documents are updated and published to the Volcano official website
  (https://volcano.sh) via the
  [volcano-sh/website](https://github.com/volcano-sh/website) repository.
