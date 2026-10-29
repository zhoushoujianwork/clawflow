# 队列选择

启用仓库中的所有 open leaf issue 默认参与队列；owner 添加 `clawflow-skip` 即可排除。旧 `clawflow` 标签不影响选择，也不会被删除。没有选择模式配置。

保留现有终态标签、暂停/取消、依赖、worker 主机亲和性及重试限制。`agent-skipped` 表示 agent 被阻塞，不等同于 owner 的退出选择。只有 owner 能添加或删除 `clawflow-skip`；算子结果、修复、Pilot 和解阻流程均不能修改它。

顺序仍为 P0–P3（无标签为 P2）、手动 rank、创建时间、issue key，仓库 `max_in_flight` 不变。`queue add/remove/move` 仅维护 rank，不改变资格；移除 rank 后仍会自动选中。

退出选择同时阻止关联托管 PR 的新 CI/验收/冲突修复、验收及自动合并。已经开始的运行不会被取消，结果仍正常写回。

`queue list` 显示每仓库跳过数量；Board 的 `skipped_issues` 列表展示 owner 跳过的 issue。它们不触发 Pilot，原 `unqueued-issue` 唤醒已移除。标准标签初始化会为 GitHub 和 GitLab 创建 `clawflow-skip`。

依赖可分多行填写，每行必须以 `Depends on:` 开头，后接逗号分隔的 `#N`（同仓库）或 `owner/repo#N`。所有匹配行按首次出现顺序合并去重，最多保留 10 个依赖；无效或超限 token 会被忽略并产生警告。依赖仍为 open 时，issue 等待执行。
