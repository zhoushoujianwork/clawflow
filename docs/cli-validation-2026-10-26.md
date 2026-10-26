# CLI validation evidence — 2026-10-26

Scope: headless local readiness, machine-binding configuration, operator diagnostics, and an isolated offload test baseline. [CLI design](cli-interaction-design.md) defines the broader intended workflow.

## Observed results

| Check | Result |
|---|---|
| `project check offload --json`, before binding | Exit 1; valid schema-version-1 JSON; `pilot_binding` and `repo_binding` blocked because binding is required |
| Inspection side effects | Config, credentials, project documents, existing snapshots and checkout index hashes/mtimes unchanged |
| Repository binding | Set to the current execution host through `repo set --bound-machine` |
| Project binding | Set through `project automation bind --machine` |
| `project check offload --json`, after binding | Exit 0; status `ready`; both machine-binding checks passed |
| Automation | Monitoring, auto-approval, auto-merge and Pilot remained disabled; sync remained paused |
| `operators doctor` | All eight built-in operators passed after correcting false duplicate-trigger detection |
| Independent readiness/diagnostic regression checks | 36 test cases passed across the two affected packages after the final fsmonitor safeguard |
| Independent repository-binding regression checks | 18 test cases passed |
| Build | CLI build passed |

The local result still warns about empty context/testing documents and disabled automation. `ready` does not certify provider authentication, remote freshness, project tests or the issue workflow; the JSON lists those capabilities as unverified. This report's separate test evidence is not automatically imported as a workflow gate.

## Offload baseline

Checked commit `ff0a109f93415fa35e67d6da7cf386ea3efca38a` in a separate local clone with a detached checkout. The working repository was not reset, switched, edited or committed by this validation.

Command:

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 ./...
```

Exit 0. Tests passed in `internal/agentinstall`, `internal/cli`, `internal/config`, `internal/live` and `internal/worker`; remaining packages had no test files. The CLI package took 74.307 seconds. Tests use fake model executables and temporary configuration; the inspected network test uses localhost. Dependency/toolchain downloads were disabled. This establishes the pinned commit's baseline, not the state of later commits or concurrent edits.

## Boundaries and next acceptance work

No desktop apps were opened or manipulated. No agent provider was invoked for a ClawFlow issue run, and no remote issue, comment, PR or merge was created as part of acceptance testing. Repository research/implementation used the separately configured offload workers below.

The next CLI delivery is noninteractive project-document configuration, followed by execution preview and durable run inspection/control. Then a designated issue can exercise the remote workflow with a bounded scope. CLI names for those future capabilities remain proposals in the design; the skill does not present them as available.

## Delegated work

All workers used profile `codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`. Every listed run completed with status `ok`, exit 0. Estimated worker cost was unavailable for each run; no verified billing or token savings are claimed.

| Work | Session | Tool calls | Measured elapsed |
|---|---|---:|---:|
| CLI feasibility | `01a1762a-5c3a-7a02-858b-9e8b58bab91e` | 40 | 98.157 s |
| Readiness and diagnostic implementation | `01a1762a-5c3a-7a02-858b-9e8b58bab91e` | 41 | 468.230 s |
| Offload test-safety inspection | `01a1762c-aa78-7753-831b-53e9461bd5de` | 5 | 115.370 s |
| Repository binding implementation | `01a17631-0a6e-7301-971d-2ae57ace30d8` | 15 | 132.092 s |

Workers encountered default Go-cache sandbox restrictions and used temporary writable caches for focused tests. The supervising agent inspected their diffs and independently ran the scoped tests and build. It added the final fsmonitor safeguard and regression test locally as a small known-file review correction.
