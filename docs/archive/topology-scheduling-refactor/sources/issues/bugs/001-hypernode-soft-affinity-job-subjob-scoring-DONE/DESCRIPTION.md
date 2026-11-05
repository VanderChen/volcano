> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# Bug 001: HyperNode soft affinity only optimizes per subjob and misses job-level packing

## Status

IP

## Reported Scenario

Cluster has 16 idle nodes:

- Nodes 1-8 are under one tier-3 HyperNode.
- Nodes 9-16 are under another tier-3 HyperNode.
- Each tier-3 HyperNode contains lower-tier HyperNodes in a 3-tier topology.

Reported vcjob:

```yaml
apiVersion: batch.volcano.sh/v1alpha1
kind: Job
metadata:
  name: test
spec:
  schedulerName: volcano
  networkTopology:
    mode: soft
    highestTierAllowed: 2
  tasks:
  - name: ps
    replicas: 4
    partitionPolicy:
      totalPartitions: 2
      partitionSize: 2
      networkTopology:
        mode: soft
        highestTierAllowed: 1
```

Note: the original report used `batch.volcano.sh/vlalphal`; this record treats it as the intended `batch.volcano.sh/v1alpha1` API version.

## Expected Result

- Pods in the same partition/subjob are scheduled into the same tier-1 HyperNode.
- All pods of the job are scheduled into the same tier-2 HyperNode.

## Actual Result

- Pods in the same partition/subjob are scheduled into the same tier-1 HyperNode.
- The two partition/subjob placements are split under two different tier-3 HyperNodes, so job-level soft affinity is not optimized.

## Reported Root Cause

The scheduler simulates subjobs one by one within a vcjob. Soft affinity within one subjob can be optimized, but soft affinity across subjobs, namely job-level soft affinity, is not considered as a whole.

## Suggested Fix Direction

In the allocate HyperNode preference stage, introduce combined job/subjob scoring. For tasks whose job-level `networkTopology` is soft, traverse HyperNodes by the job tier as well, and choose a placement whose subjob-level and job-level soft affinity scores are both optimal.

The expected code scope is the main HyperNode traversal and filtering path.
