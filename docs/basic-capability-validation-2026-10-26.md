# Basic capability validation — 2026-10-26

This checkpoint strengthens the [independent acceptance gate](independent-acceptance.md) with an explicit [execution capability check](execution-capability-check.md) and a test through the production subprocess adapter.

## Changes and evidence

- `f6b5f83`: acceptance invokes the real `execution.Run` against an isolated fake subprocess. The test verifies source-free tools-disabled planning, a persisted private plan before the reviewer starts, separate native identities/directories, default execution policy, and nonpassing results after malformed or failed native execution. Git and VCS remain fixtures; this does not prove real checkout or sandbox isolation.
- `4b4caff`: `execution check [--role acceptance] [--json]` runs only configured executable help in an empty private temporary HOME/directory with a minimal environment. It checks declared protocol flags, bounds time/output and cleans up process groups. `project check` retains its local file/config/Git inspections without invoking this Offload probe.
- `cdd7fc6`: reject unusable help bytes and indented diagnostic errors; read canonical configuration without legacy migration; guard required flags against production argv drift. It also scopes the shell helper test to non-Windows and records helper invocation before emitting deliberately rejected events.

Parent validation passed:

| Check | Result |
|---|---|
| Protocol integration with race detector | 13 tests passed |
| Capability probe with race detector | 22 tests passed |
| CLI capability tests | 6 tests passed |
| Full `go test -race ./...` | 1,323 tests passed across 27 packages |
| Focused race suite after repairs | 80 tests passed across execution and command packages |
| CLI build | Passed |
| Diff checks | Passed |

The full suite includes independently committed issue #354 work already present in the shared checkout. That work was not staged or implemented by this capability slice. The post-repair focused run initially exposed a test-harness race: a malformed caller event could cause the real adapter to kill the helper before it wrote its invocation capture. Invocation recording now precedes rejectable events, while plan validation still precedes source-tool events. The subsequent focused run passed all 80 cases; no failure assertion was removed.

## Real local help probes

The built candidate `/tmp/clawflow-capability-pilot` was used without replacing the installed ClawFlow binary or changing configuration:

```bash
/tmp/clawflow-capability-pilot execution check --role acceptance --json
```

The configured `/Users/mikas/.clawflow/runtime/offload-ae202ff` returned `ready`, exit 0, with all flag declarations present. `provider_execution` and `permission_enforcement` remain explicitly unverified.

A process-local command override to `/Users/mikas/go/bin/offload` returned `blocked`, exit 1, naming missing delivery-protocol flags. It emitted one JSON document on stdout and a generic blocked message on stderr. No configuration was written. Neither check supplied a task or profile argument to the child or contacted a provider.

Help capability is an advertised interface, not proof that a runtime implements it correctly. Runtime/profile credentials, live provider execution, sandbox enforcement and remote Actions remain separate checks.

## Independent acceptance

Reviewer session `d3522cb0-9110-4e32-bf42-c7484ddb4855` authored its plan before implementation inspection. The supervising agent corrected proposed extra quantitative targets and clarified the actual schema/phase contract before review. Candidate `4b4caff611e1f524759625801b4b1a6b59242469` received FAIL: unusable help bytes could pass, indented errors could be ignored, and config loading could migrate legacy files. All were repaired in `cdd7fc6`. The reviewer also requested a flag-drift guard, which was added.

Final verdict: **PASS at source level** for `cdd7fc6425f9f14ac2f532e2b30d4ecceb6320f2`, with zero blocking findings. The reviewer verified the repaired delta, platform tag and helper ordering. Its dynamic test calls were denied in both reviews, so this is independent static acceptance alongside parent-run dynamic evidence, not independently reproduced tests. The parent rebuilt and repeated the configured pinned-runtime help probe successfully after the repairs.

Remaining parser limits: Go-style help declarations are expected; ANSI-colored help fails closed. Unusual prefixed diagnostic logs are not comprehensively recognized. A ready report remains limited to help declarations. The argv drift test uses the existing fixture parser and excludes its synthetic default-policy field; it is not an Offload implementation contract test.

## Delegation metrics

All workers used profile `claude-claude-sonnet-5-1m-inherit-inherit`, model `claude-sonnet-5[1m]`. Costs are estimated worker costs, not verified billing, total supervisor cost or measured savings. Elapsed times include setup and waiting. All completed worker processes below exited 0; that alone is not a passing acceptance verdict.

| Assignment | Session | Tool calls | Elapsed seconds | Estimated USD |
|---|---|---:|---:|---:|
| Feasibility | `69d3cd03-32b1-4d4a-99d9-9560360badb8` | 23 | 76.830 | 0.5166 |
| Independent plan | `d3522cb0-9110-4e32-bf42-c7484ddb4855` | 1 | 45.515 | 0.0428 |
| Plan scope clarification | `d3522cb0-9110-4e32-bf42-c7484ddb4855` | 1 | 10.763 | 0.0740 |
| Protocol integration test | `69d3cd03-32b1-4d4a-99d9-9560360badb8` | 28 | 767.613 | 2.8542 |
| Capability implementation | `9012dcb8-3347-4502-84c3-b29a0ceb3b7e` | 49 | 1126.315 | 3.1176 |
| Independent review: FAIL | `d3522cb0-9110-4e32-bf42-c7484ddb4855` | 42 | 570.391 | 1.7245 |
| Review repairs and drift guard | `9012dcb8-3347-4502-84c3-b29a0ceb3b7e` | 30 | 853.722 | 7.1083 |
| Independent reacceptance: source-level PASS | `d3522cb0-9110-4e32-bf42-c7484ddb4855` | 9 | 147.993 | 2.4356 |

The capability worker reported six permission denials and one initial unrelated worktree-test index-lock failure during concurrent shared-checkout work. Its second package run passed; the parent full race run passed independently. No passing check was inferred from those failures.

No push, merge, provider task, desktop validation or service restart was performed by this slice. Platform scale remains unmeasured.
