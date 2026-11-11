# 架构决策记录

这里记录 ClawFlow 已经做出的重要决定：当时的背景、决定本身、理由、放弃的方案和相关 issue。目的是让 `implement` 和验收在动手前能读到“为什么是这样”，避免重新引入已经否决的方案。

## 怎么读（按需，不要全读）

1. 先读下面的索引，只看“改到这些内容时再读”一列。
2. 只打开与你要改的代码或要审的 PR 相关的记录，通常 0～2 条。
3. 记录是背景，不是需求。需求只来自 issue 正文和已保存的验收清单。记录与 issue 正文冲突时，以正文为准，并在 PR 里写明冲突。

## 索引

| # | 决定 | 状态 | 改到这些内容时再读 |
|---|---|---|---|
| [0002](0002-opt-out-queue-selection.md) | 队列默认选中所有 open leaf issue，用 `clawflow-skip` 排除 | Accepted | 队列、issue 选择、`clawflow-skip`、`clawflow` 标签 |
| [0003](0003-pilot-single-steward-no-auto-wake.md) | Pilot 是全平台唯一 steward，不自动唤醒 | Accepted | Pilot、steward、定期检查、`project automation` |
| [0004](0004-embedded-sqlite-only.md) | 存储只用 controller 持有的嵌入式 SQLite | Accepted | 数据库、存储、迁移、PostgreSQL、worker 与数据库的关系 |
| [0005](0005-separate-implementation-and-acceptance.md) | 实现和验收分离，验收独立取证 | Accepted | 验收、reviewer、planner、合并门禁、`acceptance_required` |
| [0006](0006-single-autonomous-lifecycle.md) | 只有一个自主生命周期，没有评估和审批阶段 | Accepted | 算子触发、`evaluate-*`、`classify`、审批标签、旧配置键 |
| [0007](0007-runner-owns-vcs-writes-via-outcome-marker.md) | 算子只写 stdout，评论和标签由 runner 写回 | Accepted | 算子 SKILL.md、outcome marker、评论和标签写入 |
| [0008](0008-local-lock-files-not-labels.md) | 执行互斥用本机锁文件，不用 VCS 标签 | Accepted | `agent-running`、`lock_label`、锁、并发控制 |
| [0009](0009-direct-offload-execution.md) | 直接调用 Offload 执行，不经过 AGH | Accepted | 执行器、Offload、profile、预算、凭据 |
| [0010](0010-restart-safe-execution.md) | 重启和升级不终止健康任务，恢复要先验证 | Accepted | controller/worker 生命周期、恢复、升级、心跳、重派 |
| [0011](0011-coordinator-and-pull-workers.md) | 一个 coordinator，worker 拉取任务，按标签匹配 | Accepted | worker、租约、fencing、标签、`bound_machine` |

[ADR-0001](../adr/0001-trigger-surface-expansion.md)（触发面扩展，Proposed）写在 `docs/adr/`，保留原位。序号在整个仓库内唯一，所以这里从 0002 开始。

## 约定

- 一条决定一个文件，文件名 `NNNN-kebab-case-title.md`，序号递增，不复用。
- 文件开头写 `状态` 和 `日期`，正文依次是五节：`背景`、`决定`、`理由`、`放弃的方案`、`相关 issue`。
- 状态取 `Proposed`、`Accepted`、`Superseded by ADR-NNNN`、`Deprecated`。
- 已 Accepted 的记录不改正文。决定变了就新写一条，并只把旧记录的状态改成 `Superseded by ADR-NNNN`。
- 来源没有写明理由时，直接写明来源没有记录，不要补写猜测。
- 保持短小。细节放在设计文档，记录里只链接。

## 新增记录

合并一项新的重大决定时，在同一个 PR 里补一条记录，并更新上面的索引。重大决定指：改变了系统的边界、所有权、存储、执行或安全模型，或明确否决了一个看起来合理的方案。普通功能和修复不需要。

模板：

```markdown
# ADR-NNNN：一句话说明决定

- 状态：Accepted
- 日期：YYYY-MM-DD

## 背景
## 决定
## 理由
## 放弃的方案
## 相关 issue
```
