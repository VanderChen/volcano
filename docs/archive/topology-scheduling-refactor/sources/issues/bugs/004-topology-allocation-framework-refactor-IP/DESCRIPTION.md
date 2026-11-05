> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 004: topology allocation framework is not composable across hard, soft, and neutral policies

## Status

IP

## Summary

Bug 001, Bug 002, and Bug 003 fixed concrete topology scheduling failures, but
the resulting implementation is specialized around those reproductions. The
allocation path still does not provide a general contract for:

- combining hard and soft topology policies;
- composing multiple HyperNode gradient plugins;
- representing a plugin that does not apply to the current Job or SubJob;
- selecting a bounded set of related SubJobs for one allocation transaction;
- comparing complete Job-level placement solutions.

This issue refactors the scheduler topology allocation framework so that
network topology and group topology affinity remain correct when they are used
alone, together, incrementally, and with mixed hard/soft policies.

## Confirmed Problems

### 1. Hard SubGroup search bypasses global soft selection

Any hard SubGroup topology term routes the Job into
`searchSubJobAllocations`. The search returns the first feasible combination
and does not compare all relevant solutions by the complete soft objective.

Consequences include:

- hard SubGroup affinity plus soft Job/SubJob network topology may satisfy the
  hard rule but choose a worse soft placement;
- preferred SubGroup terms cannot reliably influence a Job that also has a
  required SubGroup term;
- the soft SubJob completion fields introduced by Bug 001 are not populated by
  the hard-search branch.

### 2. Hard SubGroup search can over-expand the transaction

The Bug 002 termination condition continues while any target SubJob remains in
the worksheet. This can pull every matching SubJob into one DFS transaction
even when `minMember` and `minSubGroups` require only a subset.

For example, with ten matching SubJobs, `minSubGroups=1`, and capacity for one
SubJob, the scheduler can fail the entire search while one valid progress-making
allocation exists.

The search also has no general state deduplication or pruning. Its worst-case
space grows exponentially with the number of SubJobs and candidate HyperNodes.

### 3. Gradient intersection destroys soft ordering

Bug 001 expresses soft network topology preference through ordered gradient
layers based on the resulting LCA tier. When more than one plugin contributes a
gradient, the framework intersects candidate names and rebuilds layers by each
HyperNode's physical tier.

For an incrementally allocated Job/SubJob, physical tier and resulting LCA tier
can differ. A remote tier-1 candidate whose LCA is tier 3 can therefore be tried
before a local tier-2 candidate whose LCA is tier 2.

### 4. Neutral behavior is coupled to another plugin's API

Bug 003 makes `network-topology-aware` return a full subtree only when the Job
declares `topologyAffinity`. This fixes the current interaction with
`group-topology-affinity`, but it does not express a general plugin-neutral
result.

A plugin that has no constraint for an object must be able to abstain from
gradient intersection without knowing which other plugins are enabled or which
API fields they consume.

### 5. Job-level soft topology tracks only soft SubJobs

Bug 001 counts only SubJobs that themselves use soft network topology when it
decides whether a Job-level soft solution is complete. Job-level
`networkTopology` applies to the whole Job, including SubJobs with hard or no
SubJob-level network topology.

Mixed SubGroup policy modes can therefore be ranked using an incomplete Job
placement.

### 6. Soft highest-tier semantics are not documented consistently

The scheduler now treats `highestTierAllowed` in soft mode as a preferred
tier. The user guide currently states that `highestTierAllowed` is used with
hard mode and is not required in soft mode.

The refactor must establish one explicit contract and align implementation,
tests, API comments, and user documentation.

## Required Semantics

The refactor must preserve these principles:

- Hard topology is a feasibility constraint. A committed solution must satisfy
  every applicable hard plugin.
- Soft topology is a preference. It must influence solution ranking but retain
  a feasible fallback.
- A plugin with no policy for the current object is neutral and must not narrow
  another plugin's candidate set.
- Job-level topology evaluates every SubJob participating in the current Job
  solution, regardless of the SubJob's own network topology mode.
- Allocation must make progress according to gang minima without requiring all
  pending topology-related SubJobs to fit in one transaction.
- Interdependent SubJobs must be evaluated together far enough to prove the
  hard relationships used by the committed solution.
- Candidate and solution ordering must be deterministic when scores tie.
- Search cost must be bounded through working-set selection, component
  isolation, deduplication, and pruning.

## Recommended Soft Tier Contract

For compatibility with workloads covered by Bug 001:

- `mode: soft` with `highestTierAllowed` or `highestTierName` treats the
  tier as a preferred maximum LCA tier. Placement above it remains a fallback.
- `mode: soft` without a highest tier remains valid and uses ordinary
  topology-locality/bin-packing scoring without a discrete preferred threshold.
- `mode: hard` keeps the current mandatory maximum-tier behavior.

This contract requires documentation updates but no CRD schema change.

## Scope

Expected scheduler-side scope:

- `pkg/scheduler/api`: internal HyperNode plugin result and solution metadata;
- `pkg/scheduler/framework`: neutral/applicable gradient composition and
  preference aggregation;
- `pkg/scheduler/actions/allocate`: unified Job/SubJob solution search,
  termination, comparison, recovery, and diagnostics;
- `pkg/scheduler/plugins/network-topology-aware`: hard feasibility and soft
  scoring separation;
- `pkg/scheduler/plugins/group-topology-affinity`: hard feasibility and
  preferred scoring integration;
- scheduler topology documentation and focused tests.

No controller, admission, CRD schema, or user-facing field addition is planned.
If implementation investigation shows that an API/schema change is necessary,
the proposal must be updated and approved before that change is made.

## Relationship To Previous Bugs

The refactor supersedes the specialized implementation strategy, but must keep
the externally observable behavior verified by:

- Bug 001: soft Job/SubJob network topology packing and fallback;
- Bug 002: hard and preferred SubGroup topology progress when gang minima are
  zero;
- Bug 003: network topology neutrality when group topology affinity supplies
  descendant candidates.

The existing commits must not be reverted independently. Their behavior should
be retained through the new general framework and regression tests.

## Completion Requirements

This issue is complete only when:

- the proposal and verification cases are approved;
- implementation and focused tests are complete;
- related scheduler package tests pass;
- a locally built scheduler image is loaded into the existing 4-node Kind
  cluster;
- all existing topology-affinity Cases 1-6 pass;
- the additional Bug 004 Kind combination cases pass;
- commands, placements, scheduler logs, image tag, and cleanup are recorded in
  `PROPOSAL_COMMIT.md`;
- the issue folder is renamed from `OPEN` to `DONE`.
