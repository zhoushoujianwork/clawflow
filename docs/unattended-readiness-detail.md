# 本机无人值守闭环：证据与待办

范围与验收线以[主文档](unattended-readiness.md)为准。

## 当前约束与验收状态（2026-11-03）

用户已于 11 月 2 日重新启用 Offload，允许本机开发核验及独立验收；下文早期的禁用／待授权记录仅是历史状态。自动调度保持启用，仍遵守既有 profile、来源、独立验收、准确 head CI 与修复预算门禁。不得把一次成功测试或一次部署写成一周无人值守验收通过。

| 验收项 | 本轮已核实证据 | 尚缺证据 |
| --- | --- | --- |
| 正常开发交付 | 保留任务和 PR 历史可查 | 对同一个真实任务贯通源码、CI、独立验收、合并终态，不能只看 task done |
| CI 修复 | #612 / #622 有真实修复与预算耗尽交接记录 | 成功修复后完整交付的准确 head 证据 |
| 验收修复 | #622 记录过验收修复，预算耗尽未重置 | 验收失败→有限修复→新独立验收通过→合并的完整链路 |
| 重启恢复 | 11 月 3 日 13:10:27 主机重启；#651 会话与 worktree 保留，锁已释放 | 原授权到期后同一 session 恢复、回传确认与后续交付；当前尚未验证 |
| 持续运行 | 尚未启动连续验收窗口 | 连续七天、至少 20 个已授权真实任务的成功／阻塞／人工介入与恢复记录 |

每条真实流程至少绑定 issue/PR、task ID、attempt/epoch、native session、准确代码 head、CI、独立验收及最终状态。人工修配置、手工推进、环境故障和恢复操作单独记录；不计作无需人工介入的自动成功。不通过补写来源、重置预算或更换会话制造闭环。

## 11 月 3 日真实重启现场

- 重启前 #651 / PR #652 的开发来源核验 task 为 `e2dc67c335cb2697618036b01ad577dcef4a128ea0318f8a98ce189bb04d3d59`，epoch 1、attempt 0；native session `1c875860-fe17-47fd-b7c9-230a9c26b5e2`，Offload run `7b8506048eb8ea2d95c0dc74fb6a8121`。
- 13:52 后只读核对：controller 和两个 worker 已在线；该任务 journal 为 `needs_recovery`，checkpoint 未完成，执行锁与会话锁均空闲；不能将看板残留的 running 阶段当作执行仍然存活。
- 原 worktree 和固定 Offload runtime 存在，原生会话历史存在；checkpoint 指向的系统临时输出目录已不存在。原授权到 `2026-11-03T14:07:29.850586+08:00` 才到期，未提前重派或改写 epoch。
- 重启前 journal、checkpoint、executor 日志、原生历史和只读观测保存在本机私有 `backups/restart-651-20261103/`，不包含复制的 worker 凭证。旧报告／会话和遗失文件的事实保持原样。
- 持久化缺口修复将 checkpoint 管理的执行文件移到任务自己的 `execution-artifacts/` 下；每次 invocation 独立目录。该代码尚未部署，不能宣称已找回本次丢失的旧文件。
- #652 为协调语义与围栏 JSON 修复；#650 为中断报告保存修复，明确等待 #651。两者仍需真实来源、独立验收和部署，#639 尚未恢复。当前不启动一周验收计时。


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


## 历史真实流程分类核对（2026-11-03）

只读看板与 15 份已完成 repair journal 对照：其中 12 份为 `development_verification`，1 份为 `ci`，2 份为 `acceptance`。不能把所有 repair/done 都计为 CI 修复或验收缺陷修复。私有分类明细保留在 `backups/restart-651-20261103/repair-flow-audit.json`。

#610 / PR #611 的历史证据：

- CI 修复 task `9ba86bf06ed206f49807ce2381981d0322c865f05723c058836ccece55463c63`、epoch 1、session `87728782-1d1c-4816-83c2-7c565b31fe0b`，输入 head `4666c027f73f0c680b17dfb1142003eebe358668` 的 [Test 失败](https://github.com/zhoushoujianwork/clawflow/actions/runs/41105570205/job/124234340438)，输出 head `17b92f515234737ae1f9fdf8e17187a306267a4d` 的 [Test 成功](https://github.com/zhoushoujianwork/clawflow/actions/runs/41115045367/job/124266946924)。该修复 head 的 Native macOS 检查为 cancelled，不能宣称当时全部 CI 已绿。
- 验收修复 task `65ad642e0740640b1247891fe22b5d295929c06a5ca0e309c3c2cbcf9e8c6cfb` 的输入、输出均为 `e1837c8a5016437654d88279bddd14b4e2b2a5e8`，`infra_kind=no-change`。这是重新核验，不是修改代码修复验收缺陷。
- 最终 head `e1837c8a5016437654d88279bddd14b4e2b2a5e8` 的三项 CI 均成功；本地 completed/passed 独立验收绑定开发 session `574b9ee0-e1fd-4513-90f2-7a0218abcf44` 与不同 reviewer session `839c3f3a-372e-4512-8b7d-50d53aa25b1e`，无 unstructured/validation_error。GitHub 确认 [PR #611](https://github.com/zhoushoujianwork/clawflow/pull/611) 于 2026-10-31 13:35:41 UTC 合并，merge commit `493ab0dde8d935c11ae8e4c17d4ac8c9f7627e77`；本地提交图确认 CI 修复 head 为最终 head 的祖先。
- 以上证明相关阶段有真实执行与最终交付记录，不证明全过程无人介入。期间的人工跟进见 [#611 记录](pr-611-merge-followup-2026-10-31.md)，不把此历史任务计入尚未启动的一周自动验收窗口。
