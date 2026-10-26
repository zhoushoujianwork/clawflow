# AGH execution details

[User-facing setup and scope](agh-execution-design.md) governs the migration.

## Process and wire contract

The shared `internal/execution` client launches exactly one child per request:

```text
agent-host execute --execution-config ABS --native ABS --native-profile ABS
```

A strict version-1 JSON request travels on stdin (up to 4 MiB). It carries logical UUID `session_key`, a fresh `request_id`, explicit resume, absolute workspace, model, instructions, prompt, permission mode, tool policies, timeout and optional budget. Credentials are child environment values only. No automatic retries or provider replay occur after transport, parsing or execution failures.

Stdout is bounded NDJSON: 4 MiB per line and 32 MiB overall. The client checks version, request ID, contiguous sequence numbers, valid event shapes, one terminal result, logical session identity, explicit null usage and status/exit consistency. Native session IDs are evidence, never substituted for the logical session key. Native stderr is discarded to avoid leaking credentials. Persisted event lines retain the AGH schema with known environment secrets redacted.

Completed requires exit 0; failures require 1 (or malformed-request 2); cancellation requires 130 and timeout 124. Partial text survives failure for diagnostics, without converting failure into success. Observation deltas supply fallback text only when terminal text is empty. A nonempty final answer always wins, even if an earlier observation carried an outcome marker; the runner then handles any missing final marker. No Claude events are manufactured.

## Ownership and capability limits

All launched executions register with the watchdog. On cancellation the owned process group receives TERM, followed by KILL after a shared eight-second grace interval, allowing ACP cancellation receipt and native watchdog cleanup to finish. The client never scans or kills unrelated native sessions. AGH owns the isolated native profile and session lifecycle. ClawFlow does not remove global native transcripts.

Current ACP rejects nonzero `max_budget_usd` with `unsupported_budget` before native launch. The client forwards the cap unchanged and propagates this failure. Disallowed tools are forwarded to the pinned ACP bridge; incompatible runtimes must reject unsupported restrictions. There is no silent policy downgrade. Legacy `max_output_tokens` and `failover_patterns` fields remain readable but have no execution effect; configure native output limits in AGH.

Raw AGH evidence is stored in `events.jsonl`. Dashboard rendering translates observations and terminal results directly. Snapshot usage remains nil/unknown for AGH; null usage never implies zero spend or determines run status. Historical native event files remain readable.

## Validation

Fake AGH subprocess tests cover request size above 16 KiB, cwd/model/instructions/permission/tool/budget forwarding, malformed and duplicate events, request/sequence/session mismatch, missing terminal results, exit mismatch, partial text and marker recovery, redaction, no replay, cancellation/timeouts, readiness and terminal session resume. Existing operator, watchdog, snapshot and repository tests cover integration without live AI or VCS side effects.
