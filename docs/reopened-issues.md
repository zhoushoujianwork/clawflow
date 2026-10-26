# Reopened issues and issue history

An issue previously marked `agent-implemented` can run again after an explicit close/reopen sequence. The latest reopen must follow both the latest implementation-label application and a close event. Equal or missing timestamps are ambiguous and block automatic execution. Reopening expresses new-work intent; a previous PR is useful context but is not required.

A linked open PR/MR blocks another implementation, including ordinary issues without the implementation label. Existing failure, skipped, running, question and tracking exclusions, repository bindings, enabled controls and local locks still apply. The runner checks fresh issue state and remote history under the issue lock, prepares complete context, and removes only the stale `agent-implemented` label. A new implementation label consumes the reopen cycle.

Each reopened cycle uses a deterministic `fix/issue-N-reopen-TOKEN` branch, where TOKEN is a safe hash of the provider event ID. Worktrees are isolated per cycle; retries resume only their own cycle. Older branches and worktrees are preserved. A reopen requires a successful base fetch and respects the repository base branch and per-issue `ClawFlow-Base-Branch:` override. It never force-pushes.

## Read history

```bash
clawflow issue history 7 --repo owner/repo
clawflow issue history 7 --repo owner/repo --json
clawflow issue comment-list --repo owner/repo --issue 7 --json
```

History contains the current body, complete paginated comments with IDs/authors/timestamps, lifecycle and label events, and linked PR/MR summaries including bodies, states, URLs and merge/close timestamps. PR links are detected through exact managed branch names or closing references; multiple closing references are supported. The scope excludes arbitrary PR review threads, edits to earlier comment versions and raw provider/tool transcripts.

Local run summaries contain only this machine's metadata: run ID, operator, status, start/end times and recorded PR URL. Existing metadata has no separate outcome field; status is not an inferred outcome. Raw run output, tool events and error text are excluded. Missing local history means no local evidence, not no previous remote work.

A failed page or unsupported lifecycle endpoint produces an explicit incomplete result and nonzero exit; automatic reopened execution stops. GitLab installations without resource state/label event support cannot automatically establish a reopen cycle. Provider clients with history capability also stop ordinary dispatch if lifecycle retrieval fails, so a retry cannot accidentally lose its cycle identity. Custom clients without history capability retain ordinary implementation but cannot authorize reopened implementation.

## Validation

Use fake HTTP transports and temporary Git remotes for regression checks; no live provider or desktop interaction is needed. Focused coverage includes lifecycle ordering, consumed/ambiguous cycles, comment/event/PR pagination, linked open PR guards, fresh state under locks, cycle worktree isolation, fresh fetch failures and history metadata. Live provider acceptance is separate from these headless checks.
