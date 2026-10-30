# SQLite host — implementation details

Companion to the [feature design](sqlite-host-design.md), which controls scope. All changes below are proposed; current production storage remains PostgreSQL.

## Existing seams and constraints

Reuse the Go application/store boundary and existing worker/admin APIs. The current GORM PostgreSQL implementation relies on JSONB, advisory transaction locks, row locking/`SKIP LOCKED`, PostgreSQL clocks and versioned migrations. A connection-string replacement is insufficient. Relevant modules are `internal/store`, `internal/coordinator`, `internal/actions`, `internal/resourcelease` and their CLI/API callers; audit all dialect-specific statements before cutover.

Choose a maintained SQLite driver compatible with the project's Go version and supported release targets. Record its pinned SQLite version and security fixes. Prefer a portable build without a separately installed database/runtime; assess CGO and cross-compilation explicitly. GORM can remain where useful, but do not create a generic multi-database framework solely for this migration.

## Transactions and concurrency

- Keep the file on local storage with private directory/file permissions. Enable foreign keys per connection, WAL, a bounded busy timeout and an explicit durability policy suitable for acknowledged task transitions. Include the WAL in backup/checkpoint procedures; never copy a live database file alone.
- Use short write transactions with immediate writer acquisition for claiming and capacity accounting. All controller and supported local CLI write paths must obey the same transactional rules; an in-process mutex alone cannot protect multiple processes.
- Replace advisory/row locks with atomic conditional updates, uniqueness constraints and serialized transactional checks. Preserve active-task dedupe, global/repository/worker capacity, lease epoch increments, inbox/outbox identity and result/effect acceptance.
- Keep VCS/provider execution and network calls out of SQLite write transactions. Preserve durable intent and reconciliation boundaries around external effects, including ambiguous outcomes. A busy timeout is not permission to replay a mutation blindly.
- Store and compare lease time consistently in UTC, preserving the existing wire timestamps and fencing semantics. Controller time owns leases; worker clocks cannot extend authority.
- Unknown results, expired leases and interrupted finalization retain the existing evidence/reconciliation requirements. SQLite does not itself guarantee exactly-once external execution.

## Schema and records

Create SQLite-native, versioned migrations. Map JSONB to validated JSON text or explicit relational columns according to actual query needs; map token hashes and other binary values to BLOB without changing identity/authentication semantics. Rewrite JSON operators, time expressions and dialect-specific indexes intentionally, with equivalent constraints and ordering.

Preserve task IDs, worker IDs, token hashes/scopes/revocation, invitations, attempt numbers, fencing epochs, result/application state, inbox/outbox/effect identities, resource leases, run history, usage and scheduled-check records. Retain source schema/import provenance separately from SQLite schema versioning. Keep raw logs/artifacts in files and never invent missing usage or evidence.

## Import and cutover

Provide explicit CLI planning/import/verify/cutover operations using documented names at implementation time. Proposed names are not current commands. Require an idle/drained source or a controlled reconciliation plan; stop dispatch before capturing the source snapshot. Detect and refuse an active competing controller.

Import into a separate temporary SQLite store. Make interrupted import resumable or safely restartable with a source-snapshot identity. Validate integrity, foreign keys, row counts, primary identities, representative payload digests, usage totals and artifact paths. Report omitted, invalid or unsupported rows as blockers rather than silently discarding them.

Close/checkpoint the validated destination before publication. Persist a cutover record and update the shared configuration atomically. Refuse contradictory environment overrides/old DSNs; never silently choose an empty store when opening or importing fails. Preserve an untouched source database/configuration backup. Run acceptance before unloading only `com.zhoushoujianwork.clawflow.postgres`; remove PostgreSQL-specific app startup controls, host installer behavior and readiness messaging together.

Do not infer that an old database is safe to delete. Rollback before SQLite accepts new work may restore the source configuration. After new work, reconcile state/effects or perform a verified reverse import first; old snapshots cannot reauthorize completed tasks.

## Verification

Use isolated database files and real concurrent connections/processes. Cover competing claims, capacity accounting, role/tag routing, distinct reviewer identity, invitation replay, result-before/after-expiry, duplicate reports/effects, cancellation races, crash during commit/finalization/import/cutover, corruption/unwritable paths, busy timeout, disk-full handling, restart and backup restoration. Keep normal failures separate from infrastructure retry budgets.

Run the existing headless regression suite against SQLite, update fixtures and CI so tests no longer require a PostgreSQL service, and retain a bounded importer test against an isolated PostgreSQL fixture during the transition. Measure representative worker heartbeat/claim/write load and UI responsiveness; publish measured bounds rather than promising untested worker counts.

Verify the native app starts its host after login/app launch with no PostgreSQL process, and that two local workers plus a LAN worker fixture register, claim and report through the unchanged API. A real second-machine test and desktop checks require their respective authorized environments. Record observed checks and unrun acceptance separately.

Primary references: [SQLite use cases](https://www.sqlite.org/whentouse.html), [WAL and concurrency](https://www.sqlite.org/wal.html).
