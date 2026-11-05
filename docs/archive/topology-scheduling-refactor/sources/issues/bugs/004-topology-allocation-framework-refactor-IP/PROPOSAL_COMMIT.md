> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Proposal And Verification Notes

## Current Status

- Issue: Bug 004
- Phase: implementation
- Implementation: started after user approval
- Volcano source branch: `dev/podgroup-antiaffinity`
- Initial source status:

  ```text
  ?? example/topology-affinity/
  ```

The existing untracked topology-affinity examples are left untouched.

## Design Goals

1. Make HyperNode plugin composition independent of specific plugin names and
   API fields.
2. Separate hard feasibility from soft preference.
3. Evaluate complete, comparable Job/SubJob allocation solutions.
4. Make progress according to gang minima without atomically requiring every
   pending topology-related SubJob.
5. Preserve deterministic behavior and bound topology search cost.

## Proposed Design

### 1. Introduce an explicit HyperNode constraint result

Replace the implicit `[][]*HyperNodeInfo` callback contract with an internal
result that distinguishes:

- `Abstain`: the plugin has no hard constraint for this object;
- `Constrain`: the plugin applies and supplies an allowed candidate set;
- `Unschedulable`: the plugin applies but no candidate satisfies its hard
  policy;
- callback error: evaluation failed and must not be mistaken for a neutral
  result.

The concrete type and names may follow existing framework status conventions,
but the semantics above are required.

Framework composition will:

1. build the candidate universe under the caller's search root once;
2. ignore `Abstain` results during hard intersection;
3. intersect only applicable hard candidate sets;
4. preserve per-plugin exclusion diagnostics;
5. return the universe unchanged when every plugin abstains.

`network-topology-aware` will abstain for a Job/SubJob without applicable
hard network topology. It will no longer inspect `job.WithTopologyAffinity()`
to decide whether another plugin needs descendants.

### 2. Move soft semantics out of gradient intersection

Ordered gradients must not carry soft preference because framework
intersection currently cannot compose independent plugin orders.

- Hard `networkTopology` and required topology-affinity terms contribute
  allowed candidate sets.
- Soft `networkTopology` and preferred topology-affinity terms contribute
  candidate/solution scores.
- Topology compactness, preferred maximum LCA tier, group-affinity weights, and
  bin-packing scores are combined through one explicit solution-ranking path.
- A soft preference never removes the final feasible fallback.

This also removes the dependency on rebuilding soft gradient layers after
plugin intersection.

### 3. Use one allocation solution model

Use a common solution structure for sequential and interdependent SubJob
allocation. A solution must carry at least:

- recoverable statement operations;
- remaining Job/SubJob worksheets;
- final Job `AllocatedHyperNode`;
- final placement for every SubJob participating in the transaction;
- applicable hard-policy result;
- per-plugin and total soft scores;
- number and identity of progress-making SubJobs/tasks;
- deterministic tie-break data.

Solution comparison order:

1. reject any hard-infeasible solution;
2. require Job ready/pipelined or a valid non-empty progress transaction under
   zero minima;
3. prefer solutions satisfying an explicit soft preferred-tier threshold;
4. maximize merged soft/plugin score;
5. prefer tighter placement only when that is part of the defined topology
   score;
6. use stable Job/SubJob/HyperNode names as the final tie-breaker.

No branch may return a first feasible combination before the common comparison
logic has evaluated the relevant alternatives.

### 4. Build a bounded topology working set

Do not use "any target remains in the worksheet" as the search termination
condition.

For each allocation transaction:

1. start with the SubJobs required to satisfy current Job/SubJob gang minima;
2. if minima are already zero, select the next pending SubJob needed to make
   non-empty progress;
3. expand only the hard-topology relation component needed to validate that
   transaction, using required policy names and already allocated peer state;
4. for a relation that needs an unallocated peer witness, add the minimum
   number of pending peer SubJobs needed to evaluate it, not every matching
   SubJob;
5. do not reinsert an already-ready SubJob merely because it has replicas
   outside the current working set.

Later allocation iterations evaluate remaining SubJobs against the committed
Job/SubJob placement state.

### 5. Bound and prune interdependent search

The general search should:

- retain the sequential fast path for independent SubJobs;
- search only connected hard-topology components;
- deduplicate options with the same effective placement and task/node outcome;
- discard dominated candidates that have the same hard state and no better
  soft score;
- order branches by current soft score to find good solutions early;
- record explored/pruned state counts for tests and diagnostics;
- fail conservatively rather than commit an unproven hard-invalid placement if
  an explicit search guard is reached.

The implementation proposal must document any concrete search limit before it
is introduced. No arbitrary limit is approved by this document alone.

### 6. Align the soft network-topology contract

Update scheduler documentation and API comments to define:

- hard + highest tier: mandatory maximum LCA tier;
- soft + highest tier: preferred maximum LCA tier with fallback;
- soft without highest tier: valid locality/bin-packing preference without a
  discrete threshold.

No CRD schema change is expected.

## Verification Cases

The cases below are authoritative for this issue. Implementation is not
complete until every case passes or a change is separately explained and
approved.

### U1: framework abstention and hard intersection

- Purpose: prove a neutral plugin cannot constrain another plugin.
- Scope: `pkg/scheduler/framework` unit test.
- Setup:
  - plugin A abstains;
  - plugin B allows selected descendant HyperNodes;
  - additional variants where both abstain and where one applicable plugin
    returns no candidates.
- Expected:
  - A+B result equals B's candidate set;
  - both abstaining returns the full caller universe;
  - applicable-empty is unschedulable, not neutral.

### U2: soft order survives multi-plugin composition and incremental placement

- Purpose: reproduce the physical-tier/LCA-tier reordering bug.
- Scope: framework/plugin/allocate unit test.
- Setup:
  - non-empty existing `AllocatedHyperNode`;
  - local tier-2 candidate has preferred LCA tier 2;
  - remote physical tier-1 candidate has fallback LCA tier 3;
  - both topology plugins enabled.
- Expected:
  - the local tier-2 solution outranks the remote tier-1 fallback;
  - candidate physical tier does not overwrite soft LCA preference.

### U3: hard SubGroup plus soft network and preferred SubGroup scoring

- Purpose: prove hard and soft policies use the same global solution model.
- Scope: allocate unit test.
- Setup:
  - required SubGroup affinity and anti-affinity;
  - soft Job/SubJob network topology;
  - preferred SubGroup term with a layout that distinguishes first-feasible
    from best-scored.
- Expected:
  - all hard terms pass;
  - the globally better soft solution is selected;
  - fallback remains schedulable when the preferred layout is infeasible.

### U4: minSubGroups smaller than actual SubJob count

- Purpose: prevent all-SubJob atomic expansion.
- Scope: allocate unit test.
- Setup:
  - at least four topology-related SubJobs;
  - `minSubGroups=1` and Job minimum requiring one;
  - resources currently fit only one valid transaction.
- Expected:
  - at least the required/progress SubJob binds;
  - unrelated remaining SubJobs stay in the worksheet;
  - the valid minimum is not rolled back because all replicas cannot fit.

### U5: zero-minimum topology progress

- Purpose: preserve Bug 002 without searching every pending SubJob.
- Scope: allocate unit test.
- Setup:
  - `minMember=0`, `minSubGroups=0`;
  - required and preferred SubGroup variants.
- Expected:
  - allocation returns non-empty progress;
  - required relationships are satisfied for committed participants;
  - repeated allocation iterations can schedule remaining participants.

### U6: mixed SubJob network modes under Job soft topology

- Purpose: prove Job-level placement includes every participating SubJob.
- Scope: allocate unit test.
- Setup:
  - Job soft preferred tier;
  - one soft SubJob policy, one hard policy, and one policy without
    SubJob-level network topology.
- Expected:
  - final Job LCA and score include all participants;
  - no policy is omitted from solution completeness because of its mode.

### U7: soft contract with and without highest tier

- Purpose: align implementation with the documented soft API contract.
- Scope: API/plugin unit tests.
- Variants:
  - soft with preferred highest tier and feasible preferred placement;
  - soft with preferred tier infeasible and wider fallback;
  - soft without a highest tier;
  - hard highest-tier regression.
- Expected: each variant follows the contract in Proposed Design section 6.

### U8: Bug 001-003 regressions

- Run existing focused tests for:
  - soft Job/SubJob topology packing;
  - zero-minimum hard/preferred SubGroup progress;
  - hard SubGroup affinity with network plugin neutral behavior;
  - composite PodGroup/SubGroup topology affinity.

### U9: search scale and determinism

- Purpose: detect unbounded duplicate search and unstable tie behavior.
- Scope: allocate unit test or benchmark with assertions.
- Setup:
  - topology up to the workspace's eight-node test limit;
  - multiple related SubJobs and overlapping ancestor/descendant candidates.
- Expected:
  - equivalent candidate states are deduplicated;
  - explored-state count stays within the documented bound;
  - repeated runs choose the same placement.

## Package Verification

Planned commands after implementation:

```bash
gofmt -w <changed-go-files>
git diff --check

go test -count=1 ./pkg/scheduler/framework
go test -count=1 ./pkg/scheduler/actions/allocate
go test -count=1 ./pkg/scheduler/plugins/network-topology-aware
go test -count=1 ./pkg/scheduler/plugins/group-topology-affinity
go test -count=1 ./pkg/scheduler/api

go test -count=1 \
  ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/api

make vc-scheduler
```

If internal callback signatures affect additional packages, those packages must
be added to the focused gate. `make unit-test` should be run before final Kind
verification unless investigation records an environment blocker.

## Mandatory Kind Verification

### Environment

- Reuse the current 4-node Kind cluster named `kind`.
- Keep the standard three-tier HyperNode tree:

  ```text
  kind-tier3-all
    -> kind-tier2-pair-a
         -> kind-tier1-kind-control-plane -> kind-control-plane
         -> kind-tier1-kind-worker        -> kind-worker
    -> kind-tier2-pair-b
         -> kind-tier1-kind-worker2       -> kind-worker2
         -> kind-tier1-kind-worker3       -> kind-worker3
  ```

- Keep both plugins enabled:

  ```yaml
  - name: network-topology-aware
    enabledHyperNodeGradient: true
    enabledHyperNodeOrder: true
  - name: group-topology-affinity
    enabledHyperNodeGradient: true
    enabledHyperNodeOrder: true
  ```

- Create PodGroups first, wait until they exist, and only then create the
  corresponding Deployments.

### Local scheduler image

Build the changed scheduler on the host and load it into Kind:

```bash
make vc-scheduler
docker build --platform linux/arm64 \
  -t volcanosh/vc-scheduler:dev-004-topology-refactor \
  -f ../dockerfile.scheduler.local .
kind load docker-image volcanosh/vc-scheduler:dev-004-topology-refactor --name kind
kubectl set image deployment/volcano-scheduler -n volcano-system \
  volcano-scheduler=volcanosh/vc-scheduler:dev-004-topology-refactor
kubectl rollout status deployment/volcano-scheduler \
  -n volcano-system --timeout=180s
```

Controller and admission images are unchanged. If an unchanged image is
missing, pull it on the host and load it into Kind rather than rebuilding it.

### K1: existing topology-affinity Cases 1-6

- Run all current cases under `example/topology-affinity/cases`.
- Expected:
  - Cases 1-6 retain their previously recorded placements/relationships;
  - all expected Pods become scheduled;
  - no case regresses when both topology plugins are enabled.

### K2: incremental soft placement with both plugins

- First create and schedule one SubJob in pair-a.
- Then create/scale the remaining Deployment so the PodGroup is allocated in a
  second scheduler iteration.
- Configure soft topology to prefer staying within tier2.
- Expected:
  - the later Pod remains in pair-a while capacity exists;
  - a remote tier1 in pair-b is not selected merely because its physical tier
    is lower.

### K3: required SubGroup plus soft network/preferred SubGroup

- Configure required SubGroup affinity/anti-affinity together with soft
  Job/SubJob network topology and at least one preferred SubGroup term.
- Expected:
  - required relationships are satisfied;
  - preferred placement wins when feasible;
  - a separately applied resource pressure variant demonstrates fallback
    without leaving the workload permanently Pending.

### K4: minSubGroups progress under partial capacity

- Create more SubJobs than `minSubGroups`.
- Restrict current capacity or node selectors so only the minimum working set
  can schedule.
- Expected:
  - the minimum valid set becomes Running;
  - excess Pods may remain Pending;
  - the scheduler does not roll back all progress because every SubJob cannot
    fit.

### K5: zero-minimum hard and preferred progress

- Re-run the Bug 002 hard and preferred zero-minimum workloads.
- Expected:
  - Pods make progress and satisfy the applicable relationships;
  - scheduler logs show a bounded working set instead of an all-pending
    requirement.

### K6: mixed SubJob modes under Job soft topology

- Use one soft SubGroup policy and one hard or neutral SubGroup policy under a
  Job-level soft preferred tier.
- Expected:
  - all scheduled participants remain in the preferred Job domain when
    feasible;
  - fallback is observed after preferred-domain capacity is intentionally
    exhausted.

### Kind pass criteria

For every case record:

- applied PodGroup and Deployment manifests;
- PodGroup phase;
- Pod-to-node placement;
- resolved tier1/tier2/tier3 membership;
- relevant scheduler candidate, hard-filter, soft-score, explored-state, and
  selected-solution logs;
- explicit PASS/FAIL;
- cleanup commands and final namespace state.

Final cleanup must leave no Bug 004 workloads in `default`. Keep the standard
HyperNode topology and Volcano installation unless the user requests their
removal.

## Approval Gate

Approved by the user. U1-U9 and K1-K6 are the implementation completion gate.

## Implementation Record

### Framework and scheduler changes

- Added an explicit `HyperNodeGradientResult` contract. A plugin now returns
  either an applicable hard constraint or abstains; callback errors are
  propagated instead of being treated as an empty/neutral result.
- Framework intersection now starts from the caller's HyperNode subtree,
  ignores abstaining plugins, intersects only applicable hard constraints, and
  returns a deterministic single candidate layer. A plugin cannot return a
  candidate outside the caller's universe.
- Moved soft network-topology behavior out of gradient order and into
  HyperNode/solution scoring. Job-level soft topology is scored for soft,
  hard, and neutral SubJobs.
- Changed ordinary and interdependent SubJob allocation to use the same
  `subJobAllocationOption` model. All feasible candidates are compared before
  committing a solution.
- Soft scoring uses the effective LCA produced by dry-run task placement, not
  the outer HyperNode search domain. This was additionally found and fixed
  during K1 Case 2: a broad tier3 candidate could otherwise receive no soft
  PodGroup anti-affinity penalty and then place its Pods back in the occupied
  tier2.
- Added a topology transaction working set derived from current gang minima.
  Zero-minimum Jobs choose one deterministic progress SubJob. A ready minimum
  transaction no longer expands atomically to every pending SubJob.
- Added recursive search state fingerprints, dominance pruning by cumulative
  score, deterministic tie-breaking, and `exploredStates`/`prunedStates`
  logging. No arbitrary search-count guard was introduced.
- Updated the network-topology user guide and API source comments for hard,
  soft-with-preferred-tier, and soft-without-tier semantics.

### Unit and package verification

Final focused package command:

```bash
go test -count=1 \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/api
```

Final result:

```text
framework                       PASS  1.793s
actions/allocate                PASS 46.559s
network-topology-aware          PASS  5.060s
group-topology-affinity         PASS  4.789s
api                             PASS  6.110s
```

The tests include U1-U9 coverage for abstention/intersection, callback errors,
soft LCA ordering, hard+soft+preferred fallback, partial gang progress,
zero-minimum progress, mixed SubJob modes, soft topology without a preferred
tier, eight-candidate deterministic ties, and dominated-state pruning.

Additional checks:

- `make vc-scheduler`: PASS.
- `git diff --check`: PASS.
- `hack/verify-gofmt.sh`: the changed files are formatted, but the repository
  check reports pre-existing formatting differences in
  `pkg/scheduler/api/hyper_node_info_test.go` and
  `pkg/scheduler/api/topology_affinity_info_test.go`; this task did not modify
  those files.
- `make unit-test`: blocked. The sandbox cannot write the host Go test cache,
  and the required elevated execution was rejected because the tool approval
  usage quota was exhausted. No project-local Go cache workaround was used.
- A final bulk scheduler-log extraction command was rejected by the same tool
  approval quota. During Case 2 diagnosis, the relevant live log was observed:
  pair-a received the preferred anti-affinity penalty (`0`), pair-b retained
  the preferred score (`1000`), and the incorrect broad-domain solution
  bypassed that penalty until scoring was changed to the effective dry-run LCA.
- `controller-gen` is not installed. Regenerating manifests for the API comment
  changes remains blocked by the same tool-install/approval constraint.

### Kind image and environment

- Cluster: `kind`, four arm64 nodes.
- Image: `volcanosh/vc-scheduler:dev-004-topology-refactor`.
- Final image ID: `sha256:88f0fe4f2bf0e1d5f6e5d0a365be68b2f70924982dc44fc1d8c96bfa42f485d0`.
- The image was built on the host, loaded into all four Kind nodes, and rolled
  out to `deployment/volcano-scheduler`.
- Both `network-topology-aware` and `group-topology-affinity` kept gradient and
  order callbacks enabled.
- Standard HyperNode status after cleanup remained tier1 `nodeCount=1`, tier2
  `nodeCount=2`, and tier3 `nodeCount=2`.

### Kind verification results

K1, existing Cases 1-6: PASS.

```text
Case 1 hard PodGroup anti-affinity:
  group A -> kind-control-plane (pair-a)
  group B -> kind-worker2       (pair-b)

Case 2 soft PodGroup anti-affinity:
  initial Bug004 image exposed a regression: both groups selected pair-a
  after effective-placement scoring fix and rebuild:
  group A -> kind-control-plane (pair-a)
  group B -> kind-worker2       (pair-b)

Case 3 hard SubGroup affinity/anti-affinity:
  decode-0  -> kind-control-plane
  decode-1  -> kind-worker
  prefill-0 -> kind-worker2
  prefill-1 -> kind-worker3

Case 4 soft SubGroup affinity/anti-affinity:
  all four Pods Running; pair-a used with two tier1 domains

Case 5 network + group topology plugins:
  all four Pods Running; each role spread across the two pair-a tier1 domains

Case 6 PodGroup + SubGroup composition:
  anchor  -> kind-control-plane (pair-a)
  decode  -> kind-worker2       (pair-b)
  prefill -> kind-worker3       (pair-b)
```

K2, incremental soft placement: PASS.

```text
phase A -> kind-control-plane
phase B -> kind-control-plane
```

K3, required SubGroup plus soft network/preferred SubGroup: PASS.

```text
preferred feasible:
  role-a -> kind-control-plane
  role-b -> kind-worker

8 CPU local pressure fallback:
  pressure -> kind-worker
  role-a   -> kind-control-plane
  role-b   -> kind-worker2
```

K4, `minSubGroups=1` with four pending SubJobs: PASS.

```text
k4-0 -> Running on kind-worker2
k4-1 -> Pending
k4-2 -> Pending
k4-3 -> Pending
```

K5, zero-minimum hard and preferred progress: PASS.

```text
hard:      role-a -> kind-worker2, role-b -> kind-worker3
preferred: role-a -> kind-worker2, role-b -> kind-worker3
```

K6, mixed soft/hard/neutral SubJob modes: PASS.

```text
preferred feasible:
  soft, hard, neutral -> kind-control-plane (pair-a)

preferred pair-a exhausted by two 8 CPU pressure Pods:
  soft    -> kind-control-plane
  hard    -> kind-worker2
  neutral -> kind-worker2
```

All manual tests created PodGroups before their Deployments. The K2-K6
manifests are stored in this issue directory. Final cleanup removed all Bug004
workloads from `default` and removed every temporary `bug004/*` node label.

### Commit and remaining gate

Source changes were committed in the Volcano repository:

```text
dc7d5f17c refactor(scheduler): generalize topology allocation
```

`make manifests` completed successfully on 2026-07-13 with the repository-
pinned `controller-gen v0.18.0`; the generated PodGroup CRD is included in the
commit.

All agreed U1-U9 and K1-K6 behavioral cases passed. The issue remains `IP`
until the full `make unit-test` gate can be executed; it is not renamed to
`DONE` in this session.

### K7: production-shaped single-policy SubGroup anti-affinity diagnosis

User-provided production PodGroup shape examined on 2026-07-13:

- one `role-0` SubGroupPolicy with `subGroupSize: 1` and `minSubGroups: 2`;
- Pods selected by `app.modelarts.huaweicloud/role=role-0`, partitioned by
  `app.modelarts.huaweicloud/role-0-group`;
- a single-policy `subGroupAntiAffinity.preferred` term at
  `volcano.sh/hypernode`, with `weight: 40`;
- Job and SubGroup network topology both configured as `mode: soft`.

The two distinct `role-0-group` values correctly produce distinct SubJobs.
With one policy in the term, the scheduler correctly compares different
instances of that policy to each other; the term need not list a second policy
name.

#### Local Kind setup

- Cluster: `kind-kind`, four arm64 nodes; the test limited candidates to
  `kind-control-plane` and `kind-worker2`, which are separate tier1
  HyperNodes.
- Scheduler image: `volcanosh/vc-scheduler:dev-004-topology-refactor`.
- Both topology plugins had `enabledHyperNodeGradient` and
  `enabledHyperNodeOrder` set to `true`, each with plugin weight `10`.
- Artifacts:
  - `k7-production-like-subgroup-soft-anti-affinity.yaml`
  - `k7-production-like-subgroup-soft-anti-affinity-deployments.yaml`
  - `k7-production-like-subgroup-required-anti-affinity.yaml`
  - `k7-production-like-subgroup-required-anti-affinity-deployments.yaml`

The local Helm-installed PodGroup CRD predates the structured
`podGroupSelector` LabelSelector schema and rejects
`podGroupSelector.matchLabels`. The production-only PodGroup anti-affinity
term was therefore omitted from K7; it does not affect the intra-PodGroup
SubGroup anti-affinity assertion.

#### K7-S: preferred SubGroup anti-affinity

Commands:

```bash
kubectl label node kind-control-plane volcano-user-k7/candidate=true --overwrite
kubectl label node kind-worker2 volcano-user-k7/candidate=true --overwrite
kubectl apply -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-soft-anti-affinity.yaml
kubectl apply -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-soft-anti-affinity-deployments.yaml
kubectl wait pod -n default -l 'app in (bug004-k7-role-0-a,bug004-k7-role-0-b)' --for=condition=PodScheduled --timeout=90s
```

Observed result:

```text
bug004-k7-role-0-a  kind-control-plane  Running
bug004-k7-role-0-b  kind-control-plane  Running
PodGroup phase=Running, running=2, allocatedHyperNode=kind-tier1-control-plane
```

The scheduler did evaluate the term. Its candidate score log for the second
SubJob was `kind-tier1-control-plane:600.00` versus
`kind-tier1-worker2:1000.00`: the `weight: 40` term applied a 40% penalty to
the occupied tier1 domain. K7-S alone did not establish whether that penalty
was correctly consumed by the final solution comparison, so K7-W below varies
only this weight.

#### K7-W: maximum preferred weight still loses the computed split solution

Repeat K7-S with the same SubGroups, capacity, candidate tier1 domains, and
network topology, changing only `subGroupAntiAffinity.preferred[0].weight`
from `40` to `100`.

Observed result:

```text
bug004-k7-soft-100-role-0-a  kind-control-plane  Running
bug004-k7-soft-100-role-0-b  kind-control-plane  Running
PodGroup phase=Running, running=2, allocatedHyperNode=kind-tier1-control-plane
```

The scheduler log is decisive:

```text
# Same tier1 as the peer: full preferred penalty was applied.
... preferred final scores ... kind-tier1-control-plane:0.00

# Other feasible tier1 domain: no preferred penalty.
... preferred final scores ... kind-tier1-control-plane:0.00,kind-tier1-worker2:1000.00
... Allocate subJob to hyperNode success ... role-0-b,
    searchHyperNode=<cluster-top-hypernode>, score=3348.173568600319,
    allocatedHyperNode=kind-tier1-worker2

# Final commit nevertheless selects the colocated lower-tier solution.
... Allocate job to hyperNode success ... hyperNode=kind-tier1-control-plane
... update allocated hyperNode for subJob ... role-0-a ... kind-tier1-control-plane
... update allocated hyperNode for subJob ... role-0-b ... kind-tier1-control-plane
```

K7-W FAIL: this is not explained by a low preferred-term weight. The scheduler
computes the full `0` versus `1000` group-topology score and builds a split
candidate, but final Job-level HyperNode selection commits a colocated
solution.

The deterministic cause was confirmed by rerunning K7-W with the local
scheduler temporarily changed from `-v=3` to `-v=4` and inspecting the final
solution comparator:

```text
subGroup anti-affinity: preferred score detail ... weight=100,
  scoreBefore=1.00, scoreAfter=0.00
```

For the test topology, the colocated solution has Job LCA `tier1` and the
split solution has Job LCA `tier3`. The Job has `networkTopology.mode=soft`
with `highestTierName=tier3`, so both are inside the preferred maximum tier.
`jobAllocationSolutionLess` in `pkg/scheduler/actions/allocate/allocate.go`
orders Job solutions as follows:

1. whether the Job LCA is within the soft preferred maximum tier;
2. number of allocated soft-topology SubJobs;
3. lower Job LCA tier (more compact placement);
4. only then the merged SubJob/plugin score, which includes preferred
   SubGroup anti-affinity.

Consequently, tier1 is selected before the SubGroup anti-affinity score is
considered. Raising that term from `40` to `100` cannot change the result.
The production shape has the same conflict: its Job-level soft
`highestTierName=volcano.sh/hyperinstance-group` ranks a more compact
same-HyperNode LCA ahead of a split LCA that remains within that maximum tier.

This is a lexical-precedence regression in soft topology solution selection,
not a low-weight configuration. It requires scheduler work before a
`preferred` SubGroup rule can be relied on to choose the best available
domain. `required` remains the confirmed operational workaround when
separation is mandatory.

#### K7-H: required SubGroup anti-affinity

Using the same Pod labels, two candidate tier1 domains, and soft network
topology, change only the term from `preferred` to `required`.

Observed result:

```text
bug004-k7-required-role-0-a  kind-worker2         Running
bug004-k7-required-role-0-b  kind-control-plane   Running
PodGroup phase=Running, running=2, allocatedHyperNode=kind-tier3-all
```

The scheduler log explicitly rejected the conflicting candidate:

```text
subGroup anti-affinity: reject hyperNode ... reason=conflictWithPeerSubGroup ... tier=1
```

K7-H PASS: the hard rule prevents same-tier1 placement when another feasible
tier1 domain exists. K7-S is a diagnostic reproduction of the reported soft
placement and demonstrates that no hard anti-affinity guarantee was requested.

Cleanup completed:

```bash
kubectl delete -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-soft-anti-affinity-deployments.yaml --ignore-not-found=true
kubectl delete -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-soft-anti-affinity.yaml --ignore-not-found=true
kubectl delete -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-required-anti-affinity-deployments.yaml --ignore-not-found=true
kubectl delete -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-required-anti-affinity.yaml --ignore-not-found=true
kubectl delete -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-soft-anti-affinity-weight-100-deployments.yaml --ignore-not-found=true
kubectl delete -f issues/bugs/004-topology-allocation-framework-refactor-IP/k7-production-like-subgroup-soft-anti-affinity-weight-100.yaml --ignore-not-found=true
kubectl label node kind-control-plane volcano-user-k7/candidate-
kubectl label node kind-worker2 volcano-user-k7/candidate-
```

The temporary local scheduler verbosity was restored to `-v=3` after K7-W.

### K7 follow-up proposal: do not give implicit compactness lexical precedence over compatible soft subgroup topology

The K7 result is a confirmed bug, not merely a soft-preference trade-off.

For a Job with `networkTopology.mode=soft` and
`highestTierName=volcano.sh/hyperinstance-group`, a placement whose LCA is a
child `volcano.sh/hypernode` and a placement whose LCA is that same
`volcano.sh/hyperinstance-group` both satisfy the Job-level preferred maximum
tier. Splitting peer SubGroups across two child HyperNodes therefore does not
violate the Job-level soft topology preference.

The current `jobAllocationSolutionLess` incorrectly makes lower LCA tier an
absolute lexical preference before it compares `solution.score`. This creates
an implicit Job compactness rule that is not expressed by the PodGroup API and
prevents a feasible preferred SubGroup anti-affinity term from affecting the
choice, even at `weight: 100`.

#### Proposed correction

For Job-level soft network topology with a configured highest tier:

1. retain the hard boundary between a solution within the preferred maximum
   tier and a fallback solution above it;
2. do not rank two in-bound solutions by their numeric LCA tier before merged
   soft scores are considered;
3. compare the merged solution score, including preferred SubGroup affinity,
   anti-affinity, network-topology score, and normal scheduler scores, before
   using physical compactness or stable name ordering as a tie-breaker;
4. keep explicit soft compactness scoring only where it is represented as a
   score rather than as a hidden lexical constraint.

#### Proposed verification cases (approval required before implementation)

- **U10 — compatible Job soft topology and preferred SubGroup anti-affinity**
  - Setup: Job soft highest tier `tier3`; two feasible tier1 domains below the
    same tier3; two `role-0` SubJobs with different match-label values;
    preferred same-policy SubGroup anti-affinity at tier1.
  - Expected: the split tier1 placement wins because both layouts remain
    inside the Job preferred tier and the split avoids the anti-affinity
    penalty. Cover term weights 40 and 100.
- **U11 — soft Job topology fallback still ranks correctly**
  - Setup: a preferred-tier solution and an otherwise higher-score fallback
    whose LCA is above the Job soft highest tier.
  - Expected: the in-bound solution wins; the fallback is selected only when
    no in-bound solution is feasible.
- **U12 — no accidental removal of compactness behavior**
  - Setup: the same tier3 topology without applicable group-topology soft
    terms.
  - Expected: existing network-topology/binpack score behavior remains
    deterministic; no unrequested cross-tier spreading occurs.
- **K7-R — Kind regression**
  - Re-run the production-shaped K7 workload on two feasible tier1 domains
    under one tier3.
  - Expected: with preferred SubGroup anti-affinity and no violation of the
    Job soft maximum tier, the two SubGroups schedule to distinct tier1
    HyperNodes. Confirm score and final solution records in scheduler logs.

### K7 expanded impact analysis: other paths with the same lexical-precedence defect

This section records static analysis only; it does not change scheduler code
or add an implementation approval gate.

#### Exact trigger condition

`jobAllocationSolutionLess` is reached from both the ordinary Job allocation
path and the recursive hard-SubGroup allocation search.  The defect occurs
when all of the following hold:

1. the Job-level `networkTopology` is `mode: soft` and has a configured
   `highestTierAllowed`; `highestTierName` is equivalent because
   `updateJobTier` resolves it to that field before allocation;
2. there are at least two complete feasible allocation solutions;
3. both final Job LCAs are at or below the configured soft maximum tier;
4. the LCAs are at different tiers, for example same-tier1 placement versus
   two tier1 children whose LCA is tier2 or tier3; and
5. the wider-but-still-in-bound solution has a higher merged score.

The current comparison is effectively ordered by:

```text
in preferred maximum -> processed working-set size -> lower Job LCA tier -> merged score
```

Thus condition 5 cannot affect the result.  K7-W dynamically proves this for
preferred SubGroup anti-affinity: a split solution with the full `100`-weight
benefit was constructed, then discarded solely because the colocated Job LCA
was tier1 rather than tier3.

#### Confirmed mechanical scope

Every score contributing to `jobAllocationSolution.score` is vulnerable under
the trigger condition, not just the reported term.  The following are the
same defect class.

- **Preferred SubGroup anti-affinity:** confirmed by K7-W.  This includes
  one-policy terms that spread distinct instances of the same policy and
  multi-policy terms.
- **Preferred SubGroup affinity:** affected when an already placed unrelated
  SubGroup establishes a compact Job LCA and colocating the current SubGroup
  with its affinity peer widens that LCA, while remaining below the Job soft
  maximum.  This is most likely in incremental scheduling after the Job is
  already ready, or when node/resource constraints pin the unrelated group.
- **Preferred PodGroup anti-affinity:** affected when avoiding a matching
  external PodGroup expands this Job's LCA within its soft maximum.  A fresh
  one-Pod Job often does not meet the condition because either candidate has a
  tier1 Job LCA; a multi-SubGroup or incremental Job can meet it.
- **Several preferred topology-affinity terms:** any combination of
  PodGroup anti-affinity, SubGroup affinity, and SubGroup anti-affinity is
  affected whenever its higher-scoring layout is the wider in-bound layout.
  Term weight, including `100`, cannot overcome the lexical comparison.
- **Normal plugin scores, including bin-packing:** the network-topology-aware
  plugin combines bin-packing and soft-topology score before the final
  comparison.  A higher-score resource/bin-pack placement that only expands a
  Job from tier1 to tier2/tier3 within the configured soft maximum is likewise
  ignored.  This is the broader scheduler impact of the same comparator.
- **Hard SubGroup combinations plus additional preferred terms:** hard
  affinity/anti-affinity remains correctly enforced, but feasible layouts are
  selected through `searchSubJobAllocations`, which calls the same comparison.
  A preferred score that distinguishes two hard-feasible layouts can therefore
  be suppressed by lower Job LCA tier.

#### Paths examined and not found to be the cause

- **Hard constraint gradients:** not the source.  Framework intersection uses
  only applicable hard constraints and intentionally flattens surviving
  candidates into one layer; soft ordering is evaluated later.  Preferred
  terms are scored, not filtered, in `HyperNodeOrderFn`.
- **Per-SubJob option ordering:** `scoreSubJobAllocationOptions` sorts options
  for deterministic traversal, but ordinary allocation evaluates all candidate
  Job solutions and the recursive search explores each feasible option.  It
  does not create the observed score-before-selection inversion.
- **SubGroup-only soft network topology:** it does not activate the Job-level
  lexical rank.  Its compactness score competes normally with other plugin
  scores, so it is not this defect class by itself.
- **Job soft topology without a highest tier:** likewise does not enter
  `jobSoftTopologyPreferredTier`; scores are compared normally.
- **Two solutions with the same final Job LCA tier:** scores are compared, as
  covered by the existing `TestSelectBestHyperNodeForJobUsesSoftScoreWithinPreferredTier`.
- **Job hard network topology:** feasibility is correctly a hard boundary and
  should outrank preferred terms; it is not a variant of this bug.
- **A solution above the Job soft maximum versus one inside it:** current
  inside-before-outside ordering matches the documented
  "preferred, fallback-capable maximum LCA" contract.  It is a policy boundary,
  not the compatible-in-bound defect proved by K7.

`softTopologyAllocatedSubJobs` was also inspected.  For a complete feasible
transaction, the normal path processes the same working set for each solution,
and the recursive path only returns after that working set is complete.  No
separate reachable score inversion was identified there.

#### Existing test gap and proposed additions (approval required before implementation)

Existing placement tests cover (a) an in-bound solution versus an above-bound
fallback and (b) equal-LCA-tier in-bound solutions.  They do not cover two
different LCA tiers that are both inside the soft maximum.  Existing hard
SubGroup composite tests also do not make a preferred score choose between
such two hard-feasible layouts.

- **U13 — preferred PodGroup anti-affinity within the Job soft maximum**
  - Setup: an incremental or pinned multi-SubGroup Job has an existing tier1
    placement; an external matching PodGroup occupies that tier1; another
    candidate tier1 lies under the same configured Job maximum.
  - Expected: the new SubGroup chooses the non-conflicting domain when its
    merged preferred score is higher, even though Job LCA expands within the
    maximum.
- **U14 — preferred SubGroup affinity within the Job soft maximum**
  - Setup: an already scheduled unrelated SubGroup and the current SubGroup's
    affinity peer reside in distinct children of the Job soft maximum.
  - Expected: the current SubGroup follows its peer when the affinity score
    wins; a lower existing Job LCA must not short-circuit it.
- **U15 — hard SubGroup feasibility plus preferred-score choice**
  - Setup: two layouts meet all required SubGroup relations but have tier2 and
    tier3 Job LCAs below a tier3 Job soft maximum; a preferred group term
    favors the tier3 layout.
  - Expected: the favored hard-feasible layout wins by merged score.
- **U16 — non-topology score within the Job soft maximum**
  - Setup: two in-bound LCAs with different resource/bin-pack score, with no
    group preferred term.
  - Expected: the higher merged scheduler score wins; the documented soft
    maximum remains a boundary rather than an implicit compactness override.

### K7 correction approval and implementation verification plan

On 2026-07-13, the user explicitly approved starting the scheduler fix and
verification after reviewing the K7 cause and expanded impact analysis.

The approved implementation scope is deliberately narrow:

1. preserve the in-bound soft preferred-tier boundary over an above-bound
   fallback;
2. compare merged score before Job LCA compactness for solutions on the same
   side of that boundary; and
3. retain LCA compactness only as a deterministic score-tie breaker.

The authoritative verification for this correction is:

- **U10/U15 comparator regression:** a higher-scored tier3 Job-LCA solution
  must beat a lower-scored tier1 solution when both are in a Job soft tier3
  maximum.  This directly covers the normal selector and the reused recursive
  hard-SubGroup search comparator.
- **U11 fallback regression:** retain the existing test that an in-bound soft
  solution beats a higher-scored above-bound fallback.
- **U12 compact tie regression:** when merged scores tie, keep lower Job LCA
  tier as the deterministic compactness tie breaker.
- **K7-R Kind regression:** rebuild and load the local scheduler image, then
  rerun the production-shaped `weight: 100` preferred SubGroup anti-affinity
  workload.  The two SubGroups must land on distinct tier1 HyperNodes below
  the same tier3 Job soft maximum.

### K7 correction implementation and verification record

#### Implementation

Changed `jobAllocationSolutionLess` in
`pkg/scheduler/actions/allocate/allocate.go`.

- The Job soft preferred-tier in-bound/above-bound comparison remains first,
  preserving fallback semantics.
- For solutions on the same side of that boundary, `solution.score` is now
  compared before `softTopologyAllocatedSubJobs` and Job LCA tier.
- Lower Job LCA tier remains the deterministic compactness tie breaker when
  merged scores are equal.

Added focused tests in `pkg/scheduler/actions/allocate/placement_test.go`:

- `TestSelectBestHyperNodeForJobUsesSoftScoreAcrossInBoundLCATiers` (U10/U15);
- `TestSelectBestHyperNodeForJobUsesCompactnessOnSoftScoreTie` (U12).

The pre-existing tests cover U11 (in-bound solution over higher-score
above-bound fallback) and equal-tier score ordering.

#### Unit/package verification

Commands and results:

```bash
gofmt -w pkg/scheduler/actions/allocate/allocate.go \
  pkg/scheduler/actions/allocate/placement_test.go
git diff --check
go test -count=1 ./pkg/scheduler/actions/allocate
go test -count=1 ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/api
go test -count=1 -v ./pkg/scheduler/actions/allocate -run \
  'TestSelectBestHyperNodeForJob(PrefersSoftJobPlacement|UsesSoftScoreWithinPreferredTier|UsesSoftScoreAcrossInBoundLCATiers|UsesCompactnessOnSoftScoreTie)$'
```

All commands passed.  The explicit comparator regression output was:

```text
TestSelectBestHyperNodeForJobPrefersSoftJobPlacement                 PASS
TestSelectBestHyperNodeForJobUsesSoftScoreWithinPreferredTier        PASS
TestSelectBestHyperNodeForJobUsesSoftScoreAcrossInBoundLCATiers      PASS
TestSelectBestHyperNodeForJobUsesCompactnessOnSoftScoreTie           PASS
```

`make vc-scheduler` passed.

`make unit-test` was also run with the host Go cache enabled, but is blocked
outside this change's package scope on the current macOS host:

- agent QoS handler packages have no files for the selected build constraints;
- `pkg/networkqos/tc.TCCmd.PreAddFilter` returns `error`, while its current
  interface requires `(bool, error)`;
- Linux-only `runc` symbols are unavailable in the Darwin build.

The scheduler packages above and the current scheduler image build are not
affected by those failures.  This issue remains `IP` until the repository-wide
cross-platform unit-test gate is made runnable.

#### K7-R Kind verification

The scheduler was built from the final working tree, packaged, loaded, and
rolled out to the existing four-node Kind cluster:

```bash
make vc-scheduler
docker build --platform linux/arm64 \
  -t volcanosh/vc-scheduler:dev-004-k7-soft-score \
  -f ../dockerfile.scheduler.local .
kind load docker-image volcanosh/vc-scheduler:dev-004-k7-soft-score --name kind
kubectl rollout restart deployment/volcano-scheduler -n volcano-system
kubectl rollout status deployment/volcano-scheduler -n volcano-system --timeout=180s
```

Final image ID:

```text
sha256:817f9b003f38d55af4711bc3f9b90744f9d7be21231cf513848d7fc5121156cf
```

K7-R applied the existing production-shaped `weight: 100` manifests after
labelling only `kind-control-plane` and `kind-worker2` as candidates.  It
passed:

```text
bug004-k7-soft-100-role-0-a  kind-control-plane  Running
bug004-k7-soft-100-role-0-b  kind-worker2        Running
PodGroup phase=Running, running=2, allocatedHyperNode=kind-tier3-all
```

The current scheduler log confirms that the preferred SubGroup anti-affinity
score was evaluated and consumed by final selection:

```text
scores=kind-tier1-control-plane:0.00,kind-tier1-worker2:1000.00
Allocate job to hyperNode success ... hyperNode=kind-tier3-all
update allocated hyperNode for subJob ... role-0-a ... kind-tier1-control-plane
update allocated hyperNode for subJob ... role-0-b ... kind-tier1-worker2
```

K7-R cleanup completed by deleting its Deployments and PodGroup and removing
both temporary `volcano-user-k7/candidate` node labels.  A final check found
no remaining K7-R PodGroup or labelled node.

#### Final correction commit

```text
fba432356 fix(scheduler): score compatible soft topology layouts
```

### K8: Llama 70B hard-affinity-only simulation

Requested on 2026-07-14. This is an additional Kind validation of the
already-built scheduler; it makes no scheduler, API, or generated-manifest code
change.

#### Reduced test model

The supplied production PodGroup has 44 members (four 8-Pod prefill SubJobs
and two 6-Pod decode SubJobs). The request is to validate topology affinity,
not pod cardinality, so K8 uses the smallest workload that proves every stated
relationship:

- one anchor PodGroup in `supernode-a`;
- one target PodGroup with two one-Pod `prefill` shards and two one-Pod
  `decode` shards;
- `minMember: 4`, `subGroupSize: 1`, and `minSubGroups: 2` per role.

The two shards per role are necessary: a single shard cannot exercise that
role's same-policy `subGroupAntiAffinity` rule. The target retains the
production hard rules and tier names: PodGroup separation at `supernode`,
Prefill/Decode co-location at `supernode`, and per-role shard separation at
`rack`.

K8 deliberately does not add a `subGroups: [prefill, decode]` rack
anti-affinity term, because it is absent from the supplied configuration.
Therefore a prefill shard and a decode shard may share a rack; only shards of
the same role must be separated.

#### Topology and compatibility preflight

The existing Kind cluster has four Nodes. K8 temporarily renames the existing
standard tree's tier names while preserving its members and shape:

```text
kind-tier3-all (fabric)
  -> kind-tier2-pair-a (supernode) -> kind-tier1-control-plane (rack), kind-tier1-worker (rack)
  -> kind-tier2-pair-b (supernode) -> kind-tier1-worker2 (rack), kind-tier1-worker3 (rack)
```

The anchor is pinned to `rack-a0`; the target must choose `supernode-b`.
Both topology plugins are already enabled in the live scheduler ConfigMap.

An attempted separate tree with the same tier numbers was removed from the
plan. Admission resolves `highestTierName` to its numeric tier, so parallel
trees at that tier leave candidate selection ambiguous: the anchor was placed
on the original `kind-tier1-control-plane` path. Renaming the sole active tree
for the short test eliminates that ambiguity. The
`k8-llama-70b-affinity-restore-standard-topology.yaml` manifest restores the
original `tier1`/`tier2`/`tier3` names during cleanup.

The live PodGroup CRD currently declares `podGroupSelector` as an atomic
object, so server-side field validation may reject the supplied
`podGroupSelector.matchLabels`. K8 first performs a server dry run. If this
schema is not accepted, the affinity behavior will be validated in the empty
test namespace with `podGroupSelector: {}` (which selects the sole anchor
peer), while recording that the exact label-selector schema requires a CRD
upgrade before it can be used in a shared production namespace.

#### K8 verification cases

- **K8-P: schema acceptance.** Server-dry-run the target PodGroup with the
  supplied `matchLabels` selector. Pass only if the API server accepts it; a
  rejection activates the isolated-selector fallback described above.
- **K8-A: hard PodGroup anti-affinity.** Schedule the anchor first. The target
  must not use `supernode-a`; all its Pods must resolve to
  `llama-k8-supernode-b`.
- **K8-B: hard SubGroup affinity.** The `prefill` and `decode` SubJobs must
  all resolve to `llama-k8-supernode-b`.
- **K8-C: hard same-role SubGroup anti-affinity.** `prefill/p0` and
  `prefill/p1` must resolve to different `rack` HyperNodes; independently,
  `decode/d0` and `decode/d1` must resolve to different `rack` HyperNodes.

Planned commands:

```bash
kubectl apply -f k8-llama-70b-affinity-named-standard-topology.yaml
kubectl label node kind-control-plane volcano-user-k8/anchor=true --overwrite
kubectl apply -f k8-llama-70b-affinity-anchor-podgroup.yaml
kubectl apply -f k8-llama-70b-affinity-anchor-deployment.yaml
kubectl wait --for=condition=Available deployment/llama-70b-anchor-0 -n default --timeout=2m
kubectl apply -f k8-llama-70b-affinity-podgroup.yaml
kubectl apply -f k8-llama-70b-affinity-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/llama-70b-instance-0-prefill-p0 \
  deployment/llama-70b-instance-0-prefill-p1 \
  deployment/llama-70b-instance-0-decode-d0 \
  deployment/llama-70b-instance-0-decode-d1 -n default --timeout=2m
```

The exact commands, Pod placement, resolved HyperNode domains, scheduler
diagnostics, pass/fail results, and cleanup outcome will be recorded below
after execution.

#### K8-P pre-upgrade result

The current cluster rejected the exact target manifest during server-side dry
run:

```text
strict decoding error: unknown field
"spec.topologyAffinity.podGroupAntiAffinity.required[0].podGroupSelector.matchLabels"
```

The first K8 run therefore used `k8-llama-70b-affinity-podgroup-current-crd.yaml`. It
changes only `podGroupSelector` to `{}`; all hard topology terms, policies,
tiers, and role/shard labels are unchanged. At execution time the `default`
namespace contains no other user PodGroups, so the selector has exactly one
peer: `llama-70b-anchor-0`. K8-P is recorded as an expected compatibility
limitation, not a pass of the exact production CRD shape.

#### K8 topology-preflight result

The first separate-tree approach successfully built its rack/supernode/fabric
HyperNodes with the expected member sets, but the anchor PodGroup was assigned
to `kind-tier1-control-plane` from the pre-existing tree. No target workload
was created under that ambiguous layout. The anchor deployment and PodGroup,
and the separate `llama-k8-*` HyperNodes, are removed before the renamed-tree
test begins.

#### K8-A through K8-C result

Environment:

- Cluster: existing `kind`, four arm64 Nodes.
- Scheduler image: `volcanosh/vc-scheduler:dev-004-k7-soft-score`.
- Scheduler plugins: `network-topology-aware` and
  `group-topology-affinity`, both with HyperNode gradient and order enabled.
- Test topology: the standard tree temporarily named `rack` / `supernode` /
  `fabric`, with `kind-tier2-pair-a` as supernode-a and
  `kind-tier2-pair-b` as supernode-b.

Commands executed:

```bash
kind load docker-image busybox:1.36 --name kind
kubectl apply -f k8-llama-70b-affinity-named-standard-topology.yaml
kubectl label node kind-control-plane volcano-user-k8/anchor=true --overwrite
kubectl apply -f k8-llama-70b-affinity-anchor-podgroup.yaml
kubectl apply -f k8-llama-70b-affinity-anchor-deployment.yaml
kubectl wait --for=condition=Available deployment/llama-70b-anchor-0 -n default --timeout=2m
kubectl apply -f k8-llama-70b-affinity-podgroup-current-crd.yaml
kubectl apply -f k8-llama-70b-affinity-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/llama-70b-instance-0-prefill-p0 \
  deployment/llama-70b-instance-0-prefill-p1 \
  deployment/llama-70b-instance-0-decode-d0 \
  deployment/llama-70b-instance-0-decode-d1 -n default --timeout=2m
```

Observed placement:

```text
anchor                         kind-control-plane  -> kind-tier2-pair-a (supernode)
prefill shard p0               kind-worker2        -> kind-tier1-worker2 (rack)
prefill shard p1               kind-worker3        -> kind-tier1-worker3 (rack)
decode shard d0                kind-worker2        -> kind-tier1-worker2 (rack)
decode shard d1                kind-worker3        -> kind-tier1-worker3 (rack)
target PodGroup allocation     kind-tier2-pair-b   -> supernode
```

The scheduler log establishes each hard rule rather than only showing the
final placement:

- It resolved the anchor's `kind-tier1-control-plane` placement to occupied
  `kind-tier2-pair-a` at the `supernode` tier and rejected that pair's two
  racks for the target.
- It retained only `kind-tier2-pair-b` and its two racks for the target, then
  committed the Job to `kind-tier2-pair-b`.
- It explicitly rejected `decode/d1` on the occupied `kind-tier1-worker2`
  rack and `prefill/p1` on the occupied `kind-tier1-worker2` rack with
  `reason=conflictWithPeerSubGroup`.

Results:

- K8-A hard PodGroup anti-affinity: **PASS** — target used supernode-b, not
  the anchor's supernode-a.
- K8-B hard SubGroup affinity: **PASS** — all target SubJobs share
  supernode-b.
- K8-C hard same-role SubGroup anti-affinity: **PASS** — `p0`/`p1` are on
  separate racks and `d0`/`d1` are on separate racks.

This is intentionally not evidence of cross-role rack separation: `p0` and
`d0` share one rack and `p1` and `d1` share the other, which is permitted by
the submitted rules.

#### K8 cleanup result

The target Deployments and PodGroup, anchor Deployment and PodGroup, and the
temporary `volcano-user-k8/anchor` Node label were deleted. The final anchor
Pod completed background cascading deletion before the check passed.

The standard HyperNode names were restored with
`k8-llama-70b-affinity-restore-standard-topology.yaml`:

```text
kind-tier1-*  tierName=tier1  nodeCount=1
kind-tier2-*  tierName=tier2  nodeCount=2
kind-tier3-all tierName=tier3 nodeCount=2
```

No `llama-70b-anchor-0` or `llama-70b-instance-0` PodGroup, Deployment, or
Pod remains. `volcano-scheduler` is healthy with `1/1` ready replicas.

### K8-CRD: upgrade and exact `matchLabels` revalidation

On 2026-07-14, the local Kind cluster's PodGroup CRD was upgraded from the
repository's generated CRD, as requested:

```bash
kubectl apply -f volcano/config/crd/volcano/bases/scheduling.volcano.sh_podgroups.yaml
```

The installed storage-version schema now exposes
`podGroupSelector.matchLabels` as an `object`. The original, unmodified
K8 target manifest then passed server-side validation:

```bash
kubectl apply --dry-run=server -f k8-llama-70b-affinity-podgroup.yaml
# podgroup.scheduling.volcano.sh/llama-70b-instance-0 created (server dry run)
```

#### Exact-selector Kind result

The same four-node `rack`/`supernode` topology and one-Pod-per-shard workload
were run again, this time with
`podGroupSelector.matchLabels.topology.volcano.sh/group=llama-70b-prod` from
the original manifest. The persisted PodGroup confirms the full selector was
accepted without pruning.

Observed final state:

```text
anchor                         kind-control-plane  -> kind-tier2-pair-a (supernode)
target PodGroup                kind-tier2-pair-b   -> supernode
prefill shard p0               kind-worker2        -> kind-tier1-worker2 (rack)
prefill shard p1               kind-worker3        -> kind-tier1-worker3 (rack)
decode shard d0                kind-worker2        -> kind-tier1-worker2 (rack)
decode shard d1                kind-worker3        -> kind-tier1-worker3 (rack)
```

The scheduler log identifies the matched PodGroup as exactly
`default/llama-70b-anchor-0`, resolves its occupied supernode to
`kind-tier2-pair-a`, rejects that domain, and retains only
`kind-tier2-pair-b` plus its two racks. It also logs
`conflictWithPeerSubGroup` for the same-role rack conflicts before committing
the target to `kind-tier2-pair-b`.

Result: **PASS** — the exact `matchLabels` configuration is admitted and all
three hard affinity requirements (PodGroup anti-affinity, SubGroup
supernode affinity, and same-role rack anti-affinity) are satisfied.

The revalidation workloads and temporary anchor label were removed, and the
standard HyperNode tier names were restored. The upgraded PodGroup CRD remains
installed so future manifests may use `podGroupSelector.matchLabels`.

### 2026-07-15 SubGroup reviewer follow-up

#### Review disposition

1. **`highestTierName` / `highestTierAllowed` in soft mode** — retained as the
   documented soft-preference contract. `highestTierName` is resolved to
   `highestTierAllowed` by `updateJobTier`, so both forms have the same result:
   an LCA at or below the configured tier is preferred, but a wider LCA remains
   a feasible fallback. Therefore, `mode: soft` and tier 2 does **not** forbid
   crossing tier 2; `mode: hard` is required for that guarantee.
2. **Allocation ownership** — explained rather than addressed by a physical
   file split. The review question is whether this change belongs in the
   allocate action at all, not whether the implementation is placed in
   `allocate.go` or another file in the same package. The candidate and search
   code therefore remain together in `allocate.go`. Bug 002
   (`6d2abfe071b43ed5113bd78c6c15fe103c091363`)
   demonstrated that the defect was in allocation transaction control rather
   than in the topology plugin's predicate:
   - with `minMember=0` and `minSubGroups=0`, `JobReady` could already be true
     before any relevant SubJob was simulated, so allocate stopped before a
     later peer reached the plugin callback;
   - required affinity/anti-affinity needs allocate to try a SubJob placement,
     recover its `framework.Statement`, expose the tentative
     `AllocatedHyperNode` to the next peer, recurse, and discard/backtrack when
     that peer has no feasible placement;
   - preferred affinity/anti-affinity likewise needs allocate to continue past
     ordinary gang readiness until the referenced peers have participated in
     scoring.

   A callback that maps one already-selected scheduling snapshot to eligible
   gradients or scores cannot cause its caller to continue after the caller has
   already ended the loop, and it must not own the task/resource transaction or
   its commit boundary. Moving this into `group-topology-affinity` would require
   turning that policy plugin into a second allocator with access to worksheets,
   task queues, `Statement` recovery/discard, readiness checks, and every other
   predicate/plugin involved in allocation. Moving it into API
   `SubJobInfo` would mix execution with scheduler state and create the wrong
   dependency direction (`framework` already depends on `api`). Therefore the
   plugin continues to own topology-rule evaluation, while allocate owns
   enumeration, tentative state, backtracking, and commit. The current
   `subJobPlacementCandidate` type predates Bug 002 in its original form; Bug
   002 is the historical proof for the ownership of the surrounding search and
   stop conditions, not a claim that the type itself originated in that fix.
3. **Candidate naming** — accepted. `subJobAllocationOption` is now
   `subJobPlacementCandidate`, making clear that it is the result of one dry
   run, not a user-facing scheduling option.
4. **Callback error return** — accepted. Restored the public callback types to
   their original `[][]*HyperNodeInfo` return signature; no caller needs an
   upgrade adaptation.
5. **Empty-gradient semantics** — accepted. A registered plugin that returns
   an empty non-nil gradient still makes the intersection empty and the load
   unschedulable. Plugin-internal errors are logged and converted to that same
   conservative result.
6. **Candidate universe ownership** — accepted. Removed the framework-owned
   `hyperNodeCandidateUniverse`. `network-topology-aware` and
   `group-topology-affinity` now independently return their full eligible
   subtree whenever they have no hard restriction, so their intersection stays
   full in a pure-soft case.
7. **Allocate-side error handling** — accepted as part of item 4; no error is
   returned from `HyperNodeGradientForJobFn` or
   `HyperNodeGradientForSubJobFn`.

#### Implementation

- Restored `HyperNodeGradientForJobFn` and `HyperNodeGradientForSubJobFn` in
  `pkg/scheduler/api/types.go` and `pkg/scheduler/framework/session_plugins.go`
  to the compatible two-result contract.
- Removed `HyperNodeGradientResult`, callback `error` propagation, and the
  framework candidate-universe fallback.
- Restored empty-gradient unschedulability and per-plugin full-subtree
  passthrough in both topology plugins.
- Added regression coverage for full-candidate intersection and for a SubJob
  without a hard network-topology policy returning the full gradient.

#### Focused package verification

Completed against the host Go cache after the sandbox cache access restriction:

```bash
gofmt -w pkg/scheduler/actions/allocate/allocate.go \
  pkg/scheduler/actions/allocate/dry_run_placement_test.go \
  pkg/scheduler/actions/allocate/placement_test.go \
  pkg/scheduler/api/types.go pkg/scheduler/api/unschedule_info.go \
  pkg/scheduler/framework/session_plugins.go \
  pkg/scheduler/framework/session_plugins_test.go \
  pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go
git diff --check
go test -count=1 ./pkg/scheduler/framework
go test -count=1 ./pkg/scheduler/actions/allocate
go test -count=1 ./pkg/scheduler/plugins/network-topology-aware
go test -count=1 ./pkg/scheduler/plugins/group-topology-affinity
go test -count=1 ./pkg/scheduler/api
```

Result: **PASS** — all five focused packages passed and `git diff --check` was
clean.

#### K6 Kind revalidation: mixed modes and soft fallback

Environment:

- Existing `kind` cluster, four arm64 Nodes: one control-plane and three
  workers.
- Standard three-tier HyperNode tree; tier 3 contains the two tier-2 pairs.
- Both `network-topology-aware` and `group-topology-affinity` enabled for
  HyperNode gradient and ordering.
- Local image:
  `volcanosh/vc-scheduler:dev-004-review-fixes`
  (`sha256:8148c6acf89faea3294439cdd2cef20f68489b263e2370b01121fafa7d7ffc7a`).

Commands completed:

```bash
cd volcano
make vc-scheduler
docker build --platform linux/arm64 \
  -t volcanosh/vc-scheduler:dev-004-review-fixes \
  -f ../dockerfile.scheduler.local .
cd ..
kind load docker-image volcanosh/vc-scheduler:dev-004-review-fixes --name kind
kubectl set image deployment/volcano-scheduler -n volcano-system \
  volcano-scheduler=volcanosh/vc-scheduler:dev-004-review-fixes
kubectl rollout status deployment/volcano-scheduler -n volcano-system --timeout=180s

cd issues/bugs/004-topology-allocation-framework-refactor-IP
kubectl apply -f k6-podgroup.yaml
kubectl apply -f k6-phase-soft.yaml
kubectl rollout status deployment/bug004-k6-soft -n default --timeout=120s
kubectl apply -f k6-pressure.yaml
kubectl rollout status deployment/bug004-k6-pressure-control-plane -n default --timeout=120s
kubectl rollout status deployment/bug004-k6-pressure-worker -n default --timeout=120s
kubectl apply -f k6-rest.yaml
kubectl rollout status deployment/bug004-k6-neutral -n default --timeout=120s
```

Observed placement:

```text
soft SubJob     kind-control-plane  -> tier2 pair-a
hard SubJob     kind-worker2        -> tier2 pair-b
neutral SubJob  kind-worker2        -> tier2 pair-b
PodGroup phase=Running, running=1, allocatedHyperNode=kind-tier3-all
```

The two pressure Deployments exhausted pair-a after the soft SubJob scheduled.
The hard and neutral SubJobs then made progress in pair-b, demonstrating the
soft tier's fallback behavior under pressure. Relevant scheduler logs include:

```text
podGroup anti-affinity: gradient full-subtree, job=default/bug004-k6-mixed
networkTopologyAware hyperNode score is: map[kind-tier1-worker2:... kind-tier1-worker3:...]
Allocate subJob to hyperNode success, subJob=.../hard-0, searchHyperNode=kind-tier1-worker2
Allocate subJob to hyperNode success, subJob=.../neutral-0, searchHyperNode=kind-tier1-worker2
```

Result: **PASS** — plugins supplied full candidates without framework fallback;
the hard policy remained feasible and soft topology fell back across the
preferred tier when pair-a capacity was exhausted.

Cleanup completed:

```bash
kubectl delete -f k6-phase-soft.yaml -f k6-pressure.yaml -f k6-rest.yaml \
  -f k6-podgroup.yaml --ignore-not-found
```

No K6 Deployments, Pods, or PodGroup remain. The scheduler is healthy at 1/1
with `volcanosh/vc-scheduler:dev-004-review-fixes`.

#### Full unit-test gate

`make unit-test` was also run on 2026-07-15. It did not complete because of
pre-existing, out-of-scope host/platform failures in the agent and network-qos
paths, including:

- QoS handler packages with no files selected by the current build constraints;
- `pkg/networkqos/tc`'s existing `PreAddFilter` interface-signature mismatch;
- Linux-only `runc`/`x/sys/unix` symbols unavailable to the Darwin test run;
- an existing nil-pointer panic in `cmd/network-qos/tools.TestSetCmdRun`.

None of these packages were modified by this reviewer follow-up. The targeted
scheduler-package gate and K6 Kind validation above passed. The issue remains
`IP` until the broader issue's remaining verification and its full-unit-test
environment blocker are resolved.

### 2026-07-16 reviewer item 2 interpretation correction

The review refers to ownership of allocation control flow, not the physical
length of `allocate.go`. Moving the same functions to another file in the
`allocate` package would not answer that question, so the temporary
`subjob_allocation.go` split was removed. `subJobPlacementCandidate`, candidate
collection/scoring, and the cross-SubJob search remain in `allocate.go`.

The reviewer response now distinguishes two claims:

- under the current callback contract, Bug 002 requires an allocate-action
  change because the failure happens when allocate terminates before later
  plugin callbacks; and
- this is not the only possible long-term architecture: a new generic topology
  planning extension point or scheduler-internal placement planner could
  declare the working set/backtracking requirement while leaving transaction
  execution in the allocator.

Verification after the mechanical merge:

```bash
gofmt -w pkg/scheduler/actions/allocate/allocate.go
git diff --check
go test -count=1 -run \
  'TestAllocateWithHardSubGroupTopologyAffinityWhenMinMemberIsZero|TestAllocateWithPreferredSubGroupTopologyAffinityContinuesAfterJobReady|TestCollectSubJobPlacementCandidates|TestTopologySearch' \
  ./pkg/scheduler/actions/allocate
```

Result: **PASS** for the focused Bug 002 and placement-search coverage.

The full allocate package was also attempted, but
`TestAllocateWithPartitionPolicyNetworkTopology` currently fails in the case
combining PodGroup hard network topology with SubGroup soft network topology;
an isolated rerun reproduced the same missing Bind. The file merge is
mechanical and does not change the function bodies, but the full package is not
recorded as passing after this correction. This failure remains to be
investigated separately before claiming the package-wide gate.

### 2026-07-16 commit split for reviewer scope

The squashed feature was split at the allocation-action boundary requested by
the reviewer:

1. `8fcc0012c feat(scheduler): support SubGroup topology affinity`
   - API, generated manifests, admission, framework intersection semantics,
     `group-topology-affinity`, and `network-topology-aware`;
   - no file under `pkg/scheduler/actions/allocate/` is changed;
   - 34 files, 2898 insertions, 246 deletions after the mandatory API codegen,
     minimal network-plugin composition fix, and plugin-owned preferred
     SubGroup Job-container ordering were included.
2. `edaaf83a7 fix(scheduler): search SubJob topology placements jointly`
   - `allocate.go`, its three test files, the SubJob soft-topology API helper,
     and network-topology soft placement scoring plus tests;
   - contains the Bug 002 allocation-transaction working set, recoverable
     candidate enumeration, cross-SubJob backtracking, pruning, and complete
     solution comparison;
   - 7 files, 2310 insertions, 148 deletions.
3. `c49caabf9 docs: document HyperNode topology-affinity scheduling`
   - documentation only.
4. `47e691146 docs: add topology affinity examples`
   - topology-affinity README and six runnable case groups only.

During intermediate-commit verification, the mixed PodGroup-hard/SubJob-soft
network-topology regression exposed that a soft SubJob still passed its prior
`AllocatedHyperNode` into gradient construction. That accidentally converted a
soft preference into a filter and contradicted the reviewer-agreed full
candidate behavior. The plugin commit now passes an allocated placement into
gradient construction only in hard mode; soft mode represents the prior
placement exclusively through HyperNode scoring. The full-gradient test helper
also sorts names inside a tier to avoid set-iteration order dependence.

The first commit was checked out in a detached temporary worktree and verified
independently, before the allocation commit existed:

```bash
go test -count=1 ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/webhooks/admission/podgroups/validate
go test -count=1 ./pkg/scheduler/actions/allocate
```

Result: **PASS**. This proves the feature/plugin commit is independently
buildable and does not depend on the later allocate implementation to compile
or pass its package gates.

Final-tree verification:

```bash
git diff --check
go test -count=1 ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/api \
  ./pkg/webhooks/admission/podgroups/validate
make vc-scheduler
```

Result: **PASS**. The previously failing mixed hard/soft allocate case also
passed after the soft-gradient correction. The branch
`dev/podgroup-antiaffinity` now points to `47e691146`. Safety checkpoint
`backup/dev-podgroup-antiaffinity-before-review-split-20260716` retains the
pre-split tree. The existing untracked `example/topology-affinity/` directory
was not added or modified.

#### Final-tree Kind revalidation

The final scheduler binary was packaged as
`volcanosh/vc-scheduler:dev-004-commit-split`
(`sha256:460d47833ce012821a4a16a8c2f9b8de48f2b1e230a8954fa66c0a1ba1d9381f`),
loaded into the existing four-node arm64 Kind cluster, and rolled out
successfully.

The K6 mixed topology case was rerun in phases: PodGroup first, soft SubJob,
pair-a pressure, then hard and neutral SubJobs. Observed placement:

```text
soft     kind-control-plane  (tier2 pair-a)
hard     kind-worker2        (tier2 pair-b)
neutral  kind-worker2        (tier2 pair-b)
```

All three Pods were Running. The PodGroup reached `Running`, its
`volcano.sh/job-allocated-hypernode` annotation was `kind-tier3-all`, and the
scheduler Deployment was 1/1 on the final image. This confirms that soft
topology still prefers pair-a initially and remains feasible across the tier
after pair-a is exhausted, while hard and neutral SubJobs continue to make
progress.

All K6 Deployments, Pods, and the PodGroup were deleted after verification;
`default` contained no remaining resources. The final scheduler image remains
deployed for follow-up debugging.

### 2026-07-16 mandatory staging API codegen

The workspace guide now requires every API source change under
`staging/src/volcano.sh/apis/` to run, from that directory:

```bash
./hack/update-codegen.sh
```

The generated output supplied after running that command was folded into the
first feature/API commit. It adds the missing topology-affinity conversion
registration and conversion functions, generated deepcopy updates,
applyconfiguration builder documentation, and `ForKind` mappings. The rewritten
commit chain is:

```text
8fcc0012c feat(scheduler): support SubGroup topology affinity
edaaf83a7 fix(scheduler): search SubJob topology placements jointly
c49caabf9 docs: document HyperNode topology-affinity scheduling
47e691146 docs: add topology affinity examples
```

`8fcc0012c` still contains no `pkg/scheduler/actions/allocate/` changes.
`edaaf83a7` contains the four allocate files plus the soft network-topology
scoring/API helper that the joint solution comparison consumes. Safety checkpoint
`backup/dev-podgroup-antiaffinity-before-codegen-20260716` retains the tree
before the generated output was incorporated.

The first commit was independently verified in a detached worktree:

```bash
cd staging/src/volcano.sh/apis
go test -count=1 ./pkg/apis/scheduling/... \
  ./pkg/client/applyconfiguration/...
cd ../../../..
go test -count=1 ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/webhooks/admission/podgroups/validate
go test -count=1 ./pkg/scheduler/actions/allocate
```

All commands passed. Final-tree focused tests across allocate, framework, both
topology plugins, scheduler API, and PodGroup admission passed, and
`make vc-scheduler` succeeded.

The codegen-final scheduler was packaged as
`volcanosh/vc-scheduler:dev-004-codegen`
(`sha256:fc4cfe969bb9f002bff855addbfb4763c610c8a89393e9c09932dd01dfdaef25`)
and passed the four-node Kind K6 mixed topology case again:

```text
soft     kind-control-plane  (tier2 pair-a)
hard     kind-worker2        (tier2 pair-b)
neutral  kind-worker2        (tier2 pair-b)
PodGroup Running, running=3, allocated=kind-tier3-all
```

The scheduler Deployment is 1/1 on that image. All K6 resources were removed;
the `default` namespace contained no remaining resources.

### 2026-07-16 network-topology-aware commit provenance correction

Reviewing the unsquashed history on
`backup/dev-podgroup-antiaffinity-before-pr-cleanup` showed that the earlier
path-only split placed too much network-topology behavior in the feature
commit:

- `ee838ba2d` introduced SubGroup topology affinity and did not modify
  `network-topology-aware`;
- `c556fc6a2` introduced cross-SubJob soft network-topology behavior and
  `SoftTopologyPreferredTier`;
- `e45f9e0b1` fixed neutral SubGroup gradient composition with the new group
  topology plugin; and
- `dc7d5f17c` converted soft gradient ordering to HyperNode scoring while
  generalizing the allocation solution search.

The final boundary is therefore semantic rather than path-only:

- feature commit `8fcc0012c` keeps only the network plugin's no-hard-constraint
  full-candidate gradient behavior and its regression test. This is required so
  framework intersection does not remove candidates supplied by
  `group-topology-affinity`;
- joint-placement commit `edaaf83a7` owns `SoftTopologyPreferredTier`,
  `addSoftTopologyScores`, `addSoftPlacementScore`, `softPlacementTier`, and
  the three soft-score tests, in addition to the allocate transaction/search;
- docs commit is `c49caabf9`.
- examples commit is `47e691146` and contains only README plus the six case
  groups.

The first commit was verified independently in a detached worktree:

```bash
go test -count=1 ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/webhooks/admission/podgroups/validate
go test -count=1 ./pkg/scheduler/actions/allocate
```

The plugin/framework/admission gate passed. The first full allocate run had a
transient `TestAllocateWithPVC` Binding-status failure; the isolated PVC rerun
and a subsequent full allocate-package rerun both passed.

`git diff backup/dev-podgroup-antiaffinity-before-network-split-20260716..HEAD`
is empty, proving the final source tree did not change during this provenance
correction. Therefore the final focused tests, build, and codegen-final Kind K6
result recorded above apply unchanged. The safety branch retains the tree and
history before this final commit-boundary adjustment.

### 2026-07-16 staged first-commit Kind verification

Before submitting the later joint-placement fix, the basic hard SubGroup
affinity/anti-affinity behavior was verified using only feature commit
`2110dad79`. A detached worktree was created at that exact commit and used to
build the scheduler:

```bash
git worktree add --detach ../volcano-2110dad79 2110dad79
cd ../volcano-2110dad79
make vc-scheduler
docker build --platform linux/arm64 \
  -t volcanosh/vc-scheduler:dev-004-2110dad79 \
  -f ../dockerfile.scheduler.local .
kind load docker-image volcanosh/vc-scheduler:dev-004-2110dad79 --name kind
kubectl set image deployment/volcano-scheduler -n volcano-system \
  volcano-scheduler=volcanosh/vc-scheduler:dev-004-2110dad79
kubectl rollout status deployment/volcano-scheduler \
  -n volcano-system --timeout=180s
```

The local arm64 image ID was
`sha256:8c21219bd3f67b19631888a311b5e736ebbad673cd770d55e64347743a8b7c66`.
The existing Kind cluster had four Ready nodes, both topology plugins enabled,
and the standard `tier3 -> tier2 -> tier1 -> Node` HyperNode tree. The default
namespace was empty before the test.

The basic hard SubGroup case was run from the supplied examples, creating the
PodGroup before its Deployments:

```bash
kubectl apply -f example/topology-affinity/cases/case-03-sg-hard-podgroup.yaml
kubectl apply -f example/topology-affinity/cases/case-03-sg-hard-deployments.yaml
kubectl wait --for=condition=Available \
  deployment/ta-deploy-sg-hard-prefill-0 \
  deployment/ta-deploy-sg-hard-prefill-1 \
  deployment/ta-deploy-sg-hard-decode-0 \
  deployment/ta-deploy-sg-hard-decode-1 \
  -n default --timeout=180s
```

Observed placement:

```text
decode/d0   kind-control-plane  tier1=kind-control-plane  tier3=kind-all
decode/d1   kind-worker         tier1=kind-worker         tier3=kind-all
prefill/p0  kind-worker2        tier1=kind-worker2        tier3=kind-all
prefill/p1  kind-worker3        tier1=kind-worker3        tier3=kind-all
```

The PodGroup reached `Running` with `running=4` and
`volcano.sh/job-allocated-hypernode=kind-tier3-all`. All four SubJobs shared the
required tier3 domain and occupied four distinct tier1 domains, satisfying the
same-policy and cross-policy required anti-affinity terms. Scheduler logs
contained `reason=conflictWithPeerSubGroup` rejections for conflicting tier1
candidates before committing the successful layout.

**PASS:** commit `2110dad79` independently provides the basic required
SubGroup affinity/anti-affinity behavior. This staged case intentionally does
not verify preferred/soft scoring or cross-SubJob best-solution selection;
those behaviors belong to the later joint-placement commit `62e68e033`.

The four Deployments and PodGroup were deleted after the test, leaving the
default namespace empty. The scheduler Deployment was restored to its pre-test
image `volcanosh/vc-scheduler:dev-004-codegen` and rolled out successfully.

#### Expanded first-commit Cases 1-6 verification

At user request, the same `volcanosh/vc-scheduler:dev-004-2110dad79` image was
redeployed and every case under `example/topology-affinity/cases` was run in
sequence. Each case started with an empty default namespace, created its
PodGroup before its Deployments, waited for all Deployments and PodGroups, and
was deleted before the next case.

Results:

```text
Case 1 hard PodGroup anti-affinity: PASS
  A -> kind-control-plane (pair-a)
  B -> kind-worker2       (pair-b)

Case 2 soft PodGroup anti-affinity: PASS
  A -> kind-control-plane (pair-a)
  B -> kind-worker2       (pair-b)

Case 3 hard SubGroup affinity/anti-affinity: PASS
  decode/d0  -> kind-control-plane
  decode/d1  -> kind-worker2
  prefill/p0 -> kind-worker
  prefill/p1 -> kind-worker3
  PodGroup Running, running=4, allocated=kind-tier3-all

Case 4 soft SubGroup affinity/anti-affinity: PASS (preferred scorer active)
  decode/d0, decode/d1, prefill/p0, prefill/p1 -> kind-control-plane
  PodGroup Running, running=4

Case 5 network + group topology plugins: PASS
  decode/d0, prefill/p0 -> kind-control-plane
  decode/d1, prefill/p1 -> kind-worker
  PodGroup Running, running=4, allocated=kind-tier2-pair-a

Case 6 PodGroup + SubGroup composition: PASS (hard constraints and preferred scorer active)
  anchor replicas -> kind-control-plane (pair-a)
  combo decode/prefill -> kind-worker2 (pair-b)
```

Case 4 remained schedulable, as required for preferred terms, and the scheduler
logs prove that the first-commit group topology plugin evaluated the preferred
SubGroup rules: the initial candidate received score `1000`, while a candidate
conflicting with an already placed peer received score `0`. Case 6 likewise
correctly enforced hard PodGroup anti-affinity by moving the combo PodGroup from
pair-a to pair-b, kept its SubGroups in a common tier2, and invoked the preferred
tier1 SubGroup anti-affinity scorer.

Both cases committed a compact final layout because the existing allocate
consumer passed one tier1 HyperNode at a time to the SubJob scorer and accepted
the first feasible fine-tier Job candidate. This final whole-job choice is
separate from whether the plugin implements and returns the preferred SubGroup
score. The direct plugin tests in `2110dad79`, especially
`TestHyperNodeOrderFnForSubGroupPreferredTerms`, pass multiple candidates and
verify that affinity prefers the peer domain and anti-affinity penalizes it.
The group plugin implementation and tests are byte-identical to their original
feature-commit versions in `ee838ba2d`.

Therefore `2110dad79` independently contains and validates both hard and soft
SubGroup topology-affinity mechanisms. Commit `62e68e033` changes how allocate
enumerates and compares complete multi-SubJob solutions so those already
computed soft scores can determine the final globally preferred layout; it is
the later Bug 002/follow-up correction, not the implementation source of the
SubGroup soft-affinity mechanism itself.

After the expanded run, all case resources were deleted, the default namespace
was empty, and the scheduler was restored to
`volcanosh/vc-scheduler:dev-004-codegen` with one Ready Pod and zero restarts.

### 2026-07-16 first-commit Case 4 plugin-driven correction proposal

Status: **implemented, independently verified, user-approved, and amended into
the first feature commit.**

#### Corrected problem statement

SubGroup preferred affinity/anti-affinity is implemented by the first feature
commit in `group-topology-affinity`; it must not be reclassified as an allocate
feature. The Case 4 log proves that the scorer runs, but the first SubJob is
currently dry-run inside a tier1 Job candidate. That Job candidate exposes only
one tier1 HyperNode to every later SubJob, so the later peer-aware order callback
has nothing to compare even though it correctly penalizes the occupied domain.

The immediate cause is candidate shape, not missing preferred scoring:

1. the group plugin returns the complete Job candidate subtree grouped in
   ascending physical tier;
2. framework intersection also rebuilds the result in ascending physical tier;
3. allocate accepts the first feasible tier1 Job gradient; and
4. later SubJobs therefore see only that one tier1 subtree.

#### Proposed design: retain all candidates, prioritize a useful Job container

Do not change `pkg/scheduler/actions/allocate/` and do not change callback
signatures.

1. In `group-topology-affinity`, derive the minimum Job container tier needed
   to give peer-aware SubGroup ordering a real choice:
   - preferred SubGroup affinity at tier `T` needs a Job container at tier `T`;
   - preferred SubGroup anti-affinity at tier `T` needs the nearest available
     parent tier greater than `T`, so at least two tier-`T` domains can be
     visible;
   - only terms with valid weight and at least one matching pair of actual
     SubJobs participate;
   - when several terms apply, use the highest required container tier so all
     participating terms can be evaluated.
2. Reorder the group plugin's **Job** gradient layers so that candidates at the
   derived container tier are tried first, coarser candidates remain available
   as fallback, and finer candidates remain present as the final fallback.
   No HyperNode is removed, so a preferred rule does not become a hard filter.
3. Preserve an explicit non-default plugin layer priority when framework
   intersects multiple plugin candidate sets:
   - an ordinary gradient whose non-empty layers are already in ascending
     physical tier remains membership-only and does not override a soft
     preference order;
   - a plugin result whose layer order differs from ascending physical tier is
     treated as an explicit priority order;
   - with one explicit order, filter that order by the intersected candidate
     set and preserve it; with multiple explicit orders, combine their layer
     indices using the maximum rank and a deterministic tier/name tie-breaker;
   - when no plugin supplies an explicit order, retain the current ascending
     physical-tier behavior.
   Candidate membership remains the exact set intersection.
4. Keep the existing SubJob order callback unchanged. After the first SubJob is
   placed under the preferred Job container, the second SubJob sees sibling
   tier domains in the same candidate layer; the current preferred affinity or
   anti-affinity score then chooses the appropriate sibling.

For Case 4, affinity at tier2 and anti-affinity at tier1 both derive tier2 as
the preferred Job container. The first SubJob selects one tier1 child; the
second same-policy SubJob sees both tier1 children and selects the sibling after
the occupied child receives the anti-affinity penalty. Case 6 follows the same
path inside the pair-b tier2 domain left by hard PodGroup anti-affinity.

This design also preserves soft fallback under a hard constraint from another
plugin. For example, if hard Job network topology leaves only a tier1 candidate,
the tier2 preferred candidate disappears during set intersection, while the
retained tier1 fallback remains schedulable. No plugin callback API or error
contract changes.

#### Alternatives rejected

- Removing finer HyperNodes from the group gradient would make a preferred rule
  appear as a hard plugin exclusion and could produce an empty intersection
  with hard network topology.
- Expanding a SubJob search beyond the Job HyperNode passed by allocate would
  violate the caller constraint and could escape hard constraints supplied by
  another plugin.
- Adding a new callback or moving SubGroup preferred logic into allocate is
  unnecessary and would enlarge the compatibility surface.

#### Proposed unit verification cases

**U16 — preferred Job container ordering with full fallback set**

- Purpose: prove that preferred SubGroup terms expose multiple peer domains
  without filtering any candidate.
- Scope: `pkg/scheduler/plugins/group-topology-affinity` unit test.
- Setup: tier1 children under two tier2 parents; Case 4-equivalent preferred
  affinity at tier2 and anti-affinity at tier1; at least two matching SubJobs.
- Command:

  ```bash
  go test -count=1 ./pkg/scheduler/plugins/group-topology-affinity \
    -run 'TestHyperNodeGradientForJobPreferredSubGroupContainer'
  ```

- Expected/pass: tier2 is the first plugin layer, and the union of all layers
  is exactly the original full candidate subtree.

**U17 — multi-plugin intersection preserves explicit layer priority**

- Purpose: prove that framework AND intersection retains membership and honors
  the latest layer requested by either plugin.
- Scope: `pkg/scheduler/framework` unit test.
- Setup: one plugin returns the normal ascending tier order; another returns an
  explicit tier2-first order while retaining tier1 as fallback.
- Command:

  ```bash
  go test -count=1 ./pkg/scheduler/framework \
    -run 'TestIntersectHyperNodeGradientsPreservesPluginLayerPriority'
  ```

- Expected/pass: the normal-order plugin does not erase the explicit order,
  tier2 candidates are in the first combined layer, every name present in both
  plugin sets remains present, and excluded-name diagnostics are unchanged. A
  second subcase with no explicit order retains ascending physical tiers.

**U18 — hard-plugin restriction keeps soft fallback schedulable**

- Purpose: ensure the new ordering never converts preferred SubGroup topology
  into a hard constraint.
- Scope: framework plus group plugin focused unit test.
- Setup: group plugin prefers a tier2 container but a second hard plugin exposes
  only one tier1 candidate.
- Command:

  ```bash
  go test -count=1 ./pkg/scheduler/framework \
    ./pkg/scheduler/plugins/group-topology-affinity
  ```

- Expected/pass: the tier1 intersection remains non-empty and the workload can
  use it as fallback.

**U19 — peer-aware SubGroup score remains plugin-owned**

- Purpose: retain direct proof that the second SubJob prefers/avoids the peer
  domain without allocate-specific logic.
- Scope: existing and extended group plugin order tests.
- Command:

  ```bash
  go test -count=1 ./pkg/scheduler/plugins/group-topology-affinity \
    -run 'TestHyperNodeOrderFnForSubGroupPreferredTerms'
  ```

- Expected/pass: preferred affinity ranks the peer domain higher; preferred
  anti-affinity ranks the sibling domain higher.

#### Proposed first-commit Kind verification

Build and deploy a scheduler from the amended first commit only, then rerun
Cases 1-6 sequentially using the existing four-node standard topology:

```bash
make vc-scheduler
docker build --platform linux/arm64 \
  -t volcanosh/vc-scheduler:dev-004-commit1-soft \
  -f ../dockerfile.scheduler.local .
kind load docker-image volcanosh/vc-scheduler:dev-004-commit1-soft --name kind
kubectl set image deployment/volcano-scheduler -n volcano-system \
  volcano-scheduler=volcanosh/vc-scheduler:dev-004-commit1-soft
kubectl rollout status deployment/volcano-scheduler \
  -n volcano-system --timeout=180s
```

Pass/fail criteria:

- Cases 1, 2, 3, and 5 retain their recorded hard/soft relationships.
- Case 4: all four Pods are Running in one tier2; the two `prefill` shards use
  distinct tier1 domains and the two `decode` shards use distinct tier1
  domains. Logs show the occupied tier1 receives the preferred anti-affinity
  penalty before the sibling is selected.
- Case 6: anchor remains in pair-a; combo remains in pair-b due to hard PodGroup
  anti-affinity; combo `prefill` and `decode` use different pair-b tier1 domains.
- The scheduler image contains only the amended first commit, with no
  `pkg/scheduler/actions/allocate/` diff from its base.
- All test resources are removed and the pre-test scheduler image is restored.

After the first-commit gate, rebase the joint-placement and documentation commits,
run the focused allocate/framework/plugin package tests and `make vc-scheduler`,
then confirm the final tree still passes the previously approved Bug 002/K7
joint-solution regressions. Commit hashes and verification records must be
updated after the history rewrite.

#### Implementation and first-commit verification record

The implementation was developed and verified in an isolated worktree before
the user approved the amend:

```text
worktree: /Users/vanderchen/workspace/dev/volcano-workspace/volcano-review-commit1
branch:   review/004-commit1-soft
base:     2110dad79 feat(scheduler): support SubGroup topology affinity (superseded)
result:   amended as 8bed34772 feat(scheduler): support SubGroup topology affinity
```

Modified review files:

```text
pkg/scheduler/framework/session_plugins.go
pkg/scheduler/framework/session_plugins_test.go
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go
pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity_test.go
```

There is no diff under `pkg/scheduler/actions/allocate/`. After approval, the
four-file correction was amended into the first commit and the later commits
were rebased without conflict.

Implementation details:

- the group plugin derives tier2 as the preferred Job container for the Case 4
  combination of tier2 affinity and tier1 anti-affinity;
- all candidates are retained, ordered as preferred container, coarser
  fallback, then finer fallback;
- framework set intersection remains AND, while a non-default plugin layer
  order is retained instead of being overwritten by ascending physical tier;
- ordinary hard/neutral gradients remain in the original physical-tier order;
- when a hard plugin leaves only a finer fallback, that candidate remains in
  the intersection.

Formatting and focused/package verification:

```bash
gofmt -w pkg/scheduler/framework/session_plugins.go \
  pkg/scheduler/framework/session_plugins_test.go \
  pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity.go \
  pkg/scheduler/plugins/group-topology-affinity/group_topology_affinity_test.go
git diff --check
go test -count=1 ./pkg/scheduler/framework \
  -run 'TestIntersectHyperNodeGradients|TestHyperNodeGradientForJobFn'
go test -count=1 ./pkg/scheduler/plugins/group-topology-affinity \
  -run 'TestHyperNodeGradientForJobPreferredSubGroupContainer|TestHyperNodeOrderFnForSubGroupPreferredTerms|TestHyperNodeGradientForPodGroupAntiAffinity|TestHyperNodeGradientForSubJobPreferredOnly'
go test -count=1 ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/webhooks/admission/podgroups/validate
go test -count=1 ./pkg/scheduler/actions/allocate
make vc-scheduler
```

All commands passed. The full allocate package completed in `40.078s`.

The uncommitted first-commit review tree was built as an arm64 image:

```text
image: volcanosh/vc-scheduler:dev-004-commit1-soft
ID:    sha256:9816b346b15045143a3dc0db3670b573691bdaa90470d443992c475d341ca4b7
```

It was loaded into all four Kind nodes and rolled out successfully. Cases 1-6
were then run sequentially with PodGroups created before Deployments:

```text
Case 1 hard PodGroup anti-affinity: PASS
  A -> kind-control-plane (pair-a)
  B -> kind-worker2       (pair-b)

Case 2 soft PodGroup anti-affinity: PASS
  A -> kind-control-plane (pair-a)
  B -> kind-worker2       (pair-b)

Case 3 hard SubGroup affinity/anti-affinity: PASS
  decode/d0  -> kind-control-plane
  decode/d1  -> kind-worker3
  prefill/p0 -> kind-worker2
  prefill/p1 -> kind-worker
  PodGroup Running, running=4, allocated=kind-tier3-all

Case 4 soft SubGroup affinity/anti-affinity: PASS
  decode/d0, prefill/p0 -> kind-control-plane
  decode/d1, prefill/p1 -> kind-worker
  PodGroup Running, running=4, allocated=kind-tier2-pair-a

Case 5 network + group topology plugins: PASS
  decode/d0, prefill/p0 -> kind-control-plane
  decode/d1, prefill/p1 -> kind-worker
  PodGroup Running, running=4, allocated=kind-tier2-pair-a

Case 6 PodGroup + SubGroup composition: PASS
  anchor replicas -> kind-control-plane (pair-a)
  combo decode     -> kind-worker2       (pair-b)
  combo prefill    -> kind-worker3       (pair-b)
  combo PodGroup Running, running=2, allocated=kind-tier2-pair-b
```

Case 4 log evidence:

```text
preferredContainerTier=2, tierOrder=[2 3 4 1]
decode peer candidates: control-plane=0, worker=1000
Allocate job to hyperNode success ... hyperNode=kind-tier2-pair-a
```

Case 6 log evidence:

```text
kind-tier2-pair-a rejected: reason=conflictWithMatchingPodGroup
preferredContainerTier=2, tierOrder=[2 1]
prefill peer candidates: worker2=0, worker3=1000
Allocate job to hyperNode success ... hyperNode=kind-tier2-pair-b
```

All workload resources were deleted after verification. The default namespace
is empty, and the scheduler was restored to
`volcanosh/vc-scheduler:dev-004-codegen` with one Ready Pod and zero restarts.

#### Amend, rebase, and final-tree verification record

After explicit user approval, a safety checkpoint was created at the old tip:

```text
backup/dev-podgroup-antiaffinity-before-case4-amend-20260716
```

The correction was amended and the two later commits were rebased. The current
chain is:

```text
8bed34772 feat(scheduler): support SubGroup topology affinity
4312e89e0 fix(scheduler): search SubJob topology placements jointly
dbb51b2ca docs: document HyperNode topology-affinity scheduling
```

Boundary checks passed:

- `8bed34772` has no change under `pkg/scheduler/actions/allocate/`;
- `4312e89e0` retains the seven-file joint-placement scope, including the
  allocate transaction/search and soft network-topology solution score;
- compared with the pre-amend final tree, the new final tree differs only in
  the four reviewed framework/group-plugin files; and
- `git diff --check` passes. The existing untracked
  `example/topology-affinity/` directory was not added or modified.

Final-tree commands:

```bash
go test -count=1 ./pkg/scheduler/actions/allocate \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/api \
  ./pkg/webhooks/admission/podgroups/validate
make vc-scheduler
```

All commands passed; the full allocate package completed in `44.280s`.

The final scheduler was packaged as
`volcanosh/vc-scheduler:dev-004-case4-amended`
(`sha256:3c9bdaafa7e63e5afae9b07a982b6a73aea1865b2dd4a5dd3ca5f7cff06948e3`),
loaded into the four-node arm64 Kind cluster, and used for the approved K7-R
`weight: 100` regression. K7-R passed:

```text
bug004-k7-soft-100-role-0-a  kind-control-plane  Running
bug004-k7-soft-100-role-0-b  kind-worker2        Running
PodGroup Running, running=2, allocated=kind-tier3-all
```

The scheduler log recorded `preferredContainerTier=2`, committed the Job to
`kind-tier3-all`, and recorded the two SubJobs on
`kind-tier1-control-plane` and `kind-tier1-worker2`. This confirms that the
first-commit plugin-owned container ordering and the second-commit global
solution comparison compose correctly on the final tree.

The K7-R Deployments, Pods, PodGroup, and temporary node labels were removed.
The default namespace is empty. The scheduler was restored to
`volcanosh/vc-scheduler:dev-004-codegen` with one Ready Pod and zero restarts.

### 2026-07-16 reviewer follow-up: split preferred scoring from `hyperNodeOrderFn`

Status: **approved by the user for implementation and amendment into the first
feature commit.**

The reviewer observed that preferred PodGroup anti-affinity, SubGroup affinity,
and SubGroup anti-affinity all repeat candidate traversal, weighted score
deduction, and zero-floor handling inside `hyperNodeOrderFn`. The refactor will
keep the three rule-specific preparations separate because their term types,
tier resolvers, occupancy sources, and conflict predicates differ:

- PodGroup anti-affinity resolves matching PodGroups and penalizes their
  occupied ancestor domain;
- SubGroup affinity penalizes a candidate unless exactly one peer domain
  matches it; and
- SubGroup anti-affinity penalizes a candidate when its ancestor conflicts
  with a peer domain.

Each rule family will move into a named scoring function. The first refactor
draft also introduced a callback-based shared penalty helper, but the user
correctly identified that it split a simple candidate loop and its log context
across multiple closures. The final implementation therefore keeps the full
candidate predicate, deduction, clamp, and log sequence directly inside each
named rule function. `hyperNodeOrderFn` retains only orchestration, score
initialization, final plugin-weight scaling, and summary logging. No callback,
API, candidate membership, scoring order, weight calculation, or error behavior
changes.

Verification reuses the already-approved behavioral cases:

- `TestHyperNodeOrderFn` covers no preferred rule, PodGroup preferred
  anti-affinity, invalid weight/tier, and plugin-weight scaling;
- `TestHyperNodeOrderFnForSubGroupPreferredTerms` covers SubGroup preferred
  affinity and anti-affinity scores;
- the full `group-topology-affinity`, framework, and allocate packages must
  pass, followed by `make vc-scheduler`; and
- a scheduler built from the amended first commit alone must pass Kind Case 2
  (PodGroup soft anti-affinity) and Case 4 (SubGroup soft
  affinity/anti-affinity). The scheduler and resources must then be restored
  and cleaned.

#### Implementation and verification record

Implemented in `group_topology_affinity.go`:

- `hyperNodeOrderFn` now initializes and finalizes scores while delegating the
  three rule families;
- `scorePreferredPodGroupAntiAffinityTerms`,
  `scorePreferredSubGroupAffinityTerms`, and
  `scorePreferredSubGroupAntiAffinityTerms` retain the rule-specific term
  resolution, occupancy, candidate loop, conflict predicate, weighted
  deduction, zero-floor handling, logging, and error behavior.

There is intentionally no shared callback or generic term-scoring helper. The
small amount of repeated arithmetic remains local so each rule reads as one
linear operation.

The change was verified on the first-commit-only worktree:

```bash
go test -count=1 ./pkg/scheduler/plugins/group-topology-affinity \
  -run 'TestHyperNodeOrderFn|TestHyperNodeOrderFnForSubGroupPreferredTerms'
go test -count=1 ./pkg/scheduler/plugins/group-topology-affinity \
  ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/webhooks/admission/podgroups/validate \
  ./pkg/scheduler/actions/allocate
make vc-scheduler
```

The focused and related plugin/framework/admission tests passed. One combined
run produced a transient failure in
`TestAllocateWithPVC/static_pv_matched_and_node_with_enough_resources`; that
case passed immediately in isolation, and the complete allocate package then
passed in `38.659s`. `make vc-scheduler` passed. The first-only arm64 image was
`volcanosh/vc-scheduler:dev-004-order-simplify`
(`sha256:a7a3a613e88365dc0765b20680bb12442e05e3cbbefc6e246743b3ca5e17b8ff`).

Kind results:

```text
Case 2 PodGroup preferred anti-affinity: PASS
  ta-deploy-pg-soft-a -> kind-control-plane (pair-a)
  ta-deploy-pg-soft-b -> kind-worker3       (pair-b)

Case 4 SubGroup preferred affinity/anti-affinity: PASS
  decode/d0, prefill/p0 -> kind-control-plane
  decode/d1, prefill/p1 -> kind-worker
  PodGroup Running, running=4, allocated=kind-tier2-pair-a
```

The correction was amended into the first commit and the later commits were
rebased. The current chain is:

```text
8fcc0012c feat(scheduler): support SubGroup topology affinity
edaaf83a7 fix(scheduler): search SubJob topology placements jointly
c49caabf9 docs: document HyperNode topology-affinity scheduling
47e691146 docs: add topology affinity examples
```

`8fcc0012c` has no allocate-action diff. Compared with the callback-helper final
tree, the final source differs only in `group_topology_affinity.go`. Final-tree
related package tests passed, including allocate in `46.471s`, and
`make vc-scheduler` passed. Safety checkpoint
`backup/dev-podgroup-antiaffinity-before-order-simplify-20260716` retains the
callback-helper chain; the earlier pre-refactor checkpoint is also retained.

All Case 2/4 resources were deleted. The default namespace is empty and the
scheduler was restored to `volcanosh/vc-scheduler:dev-004-codegen` with one
Ready Pod and zero restarts. The existing untracked
`example/topology-affinity/` directory was not added or modified.

### 2026-07-16 topology-affinity examples commit

At user request, the previously local example directory was reduced to only a
README and the six verified case groups, then committed separately:

```text
47e691146 docs: add topology affinity examples
```

The commit contains 16 files: one README and 15 YAML files under `cases/`.
Local-only scheduler ConfigMap, HyperNode topology, admission-negative, and
patch artifacts were excluded. The README was rewritten so it does not refer
to those excluded files or to an older released image without this feature.

Validation:

```bash
git diff --check HEAD^
kubectl apply --dry-run=client -f example/topology-affinity/cases
```

All 15 YAML files passed client-side dry-run against the installed API. The
README links resolve to the repository topology-affinity and network-topology
user guides. No scheduler implementation or API file changed, so no additional
code generation or Kind deployment was required for this examples-only commit;
the six cases themselves had already passed the staged Kind verification
recorded above.

### 2026-07-17 commit-1-only Case 1–6 Kind verification

The complete examples suite was rerun using a detached worktree at exactly:

```text
8fcc0012c feat(scheduler): support SubGroup topology affinity
```

The scheduler was built from that worktree, packaged as
`volcanosh/vc-scheduler:dev-004-commit1-cases`
(`sha256:6fa01d991d1db895b60b449a8404162ec91de3dd23de13b35cccb89374b45b88`),
loaded into all four arm64 Kind nodes, and deployed successfully. The case YAML
files were taken from the later examples-only commit, but no scheduler code
after commit 1 was included in the image.

Each case began with an empty `default` namespace, created PodGroups before
Deployments, waited for all Deployments and PodGroups to reach `Running`, and
was deleted before the next case.

```text
Case 1 hard PodGroup anti-affinity: PASS
  A -> kind-control-plane (pair-a)
  B -> kind-worker2       (pair-b)

Case 2 soft PodGroup anti-affinity: PASS
  A -> kind-control-plane (pair-a)
  B -> kind-worker2       (pair-b)

Case 3 hard SubGroup affinity/anti-affinity: PASS
  decode/d0   -> kind-control-plane
  decode/d1   -> kind-worker3
  prefill/p0  -> kind-worker
  prefill/p1  -> kind-worker2
  PodGroup Running, running=4, allocated=kind-tier3-all

Case 4 soft SubGroup affinity/anti-affinity: PASS
  decode/d0, prefill/p0 -> kind-control-plane
  decode/d1, prefill/p1 -> kind-worker
  PodGroup Running, running=4, allocated=kind-tier2-pair-a
  log: preferredContainerTier=2, tierOrder=[2 3 4 1]

Case 5 network + group topology plugins: PASS
  decode/d0, prefill/p0 -> kind-control-plane
  decode/d1, prefill/p1 -> kind-worker
  PodGroup Running, running=4, allocated=kind-tier2-pair-a

Case 6 PodGroup + SubGroup composition: PASS
  anchor replicas -> kind-control-plane (pair-a)
  combo decode   -> kind-worker2       (pair-b)
  combo prefill  -> kind-worker3       (pair-b)
  combo PodGroup Running, running=2, allocated=kind-tier2-pair-b
```

Case 6 logs recorded the expected hard rejection of `kind-tier2-pair-a` with
`reason=conflictWithMatchingPodGroup`, followed by
`preferredContainerTier=2, tierOrder=[2 1]` for the combo SubGroups.

All Case 1–6 Deployments, Pods, and PodGroups were deleted. The scheduler was
restored to `volcanosh/vc-scheduler:dev-004-codegen`; it is Ready with zero
restarts, and `default` contains no remaining test resources.
