# Dispatch coordination implementation

User-facing scope: [design](dispatch-coordination.md).

## Entry points and data

The scheduler runs coordination for built-in `implement` and `repair-ci` before acceptance planning, enqueue and repair reservation. Standalone runs use the same gate before acquiring execution slots. Existing owner opt-out, dependencies, priorities and repository queue selection remain authoritative.

The coordinator reads the current issue, the latest 40 non-coordination discussion entries, up to 100 open issues, 10 closed search matches, up to 20 explicit issue references, up to 40 open PRs with up to 100 changed paths each, current active task identities and the manual queue order. A completion directive additionally reads its exact PR. Provider errors, oversized input and incomplete snapshots fail closed; incompleteness is visible as a scheduling-owned confirmation reason. The complete input is capped at 128 KiB.

A tool-free light assessment has a 45-second budget inside a 90-second coordination operation. It cannot modify files or run tools. Its typed decision must cite existing IDs, explain the reason, and identify a next action and owner. Model-proposed closure is not sufficient authority.

## Decisions and persistence

Private `data/coordination/<repository digest>/<issue>.json` records retain the versioned snapshot, digest, issue-body/title digest, decision, time and effect-application acknowledgement. Writes use a private temporary file plus rename. A persistent advisory lock serializes each issue's assessment/application. Identical evidence reuses the decision; a 30-second minimum interval bounds changing-input reassessment.

- `proceed` clears only the two informational coordination labels and passes the approved context to execution.
- `waiting` applies `agent-waiting`, posts a snapshot-deduplicated explanation and allocates no task, agent slot or repair attempt. Cheap queue checks keep unchanged waits from repeatedly consuming queue picks. Changed blockers or a maximum five-minute queue deferral cause full evidence collection again.
- `needs-confirmation` applies `agent-needs-confirmation`. A maintainer incorporates the resolution into the issue title/body, or clears that label after resolving the question. Ordinary comments alone do not authorize a scope change or resume. The model cannot override owner `clawflow-skip`.
- `superseded` requires an exact standalone issue-body line `Superseded by: #N` pointing to a retrieved different issue.
- `already-delivered` requires `Completed by: #PR`, a merged PR referencing the current issue, a merge commit identity and the assessment that it covers all requirements. Without that explicit authoritative directive, partial/ambiguous completion waits for confirmation.

Closure refuses tracking parents, unsettled execution and linked open PRs. The coordinator records its explanation before closure and rereads the snapshot immediately before closing. Provider APIs do not offer atomic issue-revision conditional closure; this is fresh validation under local scheduler/issue ownership, not a claim of distributed atomic VCS edits. Unknown comment outcomes reconcile via the existing idempotent comment API; repeated scans do not deliberately replay applied effects.

## Concurrency and freshness

After assessment, the entire input is recollected before effect application. Before enqueue, remote evidence is checked again. Inside the existing repository enqueue database advisory lock, active task identities/states/priorities must match the approved snapshot; stale concurrent assessments are rejected and reserved repair capacity is released. The existing task uniqueness, claim ordering and lease fencing remain authoritative.

Workers revalidate issue/discussion/PR evidence before clone preparation. Manual queue order remains coordinator-owned. A changed context returns a scheduling-owned blocked infrastructure result before source mutation, without an implementation outcome label. Legacy queued tasks without a snapshot remain readable; new dispatches carry one. Worker results and development effects retain existing fencing. New guarded tasks require `protocol:dispatch-coordination-v1`, automatically advertised by the updated worker. Older workers remain eligible for legacy tasks but cannot silently consume guarded work; upgrade workers before the controller or let guarded tasks wait for compatible workers. This change does not restart or upgrade a running service.

The CLI and native app reuse the issue-queue projection for waiting reasons, confirmation, superseded closure and coordination-checked readiness. This adds no Web UI and no scheduler owned by the viewer.

## Validation

Tests cover typed evidence validation, explicit versus quoted replacement directives, incomplete input, title-only/partial/open-PR completion rejection, tracking/unsettled closure guards, cache identity, comment-marker sanitization, wait/resume, duplicate active issue exclusion, stale assessment rejection, provider failures, unknown comment recovery, queue capacity, worker freshness and native status decoding. A PostgreSQL-backed concurrent enqueue test proves only one empty-snapshot producer succeeds; fresh compatible follow-up work can still enqueue.

All database verification uses a separate temporary PostgreSQL instance and schema-isolated tests. No installed app or main service is replaced or restarted. Native visual acceptance is pending an explicitly agreed desktop window.

Final quiet validation (2026-10-30): `go test -race -p 2 -timeout 8m ./...` passed with 3,117 test/subtest passes and zero skips against the isolated PostgreSQL instance. The additional owner-skip regression passed separately. `swift test --package-path macos` passed all 20 tests. The dev CLI built to `/tmp/clawflow-616-dev`; the installed CLI and app were not replaced. The worker wire-protocol regression passed five repeated runs after its heartbeat fixture was corrected to inject lease failure only in the explicit lease-loss modes.
