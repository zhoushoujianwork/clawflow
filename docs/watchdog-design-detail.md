# Watchdog：实现说明

范围和取舍以[主文档](watchdog-design.md)为准。本文记录设计依据和第一版约束，实现时可按代码现状调整。

## 现有基础

| 位置 | 现状 | 对设计的影响 |
| --- | --- | --- |
| `cmd/clawflow/commands/coordinator.go` reconcile 循环 | 每 30 秒恢复结果并对账 | 观察日志挂在这里，不新增 scheduler |
| `Store.Board` / `deriveBoard`（`internal/coordinator/board.go`） | 只读快照，推导 stage 和 `waiting_reason` | 规则以 Board 为输入 |
| `Store.Heartbeat`（`internal/coordinator/store.go`） | 只证明租约持有；durable 任务不因心跳续期 | 心跳不能代表进度 |
| `ExpireLeases`（`internal/coordinator/store.go`） | 普通任务最多 3 次基础设施重试；durable 任务跳过 | 不改变隔离语义，只观察停留时长 |
| `RecordApplicationFailure`（`internal/coordinator/effects.go`） | 写回失败 3 次后任务转为 failed | 不重复计数，只查长时间无变化 |
| `LiveStats`（`internal/execution/live_stats.go`） | 有 `Stalled`、`LastEventAt`，只覆盖本机，目前用于展示 | 不能作为 controller 对远端任务的判断依据 |
| App（`macos/Sources/ClawFlow/Models.swift`） | 已把 `recovery_pending`、`lease_expired` 和写回错误标为需要关注 | 复用现有展示，只补充原因 |

## 第一步：规则

新增纯函数，放在 `internal/coordinator`：

```go
func deriveFindings(now time.Time, board Board) []Finding

type Finding struct {
	TaskID string        `json:"task_id"`
	Rule   string        `json:"rule"`
	Since  time.Time     `json:"since"`
	Age    time.Duration `json:"age"`
	Reason string        `json:"reason"`
}
```

`Board` 增加只读的 `Findings` 字段，随快照计算，不落库。

| 规则 | 条件 | 起算时间 | 初始阈值 |
| --- | --- | --- | --- |
| `recovery_stalled` | stage 为 `recovery_pending` | `LeaseExpiresAt` | 30 分钟 |
| `finalizing_stalled` | stage 为 `finalizing` | `UpdatedAt` | 10 分钟 |
| `dispatch_stalled` | state 为 `pending`，且存在在线、未暂停、标签匹配、未满载、符合验收路由的 worker | `UpdatedAt` | 10 分钟 |

阈值是观察起点，不是已验证的值。先用代码常量，不提供用户配置；校准后如确有差异再调整。

实现时要核对：

- `dispatch_stalled` 的条件要与 `Store.Claim`（`internal/coordinator/store.go`）一致，包括 `LeaseLimits` 等限制。否则合理等待会被误报。
- `UpdatedAt` 是否只在有意义的状态变化时更新。如果不是，需要换起算时间。
- 重复失败不新增规则，由现有熔断和修复账本负责。

## 第一步：日志

reconcile 循环每轮读取 `Findings`，只在异常出现和消失时各写一条 `coordinator/watchdog` 日志。记录内容只有任务 ID、规则和持续时间，不写 payload、评论或凭据。

状态只保存在内存中，controller 重启后已有异常可能再记一次。这对观察校准没有影响，不为此新增持久化。Board 或日志失败时只写警告，不影响 reconcile 循环的其他工作。

## 第二步：远端进度

- worker 从自身的 Offload 执行事件取得最后事件时间，附在现有心跳请求中（`cmd/clawflow/commands/worker_run.go`、`internal/coordinator/http.go`）。
- 字段可选。滚动升级期间，旧 worker 不发送；controller 视为未知，不判定无进展。
- controller 保存该时间时，要校验 worker 身份和 fencing token，并拒绝明显超前的时间。
- 新增规则 `no_progress`：任务处于 `running`，已知的最后事件时间超过阈值。

长时间运行的测试可能不产生事件，所以第二步只观察。阈值用真实数据校准后才确定。还要核实 Offload `Stalled` 的判定语义，再决定是否参考它。

## 第四步的前置条件

自动处理不属于本期。将来启用时至少要满足：

- 有真实运行记录支撑阈值和误报率。
- 只调用现有恢复路径和预算，不新增重派逻辑。
- 显式取消的任务不恢复；无法确认安全时进入阻塞状态并给出原因。

## 测试

- `deriveFindings` 使用表驱动单元测试：覆盖阈值边界、暂停或满载的 worker、durable 与普通任务、未知时间。
- 日志转换测试：异常持续时不重复记录，消失时记录一次。
- 第二步的 HTTP 测试：旧 worker 无字段、错误 fencing token、超前时间。
- 涉及 Store 的测试使用现有的隔离 PostgreSQL 测试夹具。
