# Restart-safe execution: implementation details

Scope and decisions: [main design](resilient-execution-design.md).

## Existing foundations and gaps

- `cmd/clawflow/commands/coordinator.go`: embedded local worker inherits controller cancellation; replace with an independent service.
- `cmd/clawflow/commands/worker_run.go`: heartbeat failure currently cancels task execution. Separate transport failure from explicit cancellation and ownership rejection.
- `internal/coordinator/store.go`: 120-second leases and fencing protect mutations; preserve this protection while changing disconnected ownership handling.
- `internal/coordinator/effects.go`: task attempts, durable effect intents and result application provide a foundation for reconciliation. Preserve independent review, provenance and delivery gates.
- Adjacent Offload `internal/cli/cli.go` exposes `-r` session resume and session admission locking. This is evidence of a resume primitive, not proof that every backend or interrupted run is recoverable. Inspect profile, history and native session validity before invoking it.

## Process and upgrade ownership

The controller owns assignment, task identity and authoritative accepted state. A logical worker owns host identity and configured capacity. Worker generations own admissions and journals; independent executors own running processes and output capture. A controller or worker-manager lifecycle must not implicitly cancel executor lifetime.

Use versioned immutable installation directories and unique generation identities. Drain old admissions, start the compatible new generation, and let old executors/reporters finish. Sum old/new running and reserved slots against one logical worker limit and controller limits; an upgrade never creates additional scheduling capacity. Retain old binaries, backend/profile configuration and workspaces until execution has ended and pending results are durably acknowledged. Refuse incompatible protocol upgrades before draining; retain the usable old generation if the replacement fails readiness. Rollback does not replay already accepted work.

The first implementation may keep each draining worker generation alive to supervise its own tasks. Surviving manager crashes additionally require an independent executor/supervisor with a durable local status protocol. Do not claim process adoption from PID discovery alone.

## Journal and delivery contract

Before execution, durably record controller identity, logical task, attempt, ownership epoch, worker/generation, execution ID, repo/base/head, canonical worktree identity, Offload run/caller/native session IDs when available, backend/profile identity, pinned executable identities, timestamps, deadlines and recovery lineage. Never copy credential values into journals.

Persist start intent before spawning, then publish verified process identity and output locations. Recover ambiguous spawn windows through executor handshake/locking; never blindly spawn again. PID alone is insufficient because of reuse. Capture result/output independently of the manager's stdout pipe.

Write journal snapshots atomically with crash durability appropriate to the filesystem; use a private append-only event log or local SQLite transaction where useful. Final results are immutable records with stable IDs and digests. Worker retries upload until the controller durably records the result and returns an acknowledgement. A lost acknowledgement causes a duplicate delivery, not duplicate effects. Remove outbox records only after verified acknowledgement and retention policy. Disk-full/write failures must stop new admissions and surface degraded durability.

LAN workers reconnect outbound using authenticated APIs. The controller does not read arbitrary worker paths or scan remote disks. Recovery exchange returns active execution identities and pending result summaries; full results follow the existing authenticated, bounded validation path.

## Disconnection, fencing and reconciliation

Transport unavailability changes observed connectivity, not executor cancellation. Continue only the existing execution within its recorded deadline and authority; accept no new assignment offline. Explicit cancellation and confirmed stale ownership are separate signals. Offline cancellation delivery is delayed and must be visible.

Lease expiry alone must not authorize an immediate duplicate while the old executor may still run. Mark ownership uncertain and reconcile. To preserve availability without split brain, use a defined execution authority expiry: executor stops at its deadline unless renewed, and reassignment waits for confirmed stop or expiry plus the required safety margin. If this cannot be established, keep the task quarantined for recovery rather than automatically launching a replacement. An unbounded network partition cannot simultaneously guarantee uninterrupted execution and safe automatic reassignment.

Controller startup first recovers accepted results/effect intents and worker ownership, then enables eligible dispatch. Unrelated reconciled work need not wait for an unreachable worker; its unresolved tasks retain reservations. Accept current-authority duplicate results idempotently. Quarantine stale-epoch late results for inspection; never weaken fencing or apply stale VCS effects merely because a result file exists.

## Interrupted attempt recovery

1. Check cancellation, accepted completion, authority and retained result before considering execution.
2. Verify executor identity and exclusive task/worktree ownership. If alive, reconnect to its supported supervisor; do not launch another Offload invocation.
3. If dead, verify worktree ownership, repository/base/head, existing modifications, lock state, session identity, profile/backend availability and completed external effects. Preserve dirty work and logs; never reset or delete them as an automatic repair.
4. If the backend supports resuming that session, create a controller-authorized recovery attempt/segment linked to the interrupted one and invoke the supported Offload resume interface in the same worktree. Keep a new invocation/caller identity and retained session lineage; reacquire capacity and authority before launch.
5. If the session is missing or unsupported, record **Needs recovery**. A policy-authorized continuation from worktree context must be labelled a new attempt, reconcile existing commits/PRs/effects, and include prior progress. Do not describe it as resuming the old model session.

Resume is not process checkpointing: a model may re-evaluate commands. Existing durable effect deduplication and VCS reconciliation remain required. Recovery cannot bypass independent review or acceptance provenance. Cleanup excludes running, unacknowledged and recoverable worktrees until an explicit retention policy permits removal.

## Required validation

- Restart controller during execution and during upload; verify stable execution identity and exactly one accepted result.
- Lose acknowledgement and duplicate delivery; verify effects are applied idempotently.
- Upgrade with active tasks; verify old executions survive, new admissions use the new generation, capacity is shared and readiness failure leaves the old generation usable.
- Crash manager before/after spawn and result persistence; recover without launching duplicate processes or losing completed output.
- Simulate partition, stale epoch, authority expiry and offline cancellation; verify no concurrent authoritative attempt or stale effect application.
- Recover from valid sessions and dirty worktrees; reject wrong repo, reused PID, changed profile, missing session, active locks and explicit cancellation.
- Reboot worker with retained storage; resume only supported interrupted work, replay completed outbox records, and leave ambiguous cases visible.
- Use headless fake executors/backends for failure injection. Any real provider/session acceptance is a separately bounded test.
