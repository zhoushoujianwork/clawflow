# 用 offload Go 包启动任务

状态：设计，对应 #698。实现细节见[详细稿](offload-library-execution-design-detail.md)。

## 目标

ClawFlow 启动任务时不再拉起 `offload` CLI 子进程，改为直接调用 `pkg/execution`。这样可以少一层进程和文件协议，结果和错误都是类型化的，也不用再探测 CLI 支持哪些参数。

Claude 和 Codex 原生进程仍然是子进程，这一点不变。变化的只是 offload 自身的代码跑在 ClawFlow 的 executor 进程里。

## 结论

可以迁移，但有两个前提：

1. **offload 要先提供孤儿清理接口。** executor 崩溃时，原生进程会活下来。现在靠 CLI 继承 checkpoint 锁挡住恢复，改用包之后这个办法失效。如果没有清理接口，恢复任务和旧进程可能同时写同一个 worktree。
2. **先用开关灰度，默认仍走 CLI。** 这是任务执行的核心路径，出问题时 ClawFlow 无法自己修复，所以要能立即切回去。

## 五个问题的答案

| 问题 | 答案 |
|---|---|
| 进程隔离 | Offload panic 或 executor 退出时，原生进程组不会被自动终止。Manager 已经能发现 executor 死亡，恢复前新增一步：调用 offload 的清理接口，确认旧运行已结束。 |
| 版本固定 | 不用再单独固定 offload 可执行文件，它已编译进按 SHA-256 固定的 worker generation。恢复检查改为比较执行引擎，不再比较 offload 路径。 |
| 环境隔离 | 仍然先用现有逻辑算出期望环境，再和当前进程环境比较，得出 `ProcessEnvironment` 的增加和移除项。过滤规则只有一份，不会分叉。 |
| 进度和结果 | `OnRunEvent` 走现有的校验和 checkpoint。`Run` 返回后，ClawFlow 立即原子写入结果文件。如果在写入前崩溃，就按“没有结果”恢复原生会话，和今天 CLI 中途死亡的处理一致。 |
| 升级路径 | 运行中的任务固定在自己那一代二进制上，不受影响。开关只影响之后新启动的任务。 |

## 使用方式

```yaml
settings:
  execution:
    engine: library   # 默认 cli
```

切回只需改回 `cli`，新任务立刻走旧路径。

## 实施顺序

1. **offload：** 新增清理接口并发布新版本。
2. **ClawFlow：** 升级依赖，实现 `library` 引擎，默认仍是 `cli`。验证两种引擎结果一致。
3. **灰度：** 先在隔离候选里跑 `library`，再由 owner 决定是否切生产。
4. **收尾：** 生产稳定运行一周后，删除 CLI 路径和这个开关。

## 本期不做

- 不改 offload 的 profile 和凭据模型。
- 不改算子、验收和修复的流程。
- 不引入 Agent-Host 的会话模型，只参考它的 `Request` 组织方式和错误映射。
