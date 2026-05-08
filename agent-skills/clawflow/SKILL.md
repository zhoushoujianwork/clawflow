---
name: clawflow
description: "Use the `clawflow` CLI to manage GitHub/GitLab projects locally: view and operate on issues, PRs, and labels without leaving the terminal. Trigger on ANY of: user mentions 'clawflow' or 'ClawFlow'; user asks to list/create/view/comment on issues or PRs; user asks to manage labels; user asks to add/manage repos; user mentions local git project operations or VCS management."
---

# ClawFlow

A CLI for managing GitHub/GitLab projects from your local terminal. View issues, create PRs, manage labels, and operate on multiple repos without context switching.

## Preflight (first use on a machine)

Before invoking any `clawflow` subcommand, run these two checks. If either fails, surface the exact command to the user — do not guess or bypass.

### 1. Binary check

Run `command -v clawflow`. If not found, the user hasn't installed it yet. Ask them to run:

```bash
curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash
```

Supports macOS (arm64/amd64) and Linux (amd64/arm64). The installer drops the binary into `/usr/local/bin` (or `~/.local/bin` if no write perm) and creates `~/.clawflow/config/`.

### 2. Token check

Run `clawflow config show`. The output lists `GH Token:` and `GitLab Token:` — each either has a masked value like `set (***abcd)` or is unset.

If the GitHub token is unset (and the user works with GitHub repos), ask them to run:

```bash
clawflow config set-token <ghp_...>        # scope: repo, read:org
```

For GitLab:

```bash
clawflow config set-gitlab-token <glpat_...>   # scope: api
```

Env vars `GH_TOKEN` / `GITLAB_TOKEN` take priority over the stored file. Tokens live in `~/.clawflow/config/credentials.yaml` (mode 0600).

## Core commands

### Issue / PR / Label operations
```
clawflow issue list / create / view / comment / close --repo <R>
clawflow issue search "<query>" --repo <R> [--state all|open|closed]   # title+body search
clawflow issue search "<query>" --project <name>                       # fan-out across project repos
clawflow pr list / create / view / comment / merge --repo <R>
clawflow label add / remove --repo <R> --issue <N> --label <L>
```

`issue search` is the historical-context lookup: every change in a clawflow-managed
project goes through an issue, so past issues (open + closed) are the project's
decision archive. Before doing analysis on a new issue, search for related ones —
duplicates, prior decisions, and related code-area history are usually the
strongest signal. Pass `--json` for structured output.

### Repo management
```
clawflow repo add <owner/repo | URL | local path>
clawflow repo list / enable / disable / remove
clawflow label init <owner/repo>         # create standard labels in repo
```

### Advanced: Operator pipeline (optional)
```
clawflow run                             # run operators on enabled repos
clawflow run --repo owner/repo --issue N # target a single issue
clawflow operators list                  # see registered operators
```
The operator pipeline is for automated issue evaluation and PR generation. Most users can ignore this feature.

## Typical workflows

### View and manage issues
```bash
clawflow repo add owner/repo
clawflow issue list --repo owner/repo
clawflow issue view --repo owner/repo --issue 42
clawflow issue comment --repo owner/repo --issue 42 --body "Working on this"
```

### Create and manage PRs
```bash
clawflow pr create --repo owner/repo --title "Fix bug" --body "Description"
clawflow pr list --repo owner/repo --state open
clawflow pr view --repo owner/repo --pr 15
```

### Label management
```bash
clawflow label init owner/repo              # create standard labels
clawflow label add --repo owner/repo --issue 42 --label bug
clawflow label remove --repo owner/repo --issue 42 --label wontfix
```

## Operator pipeline (advanced)

For automated issue evaluation and PR generation, ClawFlow supports an operator pipeline:

1. Label an issue with `bug` or `feat`
2. `clawflow run` triggers the matching operator
3. Operator posts assessment and adds `agent-evaluated`
4. Manually add `ready-for-agent` to approve implementation
5. Next `clawflow run` creates a fix PR and adds `agent-implemented`

Most users won't need this feature for day-to-day git project management.

## Configuration

### Authentication
```bash
# GitHub
clawflow config set-token <ghp_...>
# or export GH_TOKEN=<ghp_...>

# GitLab
clawflow config set-gitlab-token <glpat_...>
# or export GITLAB_TOKEN=<glpat_...>
```

### Settings
Config and repos are stored in `~/.clawflow/config/config.yaml`

## Best practices

- Use `clawflow` commands for issue/PR/label operations to maintain consistency
- Use `git` directly for local branch and commit work
- Run `clawflow <cmd> --help` when unsure about command flags
- The CLI provides clear error messages to guide you

## Autonomous scheduling

For continuous health monitoring, use per-project automation rather than a Claude
Code skill — it runs inside `clawflow run` and doesn't require Claude Code to be
open. Enable per project:

```
clawflow project automation enable <project> --cooldown 30
```

When on, every `clawflow run` pass wakes the project manager, which can file new
issues to schedule work. See the project's `automation` section in the dashboard
for the toggle and live status.
