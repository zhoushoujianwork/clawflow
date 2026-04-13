# ClawFlow

> **Automated Issue → Fix → PR pipeline powered by AI agents.**  
> ClawFlow watches your GitHub repositories, picks up issues tagged `ready-for-agent`, and autonomously attempts to fix them — then opens a Pull Request.

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

### 1. Store GitHub token

```bash
clawflow config set-token ghp_xxxxxxxxxxxx
```

Token is saved to `~/.clawflow/config/credentials.yaml` (mode 0600) and auto-injected into all `gh` calls.

Required scopes: `repo` (full), `read:org`.

### 2. Add repositories to monitor

```bash
clawflow repo add your-org/your-repo --base main --local-path ~/github/your-repo
```

Or manage repos interactively:

```bash
clawflow repo list                        # show all repos and status
clawflow repo enable  your-org/your-repo  # resume monitoring
clawflow repo disable your-org/your-repo  # pause without removing
clawflow repo remove  your-org/your-repo  # delete from config
```

### 3. Create GitHub labels

```bash
./install.sh --create-labels your-org/your-repo
```

Labels created:

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

Repo management:
  repo list          List all configured repos
  repo add           Add a repo to monitor
  repo remove        Remove a repo from config
  repo enable        Enable a repo
  repo disable       Disable a repo (pause without removing)

Labels:
  label add          Add a label to an issue
  label remove       Remove a label from an issue

Worktrees:
  worktree create    Create an isolated git worktree for an issue
  worktree remove    Remove worktree after fix (success or failure)

Records:
  memory write       Write an issue processing record
  pr-check           Check if an open PR already exists for an issue

Config:
  config set-token   Store GitHub token (saved to credentials.yaml)
  config show        Show current config and token status

Updates:
  update             Download latest binary + update SKILL.md
  update --from-source  Rebuild from local source
```

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
│   ├── repos.yaml                  ← repos to monitor
│   ├── labels.yaml                 ← label definitions
│   ├── credentials.yaml            ← GH token (0600, not committed)
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
│   └── github/                     ← gh CLI wrapper
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
