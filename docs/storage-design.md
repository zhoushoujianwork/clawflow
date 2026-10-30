# Storage design

Proposed follow-up: [SQLite for the single-host controller](sqlite-host-design.md). The PostgreSQL-only behavior below describes the current implementation and remains active until that feature is implemented and migration is verified.

ClawFlow keeps its durable state in **PostgreSQL**, accessed through GORM. PostgreSQL is required: there is no SQLite or embedded fallback. Details for contributors are in [storage-design-detail.md](storage-design-detail.md).

## Why PostgreSQL

Runs, usage and the future coordinator need transactions, uniqueness constraints and safe concurrent writers (several `clawflow run` / `clawflow web` processes, later several machines). Flat files cannot give those guarantees.

## What lives where

| In PostgreSQL | Stays in files |
|---|---|
| `runs`、`run_model_usage`、`pilot_runs` 及用量统计 | Config YAML in `~/.clawflow/config/` |
| `schema_migrations` (applied schema versions) | Per-machine execution locks in `~/.clawflow/locks/` |
| | Raw event logs |

运行元数据写入 PostgreSQL，同时保留本机 `meta.json` 用于恢复和导入；原始事件与转录仍保存在文件中。仪表盘通过 SQL API 和 SSE 读取运行历史及用量，不再生成 `runs.json`、`usage.json` 或 `pilot-runs.json`。协调器存储仍属于后续工作。

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
clawflow db import-runs # 导入本机历史，可重复执行
```

`clawflow run` and `clawflow web` check the database before doing any work. They stop with an error if the DSN is missing, the server is unreachable, or migrations are pending (run `clawflow db migrate`). Other commands (`version`, `update`, `issue`, `pr`, `label`, `operators`, `config`, `help`) work without a database.

## Upgrade note

This is a breaking change. After upgrading you must configure a PostgreSQL DSN and run `clawflow db migrate` once; otherwise `clawflow run` and `clawflow web` refuse to start.

升级到运行历史数据库后，先执行 `clawflow db migrate`，再在每台持有历史记录的机器上执行 `clawflow db import-runs`。导入扫描 `data/runs/**/meta.json` 和 `data/pilot-runs/**/meta.json`，报告 imported、updated、skipped、invalid 数量；再次执行不会重复计数。数据库写入失败时，保留的元数据可通过重新导入恢复。`pilot wake` 和 `usage backfill --apply` 也要求数据库可用且迁移已应用。

已完成但没有 usage 的运行仍计入次数，费用和 token 显示“Not reported”；仅有 token 的运行只累加 token。运行中的记录不参与统计。历史缺失的 profile、backend、model 和 machine 统一归为 `unknown`；Pilot 用量按项目归入 `pilot:<project>`，不任意分摊到仓库。
