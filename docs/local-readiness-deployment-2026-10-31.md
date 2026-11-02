# 本地 Offload 就绪检测部署记录

2026-10-31 02:09–02:11（+08），按用户“部署啊”的授权，完成本机 controller、两条 worker admissions 和 `/Applications/ClawFlow.app` 更新。

## 部署版本

- 源码提交：`ca335a8370c42715d56a25ba0856cab1f91175df`。
- 版本：`0.75.0-dev.20261030180843.ca335a83`。
- 服务二进制 SHA-256：`f4d785f3879c68e096b5ba8eb1714418e9ef23514b54ba0a67f2ed30f328288e`。
- 该提交同时包含已上线自动恢复提交 `6516902b` 和 #618 合并提交 `453883f6`，已通过 Git 祖先关系核对。未部署尚未合并的 #609、#611 或 #617。
- 在独立工作树合并并提交后，将原工作分支快进至该提交，避免后续从旧代码构建并覆盖本地服务。独立工作树保留在 `/tmp/clawflow-deploy-readiness-20261031`。

## 兼容修复与验证

直接用 main 会丢失本机已上线的自动恢复补丁，因此保留双方实现。合并时还将 Offload 门禁接入新版 durable 循环：环境阻塞时继续注册并处理已存结果，不领取新任务；环境健康时正常领取。保留 `inspect`、`release` 两个端点、历史回执归档和 `pending_release` 重放。

扩展现有门禁用例后，未修正的组合版本在 durable 路径复现了 13 次不应发生的 claim；修复后 legacy/durable 的阻塞与健康路径均通过，相关恢复/claim race 检查通过。

- 临时 PostgreSQL 17 上完整 `go test -race -json ./... -count=1 -timeout 6m`：3,219 项通过、1 项失败、0 项跳过，命令退出 1；不能称整套全绿。
- 唯一失败为 `TestTimeoutRetainsValidatedFinalIdentity`：其 1 秒时限内未收到测试子进程身份。该测试文件与已部署旧版本相同；随后隔离 race 复测连续 3 次通过，未修改或跳过该用例。
- 相关四包 `go vet` 通过；Swift 严格并发检查 26 项通过。
- `make dev-build` 通过，App 和 bundled CLI 版本一致，签名严格校验通过。
- 完整 Go 测试原始记录及摘要位于 `/var/folders/64/xyvthqtj4xqb6xp70kw9hgf80000gq/T/clawflow-deploy-verify-wuuakgjg`；临时数据库已停止。

## 实际切换与连续性

先通过 dashboard drain 暂停两个 worker 的新领取，再原子替换独立服务入口，向 controller 和 launchd worker admissions 分别发送 SIGTERM，由既有 launchd 加载新二进制。控制器启动的 local manager 按已支持的 generation 流程滚动更新，没有终止 executor。

- Controller PID：`91217 → 64169`；launchd worker PID：`91406 → 64450`；两者运行且最后退出码为 0。
- 两个 generation descriptor 的二进制摘要均为上述部署摘要，durable protocol 为 1，逻辑容量维持 4 和 1。
- drain 前刚领取的 #612 任务 `bfd49a332d4e4f1bf406f55e5df1a1cbc7420df7e4a4b3563944bc6f8a3b94b8` 持续运行：executor PID `59667`、epoch 1、旧固定执行器和 Offload 路径不变。更新后的 controller 在 02:10:29 接受了它的新心跳。
- 该任务 checkpoint 仍使用 CC profile `claude-claude-sonnet-5-1m-inherit-inherit`，原生会话 `4f9aa594-74ef-4765-b84b-b143faffaae4`，未启动替代执行。
- 两条 worker 均 live，原先的未暂停状态已恢复；`worker executions` 无 `recovery_blocked` 条目。
- PostgreSQL PID 保持 `61176`，未重启数据库。认证/profile 配置和 token 文件未修改。

## App 与就绪状态验收

使用 dev 构建产物更新安装包；通过 CUA 仅退出和启动 ClawFlow 查看器，没有调用其服务停止/重启按钮。App 与 bundled CLI 均显示部署版本。

实际设置页显示两个 worker 的 Offload 状态为 ready，点击 Recheck Offload 后结果一致。CLI 的只读 `execution status --json --recheck` 确认服务环境中的命令、必要 flags 和所选 CC profile 有效；它不验证实时认证，也未用模型请求做 smoke test。

截图：`/tmp/clawflow-deployed-readiness-20261031/settings.png`；辅助功能树：同目录 `settings.ax.txt`。主界面继续显示 #612 正在执行和新的 controller 版本。

## 备份与恢复边界

私有备份目录：`~/Library/Application Support/ClawFlow/backups/readiness-20261031-1r04g6eq/`，保留旧 CLI、旧 App、配置、LaunchAgent、generation descriptor、643,630 字节的 PostgreSQL 自定义格式备份，以及切换前后快照、任务连续性和部署结果记录。旧 generation、journals、worktrees 和运行时均保留。

首次数据库备份因未提供现有认证而失败，未更改生产数据库；随后在同一目录使用现有连接信息完成备份，认证内容没有写入命令输出。备份不意味着可以盲目回滚：恢复前须 drain 并核对 durable journals、pending_release 和数据库兼容性，不用旧数据库覆盖运行中的新结果。
