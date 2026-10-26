# Offload runtime rollout — 2026-10-26

The user authorized installing the direct Offload runtime and continuing the failed Offload issue.

- Installed ClawFlow: `/usr/local/bin/clawflow`, version `74771e2-offload`.
- Pinned execution binary: `~/.clawflow/runtime/offload-ae202ff`, built from Offload commit `ae202ff`. The shared `offload` executable was not replaced.
- Current profile: `claude-claude-sonnet-5-1m-inherit-inherit`. Initial rollout used Codex with explicit trusted `danger-full-access`; the supervisor explicitly changed to Claude after a provider-capacity failure and removed the Codex-only sandbox option. No AGH hop or implicit runtime failover.
- Restarted the existing headless web service at `127.0.0.1:8090`; `/api/providers` reports `engine=offload`, the selected profile, and configured readiness.
- Sync remains paused. Repository enablement, required CI and auto-merge settings were preserved. Pilot remains disabled.
- Updated the installed Codex ClawFlow skill from the repository's external-agent skill.

Rollback backup: `~/.clawflow/backups/runtime-offload-8imaxa3_/`, containing the previous binaries, configuration and ClawFlow skill. Initial configuration serialization changed timestamp syntax; it was corrected from the untouched backup before restarting the service. `clawflow config show` and `clawflow project check offload --json` then passed.

## Issue 15 / PR 16 recovery

The original implementation succeeded and opened PR 16 at head `25a97e587b2e3d086c619849996e77aaa39013aa`. The first CI repair failed because it could not reproduce the Linux failure locally and had no job-log evidence. The retained repair checkout was clean and preserved.

Authenticated read-only GitHub job logs identified the precise failure: job `121455199998`, run `40287710536`, Ubuntu 24.04.5 / Go 1.24.13, `TestCancelledQueuedRunNeverLaunchesBackend`, `internal/cli/admission_test.go:162`: a canceled queued run allegedly started a backend. Formatting and vet passed. ClawFlow's current CI-log CLI is GitLab-only; a temporary credential-loader-based read-only helper retrieved GitHub logs without printing credentials. Its source was removed.

Added scoped headless instructions in `~/.clawflow/projects/offload/testing.md` and a repository test command. Removed only the `agent-failed` exclusion; retained `agent-implemented`, PR 16 and the retry ledger. Launched:

```sh
clawflow run --repo zhoushoujianwork/offload --issue 15 --timeout 60m
```

This reserved attempt 2 of 3 and selected `repair-ci`, with isolated checkout `~/.clawflow/data/ci-repair-worktrees/repair-1737203186`. It launches Offload directly and must update the existing PR branch without force-pushing or creating a duplicate PR. A launch is not evidence of a completed repair; the terminal result and remote checks are recorded separately.

Read-only recovery investigation: exit 0, profile `codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`, 14 tool calls, 269.442 s, worker API-baseline estimate USD 0.2561984, session `01a17758-a733-7c33-9a62-20e7c2e37286`. This is not verified billing or total supervisor cost.

Attempt 2 ended with native error `Selected model is at capacity`, worker exit 1, 11 tool calls, 1171.216 s, session `01a1775e-303e-7141-ad4c-80adfaa3edfd`; usage/cost unavailable. ClawFlow's run command itself returned 0 despite reporting the operator failure, so acceptance used the operator result instead. The uncommitted test patch was inspected and saved to the project testing context; there was no commit or push. Its original checkout remains preserved.

A separate no-tool Claude connectivity probe succeeded: exit 0, profile `claude-claude-sonnet-5-1m-inherit-inherit`, model `claude-sonnet-5[1m]`, 5.032 s, estimated worker cost USD 0.010053, session `ac7887fa-3e2a-41ee-add2-4be1eedde7f7`.

After the explicit profile change, the web scheduler reserved attempt 3 and started Claude in `~/.clawflow/data/ci-repair-worktrees/repair-437422746`, session `c7233e36-44c7-4a63-a63f-9f1fdb69ad4d`. A concurrent manual run correctly skipped due to the global run lock. Removing `agent-failed` returned 404 because this transport failure had not added that label; no ledger or failure counter was reset. The worker reused the preserved patch and began focused validation.

## Verified completion

Attempt 3 succeeded: worker exit 0, profile `claude-claude-sonnet-5-1m-inherit-inherit`, model `claude-sonnet-5[1m]`, 12 tool calls, 407.583 s, estimated worker cost USD 0.6517 (not verified billing or total supervisor cost). Session: `c7233e36-44c7-4a63-a63f-9f1fdb69ad4d`.

The worker committed `e5c579a7a1af253d0b4e167a656c35ce0c0f8f31` and pushed the existing `fix/issue-15` branch normally. Only `internal/cli/admission_test.go` changed. The fix distinguishes queued-backend launches with their own marker and forces a delayed holder write in the regression test, eliminating the shared-marker race without weakening cancellation checks. Focused race tests passed 20 repetitions (116.7 s), vet passed, and the full race suite passed (CLI package 155.7 s). The supervisor inspected the actual diff.

The exact pushed head passed [remote Linux CI](https://github.com/zhoushoujianwork/offload/actions/runs/40297188857/job/121485988700) at 09:09:36 UTC. ClawFlow automatically merged [PR 16](https://github.com/zhoushoujianwork/offload/pull/16) at 09:10:08 UTC, merge commit `84d49c22cf81b9e0d0dc2a9ba8b623f4906430c1`. [Issue 15](https://github.com/zhoushoujianwork/offload/issues/15) closed at 09:10:10 UTC. Read-only GitHub API checks independently confirmed both final states. The runtime log confirms branch deletion and successful completion.

The successful attempt checkout was automatically removed; the failed attempt 2 checkout and saved patch remain available for diagnosis. The web provider endpoint still reports the pinned Offload engine and selected Claude profile ready. Its generic readiness message says live execution is unverified; the successful issue run above supplies the separate live evidence. The installed Offload binary remains integration build `ae202ff`; this rollout does not install the newly merged PR 16 into the executor.
