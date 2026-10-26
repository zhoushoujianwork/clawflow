# AGH execution acceptance — 2026-10-26

> Historical record: this describes the retired AGH execution route. Current execution uses [direct Offload](offload-execution-design.md); recorded past events and results below are unchanged.

The implementation uses the CLI boundary described in the [main design](agh-execution-design.md). ClawFlow owns issues, worktrees, PRs, CI and acceptance; AGH executes one ACP turn. No direct Claude execution fallback remains in ClawFlow. This does not remove AGH's separately configured print backend for its other consumers.

## Source and rollout scope

- ClawFlow: `feat/agh-execution`, based on `f9e1c8e`, including the committed reopen/history fix.
- Agent-Host: `feat/clawflow-execution`, execution interface commit `ce5dc1f`.
- Both changes were developed in isolated worktrees. Original checkout edits and running services were preserved.
- No source push, remote merge, installed-binary replacement, real-provider execution or desktop validation was performed.
- ACP engine admission remains Claude bridge 0.81.2 only. Multi-engine switching is not claimed. Usage remains unknown; unsupported nonzero budgets fail before native execution.

## Acceptance

| Check | Result |
| --- | --- |
| ClawFlow `go test -race -p 2 -json ./...`, isolated HOME | Passed: 1,004 passing test events, 22 packages, zero failures |
| ClawFlow `go vet ./...` | Passed |
| Dashboard `pnpm build`, including TypeScript check | Passed; existing Browserslist age and bundle-size warnings remain |
| External ClawFlow skill schema | Passed |
| Agent-Host `make check`, `TMPDIR=/private/tmp GOFLAGS=-p=2` | Passed, including Go, Python and Node checks |
| Built AGH CLI → real ACP adapter → fake ACP process | Passed: 20 KB input, explicit cwd/model/tool policy, resume, unsupported-budget rejection and timeout with partial output |
| Actual ClawFlow execution client → built AGH CLI → fake ACP process | Passed: 20 KB input, final result and same-session continuation |
| Production Go source audit | No `RunClaude`, native Claude resolver or direct Claude subprocess-launch call remained |
| Diff and local documentation links | Passed |

The cancellation regression verifies a grace period long enough for AGH cleanup. Terminal text takes precedence over earlier streamed outcome markers. The reopened lifecycle fixture uses the new protocol while retaining its history, PR recheck, completion and workspace assertions. The dashboard recognizes `agh-started` and retains historical `claude-started` display support.

Initial worker checks encountered sandbox socket/process restrictions. The first unrestricted AGH check hit startup timeouts in existing CLI/channel fixtures; those tests passed separately, then the complete check passed with reduced parallelism. The first ClawFlow race run exposed the newly rebased reopen fixture's old fake-Claude setup; the fixture was migrated and the final full race suite passed. A worker capacity error preserved all edits and was resumed in the same session without switching providers.

## Local evidence

- Final ClawFlow race log: `/private/tmp/clawflow-agh-acceptance-1joo98ws/race-final.jsonl`.
- Initial ClawFlow race log: `/private/tmp/clawflow-agh-acceptance-1joo98ws/race.jsonl`.
- Successful full AGH check: `/private/tmp/agh-execution-supervisor-check-serial.log`.
- Initial unrestricted AGH check: `/private/tmp/agh-execution-supervisor-check.log`.
- Cross-process fixtures and NDJSON evidence: `/private/tmp/agh-protocol-acceptance-9iro7c51/`.

These are local temporary evidence paths, not portable release artifacts. Fake engines establish protocol behavior, not real-provider quality, billing accuracy or a live issue-to-merge rollout.

## Delegated execution records

All rows used profile `codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`. Estimated worker USD cost was unavailable for every row; this is neither zero cost nor verified billing. Elapsed times are per invocation and overlap across workers.

| Work | Status / exit | Calls | Elapsed | Session |
| --- | --- | ---: | ---: | --- |
| Focused implementation feasibility | OK / 0 | 7 | 137.241 s | `01a176d3-e5ad-7993-abee-7ab6942c871b` |
| AGH implementation | OK / 0 | 47 | 1,276.238 s | `01a176e0-9748-7821-a957-a73d8000bbbe` |
| AGH help correction | OK / 0 | 6 | 165.570 s | `01a176e0-9748-7821-a957-a73d8000bbbe` |
| ClawFlow migration, capacity interruption | Error / 1 | 67 | 1,485.222 s | `01a176e1-1a3e-7433-a4a7-427bc104a239` |
| ClawFlow resumed review corrections | OK / 0 | 10 | 297.155 s | `01a176e1-1a3e-7433-a4a7-427bc104a239` |
| Reopen fixture integration correction | OK / 0 | 8 | 149.694 s | `01a176e1-1a3e-7433-a4a7-427bc104a239` |
