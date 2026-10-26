# Direct Offload acceptance — 2026-10-26

The direct execution path passed two live, isolated Git delivery checks. Neither used AGH. These checks authorize a local fixture commit/push only; they do not prove remote GitHub/GitLab PR creation, CI, merge or production rollout.

## Evidence

The fixture starts with `Sum(2,3)` returning `-1`; its existing Go test fails. Each run receives a private skill instructing it to fix only `sum.go`, run the test, commit and push its dedicated branch to a local bare remote. Codex `danger-full-access` was explicitly selected for this trusted fixture; no automatic permission escalation was used.

| Check | Offload directly | ClawFlow production execution adapter → Offload |
|---|---|---|
| Exit/status | 0 / ok | 0 / ok |
| Native session | `01a17733-2683-7f81-8826-f8e1fe842706` | `01a1773e-b25d-7cf1-a381-ee161cbd4d29` |
| Commit | `d6470f01266683dcf369ee9c6e675e78b475d458` | `4117770750f0e09a535b8c7af48d176fc845a276` |
| Events | 24 | 17 |
| Worker elapsed | 187.277 s | 41.224 s |
| Worker API-baseline estimate | USD 0.2279632 | USD 0.1083 (rounded) |

Both runs used profile `codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`, priority tier. Estimates are not verified billing and exclude supervisor work. Independently verified: skill marker, successful final record, contiguous correlated events, passing `go test -p 1 ./...`, exact changed-file list, clean worktree, and matching local/remote commit hashes.

Private raw evidence is retained at `/private/tmp/clawflow-offload-acceptance-w2q8hvh3/`; it is not committed. The combined check used a temporary Go harness calling `internal/execution.Run`; the harness source was removed after building. It was not a real `clawflow run` issue dispatch.

## Headless validation

- Offload: `GOMAXPROCS=2 go test -p 1 ./...` and `go vet -p 1 ./...` passed. This includes the loopback test unavailable inside the implementation worker sandbox.
- ClawFlow: `GOMAXPROCS=2 go test -race -p 1 ./...` passed during integration review.
- Dashboard: `pnpm build` passed, including `tsc --noEmit`. Existing bundle-size and Browserslist-age warnings remain. No desktop/visual check was performed.
- Final recovery changes: independent race checks passed for execution, operator, Pilot, CLI commands and snapshots; execution vet and external ClawFlow skill validation passed. One CLI build initially raced dashboard asset regeneration; rerunning after the dashboard build completed passed.

The implementation worker's first ClawFlow run timed out after 1800.029 s / 68 tool calls, exit 124; usage/cost were unavailable. Its changes were inspected and preserved, and the same session `01a17724-807e-77b0-a543-bbddc7bc032f` was resumed for scoped recovery corrections. Offload implementation and corrections exited 0 in 704.396 s and 251.439 s, with API-baseline estimates USD 1.074232 and USD 0.453640, session `01a17723-e468-72a2-898a-871fab7380ee`.

The scoped ClawFlow recovery continuation exited 0 after 782.824 s / 12 tool calls, API-baseline worker estimate USD 1.2124992, using the same profile/model/session. It added crash-released advisory ownership and incremental evidence retention; 54 execution tests passed in the worker. Supervisor review and race checks cover the final changes. ClawFlow go vet also passed before these recovery edits.

## Scope limits

Git/PR capability is enabled by explicit managed-development policy; ordinary Offload permissions remain unchanged. ClawFlow retains CI/merge acceptance and worktree cleanup. Restricted Codex workspace-write Git delivery and real remote PR delivery were not exercised here. Codex rejects explicit tool allowlists/tools-disabled requests; positive dollar caps remain unsupported and fail before native execution. Existing budgeted Pilot configuration therefore does not silently lose its cap.

See the [main design](offload-execution-design.md) for setup and ownership.
