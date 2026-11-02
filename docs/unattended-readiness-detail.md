# 本机无人值守闭环：证据与待办

范围与验收线以[主文档](unattended-readiness.md)为准。

## 2026-11-02 首轮只读基线

- 工作树干净，分支 `fix/issue-627`，HEAD `a0cf2e67`。
- 安装 CLI 报告 `0.75.0-dev.20261031155048.a22c49ee`。不能据此断言 controller 和全部固定 worker executor 版本一致。
- 用户明确只在本机调度。部门 Mac mini SSH 超时不构成本轮阻塞。
- 本机 `config show` 显示 ClawFlow、Offload 仓库 enabled；未修改任何开关。新任务执行仍须遵守当前 Offload 禁用约束，不能借验收绕过。
- `queue list --json` 返回 #612 为 `parked:acceptance_stale`。
- #612 历史显示已创建 PR #622；11 月 2 日有 CI 修复成功、阻塞和失败记录，最近失败结束于 04:23:06 UTC。执行成功不等于 CI 或验收通过。
- local-worker 的 journal 中 #612 最近回执为 acknowledged；回执只说明报告已确认，不说明交付成功。
- `web service status` 为 loaded=false。该命令检查旧 web 服务，不能据此断言 native controller 停止。
- `sync status` 拒绝 `~/.clawflow` 符号链接；该链接指向迁移后的 Application Support/ClawFlow。`internal/sync/pause.go` 固定检查旧目录，而配置路径已支持迁移别名。需核对 reset 的安全边界再修复，不直接放开任意符号链接。
- `platform status` 报平台必须为 HTTPS 源地址；本地调度不依赖此远程入口，暂不修改连接配置。

## 下一步

1. 核对本机 controller/worker 服务身份及固定版本，保留运行任务。
2. 读取 #622 准确 head 的 CI、独立验收和修复预算，判定 stale 的真实原因。
3. 优先修复影响自动收尾的确定缺陷，先复现再做针对性验证、提交与必要部署。
4. 为四类真实流程记录 task、epoch、代码 SHA、CI/验收身份及终态；缺证据保持 pending。

本次未启动模型、第二调度器或重启服务，未变更线上状态。
