# ClawFlow 项目评估报告

> **评估日期**：2026-05-29
> **代码版本**：`v0.70.0` (+7 commits, `a566977`)
> **代码规模**：Go ~32.5k 行 / 130 文件 / 39 测试文件；Web 前端 React+TS ~11.8k 行；8 个内置算子；501 commits；单人主力维护
> **评估方法**：11 个维度并行深读源码 + 对每条高危发现做对抗式验证（共 19 个 review/verify agent）；本地实跑 `go build ./...`、`go test ./...`、`go vet ./...` 作为 ground truth。

---

## 0. 构建与测试基线（实跑结果）

| 检查 | 结果 |
|---|---|
| `go build ./...` | ✅ 通过 |
| `go vet ./...` | ✅ 干净，无告警 |
| `go test ./...` | ✅ 全部通过（无网络依赖、无 flaky） |
| 无测试文件的包 | `internal/vcs`(根)、`internal/clone`、`internal/projectgen`、`internal/pty`、`cmd/clawflow`(入口) |

---

## 1. 总评

> **核心引擎成熟、地道、经过实战打磨；问题几乎全部集中在边缘——VCS 层、Web 安全、测试覆盖与文档漂移。** 对一个单人维护、无数据库的自动化 CLI 而言，这是明显高于平均水准的工程。但存在**一个真实且应优先处理的安全短板**（本地 Web 服务的跨域/CSRF 防护缺失）。

### 维度评分（10 分制）

| 维度 | 分 | 一句话 |
|---|---|---|
| 整体架构 | **8** | label 驱动的算子模型连贯、可扩展，包依赖无环分层 DAG |
| 算子核心抽象 | **8** | text-in/marker-out 契约干净，解析鲁棒、测试充分 |
| run 调度循环 | **8** | scan→match→lock→execute→outcome 三层并发模型扎实，崩溃恢复细致 |
| Go 代码质量 | **8** | 0 panic、`%w` 普遍、并发纪律好、依赖极简且有理由 |
| snapshot 子系统 | 7 | 难逻辑正确且测试充分，但 1723 行混了 5 种职责 |
| 配置/多机同步 | 7 | 密钥隔离结构性安全，LWW 合并讲究；但缺校验、Settings 整块云覆盖 |
| 文档 / DX | 7 | 上手路径与算子作者文档强，但有多处与代码漂移 |
| Web API + 前端 | 6 | Go 侧并发处理细致；前端路由文件过大、无统一 API client |
| 测试 | 6 | 已有测试质量高，但覆盖严重不均：最复杂代码最少测 |
| VCS 抽象层 | 6 | 接口干净对称，但无分页、零限流、HTTP 不带 context |
| **安全** | **5** | 命令注入与 token 处理良好；本地 Web 服务跨域防护缺失 |

形态清晰：**核心 8 分，外围 6 分，安全 5 分。**

---

## 2. 真正的强项

### 2.1 算子模型这个核心抽象立得住（架构 / 算子 = 8）
- `operator.go`(111 行) 定义结构 + `Parse()`；`matcher.go` 是纯函数标签匹配(45 行)；outcome-marker 协议把"AI 只输出文本、所有 VCS 副作用归 runner"落得很干净，写回集中在 `internal/operator/runner.go:206-245`。
- 包依赖是**无环的分层 DAG**：config/vcs 为叶子，operator 依赖 claude/config/project，api/pilot 为顶层组合者；`chat` 用 adapter 避免 import `snapshot`（`schedule.go:527-534`）。
- runner 只依赖一个 **4 方法的窄 VCS 接口**（`runner.go:61-66`）而非完整 40 方法的 `vcs.Client`，并通过 `RunOptions.RunFunc` 注入 fake claude，使整条流水线可在不起子进程的情况下测试。
- "改行为 = 写 SKILL.md，不动 Go" 这句话**大体成立**。

### 2.2 地道、成熟的 Go（代码质量 = 8）
- 全代码库 **0 个 `panic()`**（已核验）；错误显式返回并用 `%w` 包裹（375 处 `fmt.Errorf` 中 176 处 wrap）；sentinel error（`ErrRateLimit`/`ErrAuthError`/`ErrNotSupported`）双重 `%w` 链正确，调用方可 `errors.Is` 分类同时保留底层 cause。
- 并发纪律好：buffered-channel worker pool + `sync.Map` per-key 锁 + `atomic.Bool` 跨 worker 信号 + `O_CREATE\|O_EXCL` 原子锁文件（带 PID 存活检测与陈旧锁回收）。`-race` 在已测并发包通过。
- 子进程生命周期正确：claude 作为进程组 leader 运行，deadline 到时 SIGTERM→SIGKILL 整个进程组，解决了 ctx 取消无法处理的孤儿孙进程挂起（#213），并有专门测试复现该场景。
- 依赖极简（仅 cobra/yaml/pty/websocket + 2 间接），且手写 VCS client / 锁文件**有理由**，非硬凑。

### 2.3 运维成熟度超出项目体量（调度 = 8）
- 一连串真实生产故障被找到并**带回归测试**修复，每条对应 issue：写回 5 分钟超时防 runner 卡死(#117)、no-marker 计入失败防死循环(#143)、进程组 kill 防孤儿(#213)、self-watchdog + git 硬化 + bounded git(#216)、auth-error(403) 不计入熔断防误伤健康 issue(#204)。
- 崩溃恢复用 `running`→`finalizing`→terminal 状态协议 + `ReconcileStaleRuns` 的 PID 存活对账（`snapshot.go:1129-1333`、`1028-1045`），并能在 runner 中途死亡时回读 `events.jsonl` 判定 claude 是否其实成功——属于真正的 defense-in-depth。
- 错误分类有层次：rate-limit / auth-error / no-marker / empty-output 各自路由到不同状态与熔断语义。

### 2.4 密钥处理是"结构性安全"而非"尽力而为"（安全 / 配置）
- token（GH/GitLab/Claude）单独存 `credentials.yaml`，权限 **0600**；非密钥 `config.yaml` 才 0644。
- Gist 同步 payload 由专门的 `syncableRepo` / `gistPayload` 类型构造——**类型上根本没有 token 字段**，因此未来改代码也无法误把凭证序列化进 Gist。CLAUDE.md 关于"token + local_path 不进 Gist"的声明经核验**属实**（`internal/config/sync.go:28-124`）。
- settings API 默认掩码（只返回 `*_set` 布尔 + 后 4 位），明文 reveal 必须显式 POST（不走 GET，避免落入 URL/历史）；clone URL 中的 token 经 `sanitizeURLForLog` 脱敏后才写日志。

### 2.5 文档与上手体验（文档 = 7）
- README → 端到端 bug 示例 + 架构/流水线配图；`docs/quickstart-claude-code.md` 补充 Claude Code 路径与真实坑（PATH 不继承）。
- `get.sh` 一行安装稳健（平台/架构探测、curl/wget 回退、sudo 检测、幂等 config 播种、shell-rc PATH 注入）。
- CLAUDE.md 的算子规范（frontmatter schema 表、outcome-marker 协议、runner 后处理步骤、设计原则）+ `evaluate-bug/SKILL.md` 作为范本，给算子作者很强的模板。
- CI 会跑 `operators validate` 硬失败于坏 frontmatter + 全子命令 `--help` 冒烟。

---

## 3. 真正的弱项（按优先级）

### 🔴 P0 — 本地 Web 服务跨域/CSRF 防护缺失（安全 = 5，已深度对抗验证）

`clawflow web` 运行期间，**任何被访问的恶意网页都能打到 `127.0.0.1:<port>`**：
- PTY WebSocket 设了 `CheckOrigin: func(r) bool { return true }`（`internal/pty/server.go:19`，已核验），`/ws/pty` 会拉起带 Bash 的 `clawflow chat`（`--dangerously-skip-permissions`）。
- 所有 `/api/*` handler **零** Origin/Host/CSRF 校验，仅查 `r.Method`；`http.Server` 直接挂裸 mux 无中间件（`web.go:341-345`）。

**对抗验证后的精确结论**：
- token **窃取**那条被验证为**夸大**：`/api/settings/reveal` 返回明文 token，但服务器不发任何 CORS 头，浏览器同源策略**挡住跨域读响应体**，纯 fetch 偷不走。
- 但**有副作用的跨域 POST 是真能打的**：handler 不校验 `Content-Type`，攻击者用 `text/plain` 发"CORS simple request"绕过预检，于是 `/api/settings/tokens`(覆写 token)、`/api/chat/spawn`(拉起本地终端)、`/api/run`、`/api/update`(自我替换二进制) 全部触发。
- 再叠加 **DNS-rebinding**（无 Host 校验）可绕过 loopback 假设、把 reveal 响应也读走，并打到那个全 origin 放行的 Bash shell——构成 RCE 级暴露。
- `/api/browse-directory` 还能枚举任意目录（无 jail，`browse_directory.go`）。

> ⚠️ **附带纠偏**：之前记忆中"`/api/version`+`/api/chat/spawn` 有 origin-checked CORS"在**当前 clawflow repo 不存在**（grep 全仓零命中）。该防护要么在 saas 侧、要么尚未落地到此仓——这正是本问题根因。

**建议修法**：加一层中间件校验 `Origin`/`Host` 在 `127.0.0.1:<port>` 白名单内 + 收紧 PTY `CheckOrigin`。半天工作量，堵住最严重的洞。威胁模型摆在这——一个持有 VCS token、能跑无人值守 Bash agent 的工具，loopback 绑定不是安全边界。

---

### 🟠 P1 — 编排核心几乎没测试（测试 = 6，已核验 4.5%）

`cmd/clawflow/commands/run.go`(1908 行) 包级覆盖率 **4.5%**——`runOnce` / `scanRepoOnce` / `runJobsParallel` / `runOneOperator`(~318 行执行心脏) / `runPostAutomation` / `checkCircuitBreaker` / worktree 生命周期**全部 0%**。产品最核心的控制流在裸奔，回归会静默上线。

`internal/clone`(304 行) **0 测试**，却包含 `buildCloneURL`(把 token 注入 clone URL) 与 `sanitizeURLForLog`(脱敏)——直接关联 CLAUDE.md"token 不得泄露到日志"的硬规则，一旦回归即泄露凭证且无网兜底。

> 说明：**已有的 39 个测试质量很高**——行为驱动、表驱动、mock seam 选得好（手写 `fakeVCS`、httptest server、注入式 `RunFunc`/`runnerStillAlive`），且大量编码真实回归并带 issue 引用。问题纯粹是**覆盖不均：最复杂、最危险的代码恰恰最少测**。

---

### 🟠 P2 — VCS 层正确性缺口（VCS = 6，已核验 confirmed）

- **无分页**：所有 list 写死 `per_page=100` 且不跟 `Link: next`(GitHub) / `X-Next-Page`(GitLab)。`do()` 直接丢弃 `resp.Header`，结构上无法续页。**>100 个 open issue 的仓库，101 号之后算子永不触发**——静默丢数据。作者其实已知（`issue.go:461` 注释专门绕开它）。
- **零限流/重试**：`do()`/`doJSON()` 单发，不处理 429 / `Retry-After` / `X-RateLimit-Remaining` / 5xx；HTTP client 用 `http.NewRequest` **不带 context**，仅 30s 固定超时——run 取消打不断在途请求。（缓解：单仓失败会 continue 并在下个 cron tick 自愈，blast radius 比初判小。）
- **抽象泄漏**：`GetIssue` 在 `issue.go:466` 直接 `github.New(...)` 构造、硬编码 github.com，忽略仓库配置的 `BaseURL`——此路径对 GitHub Enterprise 失效。
- VCS client 工厂（platform switch）在 4 处复制：`vcs_client.go`、`api/labels.go`、`pilot/schedule.go`——缺一个统一的 `vcs.NewClient`。

---

### 🟡 P3 — 算子幂等性裂缝（算子，验证为 partial：真实但有缓解）

`implement` / `decompose` 被允许调 `clawflow pr/issue create`（违反"算子不碰 VCS"契约，文档自称"个例"）。若 claude 已建 PR、而 runner 写回失败/超时，触发 label 不会被摘 → 下一轮**重复建 PR / 重复子 issue**。
- `implement` 有 worktree-resume 软缓解 + resume-context 自然语言提示，且仓里存在 `PRExistsForIssue` / `pr-check` 去重原语——但**从未接线**到 runner。
- `decompose` 用 analysis snapshot，**完全无去重保护**——重触发即产生整套重复子 issue。

**建议**：把 `PRExistsForIssue` 式去重接到 `decompose`/`implement` 触发前。

---

### 🟡 P4 — God-files（架构 / 代码质量）

| 文件 | 行数 | 问题 |
|---|---|---|
| `cmd/clawflow/commands/run.go` | 1908 | 混 scan/match、worker pool、worktree+git 管道(~600 行)、CI 轮询、watchdog、post-automation 六种职责；worktree/git 簇应独立成 `internal/worktree` |
| `internal/snapshot/snapshot.go` | 1723 | 混数据目录管理、计费/用量聚合、状态写入、对账引擎、Pilot 子系统五种职责；`lock.go` 放此包属命名错配 |
| `internal/config/config.go` | 1032 | 混 repo 配置、provider 凭证+模型角色解析(~250 行)、迁移、路径助手、git remote 解析 |

内部其实分解得不错（多为小函数），但单文件过大且职责混杂，导航成本高。

---

### 🟡 P5 — 文档漂移（文档 = 7，已逐条核验属实）

| 项 | 文档说 | 实际 |
|---|---|---|
| Dashboard 端口 | README 三处写 `8080` | 默认 `8090`(`web.go:432`)——首用即 connection refused |
| 构建命令 | CLAUDE.md `go build -o clawflow .` | 根是 `package clawflow`(只 embed)，`main` 在 `cmd/clawflow/`；从根构建出 `ar archive` 非可执行文件 |
| `labels_required_any` | CLAUDE.md schema 表**未提** | 已实现且 `evaluate-feat` 在用 |
| `evaluate-feat` | README 标"planned, post-MVP" | 已内置上线（README 自相矛盾，186 行又当 live 用） |
| 算子覆盖 | label 表只列 ~4 个 | 实际 8 个；`classify` 仅在流程箭头出现，`reply-question` 完全缺席 |
| Go 版本 | 1.21+ / 1.22+ / CI 1.22 | `go.mod` 为 `1.25.0`——三处皆不符，CI 也是潜在发布风险 |
| 配置文件名 | docs 用 `config.yaml` | `get.sh` 播种 `repos.yaml`（靠 `config.go:807` 静默迁移才工作） |
| 前端包管理器 | CLAUDE.md 写 `npm` | 实为 pnpm-only（`npm install` 会生成冲突 lockfile） |

---

### 🟡 P6 — 前端工程化（Web = 6）

- 路由文件过大：`_app.projects.$name.tsx`(1570 行, 35 个 useState, 20 处内联 fetch)、`_app.settings.tsx`(1110 行, 47 useState)、`_app.dashboard.tsx`(1129 行)——network I/O、派生状态、JSX 交织，难测难推理。
- **无统一 API client**：92 处手写 `fetch`，各自 `.then/.catch`，无 base URL/错误归一/重试/类型；未用 TanStack Query（虽用了 TanStack Router）。
- **死代码**：`sseClient.ts`(141 行) / `extractFencedBlock.ts`(49 行) 导出但全仓无引用——后端无 SSE handler，是废弃 SaaS chat 路径残留。
- 路由用字符串前缀 + 手写 method 分发；可用 Go 1.22 `mux` 的 `POST /api/.../{index}` 模式简化。

---

### 🟡 P7 — 配置层细节（配置 = 7）

- **Settings 整块"云覆盖"**：`mergeConfigsInternal` 直接 `Settings: remote.Settings`，无逐字段 LWW。但 Settings 含机器本地字段（`GithubCloneDir`/`Terminal`/`DefaultIDE`/`RunPaused`/`RunIntervalMinutes`）——从另一台机 pull 会静默覆盖。典型场景：在笔记本暂停 auto-runner，桌面机上次 push 的 `RunPaused=false` 被 pull 下来把它**重新启用**。
- **无任何校验**：没有 `Validate()`，`PollInterval`/`MaxConcurrentAgents`/`BillingCycleDay`(文档说 1–28) 等取值无边界检查，错值直通消费方并经同步传遍 fleet。
- **skills/projects 资产明文上传**：`DiscoverSkillAssets`/`DiscoverProjectAssets` 把 `~/.clawflow/skills`、`~/.clawflow/projects` 下所有 `.md/.yaml` 扫进同一 Gist，仅按扩展名过滤、**不扫密钥**——用户若在自定义 SKILL.md 硬编码 key 会随之发布（保护 `credentials.yaml` 的结构性安全**不延伸**到这些自由文本）。

---

## 4. 建议修复顺序

| 序 | 项 | 价值 | 成本 |
|---|---|---|---|
| 1 | **Web 安全中间件**（Origin/Host 白名单 + 收紧 PTY `CheckOrigin`） | 堵住最严重洞 | ~半天 |
| 2 | **VCS 分页** | 修静默正确性 bug | 中 |
| 3 | 给 `run.go` 编排核心 + `clone` 脱敏补测试 | 守住核心控制流 | 中 |
| 4 | `decompose`/`implement` 接上 `PRExistsForIssue` 去重 | 补幂等裂缝 | 小 |
| 5 | 文档修正（端口/build/`labels_required_any`/Go 版本/pnpm） | 降低新用户绊脚 | 小 |
| 6 | 拆 god-files（`run.go` worktree 簇 → `internal/worktree`） | 长期可维护性 | 中 |

---

## 附：评估覆盖维度

整体架构、算子核心抽象、run 调度循环、VCS 抽象层、snapshot 子系统、配置/多机同步、Web API+前端、测试、Go 代码质量、安全、文档/DX——共 11 个维度，每个维度的高危发现均经独立 agent 对抗式验证（confirmed / partial / refuted）。本报告已剔除被 refuted 的发现，partial 项保留并标注了精确边界。
