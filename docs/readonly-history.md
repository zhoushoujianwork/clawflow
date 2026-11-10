# 停用仓库的只读历史与 GitLab 兼容边界

对应 issue #803。本文只写已经核对过的事实、本次改动的行为，以及两个不属于本次代码范围的执行问题的处理建议。

## 结论

| 现象 | 部署版本 `df62382` (`v0.75.0-386`) | 当时 main `ae7dd9d6` | 本次改动后 |
| --- | --- | --- | --- |
| 停用仓库后 Work 页显示"无仓库/无任务" | 复现（源码 `pipeline.go` 只为 `enabled` 或 `auto_merge` 的仓库建看板） | 同一份隔离数据上复现：`/api/pipeline` 返回 0 个仓库，页面写 "No repository is tracked yet" | 显示 "Automation off" 计数、说明块和该仓库的历史条目 |
| Usage 的"运行详情"被拒 | 复现（`/runs/...` 不在只读白名单，且 SPA 只放行 `/pipeline`、`/usage`） | 复现：点击后页面内容是 `{"error":"read-only session: endpoint is not allowed"}` | 详情页可打开，显示脱敏后的失败原因 |
| 只读 `/api/runs?path=` 的返回字段 | 返回完整记录（含 `issue_body`、`summary`、`development_session`、未脱敏的 `error`） | 同左 | 只读会话只返回白名单摘要字段和脱敏后的 `error` |

这三项都不是"旧版本才有"。在 `ae7dd9d6` 上用同一份隔离数据和同一个 Chrome 流程得到相同结果，所以只升级到当时的 main 不能解决，需要本次改动。没有找到已经合入的修复提交，因此不重复实现。

最后一行是复现时顺带发现的：旧行为下 LAN 客户端读 `/api/runs?path=` 能拿到 issue 正文、运行总结和工作目录。本次一并收紧，没有放宽任何边界。

## 本次改动的行为

停止自动化和删除登记是两件事：

- `clawflow repo disable` 只把 `enabled` 置为 false。该仓库的登记、issue 快照、运行记录都保留。
- `clawflow repo remove` 才删除登记。被删除登记的仓库不会出现在看板上。

停用仓库的看板（`stopped: true`）：

- 只读本地记录：调度器上次写下的 `issues.json`、上一份看板里的 PR/验收/合并证据、运行历史。不创建 provider 客户端，不发任何 VCS 请求，不进入派发队列，不匹配 Offload 实时状态。读取页面不会重新启用仓库，也不会启动执行。
- 每条记录带 "Automation off" 标记。空闲的 issue 显示"未处理"，不再写"等待下一次运行"。
- 不计入 Running / Waiting / Attention / Delivered，只计入单独的 "Automation off" 计数和标签页。
- 调度器每轮写 `issues.json` 时保留停用仓库已有的条目（`issueSnapshotKeep`）。

只读会话（非回环来源）：

- `GET /runs/<slug>/issue-<N>/<时间戳>` 这一种页面路径放行；格式不匹配的路径仍是 403。
- `/api/runs?path=` 只返回列表同款白名单字段，加上脱敏后的 `error`（凭据、长十六进制串、本机绝对路径替换为 `[redacted]`，最长 600 字节）。
- `/data/runs/**`（含 `events.jsonl`）、设置、凭据、聊天、目录浏览、所有写方法仍然 403。详情页在只读会话下不提供转录加载按钮。

## GitLab Work Items 能力边界

GitLab 18+ 返回的 issue `web_url` 形如 `https://host/group/project/-/work_items/1`。ClawFlow 读写 issue 只用 Issues API 的项目路径加 `iid`（`/projects/:id/issues[/:iid[/notes]]`），不使用也不解析 `web_url` 做路由，所以新路径不影响读取和调度。`internal/vcs/gitlab/work_items_url_test.go` 用返回 `/-/work_items/` 的假服务验证了列表、读取和评论，并断言没有任何请求走 Work Items 路由。

唯一受影响的是用户把页面链接直接当 `--repo` 传入：`ParseRepoInput` 过去会把 `group/project/-/work_items/1` 当成项目路径，现在在 `/-/` 处截断。

不在范围内，也不宣称支持：

- Work Items 的非 issue 类型（task、epic、incident、objective、key result 等），以及 Work Items GraphQL 接口。
- 子 issue 层级：GitLab 客户端的 `ListSubIssues` / `GetParentIssue` 仍返回 `ErrNotSupported`，所有 GitLab issue 按叶子处理。
- 看板里的 issue 链接继续使用 `/issues/N`，由 GitLab 重定向到当前页面。

## 另外两个执行问题（本次不改代码）

这两个问题的证据来自 issue 描述中的现场记录。本次实现环境无法访问该现场，没有独立复核；下面只写代码里能确认的部分和建议动作。

模型拒绝（`Invalid prompt: ... flagged as potentially violating our usage policy`，18.022s，exit=1，0 turns）：

- 这是 Offload 后端对提示词的拒绝，不是 GitLab 或看板故障。运行记录里它表现为 `status=failed`、`execution_status=error`，现在可以在只读详情页看到原文。
- 建议：先看这次尝试的提示词来源（issue 标题和正文）是否含触发审核的内容，再决定改 issue 或换 Offload profile。不要通过清标签、清状态或反复重试绕过；失败尝试保留为证据。

HTTPS `git fetch` 取不到用户名：

- `describeGitFetchFailure` 把 `could not read username` 归为凭据被拒（`gitFetchAuth`）。`gitexec.Env` 固定 `GIT_TERMINAL_PROMPT=0`，所以没有凭据助手时 HTTPS 远程必然失败而不是提示输入。
- 实现任务的 worktree 创建路径（`run.go` 的 setup）把 fetch 失败当作可继续：警告后用本地缓存的 `origin/<base>` 起步，即可能基于旧基线开发。
- 建议：对该仓库的本地克隆改用 SSH 远程（`clone_protocol` 默认就是 `ssh`），或给 worker 用户配置 git 凭据助手；之后用 `git -C <local_path> fetch origin <base>` 在 worker 主机上确认。
- 是否应在基线无法刷新时拒绝开始实现，是另一个行为决定，建议单独建 issue 讨论。

## 部署与升级

默认构建只嵌入迁移页（`web-retired`），React 看板需要 `legacyweb` 构建标签（`make legacy-web`）。内网只读看板用的是后者，升级时需带同一标签构建。

服务升级按既有排空与交付流程做，本次合入不是生产发布授权。若只读看板前面有反向代理：`webguard` 只看 TCP 对端地址，与服务同机的代理会让所有客户端显示为回环，从而得到完整权限。请让代理跨机器转发，或在代理层自行限制。

## 可重复的验证

`scripts/check-readonly-history.sh` 在临时目录里建 HOME、状态目录和一个记录所有请求的假 GitLab，用非回环地址启动 `clawflow web --remote readonly`，再用无头 Chrome 走完 Work → 详情 → 运行详情、Usage → 运行详情、390px 布局和各类被拒请求。脚本在 provider 收到任何请求或仓库配置被改动时失败。

```bash
pnpm --dir web build
PLAYWRIGHT_MODULE=<playwright 或 playwright-core 路径> ./scripts/check-readonly-history.sh
```
