# Storage design: detail

User-facing overview: [storage-design.md](storage-design.md). Code: `internal/store`.

## Connection

- `ResolveDSN`: `CLAWFLOW_DATABASE_DSN` overrides `settings.database.dsn`. If both are empty it returns `ErrNoDSN`.
- `Open`: GORM with the `gorm.io/driver/postgres` (pgx, pure Go; `CGO_ENABLED=0` builds work). The GORM logger is silent so SQL parameters are never logged. Pool: 10 open, 5 idle, 30 minute connection lifetime. `Open` pings before returning.
- `MaskDSN` replaces the password with `****` in URL form (`postgres://user:pw@host/db`, including `?password=`) and in key=value form (`password=...`). Every error from `Open` contains only the masked DSN and has the password literals scrubbed from the driver's message.
- `settings.database.dsn` is removed by `config.MarshalForGist` and kept from the local config on `sync pull`.

## Migrations

- `store.Migrations` is an ordered, append-only registry of `Migration{Version, Name, Up}`. Version 1 is a no-op `baseline`. Never edit or reorder a released entry.
- Versions must be positive, unique and ascending; `Migrate` and `Status` reject anything else before touching the database.
- Applied versions are recorded in `schema_migrations(version int primary key, name text, applied_at timestamptz)`.
- `Migrate` runs each pending migration in its own transaction. The transaction first takes `pg_advisory_xact_lock` with a fixed key, so concurrent migrators serialize, re-checks that the version is still pending, runs `Up`, and inserts the `schema_migrations` row in the same transaction. A failing `Up` rolls back its DDL and its row; earlier migrations stay applied. Re-running is a no-op.
- There is no GORM AutoMigrate at startup. Schema changes are explicit migrations applied by `clawflow db migrate`.
- `EnsureReady` (used by `run` and `web`) resolves the DSN, opens the store and fails if any migration is pending. The command gate lives in `cmd/clawflow/commands/db.go` (`requireStore`, overridable only from tests).

## Test harness

`internal/store/storetest.New(t)` reads `CLAWFLOW_TEST_DATABASE_DSN` and skips the test when it is unset. For each test it creates a unique schema, opens a Store whose connections use that schema as `search_path` (pgx runtime parameter), and drops the schema with `CASCADE` in `t.Cleanup`. CI runs a `postgres:17` service and sets the variable so store tests always run there.

Local run:

```bash
export CLAWFLOW_TEST_DATABASE_DSN='postgres://clawflow@127.0.0.1:5432/clawflow_test?sslmode=disable'
go test -race ./...
```
