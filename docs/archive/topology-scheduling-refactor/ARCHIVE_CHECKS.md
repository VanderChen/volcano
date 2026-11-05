# 本次归档检查记录

日期：2026-11-05。仅核验资料归档，不执行历史工作负载。

## 文件与引用

- 从本地 issue 001–025 保存 218 个文本源文件，分别计算原文件与归档副本 SHA-256；与 [SOURCE_MANIFEST.json](SOURCE_MANIFEST.json) 对照一致。原工作区源文件没有修改。
- Markdown 内的本地文件／目录链接逐项解析，所有目标存在；Markdown 代码围栏成对。
- 41 处原本指向本机源码的链接已迁移，所选 Git 对象、路径与行号范围均存在。原来是可变工作树链接，因此不声称归档导航行号重新证明了当时每条源码结论。
- 另存 5 份原型源码文本，直接从 `c4c6c3643` 提取并记录 SHA-256，扩展名为 `.go.txt`。
- 关键最终基线代码复核：`0854d3a40` 的 `allocate.go:1054` 确实丢弃 `prioritizeNodes` 返回分；同提交存在内部 `HyperNodeIndex`。未把归档底座当作最终设计的上游基线。
- 原文完整外链保留；本次重新打开的一手资料及无法读取的两项明确记录在 [参考资料](REFERENCES_CN.md)。未宣称所有历史外链均重新在线验证。

## 原型补丁

`015-unified-topology-placement.patch` 由历史提交直接 `git format-patch` 生成。在临时 Git index 中读取父提交 `8a71ca5323fdd7e48eb2eebe6ed7169d3d586633`，应用补丁，然后写出 tree，与目标提交的 tree 完全一致：

```text
dee427fa98531c4303ded47ead3d8d3660f6e87d
```

临时 index 已删除，没有将补丁应用到任何源码工作树。

## 格式与差异范围

新增整理文档与辅助清单的 focused whitespace 检查通过：

```bash
git diff --cached --check -- docs/archive/topology-scheduling-refactor \
  ':!docs/archive/topology-scheduling-refactor/prototype/015-unified-topology-placement.patch' \
  ':!docs/archive/topology-scheduling-refactor/sources'
```

全量默认 `git diff --cached --check` 非零：原始 patch 的上下文行带有补丁格式要求的前导空格，原始材料还包含 Markdown 硬换行空格和文件末空行。它们按归档保真原则保留，不改写 patch 或历史文件以消除格式提示；这不属于代码测试失败。

本分支从 `dev-1.14@7947e875600ff450318b412bb0c8c39f7020238a` 派生，所有新增文件仅在 `docs/archive/topology-scheduling-refactor/`。原始 `volcano/` 工作树仍在 `fix/026-pending-pod-condition-update-loop`；归档使用独立 worktree。没有实现代码差异，也没有修改其他 issue 状态。

## 验证边界和远端

没有重新运行 Python 模型、Go 测试、构建镜像或 Kind。025 的 U/B/K 仍是 NOT RUN。归档分支名称为 `archive/topology-scheduling-refactor-20261105`，目标 remote 为既有 `origin`（`VanderChen/volcano`）。推送后以 `git ls-remote` 对照本地 HEAD；最终提交 SHA 和远端入口在交付回复中给出，避免在本提交内嵌套记录自身 SHA。
