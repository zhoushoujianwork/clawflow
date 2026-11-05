# 用 offload Go 包启动任务：详细稿

主文档见[这里](offload-library-execution-design.md)。

## 适配边界

`internal/execution.Run` 和 `Execute` 的对外签名不变，调用方（`internal/operator`、验收）不用改。引擎在 `Run` 内部选择，两种引擎都返回同样的 `Result`。

## 参数映射

| CLI 参数 | 包 |
|---|---|
| `-p` | `Config.ProfileName`，配置来自 `LoadConfig`；未指定时用 `DefaultProfile` |
| `-json` | 类型化 `Result` |
| `-no-salvage` | `AllowSalvage=false`，默认值 |
| `-d`、`-t`、`-m`、`-w` | `Directory`、`Timeout`、`Model`、`Write` |
| `-r` | `NativeSessionID` |
| `-caller-run-id` | `CallerRunID` |
| `-events-file` | `OnRunEvent` |
| `-instructions-file` | `InstructionsFile` |
| `-execution-policy`、`-codex-sandbox` | `ExecutionPolicy`、`CodexSandbox` |
| 工具参数 | `ToolsDisabled`、`AllowedTools`、`DisallowedTools` |
| `-capture-transcript` | `Monitor.CaptureTranscript` |

不要用 `OnEvent`：它是原始输出，不是可公开的遥测。

## 错误映射

| `RunError.Kind` | ClawFlow 处理 |
|---|---|
| 取消 | 现有的取消路径 |
| 父 context 超时、`timeout` | 现有的超时路径 |
| `configuration`、`unsupported_tools`、`launch` | 启动失败，属于基础设施失败，不消耗修复额度 |
| `globally_disabled` | 启动失败，原因单独显示 |
| 其他 | 现有的传输或协议失败 |

## 环境

1. 现有函数算出期望环境：移除 `CLAWFLOW_STATE_DIR`，写入 effect spool，替换测试 DSN，写入 GitHub App 的 `GIT_CONFIG_*`；验收再过滤 token 和私有 Go 缓存。
2. `environmentDelta(os.Environ(), desired)` 生成 `Set` 和 `Unset`。
3. Offload 自己持有的凭据和 home 变量不能修改。冲突时返回启动失败，不静默丢弃。

## 进度和结果

- `OnRunEvent` 是同步、有序的。它复用 `events.go` 的校验：caller 和 run ID 关联、序号严格递增。run ID 和 session 进 checkpoint 的时机和现在相同。
- 结果写入 `result.json`，权限 0600，先写临时文件再 rename。恢复时的“已有完成结果等待对账”检查同时认这个文件和旧的 `stdout.json`。
- 最终回答只取自 `Result.Report`，绝不从进度事件恢复。
- Library 模式没有 CLI 模式读取 stdout 时的实时大小截止；offload 包会先把 report 保存在内存里，返回后 ClawFlow 再按同一个结果大小上限拒绝过大的输出。

## 恢复

Checkpoint 增加 `Engine` 和 `RunID`。

| 引擎 | 恢复前的判断 |
|---|---|
| `cli` | 现有逻辑不变：checkpoint 锁已释放，命令和固定的 runtime 一致 |
| `library` | checkpoint 锁已释放，调用 offload 清理接口并确认旧运行已结束。清理结果不确定时不恢复，标记为需要处理 |

其余检查不变：没有未对账的结果、session 存在、worktree 身份一致、旧 fencing 已过期。

Executor 的锁不再被子进程继承。锁释放只说明 executor 死了，原生进程可能还活着，所以清理这一步不能省。

接受的差异：library 模式下 executor 死亡时，原生进程会失去 supervisor，该次运行的最终结果也会丢失。恢复流程先结束旧 run，再从已有的原生 session 恢复，因此保持单写入者，但无法对账已死亡 attempt 的结果。

## offload 需要提供的接口

调用方崩溃后，用 run ID 发现并结束遗留的原生进程树。要求：

- 不依赖调用方进程存活。
- 校验进程身份，防止 PID 或进程组 ID 被复用后误杀。
- 幂等。运行已结束时返回“已结束”。
- 结束后释放对应的 worker slot。

## 候选和灰度验证

- 同一个任务夹具分别跑两种引擎，比较结果字段、事件序列和退出分类。
- 真实崩溃测试：运行中 `kill -9` executor，确认旧进程组被清理，恢复只有一个写入者。
- 超时、取消和全局禁用都要在 `library` 引擎下有测试。

## 影响的文件

- `internal/execution`：`client.go`、`events.go`、`checkpoint.go`、`transcript.go`、`readiness.go`、`capabilities.go`，新增 `library.go`。
- `cmd/clawflow/commands`：`worker_durable.go`、`worker_recover.go`。
- `internal/config`：`settings.execution.engine`，保存和同步时要保留。
- 测试：现有 CLI 参数测试保留，新增引擎对照测试。
