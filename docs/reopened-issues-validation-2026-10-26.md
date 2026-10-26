# Reopened issue validation — 2026-10-26

User guide: [Reopened issues and issue history](reopened-issues.md).

## Verified behavior

The new history CLI was run read-only against `zhoushoujianwork/offload#11`.
It returned exit 0 and `complete: true`, with four comments, six lifecycle
events, merged PRs #13 and #14, and two local successful run summaries.
Comment `6408969218` was present. The issue remained closed. No live issue
was reopened, no implementation was launched, and no PR was created by this
acceptance check.

The live timeline showed the previous implementation label at 03:52:24 UTC,
closure at 03:53:32, reopen at 05:36:29, label removal at 05:56:19, the next
implementation label at 06:06:03, and closure at 06:07:42. This confirms the
provider supplies the evidence needed to distinguish an unconsumed reopen
from a completed subsequent attempt.

`go test ./internal/vcs/... ./internal/operator/... ./cmd/clawflow/commands/...`
passed 632 tests in five packages. `go vet ./...` passed. The local test
binary built successfully. `go test -race ./...` passed 1,043 tests in 25
packages. A final scan-to-execution integration fixture also passed in the
worker: it checks complete context, label write order, worktree cleanup,
consumed-cycle exclusion, and a linked PR appearing after the scan.
The supervisor independently reran that final fixture with `-race`; all
three test events passed. The external ClawFlow skill validator and
`git diff --check` also passed.

No desktop interaction was used. GitLab event normalization and pagination
were tested with fixtures; a live GitLab instance was not exercised. The
history scope excludes raw provider transcripts, PR review threads, and
previous versions of edited comments. Local runs are explicitly machine-local.

## Implementation references

Provider event contracts were checked against the official
[GitHub issue events](https://docs.github.com/en/rest/issues/events),
[GitLab resource state events](https://docs.gitlab.com/api/resource_state_events/),
and [GitLab resource label events](https://docs.gitlab.com/api/resource_label_events/)
documentation. Unsupported endpoints and incomplete reads stop automatic
dispatch instead of guessing a lifecycle state.

## Delegated work

All work reused session `01a176d5-e56c-7802-bcc5-f42f48ba2c5f`, profile
`codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`.

| Slice | Status / exit | Tool calls | Measured elapsed | Estimated worker USD cost |
| --- | --- | --- | --- | --- |
| Focused investigation | OK / 0 | 7 | 131.949 s | Unavailable |
| Implementation and focused tests | OK / 0 | 33 | 797.324 s | Unavailable |
| Scan-to-execution integration fixture | OK / 0 | 7 | 368.213 s | Unavailable |

The worker sandbox could not run some socket-based tests; the supervising
agent ran the affected packages independently without that limitation.
Worker costs are not verified billing and exclude supervising-agent usage.
