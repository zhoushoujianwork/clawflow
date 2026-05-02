---
name: clawflow
description: "Use the `clawflow` CLI to manage GitHub/GitLab projects locally: view and operate on issues, PRs, and labels without leaving the terminal. Trigger on ANY of: user mentions 'clawflow' or 'ClawFlow'; user asks to list/create/view/comment on issues or PRs; user asks to manage labels; user asks to add/manage repos; user mentions local git project operations or VCS management."
---

# ClawFlow

A CLI for managing GitHub/GitLab projects from your local terminal. View issues, create PRs, manage labels, and operate on multiple repos without context switching.

## Core commands

### Issue / PR / Label operations
```
clawflow issue list / create / view / comment / close --repo <R>
clawflow pr list / create / view / comment / merge --repo <R>
clawflow label add / remove --repo <R> --issue <N> --label <L>
```

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
