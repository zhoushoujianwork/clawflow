# Watchdog：实现说明

范围和取舍以[主文档](watchdog-design.md)为准。本文记录已实现的行为和仍需验证的内容。

## 现有基础

| 位置 | 现状 | 对设计的影响 |
| --- | --- | --- |
| `Store.Board` / `deriveBoard`（`internal/coordinator/board.go`） | 只读快照，推导 stage 和 `waiting_reason` | 规则以 Board 为输入 |
| `Store.Heartbeat`（`internal/coordinator/store.go`） | 只证明租约持有；durable 任务不因心跳续期 | 心跳不能代表进度 |
| `ExpireLeases`（`internal/coordinator/store.go`） | 普通任务最多 3 次基础设施重试；durable 任务跳过 | 不改变隔离语义，只观察停留时长 |
| `RecordApplicationFailure`（`internal/coordinator/effects.go`） | 永久性写回错误 3 次后任务转为 failed；任务已成功时的暂时性错误（超时、网络错误、HTTP 5xx）改走 `RecordDeferredApplication`，不计数、不转 failed | 不重复计数，只查长时间无变化 |
| `LiveStats`（`internal/execution/live_stats.go`） | 有 `Stalled`、`LastEventAt`，只覆盖本机，目前用于展示 | 不作为 controller 对远端任务的判断依据 |
| App（`macos/Sources/ClawFlow/Models.swift`） | 显示 `waiting_reason` | 原因写入这个字段，不改 App |

## 规则

规则在 `internal/coordinator/watchdog.go`，由 `Store.Board` 随快照计算，不落库。结果写入 `Board.Findings`，原因同时加到任务的 `waiting_reason` 前面。

| 规则 | 条件 | 起算时间 | 初始阈值 |
| --- | --- | --- | --- |
| `recovery_stalled` | stage 为 `recovery_pending` | `lease_expires_at` | 30 分钟 |
| `finalizing_stalled` | stage 为 `finalizing`，没有写回错误 | `updated_at` | 10 分钟 |
| `result_stuck` | stage 为 `finalizing`，且 `application_error` 非空（写回在失败或在退避重试） | `updated_at` | 10 分钟 |
| `dispatch_stalled` | state 为 `pending`，且 Claim 的容量和路由检查都会放行 | `updated_at` | 10 分钟 |
| `no_progress` | stage 为 `running`，且已知最后事件时间 | `last_event_at` | 30 分钟 |

阈值是观察起点，还没有真实数据验证，所以先用代码常量，不提供用户配置。

`dispatch_stalled` 对齐 `Store.Claim` 的检查：平台总量、仓库并发、worker 在线、未暂停、未满载、标签和验收路由。平台限制读取失败时跳过这条规则，宁可漏报，不误报。重复失败不新增规则，仍由现有熔断和修复账本负责。

## 循环和日志

`serveCoordinatorWatchdog` 是独立的 goroutine，每 30 秒读一次 Board。它不与 reconcile 共用循环，否则对账卡住时 watchdog 也会一起停。

日志写入 `~/.clawflow/logs/watchdog.log`，只在异常出现（`event=open`）和消失（`event=resolved`）时各写一条，内容是任务 ID、仓库、编号、规则和持续时间，不含 payload、评论或凭据。不复用 `runLog`：它只在 `clawflow run` 中赋值，在 controller 进程里是 nil，写入会被静默丢弃。

状态只保存在内存中，controller 重启后已有异常可能再记一次。这不影响观察校准，因此不新增持久化。

## 远端进度

- **接口：** 独立的 `POST /worker/v1/tasks/<id>/progress`，请求体为 `fencing_token` 和 `last_event_age_sec`。
- **不放进心跳：** controller 的解码器拒绝未知字段，而 worker 在心跳失败时会取消任务。把字段加进心跳，新 worker 连旧 controller 时会误杀运行中的任务。
- **尽力上报：** 每次心跳成功后上报一次，超时 5 秒，不重试。旧 controller 返回 404 或其他任何失败都被忽略，也不影响任务。
- **发送时长而非时间戳：** controller 用自己的时钟换算 `last_event_at`，worker 时钟偏差不会造成误判。
- **校验：** 校验 worker 身份和 fencing token，时长超出 0 到 86400 秒时拒绝，时间不会倒退。上报不续租约，也不写审计记录。
- **新尝试清零：** `Claim` 和 `RecoverExecution` 会清空 `last_event_at`，旧尝试的时间不会造成误报。
- **进度来源：** `execution.Run` 每收到一条通过校验的 Offload 事件就更新 `execution.Progress`，它通过 context 传递。Offload 启动时也算一次，所以一直没有事件的进程同样会被发现。没有 Offload 在运行时（如 Git、CI 阶段）时长为 0，这些阶段由各自的超时负责。
- **兼容性：** 旧 worker 不上报，`last_event_at` 保持为空，视为未知，不判定异常。

数据库迁移 12 只新增一个可空列 `coordinator_tasks.last_event_at`。

## 验证情况

已验证（测试使用临时 PostgreSQL）：

- 规则的阈值边界、暂停或满载的 worker、平台和仓库上限、标签不匹配、未知进度。
- 日志转换只记一次；Board 读取失败后循环继续。
- worker 在 progress 被 404 拒绝时继续运行和上报，任务不被取消。
- 真实 `execution.Run` 配合 Offload 测试夹具时能更新进度。
- 端到端：真实 Store、HTTP 接口、循环和日志，覆盖四条规则的出现和两条的消失；健康任务不误报；任务状态、fencing token、尝试次数和租约全程不变。

尚未验证：

- 真实 controller 和 worker 进程的长时间运行，以及安装后的滚动升级。
- 原生 App 的实际显示。这里没有改动 App，只依赖它已有的 `waiting_reason` 展示。
- 真实任务上的误报率。长时间运行的测试可能不产生事件，`no_progress` 的 30 分钟阈值最需要校准。
- Offload `Stalled` 的判定语义。目前未使用。

## 自动处理的前置条件

自动处理不属于本期。将来启用时至少要满足：

- 有真实运行记录支撑阈值和误报率。
- 只调用现有恢复路径和预算，不新增重派逻辑。
- 显式取消的任务不恢复；无法确认安全时进入阻塞状态并给出原因。
