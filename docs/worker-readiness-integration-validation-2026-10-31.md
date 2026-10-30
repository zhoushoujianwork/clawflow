# Durable readiness integration validation — 2026-10-31

PR #618 was updated against main without rebasing or discarding durable claim/recovery support. Both `recover` and `release` endpoints remain registered. The following integration defects were corrected before requesting fresh CI:

- Runtime readiness caches are process-local and excluded from journal/descriptor JSON; each served manager and prepared executor rebuilds its own gate.
- An unstarted prepared executor persists a release intent, uploads no result, and retires its local epoch after a successful or stale release response. Restart replays the intent. Resumed/checkpoint-bearing executions retain their reservation when runtime readiness fails.
- A later authorized epoch archives an acknowledged or released attempt with exclusive journal/session locks. A released attempt with a retained checkpoint is rejected. This incorporates the previously validated acknowledged-attempt repair from source commit `75828c36`.
- A blocked manager retains saved claims while continuing registration; pending result/release delivery remains recoverable. Authentication failures during registration are propagated.
- The native timer Task explicitly captures weak self for the strict Swift compiler, matching the independently tested correction in PR #617. The notification callback reacquires its notification center rather than capturing a non-Sendable instance across the callback.

Headless checks:

- Isolated PostgreSQL 17 fixture: `go test -p 1 ./cmd/clawflow/commands ./internal/coordinator ./internal/execution ./internal/repairledger -count=1`: 1,410 passed. The fixture server was stopped afterward.
- Focused readiness/worker/coordinator/execution checks before the final guard refinements: 145 passed.
- Strict-concurrency Swift checks with warnings as errors: 26 tests passed after fixing both captured-self and notification-center Sendable errors.
- Final worker claim, durable gate, and admission checks under the race detector passed (31 in the focused gate/claim group; earlier recovery/admission group also passed).
- Release fixtures cover HTTP 200 and stale 409, missing runtime before task code, replay after lost receipt, absence of a native checkpoint, and refusal to release resumed/checkpoint-bearing work.
- No provider request, production service restart, retry-budget reset or desktop action was used for this PR repair. Independent acceptance, native visual validation and fresh published-head CI remain separate gates.
