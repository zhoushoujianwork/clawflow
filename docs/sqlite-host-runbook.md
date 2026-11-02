# SQLite host: operations and migration runbook

Implements [sqlite-host-design.md](sqlite-host-design.md). The controller owns one embedded SQLite file. A fresh host needs no database server, DSN or credentials.

## Layout

| Content | Location |
|---|---|
| Task/worker state, leases, history, usage, schema versions | `<state dir>/data/clawflow.sqlite3` (macOS: `~/Library/Application Support/ClawFlow/data/`) |
| Host and repository configuration | `config/config.yaml` |
| Raw events, transcripts, large artifacts | existing files under `data/` |
| Cutover record | `data/cutover.json` |
| Service logs | `~/Library/Logs/ClawFlow/` |

The directory is created `0700` and the file `0600`. Opening refuses a network filesystem (`ErrNetworkFilesystem`). LAN workers never open the file; they use the authenticated HTTPS API.

## Storage rules

- Every connection enables `foreign_keys`, `journal_mode=WAL`, `synchronous=FULL` (an acknowledged transition survives power loss) and `busy_timeout=10000` ms.
- Every write transaction is `BEGIN IMMEDIATE`, so writers are serialized across processes, not only inside one process. Claiming, capacity checks, enqueue de-duplication, lease epochs and result acceptance run inside such a transaction. Row locks and advisory locks are gone; the partial unique indexes, conditional updates and the serialized transaction replace them.
- A busy timeout surfaces as an error. Callers do not replay a mutation after it; result and effect acceptance stay idempotent through the existing task, epoch, inbox and effect identities.
- No VCS or provider call runs inside a write transaction.
- Lease times are stored and compared in UTC. The controller clock owns them; worker clocks cannot extend a lease.
- Copying the live file alone loses WAL content. Use `clawflow db backup <file>` (`VACUUM INTO`, consistent, WAL included). Before applying migrations to an existing database the controller writes `clawflow.sqlite3.pre-migrate-<time>.bak` the same way.

## Driver

`modernc.org/sqlite v1.58.0` (pure Go, no CGO), bundling SQLite **3.53.4** (read from `sqlite_version()` at the pinned version). Check upstream release notes and `go list -m -u modernc.org/sqlite` when updating; a bump needs the full test suite. Verified with `CGO_ENABLED=0` for `darwin/arm64`, `darwin/amd64` and `linux/amd64`, the release targets.

### 已包含的安全与数据完整性修复

2026-11-02 核对上游记录：固定的 SQLite 3.53.4 包含 3.53.0 修复的 WAL-reset 数据损坏问题，以及 3.53.2 修复的 CVE-2026-11822 / CVE-2026-11824（FTS5 在特定恶意 SQL 和配置条件下的堆缓冲区越界）。版本包含修复不等于这些攻击前提在本产品可达，也不构成对未审计漏洞的保证。

参考：[3.53.4 发布记录](https://www.sqlite.org/releaselog/3_53_4.html)、[SQLite 官方漏洞状态表](https://www.sqlite.org/cves.html)、[固定 Go 驱动版本](https://pkg.go.dev/modernc.org/sqlite@v1.58.0)。升级驱动仍须独立核对版本与回归结果，本次未变更依赖版本。

## New host

Nothing to configure. `clawflow controller` (or the app's local-controller start) creates the database and applies migrations on first start.

## Moving an existing PostgreSQL host

Everything below runs on the controller host. PostgreSQL is only read; it is never modified.

1. **Stop dispatch and drain.** Pause or drain workers until no task is `leased`. Stop the controller/web service. `db plan|import|verify|cutover` take the controller lock and refuse to run while a controller is active.
2. `clawflow db plan` reads a consistent snapshot (one `REPEATABLE READ` read-only transaction) and prints row counts and the snapshot id. It lists blockers: executing (leased) tasks, a source schema other than migrations 1-11, tables or columns the importer does not copy. Nothing is ever dropped silently.
3. `clawflow db import` copies the snapshot into a staging file `clawflow.sqlite3.import-<snapshot>` next to the final path. Each table commits together with a progress row, so a crash leaves only whole tables. Rerunning resumes; `--restart` discards the staging file.
4. `clawflow db verify` re-reads the source and checks SQLite integrity and foreign keys, migrations, per-table row counts and order-independent payload digests, usage totals (tokens exact, cost in micro-dollars), and artifact paths (missing paths are warnings; they are kept as recorded). A source that changed after the import fails verification.
5. `clawflow db cutover` re-verifies, writes `config.yaml.pre-sqlite-<time>.bak` (the untouched source configuration), checkpoints and renames the staging file into place, writes `data/cutover.json` (with the DSN password masked), then rewrites `config.yaml` atomically (`settings.database.engine: sqlite`, DSN removed; all other settings kept). Repeating an interrupted cutover is safe.
6. Start the controller and run the acceptance checks: workers register, claim and report; history and usage match; delivery gates behave as before.
7. Only after that, `python3 macos/install-host.py --retire-postgres --apply` unloads and renames **only** `com.zhoushoujianwork.clawflow.postgres`. Other PostgreSQL services are never touched, and the PostgreSQL data directory is left in place. Do not delete the old database until you are certain no rollback is needed.

After cutover the host refuses contradictory input: a leftover `CLAWFLOW_DATABASE_DSN` or `settings.database.dsn`, or a missing SQLite file (restore from backup; it never starts empty). With a PostgreSQL DSN and no cutover it refuses to start on an empty SQLite store (`ErrCutoverRequired`).

### Rollback

- **Before SQLite accepted new work:** `clawflow db rollback` (controller stopped) restores the pre-cutover `config.yaml` and renames the SQLite file to `*.rolled-back-<time>`. It compares a content fingerprint with the one recorded at cutover.
- **After new work:** `db rollback` refuses (`ErrReconcileRequired`). Switching back to the older PostgreSQL snapshot would replay completed tasks and effects. Reconcile first, or do a verified reverse import (not provided by this change).

### 宿主脚本的隔离测试

`HOME` 只能隔离文件路径，不能隔离 `launchctl gui/<uid>`。宿主安装及退役脚本拒绝与系统账户主目录不一致的 `HOME`，包括预览操作；测试必须 mock 服务命令，不能用临时 HOME 配合真实 `--apply` 验证。

运行 `python3 -m unittest discover -s macos -p test_install_host.py`。测试在临时目录中验证拒绝越界、正常预览及退役行为，所有 launchctl 调用均被替换，不操作真实服务。

2026-10-31 12:05（UTC+8），独立验收使用临时 HOME 执行旧版退役脚本，但固定服务标签仍指向真实用户域，正式 PostgreSQL 被卸载，控制器持续返回 HTTP 500。17:17 重新加载原 PostgreSQL plist 后，数据库 WAL 恢复完成、快照请求成功、两个 worker 自动重新连接。未执行 SQLite 切换。`KeepAlive` 会处理已加载服务的退出，不能恢复被显式 bootout 的服务；此事故不能归为普通进程崩溃。

## Verification record

Observed in the development environment on 2026-10-31 (Apple M1, macOS 26.2):

- Multi-process claim, duplicate-enqueue and lease tests use real OS processes, each with its own connection pool (`internal/coordinator/multiprocess_test.go`).
- Importer, cutover, rollback and verify tests run against a real PostgreSQL 17 schema seeded from the released migration-11 schema (`internal/store/importer_test.go`, needs `CLAWFLOW_TEST_DATABASE_DSN`; skipped, not passed, without it).
- Worker write-path load (`CLAWFLOW_MEASURE_LOAD=1 go test -run MeasureWorkerLoad ./internal/coordinator`). Each process loops enqueue, claim, heartbeat, complete on its own connection pool:

| Worker processes | Operations | Throughput | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| 4 | 400 | 2034/s | 0.32 ms | 0.52 ms | 1.1 ms | 140 ms |
| 16 | 1600 | 1465/s | 0.36 ms | 0.71 ms | 37 ms | 950 ms |
| 32 | 3200 | 1641/s | 0.36 ms | 0.78 ms | 22 ms | 1.85 s |

These are single-run numbers for this machine and a tight loop, with no network, no provider calls and no UI load. They are not a capacity promise. Real workers heartbeat far less often than this loop. The tail grows with the number of concurrent writers because writers queue behind one lock; measure on the target host before raising worker counts. UI responsiveness under load was not measured.

## Not verified by this change

Recorded separately from fixtures so they are not mistaken for observed acceptance:

- A real second machine (LAN worker over the network).
- Launching the installed macOS app after login with no PostgreSQL process, and checking the changed screens. The Swift unit tests and `swift build` pass; no desktop session was driven.
- A cutover of the live production database. This change ships the tooling; the live cutover is a separate, operator-run step after the checks above.
- Exact-head GitHub Actions results for this branch.
