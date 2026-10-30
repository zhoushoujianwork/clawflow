# 运行独立 Worker

Native macOS onboarding is available through **Workers → Add Worker**. It creates an expiring invitation; `clawflow worker join` exchanges it for a private worker token. New invited workers start with assignments paused until resumed in the app. Existing token-based workers below retain their behavior. The independent `clawflow controller` service replaces Web lifecycle ownership; see the [native setup guide](native-macos.md).

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
任务的所有标签都必须满足；任务标签来自仓库 `worker_tags`、`worker_tags_by_kind` 和 issue 的 `worker:<tag>`。
平台类型和 VCS 地址不会自动成为任务标签；如需按平台隔离，应在仓库和 worker 两侧显式配置相同标签（如 `github`）。
使用 `--roles acceptance` 运行独立验收，或 `--roles operator,acceptance` 同时支持两类任务。

每个空闲槽位领取一个任务，空队列每 5 秒重试。执行期间每 30 秒续租；
续租失败（包括 HTTP 409）立即取消任务 context，停止上传该任务的结果、证据和 effects。
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

## 独立验收

协调器沿用已有 harvest 条件，在 CI 成功且缺少验收证据或允许基础设施重试时创建验收任务。
仓库配置 `worker_tags_by_kind.acceptance` 覆盖通用 `worker_tags`，issue 的
`worker:<tag>` 标签仍须全部匹配。coordinator 模式下，未配置的 `acceptance_worker`
默认为 `different`。显式 `any` 仍可读取，但同样不能把验收租给该 PR 的开发 worker；
会话独立性也始终强制检查。无在线且具备 `acceptance` 角色、所需标签的独立 worker 时，
任务保持排队，看板显示 `waiting for an independent reviewer`，不启动验收模型会话，
也不消耗重试次数。

最小拓扑为进程内 `local-worker` 承担开发，加至少一个独立身份的 review worker。
即使 `local-worker` 声明了 `acceptance` 角色，也不能验收它自己开发的 PR。
例如 coordinator 的配置：

```yaml
settings:
  coordinator:
    enabled: true
    local_worker:
      enabled: true
repos:
  owner/repo:
    acceptance_required: true
    acceptance_worker: different
    worker_tags_by_kind:
      acceptance: [review]
```

为 reviewer 创建独立 token（`clawflow worker token create --name review-worker`），
在其机器上配置仓库、凭据及 Offload profile，然后运行：

```sh
clawflow worker --coordinator https://HOST:8093 \
  --token-file /Users/USER/.clawflow/review-worker.token \
  --fingerprint SHA256 --roles acceptance --tags review \
  --state-dir /Users/USER/.clawflow-review-worker
```

review worker 可与 coordinator 位于同一主机，但必须使用独立身份、token 和状态目录。

成功的 implement/repair 回传原生 `development_events` JSONL（最多 32 MiB，仅元数据）。
协调器验证租约、PR head 和原生事件身份后，将其以 `0600` 权限写入对应运行目录。
缺失、截断或无效证据不会成为可信开发溯源；验收门禁保持关闭。

验收 worker 须配置已有本地 checkout 的 `local_path`，用于验证仓库 origin；实际验收使用协调器生成的契约，在本机临时目录运行 planner 和具备命令执行能力的 reviewer。
计划和验收记录通过带 fencing token 的完成请求上传。协调器复核当前契约、摘要、原生身份、
执行命令证据及不同 worker 策略后保存记录；原有协调器流程继续处理失败验收修复和条件合并。
worker 不决定合并，基础设施失败记录仍受现有重试次数和冷却时间限制。

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

## 主机亲和性迁移

每个 worker 自动注册 `host-<hostname>`（转为小写并去掉结尾 `.local`）。
在仓库配置中添加 `worker_tags: [host-work-mac]`，即可只让匹配主机领取任务。
如设置 `worker_tags_by_kind`，该类型的能力标签覆盖仓库默认标签；仓库 host 标签仍保留，保证主机约束适用于每类任务。

旧 `bound_machine: Work-Mac.local` 自动追加 `host-work-mac` 到 `worker_tags`，
迁移提示每进程只显示一次；`require_binding` 忽略并提示。保存或同步输出时省略两字段，保留其他设置。
`clawflow repo set owner/repo --bound-machine Work-Mac.local` 仍可使用，但会提示弃用并替换 host 标签；
空值清除 host 标签，保留其他能力标签。`repo show` 显示 `worker_tags`。

coordinator 的扫描、定向运行、harvest 不受机器绑定限制。未启用 coordinator 时保持本机运行能力，
不会因为迁移的 host 标签跳过仓库；主机限制在 worker 领取任务时生效。

## 平台主机与客户端调度

设置 `settings.platform.url` 的客户端不会本地调度：`clawflow run` 和 Web 自动调度均拒绝执行并提示平台地址。
`repo enable` 通过正常客户端命令转发给平台；本地配置的启用入口拒绝写入。
`clawflow worker` 仍可运行。一次性调试可用 `clawflow run --local`，命令会警告并绕过平台执行本地运行。

平台主机启用 coordinator 后只扫描、入队、收集结果和决定合并，算子及验收执行由 worker 领取。
已有 enabled 仓库继续参与调度，无需重新注册。主机需要贡献执行能力时，可启用同进程 worker：

```yaml
settings:
  coordinator:
    enabled: true
    local_worker:
      enabled: true
      max_parallel: 2
      tags: [github]
```

随 `clawflow web` 启停的本地 worker 使用普通 HTTPS API、证书指纹和独立 worker token；
token 保存在状态目录 `data/coordinator/local-worker.token`，权限为 `0600`，不要提交或同步。
`local_worker` 默认关闭，`max_parallel` 默认 1。任务仍必须匹配全部标签，不会回退到主机直接执行。
例如 `worker:creds-laptop` 任务及其验收，只能由带有 `creds-laptop` 标签的 worker 领取。
仅领取凭据任务可用 `clawflow worker --tags creds-laptop --run-untagged=false`（另需提供连接参数）；
若仓库另有显式标签要求，worker 也必须携带这些标签。
未匹配的任务保持 pending，看板显示等待的标签；任务表显示持有租约的 worker，仓库栏显示所属平台。
全局 `max_concurrent_agents` 和仓库 `max_in_flight` 在平台领取租约时执行，跨所有 worker 共享。
