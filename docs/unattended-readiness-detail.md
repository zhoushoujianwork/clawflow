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

## 本机阻塞核验与第一项修复

- launchd 确认 native controller PID 19445，LastExitStatus=0，入口为标准目录下的 bin/clawflow。旧 web 服务的 stopped 状态与它无关。
- #622 查询时 head 为 `fff2ab05d2153d5e45fd0f2e488a466a25602ca5`，仍 open。当前 acceptance status 退出 1，原因是最新开发记录缺失、未完成或不成功。此前循环日志为 unstructured report；两者不能混成“只是等待审核”。
- 修复账本已用完三次预算：development_verification 一次，acceptance 两次；另有一次 launch 失败释放了预留。未清空预算、解除 agent-failed 或重跑模型。扫描仍继续，单项阻塞未停止整个调度器。
- 修复同步暂停标记的迁移兼容：只接受安装器标准 Application Support/ClawFlow 的绝对别名；仍拒绝任意别名、符号链接父目录及链接标记。暂停标记继续位于根目录，保持 reset 归档边界。
- 新测试在旧代码上真实失败（refusing symlink），修复后定向测试通过。正式安装 CLI 尚未替换；此次修复不能写成已上线。
- 定向 race 检查通过；以新源码执行只读 `sync status` 返回 `sync paused: true`（退出 0）。保留实际暂停状态，没有 resume。
- 实际模型验收所需 Offload 例外授权已询问，未收到授权前不启动或恢复任务。

## 已安装版本对齐

本地合并 `a22c49ee`（合并提交 `0f48f8e6`），保留已安装版本的候选隔离、基础设施修复及菜单栏改进。合并无冲突；与安装版本的净差异只有同步暂停修复及本目标文档，避免从旧分支部署时丢失线上功能。净差异检查与合并后定向 race 测试通过。

local-worker 当前 generation 的固定 CLI 实测版本同为 `0.75.0-dev.20261031155048.a22c49ee`。该核对不代表其他 worker 或所有历史 executor 都已验证。

读取 PR #622 评论成功，但未找到 `repair-exhausted` 标记。仍须核查 issue #612 评论和通知入口，不能仅凭预算账本认定人工交接已经完成。此次未发消息、未改预算、未部署或重启。

## 耗尽交接已确认；测试缓存污染已定位

issue #612 的评论包含 `clawflow:repair-exhausted=pr-622`，明确三次预算用尽、最后失败为 `no-change repair left local changes`、未合并并需要人接手。通知没有丢失，只是发布在 issue；代码先发布通知再添加失败标签。

任务 `2f570cce...` 的保留工作树 `local-worker/data/ci-repair-worktrees/repair-809726528` 经只读检查，源码 diff 为空，仅有未跟踪 `macos/__pycache__/`，包含 install-host 与 test_install_host 的 Python 3.11 字节码。该缓存触发工作树清洁门禁；未删除现场或重置预算。

补充精确的 `/macos/__pycache__/` 忽略规则。隔离 Git 夹具证明旧规则报告未跟踪缓存，新规则保持清洁；源码文件仍不被忽略，diff 检查通过。此修复尚未进入 #622 的当前提交，也未将其失败记录改成成功；后续交付必须保留新 head 的 CI 和独立验收要求。
