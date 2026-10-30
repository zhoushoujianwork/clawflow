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

States include `prepared`, `running`, `pending_sync`, `acknowledged`, `rejected`, and `needs_recovery`. The executor retries transient upload failures; a restarted manager restarts pending delivery. The controller acknowledges the exact persisted report idempotently, including after finalization. Same-epoch durable results can arrive after the execution deadline while ownership is still reserved. Conflicting or superseded results are retained locally and rejected, never applied as new work.

A controller-authorized infrastructure retry can reuse a task ID with a newer epoch. When replaying its saved claim, the worker archives an acknowledged prior attempt under `history/<task-id>/epoch-<epoch>/` before preparing the new attempt. The archive retains the report, checkpoint/session reference and executor output. Executor and session locks must both be free; their original lock files remain in place. Unacknowledged, rejected, expired or identity-mismatched claims still require reconciliation. Do not delete retained journals to clear an epoch mismatch, and do not use session recovery for an already acknowledged attempt.

The controller clears the current report digest when preparing an infrastructure retry. A current receipt requires a committed result; historical receipts remain scoped to their recorded attempt epoch. If an older controller falsely acknowledged a report while the current task still has no result, verify the journal's task, owner and epoch against controller state before replaying that exact retained report through the authenticated worker API. Do not manufacture a successful result or reset retry limits.

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
