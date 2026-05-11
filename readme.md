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

## Tracking Issues & Sub-issues

For large features that span multiple implementation tasks, ClawFlow supports a **tracking issue** pattern backed by GitHub's native sub-issue relationship.

### How it works

1. Create an issue and add the `tracking` label.
2. Let it flow through `classify` → `evaluate-feat` → add `ready-for-agent`.
3. The `decompose` operator fires, reads the issue body, creates sub-issues via `clawflow issue create` + `clawflow issue add-sub`, and posts a checklist comment.
4. Sub-issues flow through the normal pipeline independently: `classify` → `evaluate` → `ready-for-agent` → `implement`.
5. After each `clawflow run`, the `track-progress` operator checks sub-issue completion via `clawflow issue list-sub`. When all sub-issues are done, it emits `agent-closed` and ClawFlow closes the parent automatically.

### Labels involved

| Label | Role |
|---|---|
| `tracking` | Marks the parent issue; prevents `implement` from touching it directly |
| `agent-decomposed` | Set after `decompose` creates sub-issues |
| `progress-check` | Ephemeral trigger for `track-progress`; re-added each run until all sub-issues are done |
| `agent-watching` | Outcome when sub-issues are still pending |
| `agent-closed` | Terminal outcome; triggers automatic close of the parent issue |

### CLI commands

```bash
# Link an existing issue as a sub-issue of a parent
clawflow issue add-sub --repo owner/repo --parent 10 --sub 11

# List all sub-issues of an issue
clawflow issue list-sub --repo owner/repo --issue 10
clawflow issue list-sub --repo owner/repo --issue 10 --json
```

GitLab does not have a native sub-issue API — `add-sub` and `list-sub` return an error on GitLab repos. The `decompose` operator still creates the issues; `track-progress` falls back to parsing the checklist in the issue body.

---

## Config Sync (multi-machine)

Working across multiple machines? `clawflow sync` keeps your repo list and settings in sync via a **private GitHub Gist** — no extra account, no SaaS backend.

```bash
# 1. Authenticate first (stores token + discovers/creates the Gist)
clawflow login <github-token>

# 2. First machine: push local config to the private Gist
clawflow sync push

# 3. Second machine: authenticate, then pull and merge
clawflow login <github-token>
clawflow sync pull

# Preview the diff before applying
clawflow sync
```

**What syncs:**

| Field | Synced? | Notes |
|---|---|---|
| `repos` | ✅ | Union merge — repos from both sides are kept |
| `settings.*` | ✅ | Cloud wins on pull |
| `credentials` | ❌ | Never synced — tokens stay local |
| `local_path` | ❌ | Machine-specific — always kept from local copy |

The Gist ID is stored in `~/.clawflow/config/credentials.yaml` after the first push. `clawflow login` auto-discovers an existing `clawflow-config` Gist if you've pushed from another machine before.

---

For projects you want ClawFlow to actively triage (not just react to labeled issues), enable the **Pilot**:

```bash
clawflow project automation enable my-project --cooldown 30
```

Or flip the toggle in the dashboard's project detail page.

When on, every `clawflow run` pass — after operators finish — wakes a per-project Pilot (a non-interactive `claude -p` invocation rooted at `~/.clawflow/projects/<name>/`). The Pilot triages the backlog at the **edges** of the operator pipeline: file new work, fix missing trigger labels, close stale/duplicate issues, comment to explain decisions. The middle of the pipeline (evaluate → ready-for-agent → implement → merge) stays owned by operators + repo-level `auto_approve` / `auto_merge`.

#### Pilot's working files

`~/.clawflow/projects/<name>/` carries the Pilot's whole world:

| File | Owner | Role |
|---|---|---|
| `CLAUDE.md` | clawflow (auto-generated) | Member repo loader. Refreshed from `project.yaml` on every wake; auto-loaded by `claude -p` so the Pilot always knows which repos belong and where they live locally. Don't hand-edit — your changes get overwritten. |
| `context.md` | **the Pilot itself** | The Pilot's evolving working memory. Read at wake start. The Pilot may rewrite it at wake end via a fenced ` ```context.md ``` ` block when something material was learned. Versioned by the project-level git repo. |
| `deployment.md` | you | Optional: log-retrieval / health-check commands. When present, the Pilot inspects logs before triaging the backlog (production errors take priority over tracker work). |
| `testing.md` | you | Optional: local-environment SOP. Used by the `implement` operator, not the Pilot. |

#### What the Pilot wakes with

Each wake's prompt carries: `context.md` (own memory), `deployment.md` (if present), the **last 3 wakes' `PILOT-RESULT` lines** (short-term memory — prevents the Pilot from re-doing what it already did), and the current backlog snapshot (open issues + PRs across all member repos).

#### Closed loop

```
clawflow run
  → operators process labeled issues
  → Pilot wakes (cooldown-gated, per project)
      → reads context.md / recent history / live backlog
      → triages: file/label/close/comment (≤2 new issues per wake)
      → optionally rewrites context.md
  → next clawflow run pass executes the changes
```

`--cooldown` (default 30 min) throttles wakes so a fast cron doesn't fire on every pass. Disable with `clawflow project automation disable my-project`.

---

## Directory Layout

```
~/.clawflow/                          ← user data
├── bin/clawflow                      ← CLI binary
├── config/
│   ├── config.yaml                   ← repos to monitor
│   ├── credentials.yaml              ← tokens (0600)
│   └── install.yaml                  ← install record
├── projects/                         ← multi-repo project groupings
│   └── my-project/
│       ├── project.yaml              ← member repos + automation config
│       ├── CLAUDE.md                 ← auto-gen Pilot repo loader
│       ├── context.md                ← Pilot's evolving memory (Pilot writes)
│       ├── deployment.md             ← optional: log/health commands
│       └── testing.md                ← optional: local-env SOP
└── skills/                           ← user-custom operators (override built-ins by name)
    └── my-operator/
        └── SKILL.md

clawflow/ (this repo)
├── cmd/clawflow/                     ← Go CLI source
├── internal/
│   ├── config/                       ← config parsing + write
│   ├── operator/                     ← operator loader + runner
│   ├── project/                      ← project grouping CRUD
│   └── vcs/                          ← platform-agnostic VCS client (GitHub + GitLab)
├── skills/                           ← built-in operators (embedded at build time)
│   ├── evaluate-bug/SKILL.md
│   ├── implement/SKILL.md
│   └── reply-comment/SKILL.md
└── agent-skills/                     ← agent skills for AI coding tools
    └── clawflow/SKILL.md             ← teaches AI tools the clawflow CLI
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
| **Issues** | `clawflow issue create / list / comment / close` |
| **PRs** | `clawflow pr create / list / view / comment / merge` |
| **Config** | `clawflow config set-token / set-gitlab-token / show` |
| **Sync** | `clawflow sync` — preview diff · `clawflow sync push` — upload to Gist · `clawflow sync pull` — merge from Gist |
| **Dashboard** | `clawflow web` — serve the local dashboard at http://127.0.0.1:8080 |
| **Update** | `clawflow update` — fetch the latest binary |
| **Operator helpers** *(invoked from SKILL.md bodies)* | `clawflow worktree` — create/remove per-issue git worktrees · `clawflow pr-check` — has an open PR for this issue? · `clawflow lang` — detect build/test commands for changed files · `clawflow status` — per-repo health summary |

> **Tool discipline:** inside operators, always use `clawflow` commands for VCS actions, never `gh` — see `CLAUDE.md` for the rationale.

---

## Local dashboard (optional)

Every `clawflow run` writes JSON snapshots and per-run event logs to `~/.clawflow/dashboard/`. Launch the web UI with:

```bash
clawflow web --open          # serves http://127.0.0.1:8080 and opens your browser
```

What it shows:

- **Dashboard** — filterable timeline of recent operator runs across every monitored repo, with status, duration, and PR links
- **Run detail** — replay of the full `claude -p` stream-json event log (tool calls, assistant messages, final result) for any past run
- **Repos** — read-only view of every repo in `config.yaml` with per-repo run history
- **Operators** — all built-in and user operators with triggers, lock labels, and descriptions

The dashboard is a static SPA (React + Vite + Tailwind) read-only view — it does not call the GitHub/GitLab API itself. All state comes from files the CLI wrote. To refresh, run `clawflow run` and reload the page. The bundle is shipped inside the binary via `embed.FS`, so there is no Node toolchain required at install time.

If you prefer not to use `clawflow web`, point any static file server (`python3 -m http.server`, nginx, …) at `~/.clawflow/dashboard/` — it's self-contained.

---

## Claude Code integration (optional)

If you use [Claude Code](https://claude.ai/code), install the agent skills that teach Claude about ClawFlow:

```bash
clawflow install-skill
```

This installs the **clawflow** skill to `~/.claude/skills/`, teaching Claude the CLI commands for issue/PR/label operations.

For autonomous scheduling, use per-project automation (`clawflow project automation enable`) instead — it runs inside `clawflow run` and doesn't depend on Claude Code being open.

Skip the flag if you just want the CLI without AI tool integration.

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
