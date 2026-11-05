> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Community PR Material: SubGroup HyperNode Topology Affinity

## Submission Scope

- **Base branch:** `wangyang0616/task2-podgroup-antiaffinity-v1.14.1` (`a42372bdc`)
- **Head branch:** `VanderChen:dev/podgroup-antiaffinity`
- **Commits to publish:**
  1. `8fcc0012c feat(scheduler): support SubGroup topology affinity`
  2. `edaaf83a7 fix(scheduler): search SubJob topology placements jointly`
  3. `c49caabf9 docs: document HyperNode topology-affinity scheduling`
  4. `47e691146 docs: add topology affinity examples`

This must be opened against the base branch above, not `master`: the base
already contains the prerequisite PodGroup HyperNode anti-affinity work.

## Suggested PR Title

```text
feat(scheduler): support SubGroup topology affinity
```

## Suggested PR Body

```markdown
## What this PR does

This PR adds optional `topologyAffinity` rules to `PodGroup` so that
HyperNode-aware scheduling can express placement relationships at two levels:

- **PodGroup level:** required/preferred anti-affinity against matching
  PodGroups.
- **SubGroup level:** required/preferred affinity and anti-affinity between
  SubGroups defined by `subGroupPolicy`.

Required rules remain hard feasibility constraints. Preferred rules and soft
network topology remain preferences: the scheduler retains a feasible fallback
when the preferred layout cannot be achieved.

### API and admission

- Add `TopologyAffinitySpec`, PodGroup anti-affinity terms, and SubGroup
  affinity/anti-affinity terms to the scheduling API and `PodGroupSpec`.
- Validate term shape, weights, referenced SubGroup policies, tier fields, and
  contradictory overlapping hard SubGroup rules in admission.
- Include the generated CRD and apply-configuration updates required by the
  new optional API.

### Scheduler behavior

- Make HyperNode gradient callbacks distinguish a plugin with no hard opinion
  from a plugin that returns an empty hard-feasibility result. The framework
  intersects only applicable hard constraints, so a neutral plugin cannot
  accidentally remove another plugin's candidates.
- Extend `group-topology-affinity` to filter candidates for required PodGroup
  and SubGroup rules, and score candidates for preferred rules.
- Move soft network-topology preference into HyperNode scoring rather than
  gradient intersection. This keeps soft fallback candidates available and
  composes correctly with topology-affinity.
- Evaluate complete dry-run Job/SubGroup placement solutions before choosing a
  Job HyperNode. The comparison combines plugin scores with Job-level soft
  placement, then uses stable tie breakers.
- For required SubGroup relationships, search only the SubGroups needed by the
  current gang transaction, memoize dominated placement states, and preserve
  progress when minima are zero or smaller than the total SubGroup count.

### Reviewer guide

1. Start with the API and validation changes in
   `staging/src/volcano.sh/apis/pkg/apis/scheduling/` and
   `pkg/webhooks/admission/podgroups/validate/`.
2. Review hard-constraint composition in
   `pkg/scheduler/framework/session_plugins.go`.
3. Review required/preferred rule evaluation in
   `pkg/scheduler/plugins/group-topology-affinity/` and the soft topology
   scoring path in `pkg/scheduler/plugins/network-topology-aware/`.
4. Review solution selection and bounded SubGroup search in
   `pkg/scheduler/actions/allocate/allocate.go`.
5. The CRD, Helm, and development-installer changes are generated/deployment
   companions for the optional API and plugin enablement; they do not introduce
   separate scheduling logic.

### Compatibility

- `topologyAffinity` is optional; existing workloads retain their current
  scheduling path when they do not configure it.
- Required terms reject infeasible placements. Preferred terms and soft
  network topology influence ranking only and keep feasible fallbacks.

### Validation

Current final-tree checks:

```bash
git diff --check a42372bdc..HEAD
go test -count=1 ./pkg/scheduler/actions/allocate ./pkg/scheduler/framework \
  ./pkg/scheduler/plugins/network-topology-aware \
  ./pkg/scheduler/plugins/group-topology-affinity ./pkg/scheduler/api \
  ./pkg/webhooks/admission/podgroups/validate
make vc-scheduler
```

All checks above passed.

Kind validation used the standard four-node topology with both HyperNode
plugins enabled. It passed the six focused topology-affinity scenarios, the
production-shaped `weight: 100` preferred SubGroup anti-affinity regression,
and the Llama-70B-shaped hard PodGroup/SubGroup affinity scenarios.

### Verified Kind scenario: exact selector plus hard SubGroup placement

The generated PodGroup CRD was applied to the four-node Kind cluster and the
following reduced Llama-70B-shaped workload was run with the original exact
`podGroupSelector.matchLabels` selector:

- An anchor PodGroup was pinned in `kind-tier2-pair-a` (`supernode-a`).
- The target PodGroup contained two one-Pod `prefill` shards and two one-Pod
  `decode` shards (`minMember: 4`); each role used `subGroupSize: 1` and
  `minSubGroups: 2`.
- Its hard rules require PodGroup separation at `supernode`, affinity of the
  target SubGroups at `supernode`, and anti-affinity of same-role shards at
  `rack`.

The exact selector was admitted without pruning. The target was placed in
`kind-tier2-pair-b`; `prefill/p0` and `decode/d0` ran on
`kind-tier1-worker2`, while `prefill/p1` and `decode/d1` ran on
`kind-tier1-worker3`. This proves all three hard rules: separation from the
anchor, a shared target supernode, and separate racks for shards of the same
role. Cross-role rack sharing is intentionally allowed by this configuration.

`make unit-test` is not listed as passing because this macOS checkout has
pre-existing out-of-scope failures in agent QoS build constraints and
`pkg/networkqos/tc`; focused scheduler, API, admission, build, and Kind
coverage above passed.
```

## History Cleanup

The original feature and follow-up fixes (`ee838ba2d` through `fba432356`) are
organized into the four reviewable commits above: feature/plugin behavior,
joint SubJob placement, design/user documentation, and runnable examples. The
old tip is retained locally at
`backup/dev-podgroup-antiaffinity-before-pr-cleanup` until the PR is merged.

When ready to publish the rewritten branch:

```bash
git -C volcano push --force-with-lease origin dev/podgroup-antiaffinity
```
