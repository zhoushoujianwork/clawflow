# ADR-0003：Pilot 是全平台唯一 steward，只在被请求时运行

- 状态：Accepted
- 日期：2026-10-29

## 背景

Pilot 起初按项目运行，每次扫描结束后唤醒。#407（2026-10-27）把它设计成自主监督者，#410 随后实现了事件驱动唤醒，带冷却时间和预算，触发条件包括停滞的扫描、卡住的 PR、未入队的新 issue 和每日摘要。

## 决定

owner 在 2026-10-29 决定 Pilot 不再自己观察（#510）：

- 只有三种方式会调用它：`clawflow pilot ask`、`clawflow pilot chat`，或用户自己定义的定期检查（#511）。
- `clawflow run` 的扫描收尾和 Web tick 都不会唤醒它。内置的唤醒、冷却和唤醒状态逻辑全部删除。
- Pilot 是一个覆盖所有启用仓库的 steward，不再属于某个项目。全局章程在 `~/.clawflow/pilot/charter.md`，缺失时从已有项目上下文初始化一次。项目的 `context.md` 只作实现上下文。
- 每次调用有动作预算和超时，不设美元上限。动作经过原有的策略门禁和审计执行器，如 `pilot_override_merge`。
- Pilot 永远不增删 `clawflow-skip`。
- `clawflow project automation ...` 变成输出迁移提示、退出码 0 的空操作；项目 YAML 里的 `automation` 被忽略，保存时删除。`pilot wake` 保留为别名，运行一次默认提示的调用。
- 定期检查（#511）：5 段 cron 加时区，每次到期按一次 steward 调用运行，同一任务同时只跑一次，停机期间错过的不补跑，连续失败三次自动停用。

## 理由

来源只写了“Pilot stops observing on its own”，没有展开动机，这里不补写。#511 写明了目标：用户想要周期性观察时，向 steward 提出并由平台运行定期检查，不必一直开着会话。

## 放弃的方案

- 每次扫描收尾唤醒（原有行为）：删除。
- 事件驱动唤醒和冷却（#407 设计、#410 实现）：删除，`docs/pilot-design.md` 注明“自动唤醒部分已被 #510 取代”。
- 按项目的自动唤醒配置（`project automation enable/disable/bind`）：改为空操作。
- Pilot 管理 `clawflow-skip`：禁止。

## 相关 issue

- [#510](https://github.com/zhoushoujianwork/clawflow/issues/510) 本决定
- [#511](https://github.com/zhoushoujianwork/clawflow/issues/511) 用户定义的定期检查，依赖 #510
- [#407](https://github.com/zhoushoujianwork/clawflow/issues/407) 被部分取代的自主监督者设计
- [#410](https://github.com/zhoushoujianwork/clawflow/issues/410) 被删除的事件驱动唤醒
- 细节见 `docs/pilot-design.md`
