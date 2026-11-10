# ADR-0010：重启和升级不终止健康任务，恢复前先验证

- 状态：Accepted
- 日期：2026-10-30

## 背景

更新 dashboard、controller 或 worker 不应该要求终止正在健康运行的任务。旧的内嵌本机 worker 与 controller 进程绑定，已经在运行的旧进程无法事后分离（runbook 的说法），所以首次切换必须先排空它们。#623（2026-10-31）把一组已完成的恢复修复合入主分支，并要求更新服务不打断任务。

## 决定

`docs/resilient-execution-design.md`（提交 `9133b741`，2026-10-30）和 `docs/resilient-execution-runbook.md` 记录了方案：

- 本机 worker 独立运行，与局域网 worker 使用同一协议。每个任务有持久的执行日志（journal）和结果发件箱（outbox）。controller 幂等接收结果，只在持久化后才确认。
- controller 重启时，运行中的任务继续，结果先留在本地，controller 回来后再同步。新调度在中断期间暂停。
- worker 升级时启动新一代处理新任务，旧一代把已有任务做完，所有结果确认后再退役。同一个逻辑 worker 的各代共享容量，不会因升级而增加。
- 恢复有三条路径：重连仍在运行的执行器；在原 worktree 里继续被中断的 Offload 会话；会话无法恢复时从保留的 worktree 上下文重新开始。最后一条是新的、被记录的执行尝试，不是透明续跑。
- 恢复前必须验证授权、归属、独占锁、profile 和后端恢复能力。不安全的恢复保持阻塞，不另起 worktree。主动取消的任务永远不自动恢复，profile 不会被悄悄切换。
- 心跳丢失不等于可以安全重派，更不能因此静默启动重复执行。
- 保留被中断尝试的 worktree、journal 和 Offload 会话谱系。
- 首次切换必须排空仍在运行的旧内嵌 worker，源码支持不会迁移已经在运行的旧 worker。
- controller 升级使用受控的滚动流程（#614）：先排空，再把候选版本提升，失败可回滚；单机单 controller 切换可能有短暂的 API 中断，要测量并报告，不声称零停机。

## 理由

用户不应因为一次更新而丢掉正在进行的实现。重复执行会产生重复的外部副作用（重复 PR、重复评论），因此“不确定时宁可阻塞也不重复”。

## 放弃的方案

- 引入消息队列或共享的 worker 数据库：设计明确不做。controller 状态是权威，worker 存储是私有且本地的。
- 把心跳丢失当作安全重派的依据：禁止。
- 恢复时静默切换 profile 或另起 worktree：禁止，恢复要么验证通过，要么阻塞。
- 声称零停机或真正的多 controller 重叠 HA：#614 把它排除在该切片之外。
- 机器重启后保留正在运行的进程：做不到，恢复只能依赖保留下来的磁盘状态和后端的恢复支持。

## 相关 issue

- [#623](https://github.com/zhoushoujianwork/clawflow/issues/623) 更新服务不打断任务，把恢复修复合入主分支
- [#614](https://github.com/zhoushoujianwork/clawflow/issues/614) 排空并安全提升已验证版本，可回滚
- [#351](https://github.com/zhoushoujianwork/clawflow/issues/351) 持久化尝试、带 fencing 的租约和对账后的外部效果
- 细节见 `docs/resilient-execution-design.md`、`docs/resilient-execution-runbook.md`、`docs/controller-upgrade.md`
