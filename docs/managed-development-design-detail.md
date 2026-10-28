# Fully managed Git development: implementation notes

Status: Proposed · Date: 2026-10-26

[Main design and scope](managed-development-design.md) is authoritative. These contracts illustrate the implementation direction and may be refined during development. No proposed schema or command here is implemented by this document.

## Current autonomous dispatch policy

The runner has one autonomous lifecycle, with no mode selector. Eligible open leaf issues enter implementation, where analysis, planning, headless tests and repair happen internally. Custom trigger constraints, terminal exclusions, machine binding and local execution locks remain. Scanning and fresh-label rechecks use the same routing. Questions/comments retain reply paths; parents retain `track-progress` through `progress-check`.

```bash
clawflow repo set owner/repo --auto-merge on --ci-required on
clawflow repo enable owner/repo
clawflow run --repo owner/repo
```

Required CI accepts only success. Supported same-repository GitHub managed PRs linked to open implemented issues can receive failed-CI repair. Pending, missing or unreadable CI does not queue repair. The runner captures the PR head, rechecks identity and failure under the local issue lock, strictly fetches that head, and reserves at most three total attempts with a five-minute cooldown. The agent reproduces tests locally, commits and pushes normally to the same branch. No CI-log ingestion, fork or GitLab repair is included; other GitLab capabilities remain. This repair ledger does not implement the general durable contracts below.

Old config input remains readable. Explicit legacy selection emits a migration notice and proceeds autonomously. Rewrites omit `workflow`, `auto_approve`, `auto_evaluate_all_issues` and `confidence_threshold`, preserving enablement, bindings, merge/CI and other supported settings. Retired exact names `classify`, `evaluate-bug`, `evaluate-feat` and `decompose` are rejected for built-ins and user overrides. Non-retired custom operators and history remain; the eval model role only serves custom `evaluate-*` extensions.

## Existing code and migration map

Historical source baseline `f9e1c8e` launched Claude directly. The existing Offload integration commit `74771e252ca845213e9c33e3bcb4fe2a5e4a0f2e` and follow-up `a4f4a88` are being combined with the independently accepted CI fix on `feat/independent-acceptance`. The installed binary still reports `74771e2-offload`. Before live self-hosting, verify adapter capabilities and execution semantics, build the reconciled source, and validate that the selected binary and executor match the recorded versions. An installed binary's behavior does not establish the integrated checkout's capabilities.

The source map below records the earlier review at base commit `dff005a`; it is historical inspection context, not the current supported control surface. Line numbers are landmarks, not stable APIs.

| Existing location | Verified role | Proposed evolution |
|---|---|---|
| `internal/operator/operator.go:17` | Operator schema; `lock_label` is retained only for compatibility | Keep schema compatibility; add versioned managed contracts |
| `internal/operator/registry.go:12` | Embedded loading and user overrides by name | Add package resource resolution, validation and digest pinning |
| `internal/operator/context.go:29` | Agent context assembly | Supply pinned skill resources and explicit step inputs |
| `internal/operator/claude.go:274` | Agent execution and failover | Reuse behind an execution adapter |
| `internal/operator/runner.go:426` | Idempotent comment write; comment failure does not prevent outcome-label progress | Persist effect intentions and individual effect completion |
| `cmd/clawflow/commands/run.go:802` | PR operator scan deferred; auto-merge sweep present | Route normalized issue/PR events into the managed scheduler |
| `cmd/clawflow/commands/run.go:1612` | Historical automated outcome follow-up | Move managed transitions into the workflow engine |
| `cmd/clawflow/commands/run.go:1711` | Workspace selection and implementation worktrees | Reuse Git handling under durable task/attempt ownership |
| `internal/snapshot/snapshot.go:1425` | Stale lock/run reconciliation | Preserve history; project managed ledger state into existing views |
| `internal/pilot/schedule.go` | Project-level scheduling | Retain backlog intake; hand eligible work to one execution authority |
| `internal/config/config.go:35`, `cmd/clawflow/commands/lang.go:70` | `TestCommand` setting and language-detection use | Do not mistake this setting for an enforced test gate |

Suggested new internal boundaries are `workflow` (graph and scheduler), `skill` (managed packages), `execution` (adapter contracts), `testing` (normalized evidence), and `store` (ledger/effect intents). Start as a modular monolith, not separately deployed services. Reuse existing project management, VCS and operator modules and the integrated Offload execution adapter. This document does not invent additional Offload commands. Keep `internal/vcs` as the provider boundary and `internal/api`/`web` as views and controls over the same engine. Extract incrementally for measured bottlenecks or independent security, failure-isolation or scaling needs.

The source review found no general durable workflow graph or unfinished-write replay mechanism. Existing metadata reconciliation and partial worktree reuse should be preserved, but do not establish those stronger guarantees. Repository-wide runtime tests were not executed for this documentation task.

## Skill package and loading

A package retains `SKILL.md` as its entry point. Its frontmatter declares schema version, package kind, version, supported kernel version, typed inputs/outputs, required capabilities, and a pointer to any larger declarative manifest. Scripts and reference files are packaged resources with content digests; loading only the entry-point Markdown is insufficient.

Proposed kinds: `workflow`, `activity`, `test`, and `policy`. Workflow and policy manifests are interpreted deterministically; their prose explains intent. Activity steps can use an agent adapter or a command adapter. Test packages may use discovery agents during configuration, but gate evaluation uses structured evidence.

Loading pipeline:

1. Discover installed built-in, administrator, and repository packages.
2. Resolve explicit project bindings and dependencies. An exact source/version binding wins; ambiguous names fail validation rather than silently overriding a trusted package.
3. Validate schema, compatibility, input types, required capabilities, dependency cycles, referenced files, paths, and bounded loop declarations.
4. Pin the complete package tree by digest and save the resolved graph and parameters as a run snapshot.
5. Materialize required resources in the attempt workspace and inject only selected agent instructions.
6. Validate results against the declared output contract before advancing the graph.

Repository instructions may supply project context but cannot grant capabilities. External package installation and repository package activation follow administrator/project source policy. Secrets are references resolved at execution time and are excluded from manifests, snapshots, prompts where unnecessary, and artifacts. A changed dependency or script changes the package digest.

Example project binding (proposed, not current configuration):

```yaml
managed:
  workflow: builtin/git-development@1
  bindings:
    implement: builtin/implement@1
    review: builtin/review@1
    tests: repo/project-tests@1
    policy: repo/development-policy@1
  parameters:
    max_repair_attempts: 3
```

The resolved snapshot pins immutable digests even when a human-readable version selector is used. Workflow graph definitions live in the bound workflow package, not duplicated in this binding file.

## Runtime model

| Record | Key information |
|---|---|
| Project binding | Provider/repository identity, package pins, environment bindings, autonomy policy |
| Task | Source issue/event, goal revision, acceptance-contract version/digest, dependencies, selected endpoint |
| Workflow run | Task generation, compiled graph, configuration snapshot, aggregate status |
| Step attempt | Step, attempt number, input digest, lease epoch, workspace, executor session, timestamps, result |
| Evidence | Contract digest, exact code/integration/base SHAs, policy/profile digests, environment identity, trusted check/workflow identity, run/job IDs and attempts, status, artifacts |
| Effect intent | Desired external write, idempotency key, preconditions, external identity, reconciliation status |
| Event | Provider delivery identity or synthetic deduplication key, source revision, timestamp, normalized payload |

Use PostgreSQL (via GORM) as the single store; the decision is recorded in [storage-design.md](storage-design.md). Start with one scheduler. Persist transitions and pending outbox effect intents in one transaction; deduplicate incoming observations in a durable inbox. The store is authoritative; GitHub Issues, labels/comments and human-readable logs are coordination mirrors or artifacts, never competing scheduling authorities. Database durability and backup remain deployment responsibilities; a database copy does not create a safe second scheduler.

The target is tens to hundreds of projects and 10,000 concurrent workers, not demonstrated capacity. Production scale requires PostgreSQL features such as atomic lease claims, uniqueness constraints, fenced result/effect commits and restart-safe migrations (versioned migrations already exist). Benchmark contention and recovery independently. Multiple schedulers are enabled only after the ownership tests pass.

Suggested step states: `pending`, `ready`, `running`, `waiting_external`, `succeeded`, `failed`, `blocked`, `cancelled`. An attempt is immutable after termination. A retry creates another attempt and preserves prior evidence. Run status is derived from required steps and the selected endpoint; it is not inferred from a label.

## Event ingestion and scheduling

Normalize polling observations and, later, webhook deliveries into one inbox. Polling remains a reconciliation mechanism even if webhooks are enabled. Keep durable per-source cursors, paginate reads, and apply backoff and rate-limit budgets. Do not infer a successful remote write from a successful HTTP request without checking the provider's documented result semantics.

Duplicate delivery maps to the existing task generation. A changed issue goal or source revision can request a new generation; it must not silently mutate a running attempt's input. Merge/close/cancel observations reconcile the task rather than creating conflicting work.

The deterministic scheduler chooses ready steps subject to satisfied dependencies and fair project, provider and resource quotas (worker slots, CPU/memory, API rate, CI runners and spend). Use bounded queues and per-project admission limits so one large backlog cannot starve other projects. Coordinate and implement the existing [dependency-gate backlog #337](https://github.com/zhoushoujianwork/clawflow/issues/337), verified OPEN and `agent-skipped`, rather than claiming reuse of an implemented gate. Persist dependency decisions against task generations and invalidate readiness when prerequisites change.

A durable lease with heartbeat and monotonically increasing fencing epoch prevents an expired worker from committing results. Claim and renewal are conditional transactions; result and effect authorization require the current epoch. Labels such as `agent-running` can mirror activity but are not locks. The reference release supports one active scheduler per store; worker processes must lose write capability when their attempt is fenced or cancelled. External writes require the gateway because an expired process cannot be trusted to self-revoke credentials.

Cancellation stops new dispatch, terminates the active execution where supported, revokes effect authorization, and then reconciles partial effects. Resume re-evaluates preconditions; it does not blindly rerun the last command.

## Workspace and execution boundary

Allocate an isolated worktree/branch for each task, with bounded per-attempt modifications and a recorded base SHA. Worktrees isolate Git edits, not arbitrary executable code: projects that execute untrusted code need a container or equivalent restricted executor with scoped credentials and network access. Command adapters use structured executable/argument/environment inputs.

Development and acceptance run in separate ephemeral isolated worker pools and separate sessions. Development capabilities permit workspace edits and submission requests. Acceptance capabilities permit read-only access to the submitted source and execution in disposable test environments, plus evidence publication; they exclude source pushes, merge authorization and policy relaxation. Test-generated files stay in scratch/overlay storage and cannot mutate the submitted source. A repair returns to a development attempt and creates a new submission and acceptance attempt.

Before reading implementation details or development conclusions, the acceptance session authors its own plan from the immutable acceptance contract and task requirements. Persist the plan digest, timestamp, input provenance and role/session identity before releasing the submitted implementation, development summaries or claimed test results to that session. Subsequent source inspection and comparisons may supplement evidence but cannot retroactively replace the independent plan. Repaired submissions require a new acceptance attempt with the same ordering constraint.

An execution request contains task/generation/attempt IDs, role, session ID, lease epoch, pinned skill and acceptance-contract digest, exact code/integration/base SHAs, policy digest, inputs, workspace, deadline, resource budget, capabilities, and artifact locations. Separate role-scoped credentials enforce permissions outside prompts. Agent sessions can be attached for diagnosis or continuation, but a resumed conversation cannot bypass the lease or input revision, and a development session cannot be reused as independent acceptance.

The structured result contains status, outputs, artifact references, evidence references, requested effects, and a failure category. A zero process exit code is necessary for some adapters but never sufficient to certify task completion. Invalid or absent output becomes a protocol failure.

Agents may edit their workspace and request effects. Pushes, PR creation, labels/comments, merge, and task closure go through the effect gateway. Existing custom skills that directly write VCS state need a constrained compatibility adapter during migration. Do not introduce another repository execution mode. The engine must not claim gateway enforcement until those paths are removed or constrained.

## External effects and crash recovery

External systems and the local store cannot share a transaction. Use durable intent plus reconciliation, with at-least-once delivery and idempotent effects where possible; do not promise exactly-once execution.

- Before dispatch: persist the effect intent with task/run/step identity, precondition SHA, and deterministic operation key.
- Before a write: check the lease and verify source state and configured permissions.
- After a write: persist the provider object ID/result; if the response is lost, read and reconcile using deterministic branch names, PR source branches, or comment markers.
- When uniqueness cannot be established: stop the effect as ambiguous and investigate; never blindly recreate it.
- After restart: reconcile outstanding effects and external CI jobs, then reclaim expired attempts. Reattach to a known CI run instead of dispatching another one.

Record partial effects even when the step ultimately fails. A stale agent result is retained for diagnosis but cannot advance the run. Every effect gateway operation must recheck fencing; advisory prompting is insufficient.

## Test configuration workflow

### Component adapters and onboarding

External executors, Offload, CI providers and optional tools expose a capability/version adapter: identity and version discovery, compatibility range, supported operations and role capabilities, health probe, request/result schema, cancellation and reconciliation semantics. Verify the installed declarations or API before binding; unsupported versions or operations fail closed with evidence. Do not infer a usable API from a product name.

Component onboarding is `discovered → verified → bound → active`, with `blocked`, `disabled` and `revoked` outcomes. Discovery is read-only; a newly found component remains disabled. Verification records probe evidence and adapter version/digest. Binding pins project, role, resource and secret references and approved capabilities. Activation records explicit operator authorization or an existing configured authorization policy. Unknown components are not invented, installed or automatically onboarded. Revocation blocks new dispatch and reconciles in-flight work. Management views expose state, pins, health, limits and failure reasons without disclosing secrets.

### Project test profile

Use a separate configuration run so incomplete onboarding cannot be treated as a ready development project:

`discovered → proposed → preflight → validated → activated`, with explicit `blocked` and `rejected` outcomes.

Discovery inspects repository manifests, existing CI definitions, test commands, runner requirements, services, and reports. It returns findings with confidence and missing inputs. Setup proposes a test package and any necessary CI change as artifacts/PR changes. It preserves existing required checks by default and requests missing secret bindings by name, never by embedding their values.

Preflight verifies commands, working directories, tool versions, dependency access, secret references, runner availability, report parsing, and at least one baseline execution. A pre-existing failing baseline is recorded as a blocker or an explicit policy exception, not silently accepted. Activation binds the validated package digest to the project and records who or what policy authorized it.

Configuration changes do not loosen an active run's requirements. A configuration PR runs the current required checks and validates the candidate profile. A code task changing tests or CI cannot approve its own lower gate; policy changes need the configured separate authorization path. Restarting a run against a new profile produces a new auditable generation.

## Test execution and evidence

A test package describes check identities, required/optional status, commands or CI job selectors, environment requirements, timeouts, report parsers, artifact collection, and any supported scope selection. Secrets are environment bindings outside the package. Begin with generic command execution and existing provider CI adapters rather than one framework implementation per language.

Normalize results to `passed`, `failed`, `pending`, `cancelled`, `infra_error`, or `unknown`. Store skip counts and collected test counts; required checks with no usable evidence cannot become `passed` merely because a process exited zero. Explicit exemptions are policy records with reason and scope.

An immutable acceptance contract pins task goal/generation, required criteria and checks, source/integration test scope, environment requirements, role capabilities and authorized policy exceptions. Its version/digest and the policy digest are captured before submission. Neither development nor acceptance can authorize its own weaker contract. A separately authorized change produces a new generation and invalidates affected evidence.

Evidence key: repository identity + task generation + contract digest + exact code SHA + integration/base SHA tuple + policy/profile digests + relevant environment identity + trusted check identity. Each record includes producer role/session, adapter version, timestamps, raw provider status/conclusion, normalized result, artifact digests and provenance. CI records additionally include workflow identity/path/digest, check suite/run IDs, workflow run ID and `run_attempt`, job IDs and their attempt association. Missing provenance is `unknown`, not success. Dependency/environment changes can invalidate reuse even when source is unchanged.

### GitHub Actions and exact-revision aggregation

GitHub Actions is a first-class asynchronous test executor. Reuse [`.github/workflows/ci.yml`](../.github/workflows/ci.yml), currently triggered by PRs to `main`: its `Test` job builds the web bundle, runs `go test -v -race ./...`, then builds and smoke-checks the CLI including operator validation. Do not duplicate these as a newly invented test pipeline. Any later workflow trigger changes require a reviewed change; the current file is not assumed to support manual dispatch or target-branch verification.

The adapter observes the workflow triggered by the submitted PR update or requests only a verified supported trigger. It persists dispatch intent, discovers and binds the resulting run, then transitions to `waiting_external` and releases the agent worker slot and session allocation. CI runner usage remains accounted separately. Webhooks plus bounded polling reconcile progress without keeping an agent alive. Restart reattaches to run IDs; ambiguous dispatch blocks instead of creating duplicate CI runs. Acceptance resumes in a separate role session when agent analysis is needed.

Aggregation is a deterministic join, not a branch-name or display-name search:

1. Read the current PR repository identity, head SHA and base SHA; select the pinned contract and trusted workflow identity/digest from authorized configuration. A changed workflow cannot establish its own trust merely by passing.
2. Resolve the actual tested revision. A PR test merge commit may differ from its head SHA. Record `(repository, PR, head SHA, base SHA, test merge SHA)` with provider/Git provenance proving that candidate corresponds to that tuple. Never assume a workflow run's `head_sha` equals checkout/test merge SHA; retain explicit checkout/candidate evidence. If the tuple cannot be proven, block integration acceptance.
3. Paginate check runs, required status contexts and workflow runs/jobs as applicable. Match exact revision, trusted app/workflow identity and required selectors, and retain all rerun attempts. Bind jobs to the selected run attempt; never combine convenient successes across attempts or SHAs. Reject ambiguous duplicate identities. A later non-success attempt supersedes earlier success under the declared rerun policy.
4. Require every expected check/job and required test step/report to complete successfully. Missing, queued, pending, skipped, neutral, stale, cancelled, unreadable or timed-out evidence cannot pass; aggregate workflow success alone cannot conceal skipped required work. The profile must obtain step/report evidence if job success is insufficient.
5. Re-read head/base and policy before committing eligibility. Changes invalidate affected evidence and require a new candidate. Gate evaluation is a pure function of the pinned contract and validated evidence, persisted atomically with its input digest.

The GitHub [check runs API](https://docs.github.com/en/rest/checks/runs) supplies revision-specific checks and distinct conclusions. The [workflow runs API](https://docs.github.com/en/rest/actions/workflow-runs) supplies run/attempt observations and rerun operations. These are provider primitives, not a guarantee that names or a successful run prove this design's gate. Validate job/checkout provenance and adapter permissions during implementation; do not broaden credentials to work around missing evidence.

Failure routing:

| Classification | Next action |
|---|---|
| Product-code failure | Repair step, bounded attempts; new revision reruns required checks |
| Test/configuration defect | Propose focused fix; preserve gate policy and validate the change |
| Runner/network/service failure | Backoff and retry the affected operation within infrastructure budget |
| Flaky failure | Apply declared rerun policy; retain every result; quarantine cannot remove a required gate without authorization |
| Missing credentials, unsupported capability, no evidence | Block with actionable reason |
| Exhausted budget or unchanged repeated failure | Block and retain diagnosis and artifacts |

Log text is untrusted task data. Review and repair agents receive bounded, redacted excerpts with links to full retained artifacts. Test status comes from execution/report adapters, not instructions embedded in logs.

## Review, merge, and completion

Review is an independent acceptance attempt bound to the immutable contract, current diff, exact code/integration/base SHAs and policy snapshot. It can request changes, provide evidence, or block; it cannot edit the submitted source or approve its own weaker policy. Required provider approvals remain authoritative and cannot be replaced with an agent-written approval comment.

Merge eligibility requires current head/integration revision evidence, required review, successful required tests, resolved conflicts, permitted action, and satisfied provider protections. Eligibility does not grant automatic merge authorization: require the existing explicit project/action policy separately. Use provider-supported conditional merge or an equivalent merge queue. Recheck the target base; if it changes, recompute the integration candidate and rerun required integration checks. If the provider cannot enforce the checked candidate, do not enable unattended merge on that capability path.

After merging, record the actual resulting target SHA and run or observe target-branch checks. Squash/rebase may change the SHA; pre-merge evidence is not automatically target-branch evidence. A failing verification creates a recovery task and marks the original workflow accordingly. Revert is a distinct authorized effect, not an unconditional error handler.

## Migration and framework validation

Replace runner internals incrementally under the single autonomous lifecycle. Map non-retired custom trigger/outcome definitions into a compatibility adapter and preserve their constraints and history. New packages require strict declared results; permissive old outcome labels must not bypass new gates. Keep one execution authority per issue.

Rollout follows the [main design's linked backlog](managed-development-design.md#scope-and-implementation-order): (1) exact-SHA GitHub aggregation, independent evidence contract and existing CI reuse; (2) durable transitions, fencing/reconciliation and coordination/implementation of #337's dependency-gate backlog; (3) versioned component adapters and management; (4) distributed backend and capacity validation. Reconcile source/binary/Offload versions before live self-hosting. Version persisted schemas, adapter protocols and policy contracts; pin each run to a compatible engine version. Roll out bounded project cohorts with dispatch pause, backup and migration validation. Unsupported versions block rather than silently reinterpret records. This is versioned evolution, not an alternate legacy workflow selector.

Migrate the autonomous implementation path end to end before extending package coverage. Keep historical records readable and reject retired override names. Rollback pauses dispatch and reconciles in-flight effects before restoring a compatible runner revision; it does not restore a selectable legacy pipeline. Do not delete the ledger or replay pending writes under another scheduler.

Framework acceptance checks should include:

- Duplicate event and poll replay produce one logical task generation.
- Restart before/after PR creation or comment write reconciles the same effect.
- Expired worker results and cancelled attempts cannot merge or advance work.
- Missing, stale, skipped, or wrong-SHA test evidence blocks completion.
- CI infrastructure failure and code failure take different bounded paths.
- Changed base, changed head, and changed test profile invalidate the appropriate evidence.
- Existing required checks cannot be removed by a task's proposed test plan.
- Acceptance cannot write submitted source, reuse the development session or authorize policy relaxation, even through tool calls or injected log instructions.
- Acceptance plan provenance proves it was recorded before that session could read development conclusions; premature disclosure invalidates independence and requires a fresh acceptance session.
- PR head/base/test-merge tuple mismatch, untrusted workflow digest, mixed attempts and skipped required steps block acceptance.
- CI waiting consumes no agent worker slot; restart resumes known runs without duplicate dispatch.
- Duplicate/out-of-order inbox events, lost write responses, outbox replay, lease expiry and scheduler partition preserve one authorized transition/effect.
- Dependency changes prevent premature dispatch; fair quotas prevent a hot project/provider from starving others.
- Unknown or incompatible components stay disabled; revocation and capability reduction stop unauthorized effects.
- Both provider adapters return explicit unsupported-capability failures.
- The full repair-and-merge scenario succeeds in a disposable repository without manual step advancement.

Use unit tests for transition and policy evaluation, adapter contract tests for provider normalization, and crash-injection integration tests for the ledger/effect boundary. Real-provider smoke tests require designated test repositories and isolated credentials. Validate the product's own test suite separately from the user-project tests it orchestrates.

## Scale validation and release limits

Run successive 100, 1,000 and 10,000 concurrent-worker scenarios across 100 projects against the production distributed backend. Include balanced and hot-project workloads, mixed CPU/memory/provider quotas, long CI waits, bursty completions, dependency fan-out and recovery storms. Record active execution workers separately from queued tasks and waiting CI runs; 10,000 waiting records do not demonstrate 10,000 concurrent workers.

Synthetic adapters measure control-plane scheduling, durable transitions and evidence/effect throughput without claiming real model or Actions capacity. Separately run authorized provider trials within actual account/rate/runner limits; report achieved concurrency, provider throttling and capacity shortfalls. Never extrapolate synthetic capacity into proven end-to-end throughput.

At every tier measure dispatch latency p50/p95/p99, queue age and starvation by project, transaction/lease contention, heartbeat lag, fencing rejections, event/effect backlog, reconciliation time, CPU/memory/storage, provider errors/rate limits and cost. Inject scheduler/worker crashes, network partitions, database failover, duplicate delivery and stale credentials. Require zero stale-worker authorized writes, duplicate logical task generations or false acceptance; recovery must preserve budgets and provenance. Set latency/recovery SLOs and resource budgets before the run and publish results against them.

Supported production limits increase only after repeatable results and recovery tests on a documented topology. No load measurements, provider capacity, elapsed benchmark time or cost figures are established by this documentation change. Distributed rollout remains a planned phase rather than indefinitely deferred or already supported capacity.
