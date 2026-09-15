# 架构决策记录（ADR）

记录 ClawFlow 的重要架构决策：**为什么**这么做、当时的约束是什么、放弃了哪些选项。

代码会变，决策的理由不会。看代码能知道「现在是什么样」，看 ADR 才知道「为什么不是另一个样」。

## 约定

- 文件名：`NNNN-kebab-case-title.md`，序号递增不复用
- 状态：`Proposed` / `Accepted` / `Superseded by ADR-NNNN` / `Deprecated`
- 已 Accepted 的 ADR **不改内容**。决策变了就写新的一篇，并把旧的标记为 Superseded
- 写作语言：中文正文，代码/字段名保持原文

## 索引

| # | 标题 | 状态 | 日期 |
|---|---|---|---|
| [0001](0001-trigger-surface-expansion.md) | 触发面从「纯 label」扩展为「label + 事件」 | Proposed | 2026-08-06 |
