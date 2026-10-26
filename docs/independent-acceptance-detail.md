# Independent acceptance: implementation details

[Main usage and scope](independent-acceptance.md) is authoritative.

## Contract and execution

`internal/acceptance` binds repository/PR, task and content generation, head/base, optional integration SHA, requirements, policy digest and developer native run/session identity. Generation is derived from requirements content; it is not a durable monotonic workflow generation.

The producer derives developer identity from the latest relevant successful managed run's bounded native event stream and verifies its captured issue title/body against current requirements. Missing, unsuccessful, ambiguous or legacy provenance blocks acceptance. Native identities come from execution results, never model-authored JSON.

Each attempt uses an empty planning directory and a separate detached source checkout. Planning has tools disabled. Its immutable plan is persisted before source review begins. Review uses a fresh default-policy read-only session. Source head and cleanliness, current PR head/base, requirements and policy are checked before and after. Source-generated artifacts that dirty the checkout block a passing result.

The model returns exactly `status`, `summary` and `evidence`. Exact schema keys are enforced; unknown, duplicate, conflicting-case or trailing JSON fields fail closed. Native errors, cancellation, developer-session reuse, missing evidence and mismatched contracts cannot pass.

## Storage and merge

Private local storage under the ClawFlow data directory retains per-attempt plans and records, with size bounds, digests and atomic publication. Lookup selects the latest complete outcome for the exact contract, including failure/cancellation. Corruption or ambiguous timestamp ordering blocks. This is a reference scan, not a transactional distributed authority. Local PID-owned locks exclude review and enrolled merge for the same repository/PR.

`acceptance_required` defaults false. Explicit enrollment protects the shared automatic merge entry point and explicit CLI merge. Required evidence and CI are rechecked at merge attempts. GitHub conditional merge receives the original accepted head SHA; only a confirmed merge with a valid resulting SHA counts as success. Unsupported conditional providers block enrolled merges. Provider branch protections remain authoritative.

Head conditional merge does not atomically pin the target base. Integration candidates, merge queues and post-merge verification require subsequent work. Read-only execution uses the configured backend's permission enforcement; it is not a new container or project credential-isolation layer. Same-host file ownership remains a trust boundary until the durable authority/effect gateway work is complete.

## Validation

Core tests cover stale contracts, self-acceptance, cancellation, phase ordering, exact JSON schemas, per-attempt retries, latest-failure selection, corruption and local storage constraints. Command fixtures cover requirements provenance, isolated checkout, policy enrollment, missing/stale evidence, local exclusion, manual merge, automatic merge/sweep and conditional retries. Provider fixtures cover expected-head requests, changed-head conflicts and unconfirmed/malformed merge responses.

Live provider execution, GitHub Actions runs and production rollout require separate recorded evidence. No live result is implied by this document.
