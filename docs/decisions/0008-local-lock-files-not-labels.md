# ADR-0008：执行互斥用本机锁文件，不用 VCS 标签

- 状态：Accepted
- 日期：2026-04-30

## 背景

早期用 `agent-running` 之类的锁标签标记“有算子正在处理这个 issue”。提交 `25825a16`（2026-04-30）记录了它的代价：每个算子都要加标签再移除，在活动流里留下成对的、对人没有意义的加/删事件；issue 侧“先检查再添加”的互斥本来就有竞争。该提交当时判断，标签承担的可见性可由 Dashboard 的运行中状态替代，单机流程也不需要跨进程互斥。

## 决定

- 锁标签退役（`25825a16`，先改为进程内锁和并行 worker 池）。随后的 `e76e15dd`（2026-05-05）加入本机锁文件，位置在 `~/.clawflow/locks/`。`agent-running` 和 `lock_label` 都不是执行锁。
- SKILL.md 里的 `lock_label` 字段只为兼容旧文件而解析，运行时忽略，新算子可以省略。
- 后来（#423，2026-10-27）把锁文件改成内核级 flock，解决了多个进程同时读到半写锁文件、同时回收陈旧锁的竞争（提交 `1151b0fc`）。
- 在 coordinator 模式下，worker 执行的任务也要持有主机本地的 issue 锁，否则清理逻辑会把正在使用的 worktree 当作废弃而删除（#562）。
- 每个仓库另有 `max_in_flight` 并发限制，但它、issue 锁和 `agent-running` 都不能代替测试环境互斥锁（`docs/testing-environment-contract.md`）。

## 理由

- 可见性改由 Dashboard 的运行中状态提供，不再占用 issue 的标签和事件流。
- 锁文件是本机真实的互斥，标签只是对外可见的协调信号，而且无法原子地检查并设置。

## 放弃的方案

- 用 `agent-running` 标签做并发闸门：退役。
- 以文件是否存在加 PID 判断持有者：被 #423 发现跨进程竞争，改用内核锁。
- 用本机锁解决跨机器互斥：本机锁不提供跨机器、跨 HOME 或非协作程序的互斥。跨机器靠 coordinator 的租约和 fencing（ADR-0011）。

## 相关 issue

- 提交 `25825a16` 退役 `agent-running` 标签
- [#423](https://github.com/zhoushoujianwork/clawflow/issues/423) 跨进程互斥和安全回收陈旧锁
- [#562](https://github.com/zhoushoujianwork/clawflow/issues/562) worker 运行期间持有主机 issue 锁
- 细节见 `internal/snapshot/lock.go`、`internal/filelock`
