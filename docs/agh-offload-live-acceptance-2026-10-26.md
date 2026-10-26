# Offload live AGH acceptance — 2026-10-26

> Historical record: this describes the retired AGH execution route. Current execution uses [direct Offload](offload-execution-design.md); recorded past events and results below are unchanged.

## Result

The new ClawFlow execution client successfully ran a real read-only Offload conversation through the built AGH CLI and Claude ACP bridge. The run completed in **27.246 seconds**, exited 0, and returned the requested `LIVE_AGH_OFFLOAD_OK` marker. The observed model was `claude-sonnet-5`.

The requested scoped repository run was also attempted:

```sh
clawflow run --repo zhoushoujianwork/offload --issue 15 --timeout 3m
```

The new binary correctly skipped execution because the existing installed scheduler run, PID 36450, held the run/issue locks. That existing run was processing issue #15 with the previous backend and was neither canceled nor duplicated. Exit 0 from this scan means a clean skip, not completed implementation.

## Live execution

The second command used the new build with four process-scoped `CLAWFLOW_AGH_COMMAND`, `CLAWFLOW_AGH_CONFIG`, `CLAWFLOW_AGH_NATIVE` and `CLAWFLOW_AGH_PROFILE` overrides:

```sh
clawflow chat --repo zhoushoujianwork/offload
```

Its prompt requested only reading `README.md`, reporting its first heading and three documented capabilities, and returning a known final marker. A separate private native profile prevented reuse of an existing Agent-Host session. The configured ACP bridge was version 0.81.2; authentication used ClawFlow's existing configured provider without copying credentials into the request or report.

Observed evidence:

- 64 protocol envelopes, with exactly one terminal result. A separate native result observation is not another execution.
- One actual `Read` tool call, targeting `/Users/mikas/github/offload/README.md`; subsequent tool updates refer to that same call.
- Final execution status `completed`, process exit 0, expected heading `# offload` and final marker present.
- README hash and repository Git status matched the pre-run snapshot.
- Usage was `null`; token usage and USD cost remain unavailable.
- Request ID: `19e81be1-f572-44c0-823a-0fbaf8fc53c7`.
- Logical session: `72741afc-ee6f-7339-0ea2-6f6cb6e91679`.
- Native session: `4277fdfc-9478-4f97-ac16-f604f37beeb1`.

Private local evidence and native session state remain under `/private/tmp/offload-agh-live-rgrw1otv/`. Raw events and profile contents are not committed. No installed binary, scheduler setting, remote issue/PR, or Offload source was changed by this acceptance run. This proves a real AGH execution on Offload; it does not claim a new issue-to-PR-to-merge delivery through AGH.

## Rendering correction

The live terminal displayed the answer twice because streamed stderr text was followed by the full final text on stdout. There was only one engine execution. Chat now writes streamed text to its own output writer, appends only a missing final suffix, and clearly labels a differing authoritative final answer. Operator streaming remains unchanged.

Focused race regressions cover complete streaming, partial streaming, final-only responses, differing final answers, partial errors/cancellation, redaction and session resume. The correction was validated with headless fixtures; the live request was not replayed solely to test terminal rendering.

Delegated correction: status OK, exit 0; profile `codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`; 21 calls; measured elapsed 236.064 seconds; estimated worker USD cost unavailable. Session: `01a176e1-1a3e-7433-a4a7-427bc104a239`.

See [execution design](agh-execution-design.md) and [initial headless acceptance](agh-execution-validation-2026-10-26.md).
