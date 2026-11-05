> 归档快照（2026-11-05）。本文保留原有阶段、基线和历史结论；其中的“当前”“完成”仅适用于原文对象。最终设计入口及证据边界见[归档说明](../../../../README.md)。本次仅迁移链接并添加此提示，未重新执行文中的命令。

# 024：required SubGroup 反亲和硬语义验证

## 目标

确认生产 `dev-1.14` 派生调度器在四个 tier1 分属两个 tier2 的拓扑中，不会因为
outer container 的逐层搜索而违反 `required` SubGroup anti-affinity：

- 四个 one-Pod peer SubJob 对四个 tier1 时，必须一域一个并全部运行；
- 五个 one-Pod peer SubJob 对四个 tier1 时，必须保持未提交，不能把两个 peer
  放入同一 tier1。

## 范围

本任务只创建 Kind 验证资源和记录结果，不修改 Volcano 实现代码。

