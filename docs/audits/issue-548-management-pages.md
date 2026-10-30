# #548 仓库与项目页面引用审计

基线：`3a2cd73`（当前 `origin/main`，已合入依赖 #547 / PR #598）。
范围仅限管理页面及导航；不修改后端、调度、配置数据或权限策略。

| 移除项 | 原引用 | 替代入口或退役依据 |
| --- | --- | --- |
| 仓库 issue 列表、运行/排队/完成/关闭计数、展开状态 | `_app.repos.$.tsx` → `IssueList` / `useIssueGroups` | 仓库筛选的 `/pipeline?workRepo=owner%2Frepo`；执行详情与历史由 Work 提供 |
| 仓库 issue Sync 按钮 | 同上 → `/api/repo/refresh-issues` | Work 页面刷新入口；不再在管理页复制旧 issue 快照视图。Git 同步 `GitSyncCell` 保留 |
| 未绑定/绑定其他主机时隐藏 issue 的说明与重复绑定按钮 | 同上 | 页面上原有 Bound machine 按钮保留；筛选导航对所有仓库可用，原 Status/Auto-merge 禁用条件保留 |
| 项目跨仓库 Backlog、分桶计数、展开状态、Sync all | `_app.projects.$name.tsx` → `BacklogSection` / `PROJECT_SECTIONS` | `/pipeline?project=...`，使用现有项目成员筛选；不再从旧 issue/run/pending 快照推导进度 |
| 项目 Latest Pilot wake、duty chips、issue digest、弹窗、wake 数量 | 同上 → `PilotRun`、`/api/project/pilot-runs` 轮询 | 保留到 `/projects/$name/pilot-runs` 的固定历史入口；Work 提供实时工作信息，管理页不再轮询摘要 |
| 仓库列表 Last activity 列及按最近运行排序 | `_app.repos.index.tsx` → `/api/runs` | 仓库名稳定排序；每行提供仓库筛选 Work / Usage 链接 |
| 上述页面的 `/api/runs`、`/data/issues.json`、`/data/pending.json` 数据读取 | 三个路由 | 删除仅服务重复摘要的状态和请求；未删除共享组件或 API，其他历史页面继续使用 |
| 已退役的手动队列选择控件（旧 issue/scope 选择器） | 当前基线四个管理路由均无引用，本次未删除控件 | `internal/config/retired.go` 的 `warnRetiredQueue` 忽略并提示退役选择器；`retired_test.go` 覆盖旧输入及保存省略。开放叶子 issue 默认入选，rank 由队列管理 |
| classifier / workflow 控件 | 当前基线四个管理路由均无引用，本次未删除控件 | `internal/config/retired.go` 接受旧 workflow 输入并迁移为自主派发；`internal/operator/registry.go` 拒绝退役的 `classify` / `evaluate-bug` / `evaluate-feat` / `decompose` |

## 保留的工作配置与上下文

- 仓库：所有项目成员关系、主项目星标及上下文链接、基线有效性、启用状态、自动合并、绑定主机、克隆、本地路径、IDE/VCS/chat 和 Git 同步。原有提交 handler、请求 payload 和禁用条件保留。
- CI / 独立验收：通过现有 `/api/pipeline` 的 `RepoBoard.ci_required` / `acceptance_required` 展示；缺失为“未报告”，不推断为关闭。策略编辑继续使用 `clawflow repo set`，自动合并开关继续保留。
- 项目：成员添加/移除、项目创建/确认删除、仓库详情链接、成员状态和 Git 同步；context.md、testing.md、deployment.md 的展开、更新、初始化/生成和聊天入口保留。
- 仓库与项目列表、详情均复用 `ContextWorkLinks`。Work 使用现有 `workSearch` 参数，清除其他筛选和详情；Usage 使用现有 repo/project 参数并整页导航，确保其初始筛选重新读取。
- 不改配置保存逻辑，无迁移、自动设置变更或数据删除。保留用户显式确认的既有删除操作。LAN 管理路由守卫及后端写入限制未改。

## 检查与限制

`web/repository-project-pages.test.cjs` 使用隔离 SSR fixture 覆盖四个页面、带特殊字符的项目名、仓库/项目筛选链接、绑定其他主机、主项目、保留的文档/配置入口和已知/未知策略。
`readonly-session.test.cjs` 覆盖 LAN 导航及管理深链拒绝；`scripts/test-work-search.mjs` 覆盖真实 Work 筛选和 URL 编解码。
SSR 检查不等同于浏览器交互或独立验收；未部署、启用 coordinator 或运行付费 agent。
