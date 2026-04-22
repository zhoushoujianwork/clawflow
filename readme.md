# ClawFlow

> **Coding as a Service.**  
> ClawFlow watches your GitHub and GitLab repositories, picks up issues tagged `ready-for-agent`, and autonomously attempts to fix them — then opens a Pull Request.

---

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash
```

Supports macOS (Apple Silicon & Intel) and Linux (x86_64 & arm64). The script:
- Downloads the correct binary for your platform
- Auto-detects your agent (`~/.claude/skills/` or `~/.openclaw/skills/`) and installs SKILL.md
- Initializes `~/.clawflow/config/` with template config
- Adds `~/.clawflow/bin` to your shell PATH

---

## Setup

### 1. Store tokens

**GitHub:**
```bash
clawflow config set-token ghp_xxxxxxxxxxxx
```
Required scopes: `repo` (full), `read:org`.

**GitLab:**
```bash
clawflow config set-gitlab-token glpat-xxxxxxxxxxxx
```
Required scopes: `api`.

Tokens are saved to `~/.clawflow/config/credentials.yaml` (mode 0600).  
Environment variables take priority over the file: `GH_TOKEN`, `GITLAB_TOKEN`.

### 2. Add repositories to monitor

`repo add` auto-detects the platform from the input — no flags needed in most cases:

```bash
# GitHub — URL, SSH, or short form
clawflow repo add https://github.com/owner/repo
clawflow repo add git@github.com:owner/repo.git
clawflow repo add owner/repo

# GitLab self-hosted — full URL (nested namespaces supported)
clawflow repo add https://gitlab.company.com/ns/group/repo
clawflow repo add git@gitlab.company.com:ns/group/repo.git

# Local directory — reads .git/config origin automatically
clawflow repo add .
clawflow repo add ~/github/my-repo
```

Override platform or instance URL manually:
```bash
clawflow repo add ns/repo --platform gitlab --base-url https://gitlab.company.com
```

For GitLab self-hosted, you can also register the host in `~/.clawflow/config/config.yaml` so short-form inputs are recognized:
```yaml
settings:
  gitlab_hosts:
    - gitlab.company.com
```

Manage repos:
```bash
clawflow repo list
clawflow repo enable  owner/repo
clawflow repo disable owner/repo
clawflow repo remove  owner/repo
```

### 3. Initialize labels

Labels are created automatically on `repo add`. To create them manually:

```bash
clawflow label init owner/repo
```

| Label | Color | Meaning |
|---|---|---|
| `ready-for-agent` | `#00FF00` Green | Owner approved — triggers fix pipeline |
| `agent-evaluated` | `#0075CA` Blue | ClawFlow has assessed this issue |
| `in-progress` | `#FFA500` Orange | Agent is actively working on it |
| `agent-skipped` | `#BDBDBD` Gray | Low confidence — needs more info |
| `agent-failed` | `#FF0000` Red | Agent attempted but failed |
| `blocked` | `#E4E669` Yellow | Waiting on dependency issues |
| `agent-split` | `#8B5CF6` Purple | Issue split into sub-issues |
| `type:bug` | `#D73A4A` Red | Issue classified as a bug report |
| `type:feature` | `#0E8A16` Green | Issue classified as a feature request |
| `type:refactor` | `#1D76DB` Blue | Issue classified as a refactoring task |
| `type:docs` | `#5319E7` Purple | Issue classified as a documentation task |

### 4. Run

Tell your AI agent:

```
ClawFlow run
```

---

## CLI Reference

```
clawflow [command]

Pipeline:
  harvest            Scan repos and output pending issues as JSON
  status             Show current state of all monitored repos
  retry              Re-trigger pipeline for a previously processed issue
  lang detect        Detect language and output build/test commands for changed files

PRs:
  pr create          Create a pull request / merge request
  pr list            List pull requests
  pr view            View a pull request
  pr comment         Post a comment on a pull request
  pr ci-wait         Wait for CI checks to complete
  pr merge           Merge a pull request via the VCS API
  pr rebase          Rebase issue branch onto base branch and force-push

Repo management:
  repo list          List all configured repos
  repo add           Add a repo (URL / SSH / local path / owner/repo)
  repo remove        Remove a repo from config
  repo enable        Enable a repo
  repo disable       Disable a repo (pause without removing)

Issues:
  issue create       Create an issue (useful for testing the pipeline)
  issue list         List open issues in a repo

Labels:
  label add          Add a label to an issue
  label remove       Remove a label from an issue
  label init         Create standard ClawFlow labels in a repo

Worktrees:
  worktree create    Create an isolated git worktree for an issue
  worktree remove    Remove worktree after fix (success or failure)

Records:
  memory write       Write an issue processing record
  pr-check           Check if an open PR already exists for an issue

Config:
  config set-token         Store GitHub token
  config set-gitlab-token  Store GitLab token
  config show              Show current config and token status

Updates:
  update             Download latest binary + update SKILL.md
  update --from-source  Rebuild from local source
```

---

## Supported Platforms

| Platform | Status | Notes |
|---|---|---|
| **GitHub** | ✅ Supported | REST API v3 |
| **GitLab** | ✅ Supported | REST API v4, compatible with self-hosted v11.11+ |

## Supported Agents

| Agent | Status | Notes |
|---|---|---|
| **Claude Code** | ✅ Recommended | Best code capability |
| **OpenClaw** | ✅ Supported | Lightweight local agent |
| Custom agent | 🔧 Configurable | `--agent custom --dir /path` |

> Local quickstart: [Getting started with Claude Code](docs/quickstart-claude-code.md)

---

## How It Works

```
New Issue
    ↓
[clawflow harvest] — scan all repos, filter + PR dedup
    ↓
[AI evaluates] — confidence score, posts proposal as comment
                → adds type:* label + agent-evaluated label
    ↓
[owner adds ready-for-agent]        [low confidence → agent-skipped]
    ↓
[clawflow worktree create] — isolated branch per issue
    ↓
[sub-agent implements fix] — in the worktree
    ↓
[PR opened]
    ↓
[Phase 5.5] Smoke test — build + unit tests for changed packages (max 2 retries)
    ↓ pass
[Phase 5.6] Conflict check — auto rebase if needed (max 2 retries)
    ↓ clean
[Phase 5.7] CI wait — clawflow pr ci-wait
    ↓ pass / no CI
[Phase 5.8] auto_merge=true → clawflow pr merge → close issue
            auto_merge=false → wait for owner review
    ↓
[clawflow worktree remove] — cleanup always runs
```

**ClawFlow never adds `ready-for-agent` itself — owner approval is always required.**

### Testing Boundary

> ⚠️ ClawFlow only runs smoke tests — it does not replace human verification

| Test type | Owner |
|-----------|-------|
| Build passes | ClawFlow (automatic) |
| Unit tests for changed packages | ClawFlow (automatic) |
| E2E / integration tests | Owner (manual) |
| Real-world scenario validation | Owner (manual) |

Smoke test failures trigger automatic retry (max 2×). On repeated failure, the issue is marked `agent-failed` and the owner is notified.

### Auto-fix

When `auto_fix: true` is set on a repo, ClawFlow automatically adds `ready-for-agent` after evaluation — no owner approval needed — if:
- confidence score >= 7.0
- no split suggestion in the evaluation

Default is `false`. Enable per repo in `~/.clawflow/config/config.yaml`:

```yaml
repos:
  owner/repo:
    enabled: true
    base_branch: main
    auto_fix: false    # true = skip owner approval when score >= 7.0
    auto_merge: false  # true = auto-merge after CI passes
```

### Language Support

| Language | Detected by | Build | Scoped test |
|----------|------------|-------|-------------|
| Go | `go.mod` | `go build ./...` | `go test ./changed/pkg/...` |
| Node.js | `package.json` | `npm run build` | `jest --testPathPattern=...` |
| Python | `pyproject.toml` / `requirements.txt` | `py_compile` | `pytest tests/changed/` |
| Rust | `Cargo.toml` | `cargo build` | `cargo test` |
| Java | `pom.xml` | `mvn compile -q` | `mvn test -Dtest=ChangedTest` |

---

## Directory Layout

```
~/.clawflow/                        ← user data (created by install.sh)
├── bin/
│   └── clawflow                    ← CLI binary
├── config/
│   ├── config.yaml                  ← repos to monitor (platform, base_url per repo)
│   ├── credentials.yaml            ← GH + GitLab tokens (0600, not committed)
│   └── install.yaml                ← install location record
└── memory/
    └── repos/
        └── owner-repo/
            └── issue-7.md          ← per-issue processing records

~/.claude/skills/clawflow/          ← skill definition (agent brain)
├── SKILL.md                        ← main pipeline instructions
├── evaluation.md                   ← evaluation strategy + comment templates
└── subagent-prompt.md              ← sub-agent task prompt template

clawflow/ (this repo)
├── cmd/clawflow/                   ← Go CLI source
├── internal/
│   ├── config/                     ← config parsing + write
│   └── vcs/                        ← platform-agnostic VCS interface
│       ├── interface.go            ← Client interface + shared types
│       ├── github/                 ← GitHub REST API v3 client
│       └── gitlab/                 ← GitLab REST API v4 client
├── skills/clawflow/SKILL.md        ← source for SKILL.md
└── install.sh                      ← installer
```

---

## SaaS Worker

The worker connects your local machine to a ClawFlow SaaS backend, polling for tasks and running the pipeline automatically.

### Setup

```bash
clawflow login --saas-url https://your-saas-instance.com
```

This saves `saas_url` and `worker_token` to `~/.clawflow/config/worker.yaml`.

### Commands

```bash
clawflow worker start            # start background worker (detaches by default)
clawflow worker start --foreground  # run inline (useful for systemd / Docker)
clawflow worker stop             # stop the background worker
clawflow worker status           # show config + verify SaaS connectivity
clawflow worker logs             # show last 200 lines of worker log
clawflow worker logs -f          # follow live output
```

The worker polls `{saas-url}/api/v1/worker/tasks` every 30 seconds (configurable via `--poll-interval`), claims pending tasks, runs `claude -p "ClawFlow run"`, and reports results (PR URL, logs, token usage) back to the SaaS.

A single-instance lock (`~/.clawflow/worker.pid`) prevents duplicate workers on the same host.

---

## Updating

```bash
clawflow update                  # download latest binary + update SKILL.md
clawflow update --from-source    # rebuild from cloned repo
```

---

## Roadmap

- [x] Go CLI for deterministic pipeline operations
- [x] Worktree isolation per issue
- [x] PR deduplication check
- [x] `clawflow update` for self-updating
- [x] GitLab support (REST API, self-hosted v11.11+)
- [x] Auto-detect platform from URL / SSH / local `.git/config`
- [x] `issue create/list` for pipeline testing
- [ ] Smarter feasibility scoring — historical issue matching
- [ ] Parallel processing — concurrent sub-agents
- [ ] Webhook-first triggering — real-time instead of cron polling

---

## Contributing

1. Fork this repository
2. Edit `skills/clawflow/SKILL.md` to improve agent logic
3. Edit `cmd/clawflow/` to add CLI features
4. Submit a PR

---

## License

MIT
