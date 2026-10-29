# 队列选择

启用仓库中的所有 open leaf issue 默认参与队列；owner 添加 `clawflow-skip` 即可排除。旧 `clawflow` 标签不影响选择，也不会被删除。没有选择模式配置。

保留现有终态标签、暂停/取消、依赖、worker 主机亲和性及重试限制。`agent-skipped` 表示 agent 被阻塞，不等同于 owner 的退出选择。只有 owner 能添加或删除 `clawflow-skip`；算子结果、修复、Pilot 和解阻流程均不能修改它。

顺序仍为 P0–P3（无标签为 P2）、手动 rank、创建时间、issue key，仓库 `max_in_flight` 不变。`queue add/remove/move` 仅维护 rank，不改变资格；移除 rank 后仍会自动选中。

退出选择同时阻止关联托管 PR 的新 CI/验收/冲突修复、验收及自动合并。已经开始的运行不会被取消，结果仍正常写回。

`queue list` 显示每仓库跳过数量；Board 的 `skipped_issues` 列表展示 owner 跳过的 issue。它们不触发 Pilot，原 `unqueued-issue` 唤醒已移除。标准标签初始化会为 GitHub 和 GitLab 创建 `clawflow-skip`。

普通扫描派发 `implement` 前，会从 issue 正文中提取明确的仓库文件路径。若同仓库的开放托管 PR 已修改这些文件，候选项本轮以 `area_busy` 停车，继续选择后续候选项；`queue list` 和 Board 显示阻塞 PR 编号。没有文件提示时跳过检查；读取 PR 文件失败时本轮不启动实现。存储迁移文件共享编号空间，也视为重叠。同一 PR 连续阻塞三轮后，若无其他可启动候选项且仍有并发空位，第四轮放行；更换阻塞 PR 或不再因区域停车会重置计数。显式定向运行沿用现有绕过队列的行为。

冲突修复算子保留主线已有迁移编号，将 PR 新增迁移排到主线最大版本之后，并更新对应引用和测试。当前基线尚未包含 #496 的实现前 issue 验收计划缓存，因此文件估计目前来自 issue 正文。
