> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Proposal And Verification Notes

## Scope

Analyze the scheduler source for HyperNode-aware allocation and propose a fix for job-level soft network topology affinity across partitioned subjobs. No implementation code has been changed yet.

## Current Workspace State

- Volcano source repository: `volcano/`
- Initial `volcano/` git status: `?? example/topology-affinity/`
- The untracked `example/topology-affinity/` directory is pre-existing and left untouched.

## Source Analysis

### API and controller path

- The reported `vcjob.spec.networkTopology` is copied into `PodGroup.Spec.NetworkTopology` in `pkg/controllers/job/job_controller_actions.go`.
- A task `partitionPolicy` is converted into `PodGroup.Spec.SubGroupPolicy` by `setPgSubGroupPolicy` / `getSubGroupPolicy`.
- The generated subgroup policy uses:
  - `Name = taskSpec.Name`
  - `SubGroupSize = taskSpec.PartitionPolicy.PartitionSize`
  - `MinSubGroups = taskSpec.PartitionPolicy.MinPartitions`
  - `MatchLabelKeys = []string{batch.TaskPartitionID}`
  - `NetworkTopology = taskSpec.PartitionPolicy.NetworkTopology`
- Therefore the scheduler sees the reported job as:
  - one job-level soft `NetworkTopology` with `highestTierAllowed = 2`
  - multiple partition-derived subjobs with soft `NetworkTopology` and `highestTierAllowed = 1`

### Scheduler allocation path

- `JobInfo.RequiresHyperNodeAllocate()` returns true when the job has `SubGroupPolicy`, even if the network topology mode is soft. This job therefore enters `allocateForJob`.
- `allocateForJob` calls `ssn.HyperNodeGradientForJobFn` once for the job, iterates candidate HyperNode gradient layers, dry-runs all subjobs under each candidate, and returns after the first gradient layer with any solution.
- For each candidate job HyperNode, `allocateForJobInHyperNode` pops subjobs one by one and calls `allocateForSubJob`. It accumulates only the returned subjob allocation score.
- `allocateForSubJob` calls `ssn.HyperNodeGradientForSubJobFn`, dry-runs candidate HyperNodes for that one subjob, and selects the best subjob HyperNode through `selectBestHyperNodeForSubJob`.
- `selectBestHyperNodeForJob` then chooses the candidate job HyperNode only by the sum of subjob scores. It does not score the final job-level `AllocatedHyperNode` / LCA produced by all subjob placements.

Relevant code:

- `pkg/scheduler/actions/allocate/allocate.go:320` routes HyperNode jobs to `allocateForJob`.
- `pkg/scheduler/actions/allocate/allocate.go:372` gets job HyperNode gradients.
- `pkg/scheduler/actions/allocate/allocate.go:394` iterates job candidate HyperNode gradients.
- `pkg/scheduler/actions/allocate/allocate.go:406` dry-runs all subjobs under one candidate job HyperNode.
- `pkg/scheduler/actions/allocate/allocate.go:430` selects the best job HyperNode from `subJobsAllocationScores`.
- `pkg/scheduler/actions/allocate/allocate.go:475` schedules subjobs sequentially.
- `pkg/scheduler/actions/allocate/allocate.go:611` gets subjob HyperNode gradients.
- `pkg/scheduler/actions/allocate/allocate.go:665` selects the best HyperNode for a single subjob.
- `pkg/scheduler/actions/allocate/allocate.go:826` scores subjob candidate HyperNodes through `HyperNodeOrderMapFn`.

### Network topology plugin behavior

`pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go` currently treats soft topology as a node/subjob scoring preference, not as a HyperNode gradient preference:

- Job gradient:
  - `network_topology_aware.go:275` registers `HyperNodeGradientForJobFn`.
  - `network_topology_aware.go:276` defaults `highestAllowedTier` to the maximum HyperNode tier.
  - `network_topology_aware.go:277` only applies `job.IsHardTopologyMode()` to lower the highest allowed tier.
  - Result: job-level soft `highestTierAllowed = 2` is ignored during job HyperNode gradient generation.
- Subjob gradient:
  - `network_topology_aware.go:289` registers `HyperNodeGradientForSubJobFn`.
  - `network_topology_aware.go:293` only traverses child HyperNodes when the subjob topology is hard.
  - `network_topology_aware.go:302` returns the current parent HyperNode for soft subjobs.
  - Result: subjob soft `highestTierAllowed = 1` is not represented as a HyperNode traversal preference; local packing relies on node order after the first pod lands.

### Root cause from source

The source matches the reported root cause.

1. The vcjob partition policy becomes multiple scheduler subjobs.
2. The scheduler dry-runs and chooses each subjob placement sequentially.
3. Soft `highestTierAllowed` is not used to build preferred job/subjob HyperNode gradient layers.
4. The job-level final placement LCA is updated during dry-run, but is not included in `selectBestHyperNodeForJob` scoring.
5. Therefore a placement that is locally good for each subjob can still be globally poor for the job, such as putting partition groups under different higher-tier HyperNodes when a single tier-2 HyperNode could satisfy the job.

This should be fixed in the HyperNode traversal and candidate-selection path, not in API conversion or admission.

## Proposed Design

### Principle

Keep hard topology as a constraint, and make soft topology a preference that is expressed in the same traversal path.

- Hard mode: candidates outside `highestTierAllowed` remain filtered out.
- Soft mode: candidates within `highestTierAllowed` are tried/scored first; candidates above the preferred tier remain available as fallback.

### Change 1: generate soft-aware HyperNode gradients

Update the network-topology-aware plugin gradient registration:

- For job soft topology with `HighestTierAllowed != nil`, generate job candidate gradients that prefer HyperNodes whose resulting job placement stays at or below the preferred tier.
- For subjob soft topology with `HighestTierAllowed != nil`, generate subjob candidate gradients that prefer HyperNodes whose resulting subjob placement stays at or below the preferred tier.
- Do not reuse the current hard-only `hyperNodeGradientFn` unchanged for soft mode, because that helper filters candidates above `highestTierAllowed`; soft mode must rank them later, not drop them.

Expected behavior for the reported case:

- Job-level soft `highestTierAllowed = 2` tries fitting the whole job in tier-2 HyperNodes before considering tier-3 or cluster-top HyperNodes.
- Subjob-level soft `highestTierAllowed = 1` tries fitting each partition group in tier-1 HyperNodes before considering wider domains.

### Change 2: record final job placement in dry-run solutions

Extend `jobAllocationSolution` to carry the final dry-run `job.AllocatedHyperNode` after all selected subjobs are simulated under a candidate job HyperNode.

This is needed because the value that matters for job-level soft affinity is the final LCA of all selected subjob placements, not only the outer candidate HyperNode name.

### Change 3: use combined job/subjob score when selecting a job HyperNode

Replace `selectBestHyperNodeForJob(subJobsAllocationScores, job)` with selection over richer candidates, for example:

- candidate HyperNode name
- final job allocated HyperNode from the dry-run
- summed subjob score
- job soft topology score

Recommended ranking:

1. Prefer candidates whose final job allocated HyperNode is within the job soft preferred tier.
2. Among those, prefer lower-tier / tighter final job HyperNode.
3. Then use the accumulated subjob score.
4. Finally use a deterministic tie-breaker by HyperNode name to avoid random drift in regression tests.

This preserves the intended priority: job-level soft affinity should not be lost merely because independent subjob bin-packing scores happen to tie or favor different branches.

### Change 4: keep sequential path but add a guard for global soft scoring

The minimal fix can keep the existing `allocateForJobInHyperNode` sequential subjob loop, because soft job gradients will constrain the reported case to tier-2 candidates before broader candidates are tried.

If follow-up testing shows locally optimal subjob choices still defeat job-level soft preference inside the same job candidate, generalize the existing `collectSubJobAllocationOptions` / `searchSubJobAllocations` path so soft jobs can compare multiple subjob option combinations. That should be bounded by gradient layer and candidate count to avoid an unbounded Cartesian search.

## Proposed Tests

### Unit tests

Add a focused scheduler allocate regression in `pkg/scheduler/actions/allocate/allocate_test.go`:

- Build a 16-node topology with two higher-tier branches and enough capacity in one preferred tier-2 domain.
- Build a PodGroup equivalent to the reported vcjob:
  - job network topology: soft, highest tier 2
  - partition/subgroup network topology: soft, highest tier 1
  - two partition subjobs, each size 2
- Assert:
  - each partition/subjob binds within a single tier-1 HyperNode
  - all four pods bind within one tier-2 HyperNode
  - no two partitions are split across different tier-3 branches when a tier-2 solution exists

Add plugin-level tests in `pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go`:

- job soft gradients include preferred tiers before fallback tiers
- subjob soft gradients include preferred child tiers before fallback parent/higher tiers
- hard mode still filters candidates above `highestTierAllowed`

Add helper tests in `pkg/scheduler/actions/allocate/placement_test.go` if a new job soft score helper is introduced:

- final job LCA at or below preferred tier outranks a wider LCA
- deterministic tie-breaker is stable when scores are equal

## Verification Plan

No implementation has been done yet. After implementation approval:

1. Run targeted unit tests:

   ```bash
   go test ./pkg/scheduler/actions/allocate -run 'TestAllocateWithPartitionPolicyNetworkTopology|Test.*Soft.*HyperNode'
   go test ./pkg/scheduler/plugins/network-topology-aware -run 'Test.*HyperNode.*Gradient|TestHyperNodeOrderFn'
   ```

2. Run focused scheduler package tests:

   ```bash
   go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/plugins/network-topology-aware ./pkg/scheduler/api
   ```

3. Run formatting:

   ```bash
   ./hack/update-gofmt.sh
   ```

4. Run the project unit gate:

   ```bash
   make unit-test
   ```

5. Perform Kind verification with a task-specific cluster no larger than 8 Kubernetes nodes. Because the reported scenario has 16 nodes, use a scaled-down equivalent topology that preserves the same shape:
   - two higher-tier branches
   - preferred tier-2 domains
   - two partition groups
   - enough resources for both groups in one preferred tier-2 domain

   Record:
   - Kind cluster name and node topology
   - local image tag and loaded images
   - Helm install/upgrade command
   - job YAML
   - pod-to-node placement
   - HyperNode membership proving same tier-1 per partition and same tier-2 for the whole job
   - scheduler logs around HyperNode screening and selected candidates

## Implementation / Commit Record

Implementation has been completed after user approval.

Final source commit:

```text
c556fc6a2 fix scheduler soft topology scoring across subjobs
```

### Code changes

- `pkg/scheduler/api/sub_job_info.go`
  - Added `SoftTopologyPreferredTier()` so allocation/plugin code can detect soft subgroup topology and its `highestTierAllowed`.
- `pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go`
  - Made job HyperNode gradient generation soft-aware.
  - Made subjob HyperNode gradient generation soft-aware.
  - Added a soft gradient helper that prefers candidates at or below the configured tier but keeps wider HyperNodes as fallback, preserving soft semantics.
- `pkg/scheduler/actions/allocate/allocate.go`
  - Extended `jobAllocationSolution` to store the final dry-run job `AllocatedHyperNode`.
  - Changed job candidate selection to combine job-level soft topology rank, subjob allocation score, and deterministic HyperNode-name tie-breaking.
  - Kept existing hard topology behavior as filtering/constraint behavior.
- Tests added:
  - `pkg/scheduler/actions/allocate/allocate_test.go`
    - Regression coverage for a 16-node, two-branch HyperNode topology matching the reported vcjob shape.
    - Verifies each partition stays in a single tier-1 HyperNode and all pods stay in one tier-2 HyperNode.
  - `pkg/scheduler/actions/allocate/placement_test.go`
    - Verifies job-level soft placement outranks a higher subjob score when selecting a job HyperNode.
  - `pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go`
    - Verifies soft HyperNode gradients prefer configured tiers before fallback tiers.

### Verification completed

```bash
gofmt -w pkg/scheduler/api/sub_job_info.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go \
  pkg/scheduler/actions/allocate/allocate.go \
  pkg/scheduler/actions/allocate/placement_test.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go \
  pkg/scheduler/actions/allocate/allocate_test.go
```

Result: passed.

```bash
go test ./pkg/scheduler/actions/allocate -run 'TestSelectBestHyperNodeForJobPrefersSoftJobPlacement|TestAllocateWithPartitionPolicySoftNetworkTopologyPacksJobIntoPreferredTier'
```

Result: passed.

```bash
go test ./pkg/scheduler/plugins/network-topology-aware -run 'TestSoftHyperNodeGradientFnPrefersConfiguredTier'
```

Result: passed.

```bash
go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/plugins/network-topology-aware ./pkg/scheduler/api
```

Result: passed.

```bash
./hack/update-gofmt.sh
```

Result: passed. The command also reformatted unrelated pre-existing test files; those unrelated diffs were reverted so the final source diff remains scoped to this issue.

## Scheduler-only Revision

The temporary controller-side idea of defaulting `MinSubGroups` to
`TotalPartitions` for partition policies with network topology was rejected
because it changes the meaning of an omitted/zero `minPartitions` field. That
change has been removed. The controller now preserves the existing conversion:

- `taskSpec.PartitionPolicy.MinPartitions` is copied directly to
  `PodGroup.Spec.SubGroupPolicy[*].MinSubGroups`.
- A vcjob with no `minPartitions` still produces `MinSubGroups=0`.

The scheduler-only fix keeps the API/controller semantics unchanged and handles
the issue during dry-run allocation:

- For job-level soft topology, count pending soft-topology subjobs from the
  current `JobWorksheet`.
- When there are multiple target soft subjobs, do not stop the dry-run merely
  because normal `JobReady` became true after the first subjob.
- Continue comparing later HyperNode gradients until a candidate covers all
  target soft subjobs and the final job placement is still within the preferred
  job tier.
- Keep partial candidates as fallback, preserving soft-mode behavior if no full
  preferred placement exists.
- Rank candidates by preferred job placement, covered soft subjob count, final
  placement tier, accumulated subjob score, and HyperNode name.

Additional regression coverage was added:

- `TestAllocateWithPartitionPolicySoftNetworkTopologySearchesAllSoftSubJobsWhenMinSubGroupsIsZero`
  uses `PodGroup.Spec.MinMember=0` and `SubGroupPolicy.MinSubGroups=0`, proving
  the fix does not depend on controller-side defaulting.

### Verification after scheduler-only revision

```bash
gofmt -w pkg/scheduler/actions/allocate/allocate.go \
  pkg/scheduler/actions/allocate/allocate_test.go \
  pkg/scheduler/actions/allocate/placement_test.go \
  pkg/scheduler/api/sub_job_info.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go \
  pkg/controllers/job/job_controller_actions.go \
  pkg/controllers/job/job_controller_actions_test.go
```

Result: passed.

```bash
go test ./pkg/controllers/job -run TestGetSubGroupPolicy
```

Result: passed.

```bash
go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/plugins/network-topology-aware ./pkg/scheduler/api
```

Result: passed.

```bash
git diff --check
```

Result: passed.

### Kind E2E verification after scheduler-only rebuild

Scheduler image was rebuilt from the current working tree after the
scheduler-only revision:

```bash
make vc-scheduler
docker build --platform linux/arm64 -t volcanosh/vc-scheduler:dev-001 -f ../dockerfile.scheduler.local .
docker image inspect volcanosh/vc-scheduler:dev-001
kind load docker-image volcanosh/vc-scheduler:dev-001 --name kind
kubectl rollout restart deployment/volcano-scheduler -n volcano-system
kubectl rollout status deployment/volcano-scheduler -n volcano-system --timeout=180s
```

Result:

- Local scheduler image: `volcanosh/vc-scheduler:dev-001`
- Image ID: `sha256:81f4eafabb2df80a4fe236d3dbd28513ceb356f44fc853edc27ecdab8bfa1293`
- Image OS/arch: `linux/arm64`
- Kind load: loaded to `kind-control-plane`, `kind-worker`, `kind-worker2`, and `kind-worker3`
- Scheduler rollout: passed

Controller was intentionally restored to the upstream image to prove the
verification does not depend on controller-side API/defaulting changes:

```bash
kubectl set image deployment/volcano-controllers \
  volcano-controllers=docker.io/volcanosh/vc-controller-manager:v1.14.1 \
  -n volcano-system
kubectl rollout status deployment/volcano-controllers -n volcano-system --timeout=180s
```

Result: passed. Final controller image:
`docker.io/volcanosh/vc-controller-manager:v1.14.1`.

Kind cluster:

```bash
kubectl get nodes -o wide
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" arch="}{.status.nodeInfo.architecture}{" cpu="}{.status.allocatable.cpu}{"\n"}{end}'
```

Result:

- `kind-control-plane`: `arm64`, `cpu=12`
- `kind-worker`: `arm64`, `cpu=12`
- `kind-worker2`: `arm64`, `cpu=12`
- `kind-worker3`: `arm64`, `cpu=12`

HyperNode topology:

- tier1: `kind-tier1-control-plane`, `kind-tier1-worker`, `kind-tier1-worker2`, `kind-tier1-worker3`
- tier2: `kind-tier2-pair-a` = control-plane + worker, `kind-tier2-pair-b` = worker2 + worker3
- tier3: `kind-tier3-branch-a` = pair-a, `kind-tier3-branch-b` = pair-b

The test job used the original no-`minPartitions` semantics:

```bash
kubectl apply -f issues/bugs/001-hypernode-soft-affinity-job-subjob-scoring-IP/hypernode-soft-e2e-001.yaml
kubectl wait --for=condition=Ready pod -l app=hypernode-soft-e2e-001 -n default --timeout=180s
```

Result: all four pods became Ready.

Observed placement:

```text
hypernode-soft-e2e-001-ps-0 partition=0 node=kind-worker2
hypernode-soft-e2e-001-ps-1 partition=0 node=kind-worker2
hypernode-soft-e2e-001-ps-2 partition=1 node=kind-worker3
hypernode-soft-e2e-001-ps-3 partition=1 node=kind-worker3
```

This satisfies the expected topology:

- partition 0 stayed in one tier1 HyperNode: `kind-tier1-worker2`
- partition 1 stayed in one tier1 HyperNode: `kind-tier1-worker3`
- all pods stayed in one tier2 HyperNode: `kind-tier2-pair-b`

PodGroup semantics stayed unchanged:

```yaml
spec:
  minTaskMember:
    ps: 0
  subGroupPolicy:
  - name: ps
    minSubGroups: 0
    subGroupSize: 2
    networkTopology:
      mode: soft
      highestTierAllowed: 1
  networkTopology:
    mode: soft
    highestTierAllowed: 2
metadata:
  annotations:
    volcano.sh/job-allocated-hypernode: kind-tier2-pair-b
```

Scheduler log included the final decision:

```text
Allocate job to hyperNode success, job=default/hypernode-soft-e2e-001-..., hyperNode=kind-tier2-pair-b
update allocated hyperNode for job, old=, new=kind-tier2-pair-b
update allocated hyperNode for subJob .../ps-0, old=, new=kind-tier1-worker2
update allocated hyperNode for subJob .../ps-1, old=, new=kind-tier1-worker3
```

Final E2E result: passed with scheduler-only changes and upstream controller
image.

### Project unit gate

```bash
make unit-test
```

Result: failed with exit code 2 on this macOS host. The failure is outside the
scheduler-only change path:

- Agent packages failed because Linux-specific handler packages are excluded by
  build tags on darwin, for example
  `pkg/agent/events/handlers/cpuqos`, `memoryqos`, `memoryqosv2`, and
  `networkqos`.
- `github.com/opencontainers/runc/libcontainer` failed on darwin due Linux-only
  symbols such as `unix.SchedAttr`, `unix.CloseRange`, and `unix.O_PATH`.
- `pkg/networkqos/tc` failed with an existing interface mismatch:
  `TCCmd.PreAddFilter` returns `error`, while the interface expects
  `(bool, error)`.
- `pkg/networkqos/utils/ebpf` had a host-dependent test failure:
  `TestCreateAndPinArrayMap` expected `new map failed` but got nil.
- `third_party/mindcluster/common/k8s` had
  `TestUpdateConfigmapIncrementally` fail with `config map already exist`.

Scheduler-related packages passed inside the same `make unit-test` run,
including:

- `pkg/scheduler/actions/allocate`
- `pkg/scheduler/api`
- `pkg/scheduler/plugins/network-topology-aware`
- the broader `pkg/scheduler/...` package set shown in the command output

The focused regression tests and Kind E2E remain the validation for this
scheduler-only fix.

```bash
git diff --check
```

Result: passed.

### Verification blocked

```bash
make unit-test
```

Result: failed for environment/platform-unrelated packages after scheduler-related packages passed.

Observed failures include Darwin/Linux build-constraint and dependency issues in agent/networkqos/resourceusage paths, plus an unrelated `third_party/mindcluster/common/k8s` configmap test conflict. Scheduler packages touched by this issue passed in the focused test run above.

```bash
TAG=dev-001 BUILDX_OUTPUT_TYPE=docker make images
```

Result: failed before local images could be built. Docker BuildKit could not fetch Docker Hub auth for `golang:1.24.0`:

```text
Post "https://auth.docker.io/token": dial tcp ...:443: i/o timeout
```

Because images could not be built, Kind verification could not be completed. A follow-up attempt to build local component binaries for a local-runtime-image workaround required elevated execution and was rejected by the approval system due the current Codex usage limit. Per workspace rules, the task remains `IP` and is not marked `DONE`.

This image-build blocker was later resolved by using local runtime Dockerfiles that copy host-built binaries into `alpine:latest`.

### Local scheduler image build

Created a local-only Dockerfile outside the `volcano/` git repository:

```text
/Users/vanderchen/workspace/dev/volcano-workspace/dockerfile.scheduler.local
```

The Dockerfile mirrors the runtime stage of `installer/dockerfile/scheduler/Dockerfile`, but does not use the `golang:1.24.0` builder stage. It expects the scheduler binary to be built by the host Go toolchain first, then copies `_output/bin/vc-scheduler` into an `alpine:latest` runtime image.

Kind node architecture was checked before building:

```bash
kubectl get nodes -o wide
kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.nodeInfo.architecture}{"\n"}{end}'
```

Result: all current Kind nodes are `arm64`.

Built the scheduler binary with the host Go toolchain:

```bash
make vc-scheduler
```

Result: passed and produced a Linux arm64 static binary at `_output/bin/vc-scheduler`. Go emitted a non-fatal warning while writing the host module stat cache:

```text
go: writing stat cache: open /Users/vanderchen/go/pkg/mod/cache/download/volcano.sh/volcano/@v/...tmp: operation not permitted
```

Built only the scheduler image:

```bash
docker build --platform linux/arm64 -t volcanosh/vc-scheduler:dev-001 -f ../dockerfile.scheduler.local .
```

Result: passed.

Built image:

```text
volcanosh/vc-scheduler:dev-001
sha256:1fb35960a0b0197db6920bb1cab85506eb28ed9a9ff11aea0dd9434652a92062
linux/arm64
```

Smoke checked the image entrypoint:

```bash
docker run --rm volcanosh/vc-scheduler:dev-001 --help
```

Result: printed `vc-scheduler` flags. The command exited with code `2` because `pflag` treats explicit help as `ErrHelp`; the binary and image entrypoint are usable.

### Follow-up controller fix

Kind verification with only the scheduler fix exposed a second issue in the real VCJob path:

- The original YAML does not set `partitionPolicy.minPartitions`.
- The controller converted that to PodGroup `subGroupPolicy[].minSubGroups: 0`.
- In the strengthened two-tier3-branch Kind topology, scheduler was allowed to bind the first partition before the second partition participated in the same job-level dry-run.
- That reproduced the bad shape: partition 0 in one tier3 branch, partition 1 in another, and PodGroup `volcano.sh/job-allocated-hypernode: <cluster-top-hypernode>`.

Additional implementation:

- `pkg/controllers/job/job_controller_actions.go`
  - When `partitionPolicy.networkTopology` is set and `minPartitions` is not set/zero, default PodGroup `minSubGroups` to `partitionPolicy.totalPartitions`.
  - Explicit non-zero `minPartitions` is still preserved.
  - Partition policies without network topology keep the previous zero default.
- `pkg/controllers/job/job_controller_actions_test.go`
  - Added coverage that network-topology partition policy defaults `minSubGroups` from `totalPartitions`.

Additional verification:

```bash
gofmt -w pkg/controllers/job/job_controller_actions.go \
  pkg/controllers/job/job_controller_actions_test.go \
  pkg/scheduler/api/sub_job_info.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go \
  pkg/scheduler/actions/allocate/allocate.go \
  pkg/scheduler/actions/allocate/placement_test.go \
  pkg/scheduler/plugins/network-topology-aware/network_topology_aware_test.go \
  pkg/scheduler/actions/allocate/allocate_test.go
```

Result: passed.

```bash
go test ./pkg/controllers/job -run TestGetSubGroupPolicy
go test ./pkg/scheduler/actions/allocate ./pkg/scheduler/plugins/network-topology-aware ./pkg/scheduler/api
```

Result: passed.

### Local controller-manager image build

Created a local-only controller-manager Dockerfile outside the `volcano/` git repository:

```text
/Users/vanderchen/workspace/dev/volcano-workspace/dockerfile.controller-manager.local
```

Built the controller-manager binary with the host Go toolchain:

```bash
make vc-controller-manager
```

Result: passed and produced a Linux arm64 static binary at `_output/bin/vc-controller-manager`. Go emitted the same non-fatal host module stat cache warning.

Built only the controller-manager image:

```bash
docker build --platform linux/arm64 -t volcanosh/vc-controller-manager:dev-001 -f ../dockerfile.controller-manager.local .
```

Result: passed.

Built image:

```text
volcanosh/vc-controller-manager:dev-001
sha256:6cdc6c9290d07a00af209b8d9660de4465b353d587ad89e8f691b7d979808a94
linux/arm64
```

### Kind end-to-end verification

Cluster:

```text
kind
Kubernetes nodes: 4 total
Architecture: arm64
```

Images loaded into Kind:

```bash
kind load docker-image volcanosh/vc-scheduler:dev-001 --name kind
kind load docker-image volcanosh/vc-controller-manager:dev-001 --name kind
kind load docker-image alpine:latest --name kind
```

Result: all passed.

Volcano deployments updated:

```bash
kubectl set image deployment/volcano-scheduler volcano-scheduler=volcanosh/vc-scheduler:dev-001 -n volcano-system
kubectl rollout status deployment/volcano-scheduler -n volcano-system --timeout=180s
kubectl set image deployment/volcano-controllers volcano-controllers=volcanosh/vc-controller-manager:dev-001 -n volcano-system
kubectl rollout status deployment/volcano-controllers -n volcano-system --timeout=180s
```

Result: both rollouts passed.

Verification topology:

```bash
kubectl delete hypernodes.topology.volcano.sh kind-tier3-all --wait=true
kubectl apply -f /Users/vanderchen/workspace/dev/volcano-workspace/issues/bugs/001-hypernode-soft-affinity-job-subjob-scoring-IP/hypernodes-two-tier3-branches.yaml
kubectl get hypernodes.topology.volcano.sh -o wide
```

Result:

```text
kind-tier1-worker2   tier=1   member node=kind-worker2
kind-tier1-worker3   tier=1   member node=kind-worker3
kind-tier2-pair-b    tier=2   members=kind-tier1-worker2,kind-tier1-worker3
kind-tier3-branch-b  tier=3   member=kind-tier2-pair-b
```

Scheduler confirmed the two-tier3-branch topology:

```text
HyperNode tiers ... tierCount=4 total=9; tier-4(tier=4): 1; tier3(tier=3): 2; tier2(tier=2): 2; tier1(tier=1): 4
```

Workload:

```text
/Users/vanderchen/workspace/dev/volcano-workspace/issues/bugs/001-hypernode-soft-affinity-job-subjob-scoring-IP/hypernode-soft-e2e-001.yaml
```

The final verification YAML keeps the reported shape: job-level soft `highestTierAllowed: 2`, partition-level soft `highestTierAllowed: 1`, `totalPartitions: 2`, `partitionSize: 2`, and no explicit `minPartitions`. Each pod requests `5500m` CPU so one partition can fit on one worker, but two partitions cannot collapse onto the same worker.

Commands:

```bash
kubectl apply -f /Users/vanderchen/workspace/dev/volcano-workspace/issues/bugs/001-hypernode-soft-affinity-job-subjob-scoring-IP/hypernode-soft-e2e-001.yaml
kubectl wait --for=condition=Ready pod -n default -l volcano.sh/job-name=hypernode-soft-e2e-001 --timeout=180s
kubectl get pods -n default -l volcano.sh/job-name=hypernode-soft-e2e-001 -o jsonpath='{range .items[*]}{.metadata.name}{" partition="}{.metadata.labels.volcano\.sh/partition-id}{" node="}{.spec.nodeName}{" cpu="}{.spec.containers[0].resources.requests.cpu}{"\n"}{end}'
kubectl get podgroups.scheduling.volcano.sh -n default -o yaml
```

Result:

```text
hypernode-soft-e2e-001-ps-0 partition=0 node=kind-worker2 cpu=5500m
hypernode-soft-e2e-001-ps-1 partition=0 node=kind-worker2 cpu=5500m
hypernode-soft-e2e-001-ps-2 partition=1 node=kind-worker3 cpu=5500m
hypernode-soft-e2e-001-ps-3 partition=1 node=kind-worker3 cpu=5500m
```

PodGroup result:

```text
subGroupPolicy[ps].minSubGroups: 2
subGroupPolicy[ps].subGroupSize: 2
subGroupPolicy[ps].networkTopology.mode: soft
subGroupPolicy[ps].networkTopology.highestTierAllowed: 1
networkTopology.mode: soft
networkTopology.highestTierAllowed: 2
volcano.sh/job-allocated-hypernode: kind-tier2-pair-b
status.phase: Running
status.running: 4
```

Pass criteria:

- Partition 0 pods are colocated in tier1 `kind-tier1-worker2`.
- Partition 1 pods are colocated in tier1 `kind-tier1-worker3`.
- All four pods are inside tier2 `kind-tier2-pair-b`.
- The job did not spread across the two tier3 branches.

Cleanup:

```bash
kubectl delete job.batch.volcano.sh hypernode-soft-e2e-001 -n default --wait=true
kubectl wait --for=delete pod -n default -l volcano.sh/job-name=hypernode-soft-e2e-001 --timeout=120s
```

Result: passed. The two-branch HyperNode topology remains in the Kind cluster for follow-up inspection.
