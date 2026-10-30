# Restart-safe scheduling and worker upgrades

Status: proposed. Current controller shutdown can cancel its embedded worker; this document defines the replacement architecture, not an existing guarantee.

## Goal

Updating the dashboard, controller, or worker must not require terminating healthy task execution. Interrupted tasks should reuse verified worktrees and recoverable Offload sessions instead of discarding completed work.

## User experience

- Controller restart: running tasks continue; results wait locally and synchronize when the controller returns. New scheduling pauses during the outage.
- Worker upgrade: start a new worker generation for new tasks, while the previous generation finishes its existing tasks. Retire it after all results are acknowledged. A single-slot worker may wait for its active task instead of temporarily adding capacity.
- Abnormal termination: show **Recovering**, then resume the verified worktree/session when supported. Show **Needs recovery** with a reason when safe recovery cannot be established. Never silently start duplicate work.
- Dashboard: distinguish executing, disconnected/last known, recovering, completed/pending sync, finalizing, and delivered. Show worker generation and recovery history in details.

## Core approach

Run the local worker independently, using the same protocol as LAN workers. Separate worker admission/reporting from task execution lifetime. Each task has a durable execution journal and a retained result outbox; the controller accepts results idempotently and acknowledges them only after persistence. Reconcile ownership and pending results before scheduling replacements.

Retain each worker generation's executable and execution environment until its tasks finish. An upgrade starts a new generation without stopping the old executor. Both generations share one logical worker's capacity budget. Offload/backend upgrades must not replace dependencies underneath existing executions.

Recovery has three distinct paths: reconnect to a surviving executor, resume an interrupted Offload session in its existing worktree, or restart from retained worktree context when session resume is unavailable. The last path is a new recorded execution attempt, not transparent continuation. Explicitly cancelled tasks never resume automatically.

## Scope and implementation order

1. Independent local worker; durable attempt journal, result outbox, acknowledgement and restart reconciliation.
2. Controller outage handling and recovery-aware task ownership; stop cancelling execution solely because heartbeats cannot reach the controller.
3. Versioned worker generations, shared capacity and graceful rolling upgrades.
4. Verified worktree/session recovery and dashboard recovery controls, with backend capability checks.

Do not introduce a message broker or shared worker database. Keep controller state authoritative and worker storage private and local. Controller outages delay status, capacity release, dependency scheduling, review and merge; execution can continue within its existing authority and deadline.

Acceptance requires process-level tests proving controller restarts, overlapping worker generations, lost acknowledgements and interrupted execution do not lose results or produce concurrent duplicate attempts. Machine reboot cannot preserve a running process; recovery depends on retained disk state and backend resume support.

See [implementation details](resilient-execution-design-detail.md).
