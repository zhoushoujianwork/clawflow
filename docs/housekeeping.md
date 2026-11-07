# 调度主机磁盘清理

每次正常 `clawflow run` 扫描执行一次 housekeeping；也可手动执行
`clawflow housekeeping`，或通过 `clawflow housekeeping --dry-run` 预览。
预览返回候选路径和可回收字节数，不删除文件，也不覆盖上次清理结果。

在机器的配置文件 `settings` 下设置：

```yaml
settings:
  housekeeping:
    disabled: false
    temp_max_age_hours: 24
    review_cache_max_bytes: 2147483648
    min_free_bytes: 2147483648
    disk_warning_percent: 85
    worktree_grace_hours: 24
```

省略或填零使用上述默认值。`disabled` 关闭清理，但不会关闭升级前的
磁盘空间检查。清理只处理 OS 临时根目录下已识别的 ClawFlow 临时目录、
`clawflow-review-go/tmp/go-build*` 和 `clawflow-review-go/build` 下的 Go
缓存桶。按最旧桶优先回收缓存，直到达到限制；未知条目保留，因此可能无法
降到限制以下。通用 `go-build*`、`offload-prompt-*`、全局 Go 构建和模块缓存
没有可靠的 ClawFlow 所有权信息，均保留。正常结束的运行仍立即清理自己的临时目录。

清理与运行使用共享/独占文件锁协调。有任何本机托管任务执行时，本次清理
直接跳过；旧版 issue 锁仍被持有或无法读取时，保留临时目录和缓存。
工作树仅在 PR 已合并或关闭、同分支无打开的 PR、无活跃调度任务、可取得 issue
执行锁且工作树干净时通过 Git 移除；数据库或 VCS 状态不可用时保留。
这替代调度过程以前按“空工作树”判断的回收，避免误删打开的 PR 所用工作树。
不强制移除，不遍历根目录内的符号链接。

### 干净且已合并的 issue 工作树

实现任务的工作树以 detached HEAD 创建，没有本地分支，所以上面按 PR 分支判断的
回收对它们不生效。另有一遍独立的回收，不依赖 VCS 状态，只处理机器状态根下
`worktrees/` 和内置 local worker 状态根（`local-worker/worktrees/`）里的
`issue-<N>-*` 目录；数据目录之外的 worktree 一律不碰。目录同时满足下列条件才会
通过 `git worktree remove`（不加 `--force`）移除：

- 工作树干净（无未提交、未跟踪的改动）；
- HEAD 已包含在默认分支中（仓库配置的 `base_branch`，未配置时为 `origin` 的默认分支）；
- 该 issue 没有排队或持有租约的调度任务，且 issue 执行锁可取得，`git worktree lock` 未设置；
- 工作树（目录、HEAD、索引）超过宽限期没有变化，默认 24 小时，由
  `settings.housekeeping.worktree_grace_hours` 配置。

任一条件不满足，或调度任务状态无法读取，目录都保留。只移除工作树，不删除分支。

同一遍还会对相关仓库执行 `git worktree prune`，清掉目录已不存在、且超过同一宽限期的
记录。如果该仓库还有位于数据目录之外的失效记录，则改为逐条清理数据目录内的记录，
数据目录外的记录保持不变。

每次执行都在 `run.log` 写入 `housekeeping/worktree_removed`（路径）、
`housekeeping/worktree_kept`（路径和原因）以及汇总行 `housekeeping/worktrees`
（移除数、清理记录数、保留数及按原因的计数）。结果文件的 `worktrees` 字段含同样的计数。
`--dry-run` 给出同样的判断，不移除任何内容。

自升级在分配构建目录前检查源目录、安装目标目录和临时目录所在卷。
任一卷可用空间低于 `min_free_bytes`（或探测失败），先尝试清理，再次检查仍不满足
则跳过升级，返回 `insufficient_disk`，并附可用/所需字节信息。

`clawflow platform status` 的状态输出包含 `disk`、`temp_disk` 和
`last_housekeeping`；磁盘的 `warning` 字段表示已达到配置的使用率阈值。
最后一次非预览结果保存在机器状态根的 `data/housekeeping.json`，包括跳过原因、
错误、已回收路径和字节数。
