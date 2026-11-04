# Repository agent instructions

## Issue 与 PR 的可读性

- 标题先说明用户能得到的改进，例如“菜单栏直接查看运行任务和排队原因”；不要只写架构名、内部机制或笼统的“优化”。
- 说明开头先写当前问题和改完后的使用效果，再写实现、验证及限制。让不读源码的用户也能判断这项工作是否解决了自己的问题。
- 技术细节和验收证据保留在后面；不要为了简化表达省略真实阻塞、降低验收条件或把未完成工作写成已完成。

## Native dashboard updates and validation

- The macOS ClawFlow app is a dashboard and control client. The controller/scheduler, workers, and database run as separate services; quitting or restarting the app leaves active execution running.
- The user authorizes agents to build, install/update, quit, and relaunch the ClawFlow dashboard app as needed to complete and validate app changes, without asking again. This is an explicit exception to the default restriction on restarting desktop apps for routine validation. Keep desktop interactions scoped to ClawFlow.
- Complete app delivery by updating the installed app and checking the changed screen when needed; do not leave installation or app restart pending solely because the app is running.
- App-only updates must not stop/restart the controller, workers, or database, replace their installed CLI executable, or run a host migration installer that reloads services. Updating the bundled dashboard CLI helper does not update the independently installed service executable.
- Core service updates are a separate deployment step. Inspect active tasks and service state first, preserve running work, and use the service's supported drain/recovery process when a restart is needed. App restart authorization does not authorize interrupting active tasks.
- On an explicit local connection, app startup may start installed services that are not loaded. Loaded services are reused. Account for this existing recovery behavior when validating app startup; do not describe app launch as having no possible service side effects.

## 本地交付与实际运行验收

- ClawFlow 更新的完成标准包括：通过必需 CI 和独立验收、更新对应的本地组件，并实际验证本地运行。提交、推送和 PR 合并只是中间状态。
- 根据改动验证本地 API 与数据库就绪、worker 在线和任务连续性，以及 App 连接、受影响页面和操作流程。交付报告记录实际安装的提交/版本、执行过的运行检查和未完成项。
- 本地更新遵循现有排空、恢复、备份和迁移流程，保留正在执行的工作。App 更新与后台服务更新分别交付；数据库切换按其专用验收和回滚条件执行。

## Local native development

- Use `make dev` to build a timestamped dev app, install it at `/Applications/ClawFlow.app`, and relaunch it. Use `make dev-build` for build-only validation; override `CLAWFLOW_DEV_INSTALL_DIR` for an isolated install. `make cli-dev` retains the separate CLI installation workflow.
- Dev bundles label the app and bundled CLI with the same version. Settings exposes a read-only stable release check and release link; dev builds continue updating locally with `make dev`. Do not run CLI self-update against a signed app bundle or confuse app updates with controller upgrades.

## Dashboard interaction and status reference

- Use GitLab Runner/job monitoring as the reference for concise execution status: recognizable pending/running/success/failure states, a spinning indicator during live execution, and an elapsed duration alongside each running task and in its details.
- Show the task activity (development, PR review, repair, reply), worker, issue/PR links, and queue/blocking reasons without requiring users to inspect logs.
- Duration belongs to the current execution attempt and starts at the controller-recorded claim/start time, never the latest heartbeat or app launch. Retries reset to the new attempt; unknown timestamps are explicitly unavailable. Completed durations stop at the recorded end time.
- Keep connection freshness visible. On stale/disconnected snapshots, stop presenting live animation and advancing elapsed time; retain last-known data. Task completion must not imply CI, review, or merge completion.
- Follow this reference in the native macOS app; keep the interaction simple and avoid adding a web dashboard dependency.

## Execution lifecycle architecture

- Follow [restart-safe execution design](docs/resilient-execution-design.md) for controller/worker lifecycle changes. Read the [durable worker runbook](docs/resilient-execution-runbook.md) and verify the deployed protocol before relying on restart safety. Source support does not migrate an already-running legacy embedded worker; the first cutover must drain it.
- Support independent local/LAN workers, durable result replay after controller recovery, and rolling worker generations that drain active tasks without terminating them or increasing shared capacity.
- Preserve worktrees, journals and Offload session lineage for interrupted attempts. Verify authority, ownership and backend recovery capability before reconnecting/resuming; never equate heartbeat loss with safe reassignment or silently launch duplicate execution.
