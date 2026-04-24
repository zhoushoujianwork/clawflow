---
name: clawflow
description: "Use the `clawflow` CLI for the ClawFlow operator pipeline: evaluating issues, auto-generating fix PRs, and managing monitored GitHub/GitLab repos through a label-driven workflow. Trigger on ANY of: user mentions 'clawflow' or 'ClawFlow'; user asks to evaluate / triage / score an issue or feature request; user asks to auto-fix / run the agent on an issue; user mentions 'ready-for-agent', 'agent-evaluated', 'agent-running', 'agent-implemented', 'agent-skipped', or the operator loop; user asks about monitored repos, bug/feat label triage, or the implement / evaluate-bug / evaluate-feat / reply-comment operators; user asks to add a repo to clawflow, init clawflow labels, or run `clawflow run`."
---

# ClawFlow

A CLI that matches each open issue/PR in configured repos against a set of operators (label-triggered SKILL.md files), then runs the matching operator through `claude -p`. All state lives in VCS labels and comments — no database.

## Core commands

### Run the loop
```
clawflow run                             # one pass over all enabled repos
clawflow run --repo owner/repo --issue N # target a single issue
clawflow operators list                  # see registered operators
```

### Repo management
```
clawflow repo add <owner/repo | URL | local path>
clawflow repo list / enable / disable / remove
clawflow label init <owner/repo>         # create the trigger + state labels
```

### Issue / PR / Label ops
```
clawflow issue list / create / comment / close --repo <R>
clawflow pr list / create / view / comment / merge --repo <R>
clawflow label add / remove --repo <R> --issue <N> --label <L>
```

## Label scheme

| Kind | Labels |
|---|---|
| **Trigger** | `bug` / `feat` / `ready-for-agent` / `agent-mentioned` |
| **State** | `agent-running` / `agent-evaluated` / `agent-skipped` / `agent-implemented` / `agent-failed` / `agent-replied` |

## Typical workflow

1. Owner labels a new issue `bug` or `feat`.
2. `clawflow run` → the matching `evaluate-*` operator posts a structured assessment comment and adds `agent-evaluated`.
3. Owner reviews the comment; if satisfied, manually adds `ready-for-agent`.
4. Next `clawflow run` → `implement` operator creates a branch, writes the fix, opens a PR, adds `agent-implemented`.

ClawFlow never adds `ready-for-agent` itself — owner approval always required.

## Tool discipline

- Prefer `clawflow` commands over `gh` for issue/PR/label operations on monitored repos — the CLI enforces the label scheme and idempotency.
- Use `git` directly for local branch/commit work.
- When a flag is unclear, run `clawflow <cmd> --help` rather than guessing. The CLI prints precise errors.

## Config

Tokens: `clawflow config set-token <ghp_...>` and `clawflow config set-gitlab-token <glpat_...>`, or export `GH_TOKEN` / `GITLAB_TOKEN`. Repos and settings live in `~/.clawflow/config/config.yaml`.
