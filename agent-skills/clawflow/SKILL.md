---
name: clawflow
description: "Control ClawFlow through its CLI: safe local reset, disabled GitHub/GitLab onboarding, project binding, provider/operator checks, authorized runs, status, issues, PRs and CI."
---

# ClawFlow lifecycle control

You are an external AI controlling the `clawflow` CLI. ClawFlow's operator
runtime still requires Claude Code CLI (`claude -p`); installing this skill in
Codex, Cursor or another tool does not replace that runtime.

Use only commands supported by the installed binary. Run help before unfamiliar
flags. Report actual errors and partial effects; do not invent commands or
silently retry actions that might already have changed state. Use `clawflow`
for VCS operations and `git` for local checkout inspection. Never use `gh`.

## Preflight

1. Locate the binary with `command -v clawflow`.
2. Read `clawflow --version` and `clawflow --help`. Inspect the specific
   subcommand's `--help` before using lifecycle flags introduced by newer builds.
3. Run `clawflow config show` to check masked authentication status. Never read
   or print credentials.yaml, complete tokens or API keys. `GH_TOKEN` and
   `GITLAB_TOKEN` override stored tokens. Only request authentication for the
   platform actually needed. `config show --repo <R> --field <field>` can query
   enabled, platform, local_path, base_branch, auto_approve, auto_merge or ci_required.
4. Run `clawflow sync status` before maintenance/onboarding. Keep an existing
   pause in effect. Do not run login or automatically resume synchronization.

If missing, the user can install the binary using:

```bash
curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash
```

Authentication commands are `clawflow config set-token <token>` and
`clawflow config set-gitlab-token <token>` (GitLab scope: api). Let the user enter
secrets privately; do not include secrets in reports, issue comments or logs.
A local-only reset does not require a token or network connectivity.

## Local reset: offline maintenance

Only reset when the user explicitly requests clearing local managed state.
Stop ClawFlow web, operator runs and external cron/schedulers first. Reset
refuses active run/issue locks and running ClawFlow web/run processes. These
checks and the global run lock support offline maintenance; they are not a
new distributed locking guarantee. Keep other writers stopped throughout.

```bash
clawflow reset --json                 # read-only plan; no migration/lock cleanup
clawflow reset --yes --json           # apply only within authorized reset scope
```

The fixed scope clears registered repositories while retaining settings and
archives original config/config.yaml, legacy config/repos.yaml if present,
and projects, data, chats, state, memory, logs and worker.log under a unique private
`~/.clawflow/backups/reset-TIMESTAMP-.../`. Entries move by rename; they are not
deleted. Credentials, source clones, worktrees, installed skills and binaries
remain in place. Remote issues, PRs/MRs, labels, repositories and Gist contents
are unchanged. A missing ClawFlow home is a no-op. An already cleared existing
home needs no new archive, but an explicit apply still pauses sync.

Apply pauses local sync first using `~/.clawflow/sync-paused`, outside archived
history. Manual sync and automatic pull/push through this binary's shared
client are blocked. Login and older binaries can bypass this pause: do not run
them during reset or while relying on that pause to prevent restoration.

Inspect the returned status, backup path, moved entries and any error. On a
partial failure, keep sync paused and inspect the backup manifest before any
retry; `moving` records an in-progress entry if interrupted. Original config
and moved directories remain recoverable from the backup. Do not replay a
reset blindly or overwrite a backup. Symlink roots/config/targets and malformed
config are refused rather than repaired.

Verify with `repo list`, `project list`, `sync status`, and the returned backup
manifest. Confirm preserved clones/worktrees and credentials remain present
without reading credential contents. Do not enable automation as verification.
Do not automatically run `sync resume`: the cloud may still contain the old
repos/projects and pull can restore them.

Local sync controls never contact the server or change credentials:

```bash
clawflow sync pause
clawflow sync status
clawflow sync resume                  # explicit decision; only removes marker
```

Resume does not immediately synchronize. Later `sync pull`, `run` or `web` can
pull old cloud state. `sync push` writes remote state and requires authorization
for that scope. Pause does not stop an already running request or old process.

## Onboard an existing checkout with automation off

Verify the actual origin URL, full namespace, local absolute path and intended
base branch. Do not infer the project from its display name. Use local Git
inspection such as `git -C <path> remote get-url origin` and
`git -C <path> branch --list`; avoid exposing credential-bearing remote URLs.
Confirm remote/base details using authorized read operations when needed.

For GitLab use the full project URL, including nested namespaces:

```bash
clawflow repo add <full-project-URL> --platform gitlab \
  --base-url <instance-URL> --local-path <absolute-checkout-path> \
  --base <verified-base> --disabled --skip-label-init
clawflow project create <project>
clawflow project add-repo <project> <namespace/repo>
clawflow repo set <namespace/repo> --primary-project <project>
clawflow repo set <namespace/repo> --auto-approve off --auto-merge off
clawflow project automation disable <project>
```

`--disabled` also skips label initialization by itself. `--skip-label-init`
can independently skip label writes while preserving the old enabled default.
Without either flag, repo add enables monitoring and initializes remote labels.
Explicit local paths must be Git checkouts whose origin provider, host and full
namespace match the requested repo; paths are stored as absolute paths.

Use `repo list`/`repo show <R>` and `project list`/`project show <project>` first
when entries may already exist. Do not delete/recreate them to bypass errors.
Existing registration updates support `repo set --auto-approve`, `--auto-merge`,
`--primary-project` and `--bound-machine`; base/local-path changes have no generic CLI setter.
Report that limitation instead of inventing a flag.

Verify disabled state with:

```bash
clawflow repo show <R>
clawflow config show --repo <R> --field enabled
clawflow config show --repo <R> --field auto_approve
clawflow config show --repo <R> --field auto_merge
clawflow project automation show <project>
```

Project creation defaults PM automation off. Membership does not enable it.
Primary project selects the authoritative context when multiple projects share
one repository. `repo disable` stops future monitoring; it does not cancel an
operator already running. `project automation disable` stops scheduled Pilot
wakes; manual `pilot wake` is a separate authorized action.

When readiness reports a required machine binding, bind the repository and
project to the intended execution host without enabling automation:

```bash
clawflow repo set <R> --bound-machine <hostname>
clawflow project automation bind <project> --machine <hostname>
```

Use the actual execution host name. Omitted `--bound-machine` preserves the
existing binding; `--bound-machine=""` explicitly clears it. Repository binding
does not change monitoring, approval, merge or primary-project settings.

## Provider, operator and test readiness

Use the local readiness check after registration:

```bash
clawflow project check <project> --json
```

This command performs local reads only: project/repo and primary bindings,
checkout origin and cached base commit, dirty state, context/testing document
presence, Git/Claude executable availability, operator definitions, automation
controls and local sync pause state. It does not migrate config, fetch, probe
providers, start agents, run tests or write snapshots. Missing cached base is
blocked; a cached commit does not establish remote freshness. Disabled automation,
missing project instructions and dirty checkouts are warnings; preserve existing work.

JSON stdout contains one document with `schema_version: 1`, `project`,
`status` (`ready` or `blocked`), `checks` and `unverified`. Each check has stable
`id`, `scope`, `status` (`passed`, `warning`, `blocked`), `message` and optional
`next_action`. Blockers return nonzero with the structured result still on stdout.
Branch on IDs/statuses, not English messages. `ready` covers local prerequisites
only: `remote_auth`, `remote_freshness`, `provider_execution`, `project_tests` and
`issue_workflow` remain explicitly unverified. Sync credentials and remote sync
configuration are not inspected. Check command help when using an older binary.

```bash
clawflow config provider list
clawflow config provider test <name-or-index>
clawflow operators list
clawflow operators validate
clawflow operators doctor
```

Provider test invokes Claude for a connectivity probe; it can consume usage.
It uses the default chat model and is not a complete configured-model test.
Provider management supports `add <name> [--base-url URL] [--model MODEL]
[--api-key KEY] [--disabled]`, enable/disable/remove <name-or-index>, and
`move <name-or-index> --to <index>`. Keep keys out of transcripts.
Operator validate/doctor check loaded embedded and user operators; they do not
run an issue or prove tests pass.

Projects carry context.md, testing.md (local environment SOP) and deployment.md
(runtime inspection SOP). `project generate <project> [--model MODEL]` uses AI
to generate context; `project chat <project> [--model MODEL]` is interactive.
There is no enforced test-profile configure/run/result lifecycle yet, nor a
generic noninteractive project-document setter. Do not claim one exists.

Run the repository's real tests in its checkout when authorized. Inspect CI
using existing commands rather than treating an operator/provider check as a
code-test result:

```bash
clawflow ci pipeline list --repo <GitLab-project-URL> --ref <branch> --json
clawflow ci pipeline view --repo <GitLab-project-URL> --id <pipeline-id> --json
clawflow ci job log --repo <GitLab-project-URL> --job <job-id>
clawflow pr ci-wait --help
```

Playing a manual job (`ci job play --repo <R> --id <job-id> [--json]`) changes
remote state and can deploy. Execute only the explicitly authorized job; inspect
its final logs/status. Keep secret values redacted when using CI variable
commands; list/view redact by default and `--show-values` reveals secrets.

## Explicit enable and bounded execution

Enabling monitoring permits future scheduler scans; only enable after the user
authorizes automation. Keep auto-approve, auto-merge and PM automation off unless
separately authorized. A disabled repo cannot be processed with `run --repo`.
Label initialization is a remote write; perform it only when authorized/needed.

```bash
clawflow label init <R>
clawflow repo enable <R>
clawflow run --repo <R> --issue <N> --timeout 60m
```

Inspect the issue and operator triggers before running. `run` has no dry-run
flag and can post comments, change labels and launch implementation/PR work.
A single-issue run still performs normal runner sync, history reconciliation,
worktree cleanup and snapshot writes, and scans other eligible enabled repos.
Scoped runs suppress scheduled project Pilot wakes; a nonzero `--issue` also
suppresses the scan-level auto-merge sweep. The selected operator can still
perform its normal implementation, PR and post-processing writes. Keep sync
paused and approval/merge settings disabled when outside the authorized scope.

Per-project Pilot scheduling is a separate opt-in:

```bash
clawflow project automation enable <project> --cooldown 30 --bound-machine <hostname>
clawflow project automation bind <project> --machine <hostname>
clawflow project automation show <project>
```

`bind --clear` removes the machine restriction. `pilot wake <project>
[--timeout <minutes>]` manually wakes Pilot and bypasses cooldown; do not use it
as a harmless readiness check. `clawflow web` can start a configured periodic
scheduler and auto-sync; it is not a read-only status command.

## Inspect, disable and report

```bash
clawflow status
clawflow issue view <N> --repo <R> --json
clawflow pr list --repo <R> --state open
clawflow repo disable <R>
clawflow project automation disable <project>
```

`status` makes remote reads for enabled repos only and summarizes issue labels.
It is not local execution history and has no JSON/repo filter. CLI lifecycle
parity is incomplete: no run history/stream/cancel command, scheduler pause
command, or enforced test-profile lifecycle. The dashboard has additional
controls; report the gap instead of inventing CLI commands. Ctrl-C can interrupt
a foreground run; disabling a repo does not kill active processes.

Common VCS operations:

```bash
clawflow issue list/create/view/comment/close --repo <R>
clawflow issue search "<query>" --repo <R> --state all --json
clawflow issue search "<query>" --project <project>
clawflow pr list/create/view/comment/merge --repo <R>
clawflow label add/remove --repo <R> --issue <N> --label <L>
```

Read subcommand help for required write fields. `issue view` takes a positional issue number;
`issue comment-list` instead uses `--issue <N>` and supports `--json`.
Search related historical issues before creating duplicates. `repo remove <R>` only unregisters the repo;
`project delete <project>` deletes its metadata; neither is a history reset.

Report exact commands, exit status, resulting enabled/automation/sync state,
verification evidence and unresolved failures. A successful command launch is
not a successful operator, CI job or deployment. Preserve partial-result and
backup evidence and never claim tests or live operations that were not run.
