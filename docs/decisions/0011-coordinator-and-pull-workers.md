# ADR-0011：一个 coordinator，worker 拉取任务，按标签匹配

- 状态：Accepted
- 日期：2026-10-27

## 背景

仓库曾通过 `bound_machine` 和 `require_binding` 绑定到某台机器，彼此独立的调度器靠这个绑定避免重复工作（#383 写明“independent schedulers still rely on binding”）。#383 要解决的是：不改 coordinator 配置、不绑定机器，就能在网络上任意位置增加执行容量。

## 决定

#383（2026-10-27）是设计记录：

- 一台机器（coordinator）负责扫描、调度、判定和合并，持有任务存储、租约、验收证据和合并门禁。
- worker 通过 `clawflow worker` 拉取任务，用自己的执行器和凭据执行，心跳续租并上报结果。worker 永远不合并。
- 没有配置 coordinator 时保持原有的单机模式，行为不变。
- 任务状态是 `pending → leased → done | failed | cancelled`。租约过期把任务退回 `pending`，次数受现有重试和熔断规则限制。每个 (仓库, issue/PR, 类型) 同时只有一个活动任务。
- 领取返回带 fencing token 的租约。每次完成或上传都要带 token，coordinator 拒绝过期 token（409），失去租约的 worker 因此写不了结果。
- 匹配用 worker 标签（类似 GitLab Runner）。任务标签来自仓库 `worker_tags`、按类型的覆盖和 issue 的 `worker:<tag>` 标签。没有匹配 worker 的任务保持等待，不回退到其他机器。
- 每个 worker 自动带 `host-<hostname>` 标签。旧的 `bound_machine` 被读成这个标签并提示，`require_binding` 读取后忽略，重写配置不再包含这两个字段。`repo set --bound-machine` 保留为弃用别名。
- 验收优先使用与实现者不同的 worker（`acceptance_worker: different|any`，有多个可用验收 worker 时默认 `different`）。
- worker API 用独立监听端口、HTTPS 自签证书加指纹固定，每个 worker 一个 token，范围只含领取、心跳、完成和上传验收记录，不含配置、合并、凭据或 Dashboard 访问。

## 理由

- 重复工作由“单一 coordinator 加租约加 fencing”防止，不再依赖机器绑定。
- 增加容量只需在任意机器启动一个 worker，不改 coordinator 配置。

## 放弃的方案

- 按仓库绑定机器（`bound_machine`、`require_binding`）：由 host 标签取代。设计注明只有 coordinator 存在后才退役，因为独立调度器仍依赖绑定。
- worker 自己合并或直接访问配置和凭据：禁止。
- 无匹配 worker 时回退到其他机器：禁止，任务继续等待。
- worker 共享数据库或数据库文件：见 ADR-0004，worker 只用 HTTPS API。

## 相关 issue

- [#383](https://github.com/zhoushoujianwork/clawflow/issues/383) 设计记录
- [#399](https://github.com/zhoushoujianwork/clawflow/issues/399) `clawflow worker` 命令，带租约、心跳和状态目录隔离
- [#351](https://github.com/zhoushoujianwork/clawflow/issues/351) 租约和 fencing 的基础
- [#353](https://github.com/zhoushoujianwork/clawflow/issues/353) 向 10,000 个 worker 规模验证（open）
- 细节见 `docs/worker-pool-design.md`、`docs/worker.md`
