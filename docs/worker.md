# 运行独立 Worker

Worker 使用本机 `~/.clawflow/config/` 中的 VCS 凭据和 Offload execution profile，
不从 coordinator 接收凭据。先在 worker 上配置需要执行的仓库及其 `local_path`。
若映射中没有 `local_path`，worker 会在状态目录的 `clones/` 下自动克隆；
已有映射但路径不存在时，使用本机 clone 协议与凭据克隆至该路径。

在 coordinator 上用 `clawflow worker token create --name mac-worker` 创建 token，
将输出的 token 保存为 worker 本机权限为 `0600` 的文件，并记录显示的 SHA-256 指纹。

```sh
clawflow worker --coordinator https://HOST:8093 \
  --token-file /Users/USER/.clawflow/worker.token \
  --fingerprint SHA256 \
  --tags github,macos --roles operator --max-parallel 2 \
  --state-dir /Users/USER/.clawflow-worker
```

`--run-untagged` 默认开启，可用 `--run-untagged=false` 禁止无标签任务。
Worker 自动添加小写且去掉 `.local` 后缀的 `host-<hostname>` 标签。
任务的所有标签都必须满足；平台标签（如 `github`）需通过 `--tags` 显式声明。
本切片仅支持 `operator` 角色，acceptance 角色属于后续切片。

每个空闲槽位领取一个任务，空队列每 5 秒重试。执行期间每 30 秒续租；
续租失败（包括 HTTP 409）立即取消任务 context，并尽力回传 `lease-lost`，不回传失效 effects。
执行前校验本地 operator 的 `skill_digest`；不同版本或用户覆盖导致不一致时拒绝执行，
需先对齐 worker 与 coordinator 的算子版本。正常结果及 effects 使用原 fencing token 回传。
连接或结果上报失败会退出，由服务管理器重启；未成功回传的任务由 coordinator 的租约回收处理。

`--state-dir` 优先于 `CLAWFLOW_STATE_DIR`。两者都未指定时沿用 `~/.clawflow`。
指定后锁、`data/`（含 `runs/`）、worktree 以及无显式 clone 路径时的 clone 均在该目录下。
配置、凭据、自定义 skills 仍从正常 home 读取。与 coordinator 共用主机时应指定独立目录；
两个独立 worker 也应分别使用不同目录和 token。显式配置的 clone 路径保持不变。

## 只读 Worker pool 看板

启用 coordinator 后，`/pipeline` 展示 workers、任务表及最近一小时 done / failed 数量。
数据通过现有 `GET /api/pipeline` 的 `worker_pool` 字段返回，LAN readonly 会话无需 worker token。
未启用时该字段为 `null`；数据库读取失败时返回通用 `worker_pool_error`，不暴露连接信息。

Worker 名称来自注册身份；`live` 表示 token 未撤销且最近 120 秒内有认证请求
（包括空闲时领取任务的轮询），否则为 `stale`。`last_heartbeat` 单独展示最近一次成功续租时间，
从未续租的 worker 显示为空。运行任务仅包含尚未过期的租约；租约过期显示 `lease_expired`，
已收到结果且等待 coordinator 写回的任务显示 `finalizing`。看板读取不回收或修改租约。
任务表的重试次数直接使用存储的 `attempt`（初始值 0，租约回收时增加）。
吞吐量统计终态任务的 `updated_at` 落在 `(当前时间 - 1 小时, 当前时间]` 内的数量，不包含 cancelled。
接口仅投影调度元数据，不返回 token/hash、fencing token、任务 payload 或 result。

## macOS LaunchAgent

将以下模板保存为 `~/Library/LaunchAgents/com.clawflow.worker.plist`。
替换 USER、HOST、SHA256 和可执行文件路径；PATH 中须包含本机 Offload、Git 及所需工具。
不要将 token 内容写入 plist 或仓库。

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.clawflow.worker</string>
  <key>ProgramArguments</key><array>
    <string>/usr/local/bin/clawflow</string><string>worker</string>
    <string>--coordinator</string><string>https://HOST:8093</string>
    <string>--token-file</string><string>/Users/USER/.clawflow/worker.token</string>
    <string>--fingerprint</string><string>SHA256</string>
    <string>--tags</string><string>github,macos</string>
    <string>--max-parallel</string><string>2</string>
    <string>--state-dir</string><string>/Users/USER/.clawflow-worker</string>
  </array>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>/Users/USER/Library/Logs/clawflow-worker.log</string>
  <key>StandardErrorPath</key><string>/Users/USER/Library/Logs/clawflow-worker-error.log</string>
</dict></plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.clawflow.worker.plist
# 停止服务（SIGTERM 会取消正在执行的任务）
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.clawflow.worker.plist
```
