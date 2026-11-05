# 单机控制面容量测量（2026-11）

## 结论

- **SQLite 适合当前低于 10 个并行任务的部署。** 5/10-worker 档 claim p95 为 12.2/41.0 ms，错误率为 0；PostgreSQL 同档为 16.7/43.3 ms。两者均有一个数量级以上的 SLO 余量。
- 两个 store 都在 50–100 个同时活跃的模拟 worker 之间越过 claim p95 500 ms 的容量线。50 档 SQLite/PostgreSQL 分别为 275.5/388.6 ms，100 档分别为 1.038/1.698 s。在本机、当前实现和该写密集负载下，PostgreSQL 没有提高 claim 容量。
- 最先触发的限制是认证/审计与完整 claim 写事务在并发下排队，不是 pending 选择或容量 count 本身。单线程 focused profile 中，选择和 count 的 p95 均低于 0.2 ms；50-worker 端到端 p95 已达数百毫秒。
- 原先“worker loss 60 s 内恢复”的 SLO **设错了**：非 durable 租约默认 TTL 是 120 s，没有缩短 TTL 的现有设置。按真实 TTL 等待后，expiry sweep 到重新 claim 用 27 ms，总计 2m0.023s、fence 前进、重复执行 0。实际 controller 只在队列 dispatch pass 中执行 expiry sweep，所以墙钟恢复还要加上到下一次 pass 的等待。
- 该恢复结果只适用于非 durable 合成任务。durable 任务到期后按设计不会被盲目重派，以避免重复执行；其恢复依赖原 worker 的 journal/result replay，本次没有测量该路径。
- 这些结果只证明本机控制面的合成协议路径，不证明真实模型并发、VCS/provider 容量、LAN、隔离环境或端到端开发吞吐。无模型或真实 VCS 调用，付费成本为 $0。

## 测量声明

| 项目 | 声明值 |
|---|---|
| SQLite 层级 | 5、10、25、50；100 档沿用同机同 harness 的紧邻测量 |
| PostgreSQL 层级 | 5、10、25、50、100；基于当时的 `origin/main`，使用独立 disposable database |
| 项目与公平性 | 100 个项目，每项目 `max_in_flight=5`；一个 noisy-neighbor 项目持有 50% 排队任务 |
| 任务时长 | 70% 为 100 ms，25% 为 1 s，5% 为 5 s |
| worker 协议 | 真实 `clawflow controller` 子进程；隔离配置和状态；认证 HTTPS register、claim、heartbeat、progress、report |
| 心跳 | 生产间隔 30 s；任务均短于 30 s。每个任务在 report 前额外发送 heartbeat 和 progress，故比同长度真实任务更写密集 |
| 每档时长 | 主负载 15 s；各 store 串行运行，不同时启动两组负载 |
| SLO | claim p95 < 500 ms；HTTP 错误率 < 0.1%；错误租约所有者成功上报数为 0 |
| 主机 | Apple M1、8 核、16 GiB、darwin/arm64、macOS 26.2；Go 1.26.3 |
| swap | 各轮前后均为 4,411.81 MiB 已用、708.19 MiB 空闲；测量未增加 swap 使用量 |

## 同档对比

吞吐是 15 秒窗口内成功 report 数除以窗口时长。HTTP 错误数所有档位均为 0；CPU 是 macOS 多核百分比。

| Store | Worker | claim p50 | claim p95 | claim p99 | 吞吐 | HTTP 错误 | CPU | RSS 峰值 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| SQLite | 5 | 4.2 ms | 12.2 ms | 12.2 ms | 0.3/s | 0 / 2,181 | 47.7% | 43.2 MiB |
| PostgreSQL | 5 | 13.6 ms | 16.7 ms | 16.7 ms | 0.3/s | 0 / 2,255 | 33.6% | 33.3 MiB |
| SQLite | 10 | 8.1 ms | 41.0 ms | 41.0 ms | 0.7/s | 0 / 4,826 | 68.1% | 44.6 MiB |
| PostgreSQL | 10 | 17.9 ms | 43.3 ms | 43.3 ms | 0.7/s | 0 / 4,858 | 58.5% | 34.1 MiB |
| SQLite | 25 | 9.6 ms | 58.9 ms | 138.9 ms | 1.7/s | 0 / 11,749 | 145.9% | 48.2 MiB |
| PostgreSQL | 25 | 92.2 ms | 143.2 ms | 149.9 ms | 1.7/s | 0 / 10,490 | 122.8% | 36.9 MiB |
| SQLite | 50 | 56.6 ms | 275.5 ms | 297.3 ms | 3.3/s | 0 / 12,234 | 166.7% | 52.0 MiB |
| PostgreSQL | 50 | 244.1 ms | 388.6 ms | 469.3 ms | 3.3/s | 0 / 10,602 | 129.5% | 41.7 MiB |
| SQLite | 100 | 429.4 ms | **1,038.4 ms** | 1,173.4 ms | 6.3/s | 0 / 11,897 | 172.9% | 57.3 MiB |
| PostgreSQL | 100 | 1,111.1 ms | **1,698.3 ms** | 1,930.1 ms | 6.7/s | 0 / 9,656 | 125.4% | 50.4 MiB |

SQLite 5–50 档和 PostgreSQL 5–100 档在本次补测中均没有数据库锁错误或 HTTP 错误。SQLite 的 100 档取自紧邻的同机运行；它使用相同项目数、任务分布、API 路径、15 秒窗口和 SLO，但不是与 PostgreSQL 同一次进程生命周期。

## 部署判断与容量边界

| 判断 | SQLite | PostgreSQL |
|---|---|---|
| 当前 <10 并行任务 | 可接受；10-worker p95 41.0 ms | 可接受；10-worker p95 43.3 ms |
| 最大实测合格档 | 50 workers，p95 275.5 ms | 50 workers，p95 388.6 ms |
| 首个实测失败档 | 100 workers，p95 1.038 s | 100 workers，p95 1.698 s |
| 本机饱和区间 | 50–100 workers | 50–100 workers |

此处的 worker 数是同时轮询、认证并写审计的模拟 worker，不等同于并行模型调用数。切换 store 不会绕过当前认证、审计和 claim 事务形状；若未来接近 50 个活跃 worker，应先重新测量目标工作负载，而不是仅凭 PostgreSQL 假设容量增加。

## Claim 路径 profile

50 tokens、100 个样本、单线程、已预热。`verify` 包含 token hash 扫描、`last_seen` 更新和一次审计；`capacity` 包含全局、worker、repo 三次 count；完整 `claim` 还包含事务、worker/任务读取、attempt 和审计写。

| Store | 阶段 | p50 | p95 |
|---|---|---:|---:|
| SQLite | token verify（含审计） | 0.223 ms | 0.557 ms |
| SQLite | 纯审计写 | 0.221 ms | 3.389 ms |
| SQLite | pending 选择 | 0.012 ms | 0.016 ms |
| SQLite | 三项容量 count | 0.035 ms | 0.104 ms |
| SQLite | 完整 claim | 0.485 ms | 1.035 ms |
| PostgreSQL | token verify（含审计） | 0.340 ms | 0.480 ms |
| PostgreSQL | 纯审计写 | 0.121 ms | 0.136 ms |
| PostgreSQL | pending 选择 | 0.034 ms | 0.042 ms |
| PostgreSQL | 三项容量 count | 0.110 ms | 0.177 ms |
| PostgreSQL | 完整 claim | 0.672 ms | 1.063 ms |

在无竞争时，pending 选择和容量 count 都不是主要耗时。并发 50 时两库的端到端 p95 比单线程 claim p95 高两个数量级，说明主因是认证/审计和 claim 写事务的并发排队。SQLite 的单写者更直接；PostgreSQL 虽允许并发写，但当前事务和连接池/锁竞争仍使 50/100 档尾延迟高于 SQLite。本 profile 没有改生产代码，也没有把相关性误写成某一条 SQL 的独占因果。

## 租约失效与恢复修正

| 项目 | 结果 |
|---|---|
| 非 durable 默认 lease TTL | 120 s，代码常量；无现有配置项可缩短 |
| 原 60 s worker-loss 观察 | 未恢复是设计预期，不能判为实现故障 |
| 默认 TTL 实测 | 2m0s 到期；sweep 后 27 ms 重新 claim；总计 2m0.023s |
| 任务身份 | 同一 task ID，attempt +1，fencing token +1 |
| 重复执行 | 0 |
| controller 触发 sweep | 在 queue dispatch pass 入队前调用 `ExpireLeases`；worker 的普通 claim 不主动 sweep |
| durable 任务 | expiry sweep 明确跳过，禁止仅凭 heartbeat 丢失重派 |

因此非 durable 的实际恢复下界是 120 s，加上等待下一次 dispatch pass 的时间。若业务需要 60 s 恢复，必须先定义新的安全恢复协议或显式可配置 TTL，并重新验证 fencing 与重复执行；本测量没有修改生产默认值。durable worker 的正确恢复指标应是原 owner/journal 重连和 result replay，而不是 lease requeue。

## 存储增长与公平性

- SQLite 100-worker 原测量：SQLite/WAL/SHM 增长 5.16 MiB，按完整 60.321 s 观察线性折算 307.7 MiB/h；审计行逻辑字节折算 90.9 MiB/h。短样本折算不是稳态预测。
- noisy-neighbor 配置始终为 `max_in_flight=5`。10/25/50/100 档实际 dispatch 份额分别为 50%/20%/10%/5%，同时租约峰值均不超过 5；其余项目获得剩余份额。
- 错误租约所有者被接受的 heartbeat/report 为 0；controller 重启后的重复执行为 0。

## 方法与限制

- 测量由 `CLAWFLOW_MEASURE_SCALE=1`、profile/recovery 专用门控显式开启；普通 `go test` 跳过。
- PostgreSQL 对照使用独立 disposable database，并在每档前重建 schema；临时 harness 未提交。SQLite 与 PostgreSQL 串行运行。
- synthetic repos 全部禁用，避免 scheduler 访问 VCS；report 无 effects，不触发真实 provider 操作。
- PostgreSQL 基线按要求来自当时的 `origin/main`，SQLite 来自本分支；这是 cutover 前后实现对照，不是同一提交仅替换 driver 的微基准。
- 未测真实模型、provider 限流、VCS、LAN、UI、worktree/执行器和 durable journal 恢复；不能外推为模型容量或生产承诺。
