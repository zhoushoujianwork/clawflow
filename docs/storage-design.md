# Storage design

ClawFlow keeps its durable state in **PostgreSQL**, accessed through GORM. PostgreSQL is required: there is no SQLite or embedded fallback. Details for contributors are in [storage-design-detail.md](storage-design-detail.md).

## Why PostgreSQL

Runs, usage and the future coordinator need transactions, uniqueness constraints and safe concurrent writers (several `clawflow run` / `clawflow web` processes, later several machines). Flat files cannot give those guarantees.

## What lives where

| In PostgreSQL | Stays in files |
|---|---|
| Durable state added by later slices (runs, usage, coordinator) | Config YAML in `~/.clawflow/config/` |
| `schema_migrations` (applied schema versions) | Per-machine execution locks in `~/.clawflow/locks/` |
| | Raw event logs |

This first slice only adds the connection, migration machinery and the `clawflow db` commands. No domain tables exist yet.

## Configure the DSN

Set one of the following. The environment variable wins.

```bash
export CLAWFLOW_DATABASE_DSN='postgres://clawflow:PASSWORD@127.0.0.1:5432/clawflow?sslmode=disable'
```

```yaml
# ~/.clawflow/config/config.yaml
settings:
  database:
    dsn: postgres://clawflow:PASSWORD@127.0.0.1:5432/clawflow?sslmode=disable
```

The DSN contains a password. It is never synced by `clawflow sync push/pull`, and commands print it with the password masked.

## Local setup

Homebrew:

```bash
brew install postgresql@17
brew services start postgresql@17
createdb clawflow
```

Docker:

```bash
docker run -d --name clawflow-pg -p 5432:5432 \
  -e POSTGRES_USER=clawflow -e POSTGRES_PASSWORD=clawflow -e POSTGRES_DB=clawflow \
  postgres:17
```

## Commands

```bash
clawflow db migrate   # apply pending migrations
clawflow db status    # masked DSN, connectivity, applied/pending migrations
```

`clawflow run` and `clawflow web` check the database before doing any work. They stop with an error if the DSN is missing, the server is unreachable, or migrations are pending (run `clawflow db migrate`). Other commands (`version`, `update`, `issue`, `pr`, `label`, `operators`, `config`, `help`) work without a database.

## Upgrade note

This is a breaking change. After upgrading you must configure a PostgreSQL DSN and run `clawflow db migrate` once; otherwise `clawflow run` and `clawflow web` refuse to start.
