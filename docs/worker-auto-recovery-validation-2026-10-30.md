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
