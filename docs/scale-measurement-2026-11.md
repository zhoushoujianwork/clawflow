# 单机 SQLite 控制面容量测量（2026-11）

## 结论

- 在本机和本报告预先声明的负载下，**100 个模拟 worker 已不能满足 claim p95 小于 500 ms 的 SLO**：实测 p95 为 1.038 s，p99 为 1.173 s。因此只能得出“满足该 SLO 的容量低于 100 个同时活跃的模拟 worker”，本次没有测出更低档位的精确上限。
- 最先触发的限制是 claim 尾延迟，而不是 HTTP 错误、`SQLITE_BUSY`、RSS 或 swap：100-worker 档错误率和 `SQLITE_BUSY` 均为 0，RSS 峰值 57.3 MiB，但控制器 CPU 平均为 172.9%（macOS 多核口径）。当前每个认证请求都会校验 token 并写审计记录，SQLite 写入串行；本次没有进一步拆分认证、审计和选队列各自的耗时，不能把全部延迟归因于单一 SQL。
- noisy-neighbor 项目占一半排队任务，但 `max_in_flight=5` 将其同时租约峰值限制为 5；它只获得 5.0% 的实际 dispatch，其他 99 个项目合计 95.0%。
- `SIGKILL` 5-worker 组后，5 个租约在 60 s SLO 窗口内没有恢复，故恢复 SLO 失败。控制器在 10-worker 重连突发中重启后 80.3 ms 恢复认证请求，重复执行为 0。
- 本次只证明单机 SQLite 控制面的合成协议路径，不证明真实模型并发、端到端开发吞吐、VCS/provider 容量、LAN 表现或隔离环境容量。没有调用模型或真实 GitHub/GitLab，也没有产生付费请求。

## 测量声明（运行前固定）

| 项目 | 声明值 |
|---|---|
| 层级 | 100、1,000、10,000 个模拟 worker；上一档明确违反 SLO 时停止后续档位 |
| 项目 | 100 个；每项目 `max_in_flight=5`；一个 noisy-neighbor 项目持有 50% 排队任务 |
| 任务时长 | 70% 为 100 ms，25% 为 1 s，5% 为 5 s |
| worker 协议 | 真实 `clawflow controller` 子进程；隔离 HOME 和状态目录；经认证 HTTPS 执行 register、claim、heartbeat、progress、report |
| 心跳 | 生产间隔为 30 s；合成任务都短于 30 s。为覆盖并加压写路径，每个任务在 report 前额外发送一次 heartbeat 和 progress，因此比同样时长的真实任务更写密集 |
| 计时 | 每档主工作负载 15 s；100-worker 档另含最长 60 s 的 worker-loss SLO 观察和控制器重启 |
| SLO | claim p95 < 500 ms；HTTP 错误率 < 0.1%；故障恢复 < 60 s；错误租约所有者成功上报数必须为 0 |
| 主机 | Apple M1、8 核、16 GiB、darwin/arm64、macOS 26.2；Go 1.26.3 |
| swap 保护 | 测量前后均为 4,419.81 MiB 已用、700.19 MiB 空闲；负载没有增加 swap 使用量。由于主机在测量前已经使用 swap，达到 SLO 后立即停止，未运行更高档位 |
| 外部访问与成本 | 无 VCS、provider、模型或付费请求；付费成本 $0 |

## 层级结果

| 模拟 worker | 状态 | 完成 report | 吞吐 | claim p50 | claim p95 | claim p99 | HTTP 错误率 | `SQLITE_BUSY` |
|---:|---|---:|---:|---:|---:|---:|---:|---:|
| 100 | **SLO 失败** | 95 | 6.3/s | 429 ms | 1,038 ms | 1,173 ms | 0 / 11,897（0.000%） | 0 |
| 1,000 | 未启动 | — | — | — | — | — | — | — |
| 10,000 | 未启动 | — | — | — | — | — | — | — |

1,000 和 10,000 档按预先声明的早停规则取消：100 档的 claim p95 已是 SLO 的 2.08 倍，继续升档只会增加主机压力，不能提供合格容量点。因此本次饱和点是“低于 100 个同时活跃 worker”；没有遇到文件描述符或临时端口上限，也不能据此声称 10,000 在这些资源上可行或不可行。

## 资源与存储

| 指标 | 100-worker 档 |
|---|---:|
| 主负载 / 完整观察时长 | 15.0 s / 60.321 s |
| 控制器平均 CPU | 172.9% |
| 控制器 RSS 峰值 | 57.3 MiB |
| SQLite 文件、WAL、SHM 合计增长 | 5.16 MiB |
| 总数据库增长线性折算 | 307.7 MiB/h |
| 审计行逻辑字节增长线性折算 | 90.9 MiB/h |
| lease churn | 100 次（100 claim、60 s 窗口内 0 expire） |

每小时数字是 60.321 s 短样本的线性折算，不是长时间稳态预测。总数据库口径包含审计表、任务/attempt 表以及 WAL/SHM；审计口径按审计行字段的逻辑字节计算，不能与 SQLite 页占用直接相加。

## 公平性与隔离

| 指标 | 结果 |
|---|---:|
| noisy-neighbor 排队份额 | 50.0% |
| noisy-neighbor dispatch 份额 | 5.0% |
| 其余 99 项目 dispatch 份额 | 95.0% |
| noisy-neighbor 同时租约峰值 | 5 |
| 非租约所有者成功 heartbeat/report | 0 |

负载生成器用另一 worker 身份和相同 fencing token 对已租任务发送 heartbeat；控制器拒绝该请求。表中的隔离失败只计“错误所有者被接受”，拒绝请求不算失败。

## 故障注入

| 故障 | 结果 | SLO |
|---|---|---|
| `SIGKILL` 5-worker 子进程组 | 5 个租约在 60.012 s 内没有回到 pending；未继续等待生产 120 s 租约 TTL | **失败** |
| 10-worker 重连突发中 `SIGKILL` 并重启 controller | 首个成功认证请求 80.3 ms；数据库沿用原状态 | 通过 |
| 控制器重启后的重复执行 | 0 | 通过 |
| 连接池关闭后 worker 重连突发 | 完成，无 HTTP 错误 | 通过 |

worker-loss 结果说明当前 120 s 租约语义不能满足小于 60 s 的恢复目标；本测量没有缩短常量、修改数据库时间或伪造更快恢复。它也没有把 heartbeat 丢失等同于可安全重派。

## 方法与限制

- 测量测试由 `CLAWFLOW_MEASURE_SCALE=1` 显式开启；普通 `go test` 会跳过。
- 任务、token 和 100 个禁用仓库配置只存在于临时目录。仓库禁用避免 scheduler 接触 VCS；report 使用无 effects 的合成失败结果，只走控制面持久化。
- TLS 连接使用隔离控制器生成的证书和 bearer token；没有跳过 worker API 的认证。负载生成器在环回地址上运行，未测 LAN 网络。
- 未测真实模型速率、provider 限流、VCS 可用性、工作树/执行器资源、UI 响应和生产数据库；这些结论不能外推为模型容量或生产承诺。
