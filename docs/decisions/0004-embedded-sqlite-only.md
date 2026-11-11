# ADR-0004：存储只用 controller 持有的嵌入式 SQLite

- 状态：Accepted
- 日期：2026-10-30

## 背景

控制面状态（任务、worker、租约、历史、用量、schema 版本）原来放在 PostgreSQL。`docs/storage-design.md` 当时写明 PostgreSQL 是必需的，没有 SQLite 或嵌入式回退，理由是多个 `clawflow run` / `clawflow web` 进程和以后的多台机器需要事务、唯一约束和安全的并发写入。

对本机 macOS 主机来说，这意味着用户要安装、配置并启动一个数据库服务，应用才能打开。

## 决定

#612（2026-10-30）把控制面存储换成嵌入式 SQLite，文件在 `<state dir>/data/clawflow.sqlite3`，只由 controller 持有。

- 不需要数据库服务、DSN 或数据库凭据。全新主机直接可用。
- 一个 controller 保持权威。本机和局域网 worker 继续通过已有的带认证 HTTPS API 访问，从不打开 SQLite 文件，文件也不放在网络共享上（打开时检测网络文件系统并拒绝）。
- 每个写事务使用 `BEGIN IMMEDIATE`，WAL 加 `synchronous=FULL`。任务领取、容量检查、入队去重、租约 epoch 和结果接收都在这样的事务里完成。
- 持有 SQLite 写锁的事务内不做任何 VCS 或 provider 调用。外部写入保持至少一次且幂等，靠任务、epoch、inbox 和 effect 标识恢复。
- PostgreSQL 只作为一次性迁移的来源保留：`clawflow db plan/import/verify/cutover`，流程见 `docs/sqlite-host-runbook.md`。
- 不提供长期的数据库选择开关，也不做自动回退。

## 理由

- 本机单 controller 是这个产品的范围，SQLite 满足它，同时去掉了安装和启动数据库服务这一步。
- 实测（`docs/scale-measurement-2026-11.md`）：低于 10 个并行任务时，5 和 10 个 worker 档 claim p95 分别是 12.2 ms 和 41.0 ms，错误率为 0，比 PostgreSQL 同档的 16.7 ms 和 43.3 ms 更低。50 到 100 个模拟 worker 之间两者都越过 500 ms 的 claim p95 容量线，PostgreSQL 没有提高 claim 容量。
- 保留 PostgreSQL 只为导入和回滚，避免维护两套长期存储。

## 放弃的方案

- 继续把 PostgreSQL 作为必需服务（`docs/storage-design.md` 的原设计）：该文档现已标注“Superseded for the controller store”，PostgreSQL 相关文字仅作历史。
- 同时支持 SQLite 和 PostgreSQL 并提供选择开关或自动回退：设计明确不做。
- 多 controller 并行或把大量写入的部署作为目标：不在这个范围内。
- 让 worker 共享数据库文件或直连数据库：worker 只用 HTTPS API。

## 相关 issue

- [#612](https://github.com/zhoushoujianwork/clawflow/issues/612) 本决定
- [#351](https://github.com/zhoushoujianwork/clawflow/issues/351) 其 supervisor 决定（2026-10-29）曾把存储定为 PostgreSQL，被本决定取代
- [#752](https://github.com/zhoushoujianwork/clawflow/issues/752) 切换后的收尾和 PostgreSQL 退役（open）
- 细节见 `docs/sqlite-host-design.md`、`docs/sqlite-host-runbook.md`
