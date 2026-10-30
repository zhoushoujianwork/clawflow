# Automatic worker reconciliation validation — 2026-10-30

This extends the [claim recovery repair](worker-claim-recovery-validation-2026-10-30.md). Runtime operations follow the [durable worker runbook](resilient-execution-runbook.md).

## Behavior covered

- Saved claims and retained execution journals reconcile at startup and during manager polling. One blocked or corrupt item retains its evidence and a private diagnostic without terminating admissions or blocking other items.
- Expired durable claims can report a bounded infrastructure launch failure only with matching controller ownership/epoch, free executor/session locks, and evidence that the attempt never started. Older acknowledged attempts are archived before replacement. Resumed, running, conflicting or checkpoint-bearing attempts are preserved for verified recovery.
- A falsely acknowledged report returns durably to the outbox and replays its retained body only when the controller confirms the same epoch is still leased without a committed result. Committed/superseded receipts are not uploaded again. Manager-local caching avoids repeatedly inspecting unchanged verified receipts; each new manager verifies again.
- Receipt inspection uses the pinned authenticated worker channel and returns no task payload or report. Token and TLS trust failures remain fatal.
- `worker executions` lists readable executions and per-item blocked reasons, including corrupt source journals and diagnostic files. Shared snapshot corruption is preserved for evidence-based repair; retry limits and profile/session recovery checks remain in effect.

## Headless validation

- Full `go test ./...`: 3,076 passed, 101 skipped, one failure in `internal/api.TestProjectPipelineExecutionsUsesAdapterWithoutMutatingCache`. This is the same live-adapter timing failure observed during the preceding repair. Isolated `go test ./internal/api -count=1`: all 145 passed. The full run was not entirely green.
- Isolated PostgreSQL 17 fixture, `go test ./internal/coordinator -count=1`: 97 passed, including persisted receipt inspection. The temporary server was stopped after testing; production data was not used as a fixture.
- Worker race suite (`TestWorker`, `TestDurableWorker`, `TestRollingManagers`): 89 passed. Final focused race checks after receipt caching: 23 passed, including epoch-change invalidation and manager restart reinspection.
- Focused corruption/status check verifies both a healthy saved claim and a healthy interrupted journal progress alongside corrupt files. Expiration/report tests cover held executor and session locks, stale ownership, retained checkpoints, resumed sessions, lost upload responses and report-epoch mismatch.
- `gofmt` and `git diff --check` pass. These checks invoke neither a paid provider nor the desktop dashboard.

## Local service deployment

At approximately 23:37 +08, built and installed source commit `6516902` as `dev-auto-recovery-6516902` in the independently installed service executable (`~/Library/Application Support/ClawFlow/bin/clawflow`). The preceding binary is retained under `backups/auto-recovery-6516902-_m11_3wb/`.

- Graceful SIGTERM of the durable controller loaded the new executable through launchd, PID 41494 → 91217. Its controller-managed local worker moved to the new pinned generation, admissions PID 91221.
- Graceful SIGTERM of the launchd admissions manager loaded the new executable, PID 35581 → 91406. Both generation descriptors report source commit `6516902`; logical capacities remain one and four, with durable registration enabled.
- Issue #615 task `db3a314ea9b839bb8b8d254cbf6e375f9915ead974d3329dbbd69d4066be5208` remained running at epoch 1. Executor PID 88489, native session `7edf8d01-7cb5-44bd-84de-7c7e3027f2f3`, pinned older executor binary, and retained worktree were unchanged. Its checkpoint advanced and the controller accepted a heartbeat at 23:37:27 after both manager updates. No active task was terminated or duplicated for deployment.
- The pinned, authenticated receipt endpoint returned HTTP 200 with `state=leased` and `report_committed=false` for that active task. The native dashboard's read-only JSON snapshot was available; no desktop interaction occurred.
- Both managers' `worker executions` status was readable and reported zero `recovery_blocked` diagnostics. PostgreSQL retained PID 61176 and launchd run count 1. The dashboard app and database were not restarted.

Live acceptance verifies deployed protocol availability, registration and continuity. Recovery faults were injected only in isolated fixtures; no production session was intentionally crashed. Prior issue attempts include deadline failures and coordination blockers; healthy service operation does not establish CI, acceptance, merge or issue completion.
