# ClawFlow

> **Label-driven automation that turns issues into PRs on GitHub and GitLab.**
>
> ClawFlow polls the repositories you configure, matches each open issue/PR against a set of **operators** (self-contained `SKILL.md` files), and runs the matching operator through `claude -p`. State lives entirely in VCS labels and comments — there is no database, no SaaS backend, and no orchestrator service. Run it once, run it on cron, run it from your editor — it's the same binary either way.

---

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash
```

Supports macOS (Apple Silicon & Intel) and Linux (x86_64 & arm64). The installer:

- Downloads the right binary for your platform into `~/.clawflow/bin/clawflow`
- Initializes `~/.clawflow/config/` with a template config
- Adds `~/.clawflow/bin` to your shell `PATH`

You also need the [Claude Code CLI](https://claude.ai/code) on `PATH` — ClawFlow shells out to `claude -p` to run operators.

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

Tokens are saved to `~/.clawflow/config/credentials.yaml` (mode 0600). Environment variables take priority: `GH_TOKEN`, `GITLAB_TOKEN`.

### 2. Add repositories to monitor

`repo add` auto-detects the platform from the input — no flags needed in most cases:

```bash
# GitHub — URL, SSH, or short form
clawflow repo add https://github.com/owner/repo
clawflow repo add git@github.com:owner/repo.git
clawflow repo add owner/repo

# GitLab self-hosted — full URL (nested namespaces supported)
clawflow repo add https://gitlab.company.com/ns/group/repo

# Local directory — reads .git/config origin automatically
clawflow repo add .
clawflow repo add ~/github/my-repo
```

Override platform or instance URL manually:
```bash
clawflow repo add ns/repo --platform gitlab --base-url https://gitlab.company.com
```

Manage repos:
```bash
clawflow repo list
clawflow repo enable  owner/repo
clawflow repo disable owner/repo
clawflow repo remove  owner/repo
```

### 3. Initialize labels

Labels are created automatically on `repo add`. To (re)create them manually:

```bash
clawflow label init owner/repo
```

**Trigger labels** — gate which operator fires on an issue:

| Label | Fires operator |
|---|---|
| `bug` | `evaluate-bug` |
| `feat` | `evaluate-feat` *(planned, post-MVP)* |
| `ready-for-agent` | `implement` — you add this manually after reviewing the evaluation |
| `agent-mentioned` | `reply-comment` |

**State labels** — written back by operators:

| Label | Meaning |
|---|---|
| `agent-running` | Universal execution lock. Added before an operator runs, removed after (success or fail). |
| `agent-evaluated` | An `evaluate-*` operator has posted its assessment. Stops re-evaluation. |
| `agent-skipped` | Confidence too low — operator declined to proceed. |
| `agent-implemented` | `implement` finished — PR is open. |
| `agent-failed` | An operator errored; see the failure comment on the issue. |
| `agent-replied` | `reply-comment` has replied to the latest mention. |

ClawFlow never adds `ready-for-agent` itself — owner approval is always required to cross from evaluation to implementation.

### 4. Run

```bash
clawflow run
```

Scans every enabled repo once, runs any matching operators, exits. Schedule it with cron, launchd, or your editor's agent — ClawFlow holds no long-running state.

---

## How It Works

```
clawflow run
  └─ for each configured repo
       └─ list open issues and PRs
            └─ for each one, match its labels against every registered operator
                 └─ on first match:
                      1. add the operator's lock label (concurrency guard)
                      2. invoke `claude -p` with the operator's SKILL.md + issue context
                      3. the operator posts its comment / label / PR
                      4. remove the lock label
```

No orchestrator, no sub-agents, no DAG. Operators only coordinate through the labels and comments they read and write — one operator's output becomes the next operator's trigger, implicitly.

Example end-to-end flow for a bug:

1. Someone opens an issue and labels it `bug`.
2. Next `clawflow run` — `evaluate-bug` matches, writes an evaluation comment, adds `agent-evaluated`.
3. Owner reads the evaluation, adds `ready-for-agent`.
4. Next `clawflow run` — `implement` matches, creates a branch, writes code, opens a PR, adds `agent-implemented`.

---

## Architecture: Operators

An **operator** is a single `SKILL.md` file — frontmatter plus a prompt — that declares:

- **Trigger**: which issues/PRs it runs on (target type + required labels + excluded labels)
- **Lock label**: the label used as a per-run mutex
- **Body**: the prompt that `claude -p` receives, with issue context injected

Operators live in two places:

- **Built-in**: `skills/<name>/SKILL.md` inside this repo — embedded into the binary at build time.
- **User overrides**: `~/.clawflow/skills/<name>/SKILL.md` — same name overrides the built-in.

That's the whole extension model. To add a new operator, drop a `SKILL.md` in one of those directories. No plugin API, no registration step.

See [`CLAUDE.md`](CLAUDE.md) for the frontmatter schema and operator design principles.

---

## Directory Layout

```
~/.clawflow/                          ← user data
├── bin/clawflow                      ← CLI binary
├── config/
│   ├── config.yaml                   ← repos to monitor
│   ├── credentials.yaml              ← tokens (0600)
│   └── install.yaml                  ← install record
└── skills/                           ← user-custom operators (override built-ins by name)
    └── my-operator/
        └── SKILL.md

clawflow/ (this repo)
├── cmd/clawflow/                     ← Go CLI source
├── internal/
│   ├── config/                       ← config parsing + write
│   ├── operator/                     ← operator loader + runner
│   └── vcs/                          ← platform-agnostic VCS client (GitHub + GitLab)
└── skills/                           ← built-in operators (embedded at build time)
    ├── evaluate-bug/SKILL.md
    ├── implement/SKILL.md
    └── reply-comment/SKILL.md
```

---

## CLI Reference

Commands are organized by category. Run `clawflow <cmd> --help` for flags.

| Category | Commands |
|---|---|
| **Core loop** | `clawflow run` — scan and execute matching operators once |
| **Operators** | `clawflow operators list` — show which operators are registered (built-in + user) |
| **Repos** | `clawflow repo add / remove / list / enable / disable` |
| **Labels** | `clawflow label add / remove / init` |
| **Issues** | `clawflow issue create / list / comment` |
| **PRs** | `clawflow pr create / list / view / comment / merge` |
| **Config** | `clawflow config set-token / set-gitlab-token / show` |
| **Update** | `clawflow update` — fetch the latest binary |

> **Tool discipline:** inside operators, always use `clawflow` commands for VCS actions, never `gh` — see `CLAUDE.md` for the rationale.

---

## Supported Platforms

| Platform | Status | Notes |
|---|---|---|
| **GitHub** | ✅ | REST API v3 |
| **GitLab** | ✅ | REST API v4, self-hosted v11.11+ |

> Local quickstart: [Getting started with Claude Code](docs/quickstart-claude-code.md)

---

## Updating

```bash
clawflow update                    # fetch the latest binary
clawflow update --from-source      # rebuild from cloned repo (dev)
```

---

## Contributing / Extending

The project is deliberately small. To change behavior, you almost always want to edit or add an operator, not Go code:

1. Create `skills/<operator-name>/SKILL.md` (built-in) or `~/.clawflow/skills/<operator-name>/SKILL.md` (user).
2. Declare the frontmatter: `name`, `description`, `operator.trigger`, `operator.lock_label`.
3. Write the prompt body.
4. Run `clawflow operators list` to confirm it's registered, then `clawflow run` to exercise it on a test issue.

Go CLI work goes under `cmd/clawflow/commands/` and `internal/`. The tight spot to touch for extending the loop itself is `internal/operator/`.

---

## License

MIT
