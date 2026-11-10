# ADR-0009：直接调用 Offload 执行，不经过 AGH

- 状态：Accepted
- 日期：2026-10-26

## 背景

ClawFlow 曾把所有 AI 请求（算子、Pilot、文档生成、provider 探测、终端对话）经 `agent-host execute` 发出，AGH 是必经的一跳，没有直接的原生执行后端，也没有自动回退（`docs/agh-execution-design.md`，现标注为历史记录）。

## 决定

提交 `74771e25`（2026-10-26）让 ClawFlow 直接把受限的工程工作交给 Offload，`docs/offload-execution-design.md` 记录了这个方案，并写明它取代“执行必须经过 AGH”的要求。

- 分工：ClawFlow 负责 issue 选择、执行锁、worktree、验收、CI 门禁、合并策略和清理；Offload 负责 profile 选择和原生编码 agent 进程。
- 模型凭据只在 Offload 配置里，ClawFlow 不持有。`settings.execution.profile` 是显式的本机设置，不进入配置同步。
- 在明确授权的交付模式下，原生 agent 可以通过 ClawFlow CLI 编辑、测试、提交、推送并创建 PR。最终报告本身不证明 PR 有效或 CI 通过。
- 实现、CI 修复、Pilot 和显式的 issue 编辑对话使用受管交付模式；辅助算子和文档生成使用只读执行。
- 正的美元预算在启动前以 `unsupported_budget` 失败，不会被悄悄忽略。
- 当前的演进（#698，设计中）：把启动方式从 `offload` CLI 子进程改为直接调用 `pkg/execution`，开关 `settings.execution.engine`，默认仍是 `cli`，需要 offload 先提供孤儿清理接口。这不改变 profile 和凭据模型，也不改变算子、验收和修复流程。

## 理由

- 去掉 AGH 这一跳后，执行路径更短，凭据归属更清楚（只在 Offload 的 profile 里）。
- 设计文档没有展开更多取舍原因，这里不补写。

## 放弃的方案

- 经 AGH 的执行路线：退役，文档保留为历史记录。
- 在 ClawFlow 内引入 Agent-Host 的会话模型：#698 的设计明确不引入，只参考它的 `Request` 组织方式和错误映射。
- 导入 Offload 的内部 Go 包：直接 Offload 方案使用 CLI；#698 评估用公开的 `pkg/execution` 替代，且要灰度，不一次切换。

## 相关 issue

- 提交 `74771e25` 直接 Offload 执行
- [#698](https://github.com/zhoushoujianwork/clawflow/issues/698) 迁移到 `pkg/execution`（open）
- [#754](https://github.com/zhoushoujianwork/clawflow/issues/754) 生产切到 library 引擎后的验证和收尾（open）
- 细节见 `docs/offload-execution-design.md`、`docs/offload-library-execution-design.md`
