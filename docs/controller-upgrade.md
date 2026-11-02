# Controller 与 worker 的滚动升级（`clawflow upgrade`）

状态：随 #614 实现，仍需 CI 与独立验收。范围：CLI 与后台服务；不涉及 Web UI，不重启原生 App。

## 解决的问题

此前升级 controller 只能「构建、迁移、替换二进制、重启服务」，正在运行的任务是否受影响全靠运气，失败后也没有经过验证的回退路径。`clawflow upgrade` 把升级拆成可观察、可恢复的阶段：先排空，再切换，再验证，失败时只在可证明安全时回退，并如实记录中断时长。

## 保证与不保证

| 项 | 保证 |
| --- | --- |
| 调度权 | 任意时刻至多一个 controller 持有调度与租约。切换在持有 `controller.lock` 的栅栏内改写服务定义，旧进程退出且锁释放前不会启动新进程。 |
| 运行中的任务 | durable 执行器是独立进程，controller 重启不终止它们。升级不取消、不重放任务；切换前后逐项比对租约 owner、fencing token、`attempt` 与 `application_attempts`，任何变化都使验证失败。 |
| 结果与写回 | 已接受但未落实效果的结果（`result-accepted`、待处理 effect intent）必须在停机前处理完。同一结果的第二次写回由 fencing 拒绝。 |
| 内嵌 worker | 与 controller 进程绑定的（非 durable）租约必须先自然结束；超时则「推迟」，服务保持不变，任务不被取消。 |
| API 中断 | 单机单 controller 切换**有**短暂中断，**不是零停机**。记录从停机前最后一次健康探测到启动后首次健康探测的时间，分辨率为 `--poll`。 |
| 原生 App | App 是独立客户端，本命令不重启、不观察它。只记录它读取的 dashboard 端点恢复时间（`viewer_endpoint_recovery_ms`）；`viewer_app_observed` 恒为 `false`。 |
| 数据库回退 | 二进制回退**不是**数据库回退。见下文「回退资格」。 |
| 不支持 | 两个 controller 重叠运行的 HA、自动打开 `self_upgrade`、切换到 SQLite、停止正式数据库。 |

## 门禁

`plan`、`controller`、`workers` 都要求：

- `--evidence`：该提交的隔离候选证据（`clawflow candidate down` 写出），由 `candidate.Verify` 校验提交、运行时身份与 `passed` 状态，且 `binary_sha256` 必须等于 `--binary` 的实际摘要；
- `--ci-ref`、`--acceptance-ref`：必需 CI 与独立验收的引用（由人提供，本命令只检查非空并写入记录，不据此推断已通过）；
- `--approve`：必须等于 `--commit`（40 位完整 SHA）；
- 目标二进制必须提供 `upgrade manifest`。缺少该命令的旧二进制不能作为目标；作为被替换的旧版本时，其 schema 级别按其正在使用的数据库推断，并在记录中标注 `inferred`。

## 数据库迁移：expand/contract

新旧进程会共用同一数据库，因此：

1. **expand（默认）**：只做新增（表、可空列、带默认值的列、索引）。旧二进制必须仍能正常运行。`store.Migration.Contract == false`。
2. **contract**：删除、改名、改类型、无默认值的 NOT NULL。必须标记 `Contract: true`，并且**晚于**停止使用旧结构的版本发布。
3. 升级引擎在改动任何东西前运行 `CheckTransition`：目标含待执行的 contract 迁移、数据库 schema 高于目标、目标 schema 或 worker 协议低于运行中版本，一律拒绝，服务保持不变。
4. 迁移在停机前、旧 controller 仍在服务时执行（每个迁移一个事务）。迁移失败则当前服务不受影响。

发布顺序示例：版本 N 新增列并开始写入；版本 N+1 停止读取旧列；版本 N+2 以 `Contract: true` 删除旧列。N+2 不能与 N+1 之前的二进制共用数据库，因此不可通过滚动升级直接推进。

## 回退资格

回退只重启预先保存的旧二进制，数据库保持升级后的 schema。仅当 `CheckRollback` 肯定地证明安全时才执行，否则**失败即关闭**：

- 旧二进制的 schema 级别未知 → 不可回退；
- 数据库 schema 高于升级后的二进制（别处迁移过）→ 不可回退；
- 升级期间应用了 contract 迁移 → 不可回退；
- 旧二进制的 worker 协议低于目标且存在 durable 任务或未落实结果 → 不可回退。

不可回退时结果为 `rollback-blocked`：派发保持暂停，**不要**用旧二进制启动此数据库；向前修复，或从已验证的备份恢复数据库。新旧二进制均保存在 `<state>/upgrade/artifacts/<sha256>/clawflow`（内容寻址、只读，读取时校验哈希）。

## 阶段

控制器升级：`preflight` → `artifacts` → `migrate-expand` → `quiesce` → `settle` → `stop-old` → `fence` → `install` → `start-new` → `verify` → `resume`。失败后回退：`rollback-stop` → `rollback-fence` → `rollback-install` → `rollback-start` → `rollback-verify` → `rollback-resume`。

- 停机前（至 `settle`）任何失败：服务原样运行，仅恢复**本次升级暂停**的 worker（操作员早先暂停的保持暂停），结果 `failed` 或 `postponed`。
- `settle` 与 worker 排空的截止时间写入记录；到期即「推迟」，不取消、不重放，也不重置修复预算（`attempt`、`application_attempts` 在切换前后逐项比对）。
- `stop-old` 起每个阶段先落盘再执行。进程在任一边界崩溃后，`resume` 用观察到的宿主状态判定当前持有者（`prior`/`target`/`none`/`ambiguous`），已完成的阶段不重复。运行中的 controller 既不是记录的旧版本也不是目标版本时拒绝继续（`ambiguous`）。
- `verify` 要求：正确二进制持有锁并服务；API 与 dashboard 端点健康；升级前活跃的 worker 在新启动之后重新联系（liveness 窗口为 2 分钟，因此要求「联系时间晚于启动」，而非仅看在线）；租约连续性通过。失败即进入回退。

Worker 升级：对每个 worker 依次 `drain`（先落盘再暂停，等待其租约与未落实结果清零）→ `roll`（`worker roll` 启动新 manager 代，执行器保持各自固定的二进制）→ `ready`（要求新代在启动后重新注册并在线）→ `resume`。同一时刻最多替换一个；若其余在线、未暂停 worker 的并行容量低于 `--min-capacity`，则推迟而不排空。新代未就绪时该 worker 保持暂停。

## CLI 运行手册

前置：已运行 `clawflow candidate prepare/up/down` 并得到该提交的 `passed` 证据；必需 CI 与独立验收已完成；目标二进制由同一提交构建。

```sh
# 0. 只读预检，不改变任何东西
clawflow upgrade plan --binary ./clawflow-new --commit <sha> --approve <sha> \
  --evidence <evidence.json> --ci-ref <run-url> --acceptance-ref <verdict-ref>

# 1. Controller 切换（短暂 API 中断）；必须显式 --execute
clawflow upgrade controller --execute --binary ./clawflow-new --commit <sha> --approve <sha> \
  --evidence <evidence.json> --ci-ref <run-url> --acceptance-ref <verdict-ref>

# 2. Worker 逐个升级（controller 先升级；描述文件见 worker roll）
clawflow upgrade workers --execute --binary ./clawflow-new --commit <sha> --approve <sha> \
  --evidence <evidence.json> --ci-ref <run-url> --acceptance-ref <verdict-ref> \
  --worker w1=<state>/worker-executions/<id>/generation.json --worker w2=...

# 观察与恢复
clawflow upgrade status [ID]    # 阶段、截止时间、结果、当前调度持有者、测得的中断
clawflow upgrade resume ID      # 命令被中断（崩溃、SSH 断开）后继续
```

结果与处置：

| 结果 | 含义 | 处置 |
| --- | --- | --- |
| `succeeded` | 已升级并验证 | 无 |
| `postponed` | 截止时间到期，服务/worker 未变，任务未取消 | 待任务结束后重跑 |
| `failed`（停机前） | 预检、迁移或排空失败，服务未变 | 修复原因后重跑 |
| `rolled-back` | 目标不健康，已回到经验证的旧版本 | 修复目标后重试；数据库保留 expand 后的 schema |
| `rollback-blocked` | 目标不健康且回退不安全 | 派发保持暂停；不要启动旧二进制；向前修复或恢复备份，再执行 `dashboard resume NAME` |
| `failed`（回退阶段） | 回退本身失败 | 两个版本均未验证；`upgrade status` 查看；在控制器锁下手动启动记录中的 `prior.path` |

记录保存在 `<state>/upgrade/records/<id>.json`（权限 0600），包含目标 SHA、各阶段与截止时间、结果与恢复说明；写入前对 DSN、口令、bearer 令牌与长十六进制串脱敏。

## 在隔离环境验证

升级逻辑通过 `internal/upgrade` 的集成测试在隔离的 PostgreSQL schema 中验证，使用真实的 coordinator 存储，只模拟进程与服务管理器：

- 长任务期间升级：无重复执行、无租约丢失、无重复写回，任意时刻至多一个 controller；
- 内嵌 worker 与未落实结果阻止切换，到期推迟且不取消；
- 逐个升级 worker，保持容量，先排空后替换，操作员暂停的 worker 保持暂停；
- 不兼容迁移、迁移失败、停机失败均不改变当前服务；
- 健康检查失败触发经验证的回退；不可回退时失败即关闭；
- 在每个阶段边界与每个宿主动作之后崩溃，`resume` 恢复唯一持有者且不重放。

运行方式：`CLAWFLOW_TEST_DATABASE_DSN=... go test -race ./internal/upgrade/`。未设置该变量时这些测试被跳过，不构成通过证据；以精确提交的 CI 结果为准。

这些测试不替代对已安装宿主的生产推广：生产切换需要具体候选、CI、独立验收与上述显式门禁。
