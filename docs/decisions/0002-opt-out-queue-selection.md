# ADR-0002：队列默认选中所有 open leaf issue，用 `clawflow-skip` 排除

- 状态：Accepted
- 日期：2026-10-29

## 背景

队列最初需要先给 issue 打 `clawflow` 标签才会被处理（opt-in）。更早还存在子 issue 队列和按范围选择的多种模式。多种模式曾让人困惑：2026-10-28，Agent-Host 的 #21 到 #25 因为仓库被迁移到另一种模式而被忽略（见 #468）。#468 当时把选择规则收敛为“只有 `clawflow` 标签”这一种模式。

## 决定

owner 在 2026-10-29 决定改为 opt-out（#486）：启用仓库里的每个 open leaf issue 默认进入队列，owner 添加 `clawflow-skip` 才排除。

- 旧的 `clawflow` 标签不再影响选择，也不会被删除。选择方式没有配置项，行为固定。
- 原有排除条件继续生效：终态标签、暂停/取消、未满足的 `Depends on:`、worker 标签亲和性、重试上限。
- 排序不变：P0 到 P3 标签（无标签按 P2）、手动 rank、创建时间、issue key；每仓库 `max_in_flight` 不变。
- `clawflow-skip` 只有 owner 能增删。算子结果、修复、Pilot 和解阻流程都不能改它，它也不同于 agent 自己加的 `agent-skipped`。
- 跳过同样覆盖该 issue 的托管 PR：不再启动新的实现、CI/验收/冲突修复、验收和自动合并。已经在跑的任务不被终止，结果照常写回。
- `queue add/remove/move` 只维护 rank，不改变资格。

## 理由

来源只记录了 owner 的决定本身，没有展开动机，这里不补写。能确认的相关事实：

- 产品方向是人负责项目方向和功能描述，已具备条件的工作由 ClawFlow 自主完成（见 `docs/managed-development-design.md`）。默认选中与这个方向一致。
- #468 的经验是选择模式越多越容易误配，所以 #486 仍然不提供模式开关。

## 放弃的方案

- opt-in 标签模式（#468）：需要每个 issue 先加 `clawflow`。被 opt-out 取代，#486 写明它取代 #468 的“只有标签模式”规则。
- 保留多种选择模式（子 issue 队列、按范围选择）：#468 已移除，#486 不恢复。
- 在配置里提供 opt-in/opt-out 开关：#486 写明没有配置项，行为固定。
- 让 Pilot 或算子管理 `clawflow-skip`：被禁止，只有 owner 能改。

## 相关 issue

- [#486](https://github.com/zhoushoujianwork/clawflow/issues/486) 本决定
- [#468](https://github.com/zhoushoujianwork/clawflow/issues/468) 被取代的标签模式
- [#427](https://github.com/zhoushoujianwork/clawflow/issues/427) 跨仓库统一队列、P0 到 P3 和每仓库并发
- 细节见 `docs/queue.md`
