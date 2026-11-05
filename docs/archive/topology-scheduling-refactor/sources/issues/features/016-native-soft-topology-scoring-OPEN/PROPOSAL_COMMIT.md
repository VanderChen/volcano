> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Feature 016 Proposal and Verification Plan

## Status

- Stage: proposal complete, awaiting explicit approval
- Task state: `OPEN`
- Implementation: not started
- Upstream base: `volcano-sh/volcano master`
- Base commit: `a4b775ad2c2d456ca85233f35f305e16707126b3`
- Working branch: `feat/016-native-soft-topology-scoring`
- Related community issue: https://github.com/volcano-sh/volcano/issues/5347

No implementation code may be changed until this proposal and its verification
cases are explicitly approved.

## 1. Problem statement

The upstream scheduler currently converts every Job/SubJob
`networkTopology.mode: soft` into `hard` during Session initialization. It gives
the converted object a cluster-wide tier and then reuses the hard HyperNode
gradient path.

That implementation preserves fallback capacity, but it changes the scheduler's
type system:

```text
required/hard constraint  -> candidate filtering
preferred/soft constraint -> candidate filtering order
```

The second line should instead be:

```text
preferred/soft constraint -> plugin score -> configured weight aggregation
```

The current allocate flow visits HyperNode gradients from the lowest tier upward
and returns after the first layer with a feasible solution. HyperNode/Node scores
are therefore evaluated only after an earlier topology-order decision has already
removed later candidates. Another plugin's weight cannot make a later but
otherwise hard-feasible placement win.

The conversion also erases the declared mode, overwrites soft tier fields, and
makes it difficult to compose future required and preferred topology plugins.

## 2. Delivery split

This work is deliberately split before group topology affinity is introduced.

### PR 1: native soft topology scoring (this proposal)

- preserve the declared hard/soft mode;
- let only hard NTA rules restrict candidates;
- evaluate soft NTA as a bounded plugin score at Node selection time;
- aggregate that score with existing Node scoring plugins;
- preserve actual Job/SubJob topology anchors through dry-run attempts;
- retain current greedy SubJob order and one selected solution per SubJob.

### PR 2: bounded joint placement search (follow-up)

- retain ordered alternative placements for each SubJob;
- use `Statement` rollback when a later SubJob reaches a dead end;
- retry an earlier SubJob's next candidate within a bounded attempt budget;
- use existing multi-SubJob NTA as the reproducer and justification;
- introduce no topology-affinity API/plugin.

### Future: group topology affinity

The group topology affinity work in #5347 will consume hard filtering, soft
scoring, and bounded placement search after they have independently landed and
been validated with existing NTA behavior.

PR 1 does not claim to solve the existing multi-SubJob joint-search false
negative. Separating the changes keeps the score semantics reviewable without
hiding a scheduler-control-flow rewrite in the same PR.

## 3. Goals

1. Preserve Job/SubJob `soft` mode for the entire scheduler Session.
2. Preserve the user-provided scheduler-internal tier value; do not replace it
   with the cluster-top tier.
3. Keep hard topology as a candidate constraint and soft topology as a score.
4. Compare soft topology scores with other enabled plugin scores over the same
   hard-feasible Node candidates for each placement decision.
5. Make `network-topology-aware.arguments.weight` observably capable of changing
   the selected Node when another scoring plugin prefers a different Node.
6. Prefer a lower resulting LCA tier when soft NTA has an established anchor,
   while retaining wider-domain fallback candidates.
7. Support Job-level soft, SubJob-level soft, and mixed hard/soft configurations.
8. Preserve actual Job/SubJob LCA anchors across dry-runs and scheduling cycles;
   never persist the neutral cluster-root search container as a soft placement.
9. Preserve existing hard NTA allocation and eviction constraints.
10. Make no API/CRD change.

## 4. Non-goals

- No unified planner or replacement of the whole `allocate` action.
- No retry of an earlier SubJob choice after a later SubJob fails.
- No exhaustive or globally optimal multi-pod placement search.
- No `topologyAffinity` API or `group-topology-affinity` plugin.
- No PodGroup/SubGroup occupancy index.
- No multi-plugin hard-gradient intersection contract in this PR.
- No `Applied/Abstain` callback signature change unless implementation proves it
  is strictly required for native NTA soft scoring. It is expected to be handled
  with the hard-composition/planner foundation before #5347.
- No preempt/reclaim joint optimization. Soft topology must not filter victims;
  adding a soft victim score is separate work.
- No new scheduler configuration key or feature gate.
- No change to the documented meaning of `highestTierAllowed` or
  `highestTierName`: those fields define hard boundaries; soft mode does not turn
  them into a hidden hard constraint.

## 5. Current upstream behavior

### 5.1 Session mutation

`adjustNetworkTopologySpec` currently:

1. translates `highestTierName` into `highestTierAllowed`;
2. converts all soft Job and SubJob copies to hard;
3. replaces Job soft tier with the cluster-top tier;
4. replaces SubJob soft tier with the effective Job/cluster tier.

After this point, plugins cannot observe the user's soft intent.

### 5.2 Gradient-first allocation

NTA registers Job and SubJob gradient callbacks. A hard rule returns lower-tier
to higher-tier BFS layers. A non-hard rule returns only the caller's input
HyperNode. Because conversion runs first, upstream soft NTA is normally observed
as hard and follows BFS layers.

`allocateForJob` and `allocateForSubJob` stop at the first gradient containing a
feasible solution. Weight aggregation occurs only inside that layer.

### 5.3 Existing score path

NTA already registers `BatchNodeOrderFn`. `PrioritizeNodes` adds its result to
NodeOrder map/reduce scores and other batch scores. The integration point needed
for plugin weight reconciliation therefore already exists; the problem is that
soft candidates are narrowed before they reach it.

### 5.4 Soft placement state

`allocateResourcesForTasks` incrementally derives an allocated HyperNode LCA for
network-aware tasks. However, after native soft mode is preserved, using a neutral
root HyperNode as the search container must not overwrite that actual LCA.

Dry-run attempts also mutate Job/SubJob anchors. The current recorder snapshots
only part of this state, so native soft scoring needs explicit capture, restore,
and selected-state recovery even before PR 2 introduces alternative search.

## 6. Proposed semantics

### 6.1 Required versus preferred

```text
Job/SubJob hard topology
        -> restrict the Node universe through the existing HyperNode path

Job/SubJob soft topology
        -> keep every hard-feasible Node
        -> calculate NTA score
        -> add it to existing plugin scores
        -> choose the highest aggregate score
```

Hard constraints are never overridden by weight. Soft preferences never remove a
hard-feasible Node.

### 6.2 Decision scope

PR 1 guarantees score composition for every individual Node selection made by
the existing allocator. It does not claim to compare every possible complete
multi-SubJob layout.

This distinction is intentional:

```text
PR 1: Which Node should the current task try first?
PR 2: If that choice blocks a later SubJob, which earlier alternative is next?
```

### 6.3 Soft tier behavior

The existing documentation defines `highestTierAllowed` and `highestTierName` as
hard-mode boundary fields. Therefore:

- soft mode requires neither field;
- no default value is written into `HighestTierAllowed`;
- the first successful soft placement establishes the initial anchor without a
  location bias from NTA;
- later placements receive a higher NTA score when their resulting LCA is at a
  lower/closer tier;
- every hard-feasible wider-domain Node remains a fallback;
- if a soft object contains a tier field, the scheduler preserves the internal
  value but does not use it as a hard filter in PR 1.

This retains the user-facing meaning “same HyperNode as much as possible” without
creating an implicit boundary.

### 6.4 Mixed-mode matrix

| Job topology | SubJob topology | Candidate filtering | Soft scoring |
| --- | --- | --- | --- |
| none | soft | none beyond normal predicates | SubJob resulting LCA |
| soft | none | none beyond normal predicates | Job resulting LCA |
| soft | soft | none beyond normal predicates | normalized Job + SubJob preference |
| hard | soft | Job hard HyperNode boundary | SubJob preference inside Job boundary |
| soft | hard | SubJob hard HyperNode boundary | Job preference inside SubJob boundary |
| hard | hard | existing Job and SubJob hard behavior | existing hard-path ordering unchanged |

“None beyond normal predicates” still includes resource, node affinity, taints,
DRA, queue, shard, and every existing non-topology feasibility check.

## 7. Proposed design

### 7.1 Preserve mode during Session initialization

Change `adjustNetworkTopologySpec` so it only translates tier names in the
scheduler-owned copies.

Remove:

- `convertSoftToHardTopology`;
- `SubJobInfo.ConvertToHardTopology`;
- conversion-specific tests and comments.

Add tests proving:

- Job soft remains soft with and without a tier field;
- SubJob soft remains soft with and without a tier field;
- hard mode and tier translation remain unchanged;
- original PodGroup/API objects remain unmodified.

### 7.2 Keep soft out of BFS restriction

For NTA gradient callbacks:

- hard mode continues to call `hyperNodeGradientFn`;
- soft/non-hard mode contributes only the caller's neutral candidate universe;
- no soft rule creates ascending BFS preference layers;
- eviction/preemption searches receive no soft topology restriction.

The current callback API returns the neutral input HyperNode for non-hard mode.
PR 1 may retain that representation because upstream has one topology-gradient
plugin. The representation must not be interpreted as the actual soft placement
anchor and must not be persisted.

Multi-plugin `Applied/Abstain` and hard-gradient intersection remain separate
work required before adding `group-topology-affinity`.

### 7.3 Score soft topology on physical Node candidates

Reuse the existing `BatchNodeOrderFn`/`PrioritizeNodes` aggregation path. Do not
introduce a second score accumulator in `allocate`.

For candidate Node `n` with leaf HyperNode `leaf(n)`, calculate the hypothetical
resulting LCA for every active soft scope with a non-empty anchor:

```text
newScopeAnchor = LCA(currentScopeAnchor, leaf(n))
scopeScore      = scoreHyperNodeWithTier(newScopeAnchor.tier)  // [0, 1]
```

Active components are:

- Job soft proximity when `job.AllocatedHyperNode` is established;
- SubJob soft proximity when the in-progress SubJob anchor is established;
- the existing task-density preference, if retained for soft NTA.

Combine active NTA components with a normalized average, not a lexicographic
rank and not an unbounded sum:

```text
rawSoftScore   = sum(activeComponentScores) / activeComponentCount
finalNTAScore  = MaxNodeScore * networkTopologyAware.weight * rawSoftScore
```

Requirements:

- `rawSoftScore` remains in `[0, 1]`;
- `finalNTAScore` is scaled exactly once;
- Job and SubJob soft components have equal internal weight because the existing
  API exposes no separate Job/SubJob soft weights;
- when no soft anchor exists, NTA contributes zero and other plugins choose the
  first anchor;
- no separate `softDomainRank` or preferred-candidate filtering occurs before
  `PrioritizeNodes`.

Existing hard NTA HyperNode binpack behavior is not redesigned in this PR.

### 7.4 Apply Job-level soft scoring to explicit SubJobs

Today NTA chooses the network-aware score path primarily from
`subJob.WithNetworkTopology()`. For a Job-level soft policy, explicit SubJobs may
not carry their own topology rule, but their Nodes still contribute to the Job's
soft placement extent.

The NTA scoring decision must therefore consider both scopes:

```text
job has native soft topology OR subJob has native soft topology
```

This makes Job soft semantics apply across explicit SubGroups without inventing
a SubJob hard constraint.

### 7.5 Preserve actual topology anchor state

Introduce a small allocate-internal value snapshot for placement metadata, not a
new API type or planner SPI:

```text
job.AllocatedHyperNode
subJob.AllocatedHyperNode for affected SubJobs
```

Rules:

1. Capture anchors before every Job/SubJob dry-run candidate.
2. During a successful task allocation, update the in-progress soft scope anchor
   from the actual selected Node leaf/LCA.
3. After a discarded candidate, restore the captured anchors exactly.
4. Store the resulting anchor snapshot next to every successful dry-run
   `Statement` backup.
5. When the current greedy winner is recovered, recover its matching anchor
   snapshot as well.
6. Persist the actual selected LCA through the recorder/PodGroup annotations.
7. Never combine a soft placement anchor with `ClusterTopHyperNode` merely
   because the cluster root was used as the neutral search container.
8. Ensure failed `Statement` recovery or discard leaves no anchor state behind.

This snapshot is intentionally reusable by PR 2, but PR 1 keeps only one selected
alternative and performs no recursive search.

### 7.6 Preserve existing routing

Do not add a new scheduler action or planner route.

- A default soft Job without SubGroupPolicy continues through normal task/Node
  allocation and receives NTA BatchNodeOrder scores.
- Jobs with SubGroupPolicy continue through `allocateForJob` for SubGroup gang
  semantics; soft-only scopes use the neutral physical Node universe.
- Any real hard Job/SubJob topology continues through the existing HyperNode
  allocation path.

The proposal may add a narrowly named helper describing “has native soft
topology” if it reduces repeated mode checks, but it must not conflate that with
“contains hard topology”.

### 7.7 Action compatibility audit

Removing Session conversion affects every action observing the mode. The
implementation review must audit:

- allocate: native soft score, hard filter unchanged;
- backfill: soft Node scoring remains non-filtering;
- preempt/reclaim/gang-aware eviction: soft topology does not restrict candidate
  domains; hard topology keeps its existing gradient behavior;
- Session reconstruction: running task placement still reconstructs Job/SubJob
  anchors from actual Nodes;
- nomination: a nominated Node is accepted only when it remains within the real
  hard candidate set; soft preference cannot invalidate it.

Soft preference for victim ordering is not introduced.

## 8. Expected code scope

| Area | Expected change |
| --- | --- |
| `pkg/scheduler/framework/session.go` | remove conversion; retain tier-name translation |
| `pkg/scheduler/framework/session_test.go` | replace conversion expectations with mode-preservation matrix |
| `pkg/scheduler/api/sub_job_info.go` | remove `ConvertToHardTopology` |
| `pkg/scheduler/api/sub_job_info_test.go` | assert native soft copies and tier preservation |
| `pkg/scheduler/plugins/network-topology-aware/` | native soft Node score using Job/SubJob anchors; keep hard gradient |
| `pkg/scheduler/actions/allocate/allocate.go` | do not persist neutral root; capture/recover actual soft anchors |
| `pkg/scheduler/actions/allocate/recorder.go` | persist/restore selected Job and SubJob anchor state |
| allocate/recorder tests | weight arbitration, mixed modes, rollback isolation, actual LCA persistence |
| NTA design/user documentation | explain hard filter, soft weighted preference, and fallback |

No staging API, generated client, CRD, controller, or webhook file is expected to
change. If implementation discovers that an API change is required, stop and
return to proposal review before editing API sources.

## 9. Compatibility and risk analysis

### 9.1 Intended behavior change

For soft topology, a lower-tier candidate no longer wins merely because it was
visited in an earlier BFS layer. It wins only when the weighted aggregate score
selects it.

Changing NTA or another plugin's weight may therefore change placement. That is
the intended restoration of plugin configuration semantics.

### 9.2 Hard topology

Hard candidate boundaries, resource feasibility, and current first-feasible hard
gradient behavior remain unchanged in PR 1. A later planner PR may change search
completeness, but not this PR.

### 9.3 Scheduling completeness

PR 1 remains greedy. It can still choose a high-scoring placement for SubJob A
that makes SubJob B fail even though an alternate placement for A would allow the
whole Job. This is explicitly accepted only until PR 2 and must have a separate
reproducer/issue.

### 9.4 Performance

Soft-only scheduling avoids repeated tier-by-tier HyperNode dry-runs and uses the
existing parallel Node scoring path. The added work is:

- at most a small constant number of LCA lookups per candidate Node;
- small Job/SubJob anchor snapshots around existing dry-runs.

No unbounded candidate enumeration or backtracking is added.

### 9.5 Score calibration

The main scoring risk is accidentally double-scaling NTA or allowing an internal
component sum above the plugin's advertised weight range. Unit tests must assert
the score bounds and exact winner changes at two weight configurations.

### 9.6 State leakage

The main correctness risk is a rejected dry-run leaving a Job/SubJob anchor that
affects the next candidate. Snapshot/restore tests must cover success, discard,
failed recovery, and multiple outer candidates.

## 10. Verification cases

The following cases are authoritative. Implementation is not complete until all
cases pass. Any change to these cases requires explanation and approval.

### U1: Session preserves native topology modes

- Purpose: prove Session initialization no longer converts soft to hard.
- Scope: unit tests in `pkg/scheduler/framework` and `pkg/scheduler/api`.
- Setup: table of Job/SubJob none, soft, hard, soft with tier name, soft with
  numerical tier, and mixed modes.
- Commands:

  ```bash
  go test ./pkg/scheduler/framework -run 'Test.*AdjustNetworkTopologySpec.*' -count=1
  go test ./pkg/scheduler/api -run 'Test.*SoftTopology.*Preserved|Test.*HardTopology' -count=1
  ```

- Expected result: soft mode and provided internal tier survive; hard mode and
  translation remain correct; original PodGroup spec is not mutated.
- Pass/fail: every table row matches exactly and no conversion helper remains.

### U2: Hard gradients and soft non-filtering

- Purpose: prove only hard NTA restricts HyperNode candidates.
- Scope: NTA unit tests.
- Setup: identical HyperNode tree tested with Job/SubJob hard and soft modes.
- Command:

  ```bash
  go test ./pkg/scheduler/plugins/network-topology-aware \
    -run 'Test.*(Hard|Soft).*Gradient|Test.*Soft.*NonFiltering' -count=1
  ```

- Expected result: hard respects the configured tier; soft exposes the neutral
  full hard-feasible universe and never creates low-tier-first terminal layers.
- Pass/fail: no hard boundary regression and no soft candidate removal.

### U3: NTA soft score is normalized and weight is effective

- Purpose: prove plugin weight can arbitrate a conflict with another Node score.
- Scope: NTA and allocate unit tests.
- Setup:
  - two tier-1 HyperNodes under a common parent;
  - an existing soft anchor in HyperNode A;
  - candidate A is topologically closer;
  - a fake scoring plugin prefers candidate B;
  - run once with NTA weight dominating and once with the fake plugin dominating.
- Commands:

  ```bash
  go test ./pkg/scheduler/plugins/network-topology-aware \
    -run 'Test.*Soft.*Score|Test.*Soft.*ScoreBounds' -count=1
  go test ./pkg/scheduler/actions/allocate \
    -run 'Test.*SoftTopology.*WeightArbitration' -count=1
  ```

- Expected result:
  - raw NTA soft scores are within `[0,1]`;
  - scaled scores are within `MaxNodeScore * NTAWeight`;
  - high NTA weight selects A;
  - high competing weight selects B;
  - both Nodes remain feasible in both runs.
- Pass/fail: changing only weights flips the winner exactly as expected.

### U4: Job/SubJob soft anchor composition

- Purpose: prove soft scoring works at both hierarchy levels.
- Scope: NTA and allocate unit tests.
- Setup: mode matrix from section 6.4, including explicit SubJobs that have no
  SubJob topology while the Job is soft.
- Command:

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'Test.*NativeSoftTopology.*ModeMatrix|Test.*SoftTopology.*AcrossSubJobs' -count=1
  ```

- Expected result: hard scopes restrict candidates; Job/SubJob soft scopes only
  affect scores; Job soft applies across explicit SubJobs.
- Pass/fail: all matrix rows match section 6.4.

### U5: Dry-run anchor state is isolated and actual LCA is persisted

- Purpose: prevent neutral-root persistence and state leakage.
- Scope: allocate and recorder unit tests.
- Setup: at least two dry-run candidates where the first is discarded and the
  second selected; include failed recovery and already-running anchor cases.
- Command:

  ```bash
  go test ./pkg/scheduler/actions/allocate \
    -run 'Test.*TopologyAnchor.*(Snapshot|Restore|Persist)|Test.*SoftTopology.*DryRunIsolation' -count=1
  ```

- Expected result: discarded attempts restore original anchors; the winner
  restores its actual Node-derived Job/SubJob LCA; `ClusterTopHyperNode` is not
  persisted solely because it was the neutral search root.
- Pass/fail: anchor state is exact before and after every transition.

### U6: Existing hard topology regression

- Purpose: prove Phase 1 does not change hard NTA behavior.
- Scope: focused scheduler packages followed by the repository unit gate.
- Commands:

  ```bash
  go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/framework \
    ./pkg/scheduler/plugins/network-topology-aware -count=1
  make unit-test
  make verify
  ```

- Expected result: all existing hard/mixed topology tests pass.
- Pass/fail: every command exits zero.

### K1: Soft preference chooses the closer tier

- Purpose: prove native soft placement without a tier field prefers a lower LCA.
- Scope: manual Kind workload.
- Setup:
  - cluster `volcano-dev-016`, one control-plane plus four workers;
  - explicit tier-1 HyperNode per worker, two tier-2 pairs, one tier-3 root;
  - NTA enabled with a dominant weight;
  - soft PodGroup with non-zero CPU requests and enough capacity in one lower
    topology domain.
- Planned artifacts:
  - `kind-4node.yaml`
  - `hypernodes.yaml`
  - `scheduler-config-high-nta.yaml`
  - `k1-soft-preferred-podgroup.yaml`
  - `k1-soft-preferred-deployment.yaml`
- Commands:

  ```bash
  kind create cluster --name volcano-dev-016 \
    --config ../issues/features/016-native-soft-topology-scoring-OPEN/kind-4node.yaml
  make vc-scheduler
  TAG=dev-016 BUILDX_OUTPUT_TYPE=docker make vc-scheduler-image
  kind load docker-image volcanosh/vc-scheduler:dev-016 --name volcano-dev-016
  helm upgrade --install volcano installer/helm/chart/volcano \
    --namespace volcano-system --create-namespace \
    --set basic.scheduler_image_tag_version=dev-016 \
    --set basic.image_pull_policy=IfNotPresent \
    --set-file custom.scheduler_config_override=../issues/features/016-native-soft-topology-scoring-OPEN/scheduler-config-high-nta.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/hypernodes.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k1-soft-preferred-podgroup.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k1-soft-preferred-deployment.yaml
  kubectl get pods -o wide
  kubectl -n volcano-system logs deployment/volcano-scheduler --since=10m
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k1-soft-preferred-deployment.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k1-soft-preferred-podgroup.yaml
  ```

- Expected result: the workload becomes Ready/Running and its resulting LCA is
  the lowest feasible tier; scheduler logs contain no soft-to-hard conversion.
- Pass/fail: all required Pods run and placement matches the expected tier.

### K2: Soft preference falls back without filtering

- Purpose: prove native soft never blocks an otherwise feasible wider placement.
- Scope: manual Kind workload on the same cluster.
- Setup: fillers make every preferred lower domain too small for the whole soft
  workload while aggregate hard-feasible capacity remains sufficient.
- Planned artifacts:
  - `k2-fillers.yaml`
  - `k2-soft-fallback-podgroup.yaml`
  - `k2-soft-fallback-deployment.yaml`
- Commands:

  ```bash
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k2-fillers.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k2-soft-fallback-podgroup.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k2-soft-fallback-deployment.yaml
  kubectl get pods -o wide
  kubectl -n volcano-system logs deployment/volcano-scheduler --since=10m
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k2-soft-fallback-deployment.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k2-soft-fallback-podgroup.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k2-fillers.yaml
  ```

- Expected result: the Job becomes Ready/Running across a wider LCA rather than
  remaining Pending.
- Pass/fail: required Pods run and the observed LCA is wider than K1.

### K3: Configured plugin weights flip a real placement

- Purpose: prove weight reconciliation outside a fake unit plugin.
- Scope: manual Kind workload using NTA and binpack.
- Setup:
  - an already-running anchor Pod in tier-1 domain A;
  - its Node cannot fit the target, but a peer Node in A can;
  - filler utilization makes binpack prefer a Node in remote domain B;
  - target Pod belongs to the same soft PodGroup;
  - run isolated copies under two scheduler configurations.
- Planned artifacts:
  - `scheduler-config-high-binpack.yaml`
  - `k3-anchor-podgroup.yaml`
  - `k3-anchor-deployment.yaml`
  - `k3-binpack-fillers.yaml`
  - `k3-target-deployment.yaml`
- Commands:

  ```bash
  helm upgrade volcano installer/helm/chart/volcano \
    --namespace volcano-system \
    --set basic.scheduler_image_tag_version=dev-016 \
    --set basic.image_pull_policy=IfNotPresent \
    --set-file custom.scheduler_config_override=../issues/features/016-native-soft-topology-scoring-OPEN/scheduler-config-high-binpack.yaml
  kubectl -n volcano-system rollout status deployment/volcano-scheduler
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-podgroup.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-deployment.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-binpack-fillers.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-target-deployment.yaml
  kubectl get pods -o wide
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-target-deployment.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-binpack-fillers.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-deployment.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-podgroup.yaml
  helm upgrade volcano installer/helm/chart/volcano \
    --namespace volcano-system \
    --set basic.scheduler_image_tag_version=dev-016 \
    --set basic.image_pull_policy=IfNotPresent \
    --set-file custom.scheduler_config_override=../issues/features/016-native-soft-topology-scoring-OPEN/scheduler-config-high-nta.yaml
  kubectl -n volcano-system rollout status deployment/volcano-scheduler
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-podgroup.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-deployment.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-binpack-fillers.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-target-deployment.yaml
  kubectl get pods -o wide
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-target-deployment.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-binpack-fillers.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-deployment.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k3-anchor-podgroup.yaml
  ```

- Expected result:
  - high-binpack/low-NTA configuration selects domain B;
  - after cleanup and rerun with `scheduler-config-high-nta.yaml`, the equivalent
    target selects the peer Node in domain A;
  - both A and B pass predicates in both runs.
- Pass/fail: changing only configured plugin weights flips the selected domain.

### K4: Mixed hard/soft boundary

- Purpose: prove weight never overrides a real hard topology boundary.
- Scope: manual Kind workload.
- Setup: Job hard at tier 2 with a soft SubJob, plus a conflicting binpack score
  outside the Job's allowed tier-2 domain.
- Planned artifacts:
  - `k4-mixed-podgroup.yaml`
  - `k4-mixed-deployment.yaml`
- Commands:

  ```bash
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k4-mixed-podgroup.yaml
  kubectl apply -f ../issues/features/016-native-soft-topology-scoring-OPEN/k4-mixed-deployment.yaml
  kubectl get pods -o wide
  kubectl -n volcano-system logs deployment/volcano-scheduler --since=10m
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k4-mixed-deployment.yaml
  kubectl delete -f ../issues/features/016-native-soft-topology-scoring-OPEN/k4-mixed-podgroup.yaml
  ```

- Expected result: all selected Nodes remain inside the hard tier-2 boundary;
  soft NTA ranks only candidates inside that boundary.
- Pass/fail: Job runs and no Pod escapes the hard domain.

### K5: Cleanup and resource bound

- Purpose: leave no task-specific cluster or workload behind.
- Commands:

  ```bash
  kubectl get nodes -o wide
  kind delete cluster --name volcano-dev-016
  kind get clusters
  ```

- Expected result: the verification topology never exceeds five Kubernetes
  Nodes and `volcano-dev-016` is removed.
- Pass/fail: node bound respected and cluster absent after cleanup.

## 11. Evidence to record during implementation

For every verification case, record in this file or an `artifacts/` result file:

- exact commit and local image tag;
- Go/Make command outputs;
- Kind and Kubernetes versions;
- Node architecture and HyperNode tree;
- scheduler configuration and effective weights;
- Pod-to-Node placement and computed Job/SubJob LCA;
- relevant scheduler score/decision logs;
- per-case pass/fail result;
- cleanup result;
- final implementation commit hashes, if commits are requested.

## 12. Approval gate

Approval of this proposal authorizes implementation only for PR 1 as described
above. It does not authorize:

- the PR 2 bounded planner/backtracking implementation;
- group topology affinity code;
- API/CRD changes;
- weakening or replacing the verification cases.

If implementation shows that native soft scoring cannot be made correct without
one of those scope expansions, stop and return to proposal review.
