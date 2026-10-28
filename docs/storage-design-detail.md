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

### 升级兼容性

自升级和 release 通道的 `clawflow update` 在替换已安装二进制之前，使用候选二进制执行 `db migrate`（最多 5 分钟）。失败或超时会中止升级，保留旧二进制；自升级记录 `run/self_upgrade result=failed reason=migrate`，错误详情可在流水线看板查看。没有待执行迁移时，此步骤为空操作。成功替换后，源码自升级按原有流程重启正在运行的 web 服务。

迁移必须 **expand-only**，兼容上一版本二进制的读写：允许新增表、索引及兼容的列（例如 nullable 或有默认值），禁止删除或重命名旧列、改变旧字段语义，或增加会拒绝旧版写入的约束。迁移期间旧 web/run 进程仍在运行，替换后的回滚也可能恢复旧二进制。每个迁移独立提交，因此升级失败时较早成功的迁移可能保留；不得依赖二进制回滚来回滚数据库。

## Test harness

`internal/store/storetest.New(t)` reads `CLAWFLOW_TEST_DATABASE_DSN` and skips the test when it is unset. For each test it creates a unique schema, opens a Store whose connections use that schema as `search_path` (pgx runtime parameter), and drops the schema with `CASCADE` in `t.Cleanup`. CI runs a `postgres:17` service and sets the variable so store tests always run there.

Local run:

```bash
export CLAWFLOW_TEST_DATABASE_DSN='postgres://clawflow@127.0.0.1:5432/clawflow_test?sslmode=disable'
go test -race ./...
```

## 运行与用量（迁移 002）

- `runs` 和 `pilot_runs` 以绝对 `run_dir` 为主键；开始时间、仓库与开始时间、machine、profile 有索引。Pilot 另有项目与开始时间索引。
- 查询列包括执行归属、状态、起止时间、耗时、摘要、错误、原始日志路径及 nullable 费用/token。`payload` 保留 UI 与会话恢复需要的其他元数据。
- `run_model_usage` 以 `(run_dir, kind, model)` 为主键；一次 upsert 在同一事务内替换主行和全部模型明细。重复导入根据规范化元数据判断 skipped，事务级 advisory lock 序列化同一运行的写入。
- API：`GET /api/runs?kind=run|pilot&repo=...&project=...&from=...&until=...&limit=...`；`GET /api/usage?repo=...&period=current|all|YYYY-MM-DD`。时间区间为 `[from, until)`，可传 RFC3339 或日期。账期使用 UTC 和配置中的 `billingCycleDay`（1–28）；返回的 `period_end` 是最后一天。
- 用量只读取 SQL 中的终态记录；没有 usage 的记录参与所有维度的次数统计，缺失费用/token 不进入求和。结果中的 nullable 总量区分“未报告”和实测零值。没有模型明细时归入记录的 model（历史缺失则为 `unknown`）。
- 本机 `meta.json` 写入后同步 upsert。跨文件与数据库没有分布式事务；失败会返回错误，重新运行 `db import-runs` 可修复缺失行。原始日志不导入数据库。
