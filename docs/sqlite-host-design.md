# SQLite for the single-host controller

Status: Implemented (tooling) · 2026-10-31. Live cutover is a separate operator step; see [sqlite-host-runbook.md](sqlite-host-runbook.md).

Replace the required PostgreSQL service with embedded SQLite for the native macOS host. The app should open, recover its controller and schedule work without installing, configuring or starting a database server.

One controller remains authoritative. Multiple workers on this Mac and LAN workers continue using the existing authenticated HTTPS API. Roles, tags, capacity, leases, independent acceptance and delivery policy remain unchanged.

## Storage and daily use

| Content | Location |
|---|---|
| Task/worker state, leases, history, usage and schema versions | `~/Library/Application Support/ClawFlow/data/clawflow.sqlite3` |
| Editable host/repository configuration | Existing `config/config.yaml` |
| Raw events, transcripts and large execution artifacts | Existing files |
| Service logs | `~/Library/Logs/ClawFlow/` |

The controller owns the local database. Workers need only their CLI, execution profile, credentials and registration; they do not need a database server or a shared database file. SQLite is never placed on a network share or opened by LAN workers. The app and CLI use the same host configuration and state contracts.

SQLite is the default and intended store for this product scope. PostgreSQL is retained only long enough to import and roll back the existing installation, rather than adding a permanent database selector or automatic fallback. Multi-controller operation and very large write-heavy deployments are outside this feature.

## Migration and delivery

1. Replace PostgreSQL-specific storage and locking with tested SQLite transactions, retaining task identity, deduplication, fencing and current retry boundaries.
2. Add an explicit, resumable offline importer. Drain execution, stop dispatch, back up configuration and capture a consistent PostgreSQL snapshot. Validate the imported database before switching the configured store.
3. Switch app, controller and CLI together. Remove the database dependency from worker startup and host readiness checks. App/login startup then manages only the controller and worker services.
4. Verify restart recovery, local/LAN worker claim and reporting, history, usage and delivery gates. Only after successful cutover disable ClawFlow's dedicated PostgreSQL LaunchAgent. Retain the original database and rollback instructions; never stop unrelated PostgreSQL services.

No live database or scheduling policy changes are authorized merely by accepting this design issue. Implementation must preserve current work and perform the controlled cutover after its acceptance checks pass. Rollback after new SQLite writes requires reconciliation; switching back to an older PostgreSQL snapshot must not replay completed work.

## Acceptance

- A fresh host starts without PostgreSQL, a DSN or database credentials; the app can start its local controller.
- Multiple local and LAN CLI workers retain role/tag matching, concurrency limits, unique claiming, heartbeat, fencing and independent-review constraints.
- Concurrent claims, cancellation, crash/restart and stale results cannot cause an accepted duplicate execution or external effect.
- Existing records and identities migrate without loss or double-counted usage. Configuration, logs, credentials and artifacts keep their current separation.
- Offline and failure-path tests pass; startup UI no longer exposes a PostgreSQL service. Real desktop or LAN acceptance is recorded separately from fixtures.

See [implementation details](sqlite-host-design-detail.md). This proposed direction supersedes the PostgreSQL-only requirement in the [current storage design](storage-design.md) once implemented.
