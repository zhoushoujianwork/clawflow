# ClawFlow

> **Coding as a Service.**  
> ClawFlow watches your GitHub and GitLab repositories, picks up issues tagged `ready-for-agent`, and autonomously attempts to fix them — then opens a Pull Request.

---

## Install

### Option A — from source (recommended)

```bash
git clone https://github.com/zhoushoujianwork/clawflow
cd clawflow && ./install.sh
```

The installer:
- Auto-detects your agent (`~/.claude/skills/` for Claude Code, `~/.openclaw/` for OpenClaw)
- Builds and installs the `clawflow` CLI to `~/.clawflow/bin/clawflow`
- Initializes `~/.clawflow/config/` with template config files
- Records install location to `~/.clawflow/config/install.yaml` (used by `clawflow update`)

For a specific agent:

```bash
./install.sh --agent claude     # Claude Code
./install.sh --agent openclaw  # OpenClaw
./install.sh --agent custom --dir /path/to/skills
```

### Option B — download binary

```bash
# macOS Apple Silicon
curl -L https://github.com/zhoushoujianwork/clawflow/releases/latest/download/clawflow_darwin_arm64 \
  -o ~/.clawflow/bin/clawflow && chmod +x ~/.clawflow/bin/clawflow

# macOS Intel
curl -L https://github.com/zhoushoujianwork/clawflow/releases/latest/download/clawflow_darwin_amd64 \
  -o ~/.clawflow/bin/clawflow && chmod +x ~/.clawflow/bin/clawflow

# Linux x86_64
curl -L https://github.com/zhoushoujianwork/clawflow/releases/latest/download/clawflow_linux_amd64 \
  -o ~/.clawflow/bin/clawflow && chmod +x ~/.clawflow/bin/clawflow
```

Then add to PATH:

```bash
echo 'export PATH="$HOME/.clawflow/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
```

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

For GitLab self-hosted, you can also register the host in `~/.clawflow/config/repos.yaml` so short-form inputs are recognized:
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
                → adds agent-evaluated label
    ↓
[owner adds ready-for-agent]        [low confidence → agent-skipped]
    ↓
[clawflow worktree create] — isolated branch per issue
    ↓
[sub-agent implements fix] — in the worktree
    ↓
[PR opened] → [clawflow worktree remove] — cleanup always runs
```

**ClawFlow never adds `ready-for-agent` itself — owner approval is always required.**

---

## Directory Layout

```
~/.clawflow/                        ← user data (created by install.sh)
├── bin/
│   └── clawflow                    ← CLI binary
├── config/
│   ├── repos.yaml                  ← repos to monitor (platform, base_url per repo)
│   ├── credentials.yaml            ← GH + GitLab tokens (0600, not committed)
│   └── install.yaml                ← install location record
└── memory/
    └── repos/
        └── owner-repo/
            └── issue-7.md          ← per-issue processing records

~/.claude/skills/clawflow/          ← skill definition (agent brain)
└── SKILL.md

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
