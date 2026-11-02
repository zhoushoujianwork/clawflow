# ClawFlow

ClawFlow 在自己的机器上调度开发任务，通过 Offload 执行开发与审核，把 Issue 推进到经过 CI 和独立验收的 PR。当前主线是**本机单 controller、独立 worker、原生 macOS App**。

无人值守闭环仍在验证中，不能把一次任务成功等同于长期可靠运行。实际部署与限制见[部署记录](docs/unattended-deployment-2026-11-02.md)，目标与范围见[托管开发设计](docs/managed-development-design.md)。

## 使用入口

- [原生 App 安装与连接](docs/native-macos.md)：查看任务、worker、项目和排队原因。App 是控制客户端，后台服务独立运行。
- [执行能力检查](docs/execution-capability-check.md)：确认执行环境可用，再启用项目自动调度。
- [队列说明](docs/queue.md)：理解任务为什么等待或被阻塞。
- [独立验收](docs/independent-acceptance.md)：了解开发完成后如何检查结果。

已安装并运行 controller 时，使用现有服务调度；不要为催促任务另起 scheduler 或 cron。当前部署使用 PostgreSQL；存储迁移需要单独验证，不是这一轮闭环验收的前提。

## 本地开发

| 命令 | 用途 |
| --- | --- |
| `make build` | 仅构建 CLI，不安装或启动服务 |
| `make dev-build` | 仅构建原生开发 App |
| `make dev` | 构建、安装并重启 ClawFlow App |
| `make cli-dev` | 构建并安装本地 CLI |
| `make native-test` | 原生 App 单元测试 |
| `make test` | Go 全量 race 测试 |

App 更新与后台服务升级分开进行。`make dev` 不替换独立安装的 controller/worker 可执行文件。后台升级按[controller 升级说明](docs/controller-upgrade.md)执行，先检查活跃任务并使用排空与恢复流程。

旧 Web 界面通过 `make legacy-web` 保留兼容构建；它不再是默认操作入口，也不是本机闭环所需组件。

## 自动开发与交付边界

符合条件的 Issue 进入开发流程。CI、独立验收和来源校验分别提供证据，任务执行成功不代表 PR 已验收或已合并。自动合并仍受项目配置和必需检查约束。

当前闭环重点覆盖同仓 GitHub PR。CI 修复、验收修复保留重试预算；预算耗尽、证据不完整或需要范围决策时明确阻塞。无效审核报告不能授权代码修复或合并。现有 GitLab CI 运维功能保留，命令见[GitLab CLI 参考](docs/gitlab-cli.md)。

执行中断后保留 worktree、journal 和 Offload session 来源。不能仅凭 heartbeat 丢失就重派任务。恢复操作见[持久 worker runbook](docs/resilient-execution-runbook.md)，架构见[可恢复执行设计](docs/resilient-execution-design.md)。

## 当前范围

先验证正常交付、CI 修复、验收修复、重启恢复四条真实链路，再积累一周、至少 20 个真实任务的运行证据。多 controller、一万 worker、工作流市场和新增 Web 控制面不属于本期目标。

Operator 继续通过技能文件定义执行活动；历史 `classify`、`evaluate-bug`、`evaluate-feat`、`decompose` 阶段已退役，不需要恢复它们才能启动开发。

其他参考：[组件说明](docs/components.md) · [清理与保留策略](docs/housekeeping.md) · [候选版本隔离](docs/candidate-isolation.md)。

## License

MIT
