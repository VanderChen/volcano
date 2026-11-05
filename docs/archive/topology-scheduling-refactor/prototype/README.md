# 015 历史原型

本目录保存历史实现差异用于离线审视，不是最终设计的实现。

- 提交：`c4c6c3643efed8a17f1ef31e8991d9ce1e25f891`
- 父提交：`8a71ca5323fdd7e48eb2eebe6ed7169d3d586633`
- 原分支：`feat/015-unified-topology-placement`
- 生成方式：`git format-patch -1 --stdout --full-index --binary c4c6c3643efed8a17f1ef31e8991d9ce1e25f891`
- 补丁 SHA-256：`fdb6af1aa684cbb6f5e165235242a3fd7dab160d834596abe321879ded767689`

原型未被 cherry-pick 或 merge 到归档分支；[验证记录](../sources/issues/features/015-unified-topology-planning-framework-IP/artifacts/VERIFICATION_RESULTS.md)和[后来设计的变化](../HISTORY_CN.md)须一起阅读。若要复现实验，应在上述父提交的独立工作树应用补丁，并使用匹配的工具链和验证环境。

## 离线代码快照

由于该原型未发现任何包含它的本地远端跟踪分支，额外保存引用到的源码与 planner 核心，文件名加 `.txt`，避免成为本归档分支的新 Go package。内容直接由 `git show <SHA>:<path>` 提取，未修改。

- [pkg/scheduler/plugins/binpack/binpack.go](source/pkg/scheduler/plugins/binpack/binpack.go.txt)
- [pkg/scheduler/plugins/nodeorder/nodeorder.go](source/pkg/scheduler/plugins/nodeorder/nodeorder.go.txt)
- [pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go](source/pkg/scheduler/plugins/network-topology-aware/network_topology_aware.go.txt)
- [pkg/scheduler/actions/allocate/topology_planner.go](source/pkg/scheduler/actions/allocate/topology_planner.go.txt)
- [pkg/scheduler/api/topology_placement.go](source/pkg/scheduler/api/topology_placement.go.txt)
