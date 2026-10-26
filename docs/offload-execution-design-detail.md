# Direct Offload execution details

[Main design](offload-execution-design.md) defines scope and ownership. The delivered CLI contract and acceptance hooks are recorded below.

## Required execution contract

- Explicit absolute workspace, configured Offload profile, bounded timeout and caller run identity.
- Private, bounded instruction input for the selected operator skill and project context; task input through stdin.
- Explicit opt-in delivery policy. Default Offload read-only/write behavior must not change. Delivery permission is not permission to weaken CI, force-push, merge arbitrary PRs, or recursively launch workers.
- A machine-readable final record and correlated versioned progress evidence. A successful native process is distinct from an accepted outcome marker, verified Git changes, a delivered PR and merge eligibility.
- No implicit backend/profile fallback or replay after an uncertain failure. Resume pins the intended profile, worktree and session.
- Process cancellation and session/worktree exclusion must preserve evidence. Process groups do not guarantee containment of detached children; resource admission work remains separately scoped.

## Migration constraints

Remove AGH-specific command flags, native-profile paths and ACP assumptions from ClawFlow's active execution client. Retain compatibility when reading historical traces. Offload owns model authentication; retired ClawFlow provider fields must not silently override an Offload profile.

Offload token/cost work (committed at 95c9122) and the issue #15 resource-admission implementation belong to separate tasks. Integration changes use isolated worktrees and must not overwrite either task. Machine-local execution settings remain excluded from configuration synchronization.

## Delivered invocation

```text
offload -json -no-salvage -p PROFILE -d ABS_WORKSPACE -t DURATION   -caller-run-id ID -events-file ABS_NONEXISTENT -instructions-file ABS_PRIVATE
```

Task text travels on stdin through a private 0600 temporary file descriptor (avoiding inherited pipe hangs); selected operator instructions, project context and root `AGENTS.md` / `CLAUDE.md` instructions use a temporary 0600 instructions file inside a 0700 directory. The events path does not exist when Offload starts. Explicit model input adds `-m MODEL`; ClawFlow never resolves models from legacy Claude credentials. `role_profiles` selects by role, with an explicit environment profile taking precedence. No AGH, ACP, native-profile discovery, credential migration or fallback occurs.

Delivery adds `-w -execution-policy managed-development` and optionally `-codex-sandbox workspace-write|danger-full-access`. The retained internal API maps `PermissionMode=default` to read-only and `bypassPermissions` to that explicit delivery policy. Restrictions use `-tools-disabled`, `-allowed-tools A,B`, and `-disallowed-tools A,B`; unsupported native capabilities must fail with `unsupported_tools`. Positive budgets fail locally with `unsupported_budget` before launching a process.

Stdout contains one JSON record with `status` (`ok|error|timeout`), `exit_code`, `session`, `profile`, `backend`, `model`, `result`, `error`, `error_kind`, `usage`, `run_id`, `caller_run_id`, and `execution_policy`. Events use schema version 1, contiguous `seq`, RFC3339 timestamps, and `start|progress|final`. Caller/run/session/backend/model identities, one final record, status/exit agreement, requested profile and delivery policy are validated. Missing, truncated, duplicate, oversized or conflicting evidence fails without retry or salvage. Evidence stores declared metadata only with known environment secrets redacted. Native stderr is discarded; returned final text is redacted. Progress contains no answer text. Chat displays the terminal answer once, then resumes the actual returned native session with `-r SESSION`.

Task/request input remains bounded at 4 MiB; combined instructions (including repository instructions) are limited to 256 KiB; event lines at 4 MiB; total stdout and events at 32 MiB each. Files are monitored while running and checked again on completion. Cancellation and normal completion terminate the owned process group; stderr uses a null file descriptor so native descendants cannot keep an os/exec copy pipe alive; detached children remain outside POSIX group guarantees. Workspace and resume-session ownership uses nonblocking advisory `flock` held by open file descriptors on Darwin/Linux in `~/.clawflow/execution-locks/`, separate from issue locks. Closing the descriptors or process death releases ownership. Lock inodes remain in place and must not be unlinked while in use, avoiding split ownership. Other platforms explicitly reject unsupported ownership. No uncertain attempt is automatically replayed.

## Acceptance hooks

`internal/execution/client_test.go` runs an isolated fake executable through the actual CLI boundary. It covers policy/arguments, private inputs, correlation failures, missing/duplicate final, timeout/cancel, no replay, redaction, profile roles, tool restrictions, unsupported budgets and native chat resume. `internal/operator/execution_test.go` verifies semantic outcomes. `cmd/clawflow/commands/reopened_lifecycle_test.go` crosses scanner, fake Offload process, Git worktree and VCS writeback. `internal/config/offload_test.go` retains the machine-local sync regression. API and CLI tests cover retired providers and readiness.

Run focused checks serially with `GOMAXPROCS=2 go test -p 1` and the relevant packages/test names. No live provider or desktop check is part of this implementation. The supervisor must review before full-suite or live acceptance.

Supervisor validation supersedes the worker-only notes: Offload full tests/vet, ClawFlow full race suite, dashboard production build/typecheck, and both live local Git delivery checks passed. Final recovery changes received focused execution and nearest-dependency checks. See the [acceptance record](offload-live-acceptance-2026-10-26.md). No desktop, real remote PR/merge or installed-runtime rollout was performed.
