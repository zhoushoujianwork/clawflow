# Durable worker operations

Architecture: [restart-safe execution](resilient-execution-design.md).

## Compatibility and first deployment

The new controller and worker use durable worker protocol 1. Migration 11 adds worker capability and an accepted-report digest; legacy workers retain their existing lease behavior. An old controller rejects the new registration fields instead of pretending to support durable execution. `worker --durable=false` is explicit legacy mode and does not provide restart safety.

The first production cutover must drain tasks owned by the old embedded local worker, or explicitly interrupt them gracefully and adopt their verified interruption records offline before restarting that controller. The new architecture cannot retroactively detach already-running old processes. Deploy and migrate the controller first, then roll workers. Once migrated, controller shutdown no longer cancels the local worker manager or its independent task executors.

Do not roll back to a controller that lacks durable-task quarantine while durable tasks or pending results exist. Database rollback is not binary rollback. Candidate validation must use isolated state and database fixtures; never start another scheduler against production state.

## Worker startup and rolling upgrade

Existing `clawflow worker --coordinator ... --token-file ... --fingerprint ... --state-dir ... --tags ... --roles ... --max-parallel ...` starts a durable admissions manager by default. Authentication secrets remain in the referenced token file.

The execution root is `<state-dir>/worker-executions/<identity-hash>/`. Its `generation.json` records the worker options and pinned CLI. To roll a verified candidate:

```sh
clawflow worker roll --descriptor /absolute/execution-root/generation.json --binary /absolute/verified-candidate/clawflow
```

The command probes the candidate's local protocol command, stages an immutable copy, and launches it. This is a compatibility probe and launch request, not independent code acceptance or proof of provider readiness. Check `manager.log`, worker registration and execution state after launch. Existing executors continue using their original CLI and Offload binary. The old manager releases admissions; the controller counts existing tasks against the same logical worker capacity. Superseded detached managers exit after releasing admissions. A directly launched service manager remains inert until its owning service is retired, preventing a KeepAlive policy from resurrecting an obsolete admission loop. Do not interpret an inert manager as another worker slot.

For service supervision, a pinned manager can also run with `clawflow worker serve --descriptor /absolute/generation.json`. Controller-started local managers persist across controller exits and are started/reconciled again on controller startup. A separate launchd/systemd unit remains advisable for unattended manager restart; controller-started detached managers alone are not a new always-on service installation mechanism.

## Result replay and status

Each task directory retains `journal.json`, `executor.log`, and `execution.json` when a managed development session starts. Files are private. A persisted claim ID makes assignment replay safe after a lost response. The journal stores task identity, epoch, pinned executable paths and result outbox; it contains no worker token value. The checkpoint points to retained native output artifacts, including inputs, which must remain private.

```sh
clawflow worker executions --root /absolute/execution-root
```

States include `prepared`, `running`, `pending_sync`, `acknowledged`, `pending_release`, `released`, `rejected`, and `needs_recovery`. The executor retries transient upload failures; a restarted manager restarts pending delivery. The controller acknowledges the exact persisted report idempotently, including after finalization. Same-epoch durable results can arrive after the execution deadline while ownership is still reserved. Conflicting or superseded results are retained locally and rejected, never applied as new work.

A controller-authorized infrastructure retry can reuse a task ID with a newer epoch. When replaying its saved claim, the worker archives an acknowledged prior attempt under `history/<task-id>/epoch-<epoch>/` before preparing the new attempt. The archive retains the report, checkpoint/session reference and executor output. Executor and session locks must both be free; their original lock files remain in place. Unacknowledged, rejected or identity-mismatched attempts remain blocked unless an existing verified recovery path applies. Do not delete retained journals to clear an epoch mismatch, and do not use session recovery for an already acknowledged attempt.

The controller clears the current report digest when preparing an infrastructure retry. A current receipt requires a committed result; historical receipts remain scoped to their recorded attempt epoch. Managers now inspect acknowledged receipts through the authenticated worker API. If the same owner and epoch are still leased without a committed result, the manager durably returns the exact retained report to `pending_sync` and replays it. Committed or superseded receipts are not replayed. The inspection response exposes only state and whether a result is committed. Do not manufacture a successful result or reset retry limits.

Managers reconcile saved claims and journals on startup and during polling. A durable claim whose execution deadline passed can report a launch failure only when executor and session locks are free and local evidence proves it never started: no journal/checkpoint, an unresumed prepared journal without a checkpoint/report, or an older acknowledged attempt archived before replacement. The controller must confirm the current owner and epoch are still leased without a committed result. Reporting uses the existing infrastructure retry budget; it neither extends authority nor starts a replacement session.

Each unsafe or corrupt item is retained and diagnosed independently under `recovery/`; it no longer terminates the entire manager. `worker executions` includes these `recovery_blocked` reasons alongside readable journals. Reconciliation clears a diagnostic after success. Authentication and TLS trust failures remain fatal. Corrupt shared snapshots such as `queue.json` are not replaced with empty state: retain the file and repair it from verified evidence. Session/profile changes still require the guarded recovery checks above.

Execution has a fixed deadline based on the task timeout, plus ten minutes for reporting. Heartbeats do not extend execution authority indefinitely. If the controller remains unavailable, the executor stops at the execution deadline and preserves its result. Expired durable tasks retain their reservation rather than being blindly reassigned. This trades automatic replacement availability for duplicate-execution safety.

## Interrupted execution recovery

Managers detect interrupted executors and mark them `needs_recovery`. After the original authority expires, they attempt guarded recovery of implementation and repair sessions. It can also be requested explicitly:

```sh
clawflow worker recover --journal /absolute/task-directory/journal.json
```

Recovery requires exclusive executor and Offload locks, retained canonical worktree/repository, matching execution profile and pinned runtime, a native session, and controller approval for a new epoch. Dirty edits are preserved. The same native session is resumed using Offload `-r`, with a new invocation and reconciliation prompt. Old epochs cannot heartbeat or apply results. The controller bounds recovery attempts using the existing infrastructure-attempt limit.

Recovery does not run before the old authority expires. A still-running Offload process inherits the recovery lock, including after executor death, so another invocation cannot be launched concurrently. A completed native result without a complete worker report is deliberately left for result reconciliation rather than guessed from text. Unsupported/missing sessions, changed profiles, corrupt journals and reply/acceptance tasks remain visibly blocked for a fresh, explicitly reviewed retry; they are not silently restarted with a new model session. Existing independent acceptance and provenance requirements still apply.

This supports controller/manager restarts and retained development/repair session continuation. It is not a memory checkpoint, arbitrary process adoption, transparent recovery of every task kind, controller HA, or a verified claim that all Offload backends survive host reboot. Native backend/profile configuration and credentials must remain available; pinning the Offload binary does not snapshot all external dependencies.

## Validation boundary

Headless fixtures exercise durable authority, fencing, late/duplicate reports, journal corruption, admission generation transfer, checkpoint resume and a real subprocess crash with an inherited recovery lock. These tests do not invoke a paid provider or restart production services. Record real backend and first-production-cutover acceptance separately.

## Explicit legacy interruption import

For an owner-authorized recovery test, capture task records, native session IDs, worktree paths and worker options before sending SIGTERM to legacy Offload processes. Stop controller and worker services, back up the database, and apply current migrations. Verify native history records the interruption and no original process survives.

`clawflow worker import-legacy --manifest /private/captured-manifest.json` preflights worker credentials, repository/worktree identity and configured profile. `--apply` additionally requires the exact original task epoch, a sealed `failed` / `Offload execution: canceled` report, no effect intents, and no explicit task cancellation or successful result. It archives the interruption report in the journal, grants one new epoch, and prepares a same-session recovery without starting it. The controller ownership lock must be available throughout import. The private manifest contains token file paths, never token values.

Start the new controller and managers only after reviewing the imported journals. Existing paused worker registration does not prevent recovery of its already-owned tasks. Keep new dispatch paused during the recovery test, then restore the previous dispatch/drain state after verifying resumed native session IDs and worktree identity. An interruption import consumes a bounded infrastructure attempt; it does not reset repair budgets, weaken provenance or replay previously queued effects.

## Offload readiness during durable handoff

Readiness gates are process-local caches and are excluded from generation and execution journals. A manager loaded through `worker serve` rebuilds its gate in that worker's environment. A prepared executor checks its own runtime before starting task code. If blocked, it durably records `pending_release` and returns the unused lease without a result or attempt charge; successful or stale release responses retire the local attempt as `released`. Managers replay pending releases after restart. A later authorized claim archives that released attempt before preparing a new execution, with exclusive executor/session locks and no retained checkpoint. Reports and interrupted sessions retain their separate delivery/recovery paths.


## PR 缺少开发来源时的自动恢复

人工协助更新 PR、旧执行主机不可达或本机缺少原开发记录时，CI 全绿也不能替代开发来源。对已开启自动合并、必需 CI 和独立验收的受管 PR，扫描器现在可派发一次真实的 `development_verification`：CC 在隔离 checkout 中按当前 issue 重新核查整个差异并执行相关无界面检查。成功后，再由不同原生会话独立验收，最后仍走准确 head 的 CI 与条件合并。

这是新的开发核验记录，不是补写旧会话、不宣称原作者身份，也不修改或挪用历史证据。核验不能修改源码、提交、推送或更改来源文件；需要保留真实 native 工具活动，报告实际命令与限制，并由 runner 确认工作区干净、准确提交仍在原分支发布。成功使用 `no-change`，但仍消耗共享三次修复预算中的一次；原有基础设施上限、冷却、退避和 worker 互斥保留。失败、检查不完整或发现缺陷均不能生成成功来源。

仅“缺少历史”或“当前 head 没有完成记录”可自动核验。损坏／歧义记录、正在运行的开发、未通过的 CI、fork／非受管分支、手动 skip、未启用的策略、预算耗尽和读取错误保持阻塞，不通过创建新记录掩盖它们。协调器 payload 保留核验原因，重启后的 worker 与结果回放沿用同一目的；审计 metadata 记录 `repair_reason=development_verification`。无需启动第二个调度器。

验收入队从数据库读取开发任务时必须同时保留 payload 与 result，才能继承实际读过的 SOP 版本；从 VCS 列表取得摘要后，后续来源检查使用契约绑定的完整 PR 信息。缺字段不应被误报为证据损坏或永久等待。数据库检索与完整入队的隔离 PostgreSQL 回归覆盖这两个边界。

## 验收结果回传超时

`pending_sync` 表示执行结果已经落入 worker journal，不能重新启动模型或清空任务。先检查 executor 是否仍持有锁，以及结果上传日志；控制器重建验收契约时仍会核对远端 PR 和开发来源。

来源继承只需要提交祖先关系，临时 Git 仓库使用 `--filter=blob:none` 获取提交图，保留完整的 head、父节点及主分支关系检查。结果 `/complete`、`/fail` 请求单独使用 90 秒有限时长，注册和心跳继续使用 10 秒，调用方取消仍生效。不会通过放宽验收合约来接受结果，也不增加修复预算。

滚动更新先检查活动会话与待回传 journal，备份并暂停新领取；保留已完成结果、原 epoch、worker 所有权和会话身份。旧执行器固定使用旧二进制，因此更新 controller 后须实际观察旧 journal 获得确认、名额释放、后续独立验收开始，不能仅凭 worker 在线宣布恢复。
