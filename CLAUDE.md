# CLAUDE.md

## 工具规范

**禁止使用 `gh` CLI**，所有 VCS 操作（issue、PR、label、comment）统一使用 `clawflow` 命令：

```bash
clawflow issue list/create/comment/close
clawflow pr list/create/view/comment/ci-wait
clawflow label add/remove
```

`gh` 仅允许在 `clawflow update` 内部实现中使用（拉取 release assets），其他场景一律禁止。

---

## Cross-Repo API Contract

**This section is mirrored in `/Users/mikas/github/clawflow-saas/CLAUDE.md`. Keep them in sync — if you edit one, edit the other in the same change.**

Two repos, one protocol. The open-source CLI (this repo, Go) and the commercial SaaS (`clawflow-saas`, Rust) talk to each other over the endpoints below. The contract lives in **both** CLAUDE.md files so whichever repo a session is opened in, it sees the same source of truth.

### Ownership

- **CLI dev** (this session, if you're in `clawflow`): owns the Go client code that calls these endpoints, stream-json parsing, worker loop, config sync logic on the local side.
- **SaaS dev** (session in `/Users/mikas/github/clawflow-saas`): owns the HTTP/WS handlers, DB schema for shared tables (`pipeline_runs`, `pipeline_logs`, `billing_usage`, `agents`, `org_repos`), and all auth/rate-limit/credit logic.

Neither session should edit the other's code. Changes that span both = draft the contract diff in one session, paste it into the other.

### Endpoints (CLI ↔ SaaS)

Base: `https://clawflow.daboluo.cc/api/v1` (prod) · local dev: `http://localhost:3000/api/v1`

Auth on all worker endpoints: `Authorization: Bearer <worker_token>` header. Token obtained at `POST /agents/register`.

| Method | Path | Direction | Purpose |
|---|---|---|---|
| `POST` | `/agents/register` | CLI → SaaS | Register a worker, returns `worker_token` + `sync_token` |
| `POST` | `/worker/heartbeat` | CLI → SaaS | Keep-alive, advertises capacity |
| `GET` | `/worker/tasks` | CLI → SaaS | List claimable `pending` pipeline_runs for this worker |
| `POST` | `/worker/tasks/:id/claim` | CLI → SaaS | Atomically claim a run (status → `running`, sets `started_at`) |
| `POST` | `/worker/tasks/:id/logs` | CLI → SaaS | **Batch** upload of buffered log entries (see `LogEntry` below) |
| `POST` | `/worker/tasks/:id/usage` | CLI → SaaS | Report `UsageReport` (token counts + cost) for billing |
| `POST` | `/worker/tasks/:id/complete` | CLI → SaaS | Mark run `success` + optional `pr_url` |
| `POST` | `/worker/tasks/:id/fail` | CLI → SaaS | Mark run `failed` with error message |
| `GET` | `/worker/tasks/stale-prs` | CLI → SaaS | **v0.24.0+** — list PRs whose run is `reconciling` and claimable |
| `POST` | `/worker/tasks/:run_id/reconcile/claim` | CLI → SaaS | Lock one stale-PR for a ~30 min reconcile pass |
| `POST` | `/worker/tasks/:run_id/reconcile/report` | CLI → SaaS | Report reconcile verdict (`merge`/`close`/`defer` + rebase/test/review results) |
| `POST` | `/worker/pipelines` | CLI → SaaS | Push a pipeline_run CLI discovered locally (private VCS) |
| `POST` | `/worker/discover` | CLI → SaaS | Fallback issue-discovery push (no webhook) |
| `POST` | `/worker/health-report` | CLI → SaaS | Per-repo health (for the repo-list badge) |
| `GET` | `/worker/repo-jobs` | CLI → SaaS | List pending repo-level jobs SaaS enqueued (kind=`label_init`, …) |
| `POST` | `/worker/repo-jobs/:id/claim` | CLI → SaaS | Atomically claim a repo job (pending → running) |
| `POST` | `/worker/repo-jobs/:id/complete` | CLI → SaaS | Mark repo job done |
| `POST` | `/worker/repo-jobs/:id/fail` | CLI → SaaS | Mark repo job failed with `{reason}` (truncated to 500 chars server-side) |
| `POST` | `/sync/config` | CLI → SaaS | Push local repo config (upsert by `org_id+platform+full_name`) |
| `GET` | `/sync/config?since=<ts>` | CLI ← SaaS | Pull incremental repo config changes |
| WS | `/ws/worker/tasks/:run_id/stream` | CLI → SaaS | Live per-line log stream during `claude -p` execution |
| WS | `/ws/worker/channel` | CLI ↔ SaaS | Bidirectional control channel (dispatch, cancel) |

### Shared payload shapes

**`LogEntry`** — used by both the WS stream and the `/logs` batch upload. Both paths MUST send `raw_event` when the source line parses as JSON:

```json
{
  "level": "info | warn | error",
  "message": "string (human-readable summary)",
  "timestamp": "RFC3339",
  "raw_event": { /* original claude stream-json event, or null */ }
}
```

- SaaS stores `raw_event` in `pipeline_logs.raw_event` (jsonb). The frontend renders it as rich cards (`frontend/src/components/PipelineEventCard.tsx`).
- **Known past bug**: CLI v0.21.0 and earlier dropped `raw_event` on the batch HTTP path — fixed in v0.21.1. SaaS side has always accepted it.

**`UsageReport`**:

```json
{
  "model": "claude-opus-4-6",
  "input_tokens":  0,
  "output_tokens": 0,
  "cache_creation_tokens": 0,
  "cache_read_tokens":     0,
  "total_cost_usd": 0.0
}
```

Cost metering rule: `agent_id IS NULL` → SaaS-hosted run, charges credits (see `clawflow-saas/CLAUDE.md` "Billing: Executor Isolation"). `agent_id IS NOT NULL` → user's own worker, usage recorded but no credit deduction.

**`RepoJob`** — per-repo background work SaaS needs the CLI worker to execute locally. First use case: `label_init` (when a user adds a repo through the SaaS UI, SaaS has no creds for the user's VCS — especially for private-network GitLab — so it enqueues this and the CLI runs `InitLabels` on the next tick). CLI-side implementation lives in `cmd/clawflow/commands/worker_repo_jobs.go`.

```json
{
  "id": "uuid",
  "repo_id": "uuid",
  "kind": "label_init",
  "platform": "github | gitlab",
  "full_name": "owner/repo",
  "attempts": 0,
  "created_at": "RFC3339"
}
```

- SaaS enqueues idempotently on `(repo_id, kind)` while status ∈ `pending|running`. Re-adding an already-initialised repo is a safe no-op.
- CLI reports unsupported kinds via `/fail` so a SaaS-side rollout of a new kind can't strand the queue on older CLIs.
- `GET /worker/repo-jobs` returns `{"jobs": [RepoJob, ...]}`.
- Loop cadence: 60s (see `repoJobsInterval`).

**`ReconcileReport`** (v0.24.0+) — body of `/worker/tasks/:run_id/reconcile/report`:

```json
{
  "action": "merge | close | defer",
  "reason": "<= 500 chars, required",
  "current_base_sha": "abc123…",
  "rebase_result":  "clean | conflicts_resolved | conflicts_unresolvable | not_attempted",
  "test_result":    "passed | failed | skipped | not_attempted",
  "review_state":   "approved | changes_requested | no_review | comments_only",
  "usage":      { /* UsageReport, optional */ },
  "vcs_action": { "type": "merged|closed|none", "performed_at": "RFC3339", "commit_sha": "…" }
}
```

- SaaS Phase 1 hard-downgrades `action=merge` to `defer` in its DB, but the CLI has already performed the real merge via the repo's GitHub token by the time this fires — the CLI owns the VCS side.
- SaaS returns `400` if `reason` > 500 chars, or if `action=close` with `test_result ∈ {skipped, not_attempted}` (close requires a real test outcome).
- SaaS returns `409` if the run isn't in `reconciling` state.
- Phase 1 is GitHub-only; GitLab stale-PRs are filtered out server-side.

### Evolving the contract

1. **Only add backward-compatible fields.** New optional fields OK. Renaming or removing a field = coordinated release on both sides, with the CLI bump going out first.
2. **Never break an older CLI** without bumping the CLI's minimum version check. Real users may be on v0.19 for weeks.
3. **When the change spans both repos**:
   - SaaS dev drafts the contract change in the shared section + in `api-types` crate.
   - Paste the new shape into the CLI session.
   - CLI dev mirrors the change in its Go structs, cuts a new CLI tag.
   - SaaS dev merges + deploys.
4. **Mention the cross-cutting change in both commits** so `git log` shows it on both sides (e.g., `feat(worker/contract): add RawEvent to batch LogEntry — matches SaaS 0abc123`).

---



每次新版本发布只需两步：

### 1. 打 Tag 并推送

```bash
git tag -a v{x.y.z} -m "{release message}"
git push origin v{x.y.z}
```

GitHub Actions（`.github/workflows/release.yml`）会自动完成：
- 三平台构建（darwin arm64/amd64、linux amd64）
- 创建 GitHub Release 并上传所有平台二进制

版本号规则：
- `v0.x.0` — 新功能（minor）
- `v0.x.y` — bug 修复（patch）
- `v1.0.0` — 首个稳定版

#### Release Tag 描述规范

Tag message 即 GitHub Release 的标题和正文，格式：

```
{一句话概括本版本核心变化}

## What's New
- {功能1}：{一句话说明用户能感知到的变化}
- {功能2}：{同上}

## Bug Fixes
- {修复1}：{影响范围}

## Breaking Changes（如有）
- {变更说明}：{迁移方式}
```

示例：

```
feat: GitLab 支持 + 自动克隆缺失仓库

## What's New
- GitLab 支持（v11.11+）：可监控 GitLab 仓库的 issue，自动评估并提 MR
- 自动克隆：repos.yaml 中配置的仓库本地路径不存在时自动 clone，无需手动操作
- 标签自动初始化：`clawflow repo add` 时自动在目标仓库创建所需标签

## Bug Fixes
- 修复 GitHub URL 格式化问题（normalize 处理 https/ssh 混用场景）
```

### 2. 验证用户可以自动更新

```bash
gh release view v{x.y.z}
# 确认 assets 列表有各平台文件（Actions 完成后）

# 模拟用户更新
clawflow update
# 应输出：binary updated + SKILL.md updated
```

---

## 用户更新流程

用户安装后，后续版本只需运行：

```bash
clawflow update           # 从 GitHub 下载最新 binary + 更新 SKILL.md
clawflow update --from-source  # 从本地 repo 重新构建（开发用）
```

`clawflow update` 自动读取 `~/.clawflow/config/install.yaml`（由 install.sh 写入），
知道 SKILL.md 装在哪个 agent 目录里，并自动更新。

---

## .gitignore 说明

| 规则 | 原因 |
|------|------|
| `/config/` | 用户配置（现存于 `~/.clawflow/config/`，不进 repo） |
| `clawflow` | 构建产物根目录二进制（通过 release asset 分发） |
| `clawflow_*` | 各平台构建产物 |

---

## Skills 构建规范

### 目录结构

```
skills/<skill-name>/
├── SKILL.md           # 必须，主流程入口
├── evaluation.md      # 详细评估策略、评论模板等
├── subagent-prompt.md # Sub-agent Task Prompt 模板
└── scripts/           # 可执行脚本（如有）
```

### SKILL.md 规范

- **控制在 500 行以内**，超出部分拆到独立文件
- 详细内容（评论模板、prompt 模板、评估维度）放到独立 `.md` 文件
- 在 SKILL.md 里用 Markdown 链接引用，说明文件内容和何时加载：
  ```markdown
  详见 [evaluation.md](evaluation.md)，包含：
  - Bug / Feature / 通用三种评估维度
  - 高/低置信度评论模板
  ```

### Frontmatter 关键字段

```yaml
---
name: skill-name
description: "触发条件描述，Claude 用此判断何时自动加载"
metadata:
  openclaw:
    requires:
      bins: ["git", "clawflow"]
    primaryEnv: "GH_TOKEN"
---
```

- `description` 前置核心触发词，总长度不超过 1536 字符
- `disable-model-invocation: true` — 只允许用户手动 `/skill-name` 触发（适合有副作用的操作）
- `allowed-tools` — 预授权工具，避免每次询问用户

### 拆分原则

| 放 SKILL.md | 放独立文件 |
|------------|-----------|
| 流程步骤、CLI 命令 | 评论/prompt 模板 |
| 安全约束、标签流程图 | 评估维度表格 |
| 文件引用说明 | 详细示例、参考文档 |
