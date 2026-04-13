# ClawFlow

> **Automated Issue → Fix → PR pipeline powered by AI agents.**  
> ClawFlow watches your GitHub repositories, picks up issues tagged `ready-for-agent`, and autonomously attempts to fix them — then opens a Pull Request.

---

## Install

ClawFlow is a skill that can be installed into any AI agent that supports the skill format (Claude Code, OpenClaw, etc.).

### One-line install

```bash
git clone https://github.com/zhoushoujianwork/clawflow
cd clawflow && ./install.sh
```

The installer auto-detects your agent (`~/.claude/skills/` for Claude Code, `~/.openclaw/` for OpenClaw).

### Install for a specific agent

```bash
# Claude Code
./install.sh --agent claude

# OpenClaw
./install.sh --agent openclaw

# Custom directory
./install.sh --agent custom --dir /path/to/your/skills
```

### Manual install

Copy the skill definition into your agent's skills folder, and initialize the user data directory:

```
~/.claude/skills/clawflow/
└── SKILL.md              ← skill definition (the brain)

~/.clawflow/              ← user data (created by installer)
├── config/
│   ├── repos.yaml        ← repos to monitor (edit this)
│   └── labels.yaml       ← GitHub label definitions
└── memory/
    └── repos/            ← per-repo issue records (auto-created)
```

---

## Setup

After installing:

### 1. Configure repos to monitor

Edit `~/.clawflow/config/repos.yaml`:

```yaml
repos:
  your-org/your-repo:
    enabled: true
    base_branch: main
    owner: your-org
    description: "Short description"
    added_at: 2026-01-01
```

### 2. Authenticate GitHub CLI

```bash
gh auth login
```

### 3. Create GitHub labels in each monitored repo

| Label | Color | Meaning |
|---|---|---|
| `ready-for-agent` | `#00FF00` Green | Owner approves — triggers the fix pipeline |
| `agent-evaluated` | `#0075CA` Blue | ClawFlow has assessed this issue |
| `in-progress` | `#FFA500` Orange | Agent is actively working on it |
| `agent-skipped` | `#BDBDBD` Gray | Low confidence — needs more info |
| `agent-failed` | `#FF0000` Red | Agent attempted but failed |

用安装脚本一键创建：

```bash
./install.sh --create-labels your-org/your-repo
```

或者手动逐条创建：

```bash
gh label create "ready-for-agent" --color "00FF00" --description "Triggers ClawFlow fix pipeline" -R your-org/your-repo
gh label create "agent-evaluated"  --color "0075CA" --description "ClawFlow has assessed this issue" -R your-org/your-repo
gh label create "in-progress"      --color "FFA500" --description "Agent is working on it" -R your-org/your-repo
gh label create "agent-skipped"    --color "BDBDBD" --description "Skipped — needs more info" -R your-org/your-repo
gh label create "agent-failed"     --color "FF0000" --description "Agent attempt failed" -R your-org/your-repo
```

### 4. Trigger a run

Tell your AI agent:

```
ClawFlow run
```

---

## How It Works

```
New Issue
    ↓
[Phase 1] Harvest — scan open issues
    ↓
[Phase 2] Evaluate — assess feasibility, post comment with proposal
    ↓
[owner adds ready-for-agent]
    ↓
[Phase 3] Execute — sub-agent clones repo, implements fix, opens PR
```

1. ClawFlow scans configured repos for open issues
2. Each unreviewed issue gets a feasibility assessment posted as a comment
3. Owner reviews the proposal and adds `ready-for-agent` to approve
4. ClawFlow spawns a sub-agent to implement the fix and open a PR

**ClawFlow never adds `ready-for-agent` itself — owner approval is always required.**

---

## Usage

| Command | What it does |
|---------|--------------|
| `ClawFlow run` | Run a full harvest + fix cycle |
| `检查 ClawFlow 状态` | Show configured repos and pending issue counts |
| `ClawFlow 添加仓库 <owner/repo>` | Add a new repo to the config |

---

## Project Structure

```
clawflow/
├── install.sh              ← skill installer
├── readme.md               ← this file
├── skills/
│   └── clawflow/
│       └── SKILL.md        ← skill definition (installed to agent)
└── config/
    ├── repos.yaml          ← repos to monitor (template)
    └── labels.yaml         ← label definitions (template)
```

---

## Roadmap

- [ ] Smarter feasibility scoring — historical issue matching, code similarity
- [ ] Parallel processing — concurrent sub-agents
- [ ] Webhook-first triggering — real-time instead of cron polling
- [ ] Rollback & learning — record rejection reasons and adapt

---

## Contributing

1. Fork this repository
2. Edit `skills/clawflow/SKILL.md` to improve agent logic
3. Update `config/repos.yaml` to add new repositories
4. Submit a PR

---

## License

MIT
