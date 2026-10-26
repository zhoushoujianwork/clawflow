# Fully managed Git development: implementation notes

Status: Proposed · Date: 2026-10-26

[Main design and scope](managed-development-design.md) is authoritative. These contracts illustrate the implementation direction and may be refined during development. No proposed schema or command here is implemented by this document.

## Current autonomous dispatch policy

Repository `workflow` defaults to `autonomous`; explicit `legacy` preserves the earlier classify/evaluate/approval pipeline. Autonomous dispatch sends eligible open leaf issues directly to implementation, where analysis, planning, headless tests and repair occur internally. It removes the built-in `ready-for-agent` prerequisite without discarding custom trigger constraints, terminal exclusions, machine binding or execution locks. The same routing applies during scanning and the fresh-label recheck. Questions retain their reply path, and parents retain tracking behavior.

`repo set --workflow autonomous --auto-merge on --ci-required on` selects unattended delivery. Merge authorization remains separate from dispatch; required CI accepts only success, never missing/pending/failed/unreadable results. These changes use the existing runner and merge sweep. The first post-PR repair slice is restricted to same-repository GitHub managed branches linked to an open implemented issue. Failed CI queues repair against a captured PR head; pending, missing or unreadable CI does not. Rechecking identity and failure under the issue lock, strictly fetching the expected head, and a durable three-attempt budget with a five-minute cooldown prevent stale or unbounded repair. The agent reproduces tests locally, commits and pushes to the same branch without force. This slice does not ingest CI logs or implement the full durable workflow contracts below.

## Existing code and migration map

Source paths below refer to base commit `dff005a`; line numbers are inspection landmarks, not stable APIs.

| Existing location | Verified role | Proposed evolution |
|---|---|---|
| `internal/operator/operator.go:17` | Operator schema; `lock_label` is retained only for compatibility | Keep a legacy parser; add versioned managed contracts |
| `internal/operator/registry.go:12` | Embedded loading and user overrides by name | Add package resource resolution, validation and digest pinning |
| `internal/operator/context.go:29` | Agent context assembly | Supply pinned skill resources and explicit step inputs |
| `internal/operator/claude.go:274` | Agent execution and failover | Reuse behind an execution adapter |
| `internal/operator/runner.go:426` | Idempotent comment write; comment failure does not prevent outcome-label progress | Persist effect intentions and individual effect completion |
| `cmd/clawflow/commands/run.go:802` | PR operator scan deferred; auto-merge sweep present | Route normalized issue/PR events into the managed scheduler |
| `cmd/clawflow/commands/run.go:1612` | Automated outcome follow-up including approval/merge | Move managed transitions into the workflow engine |
| `cmd/clawflow/commands/run.go:1711` | Workspace selection and implementation worktrees | Reuse Git handling under durable task/attempt ownership |
| `internal/snapshot/snapshot.go:1425` | Stale lock/run reconciliation | Preserve history; project managed ledger state into existing views |
| `internal/pilot/schedule.go` | Project-level scheduling | Retain backlog intake; hand eligible work to one execution authority |
| `internal/config/config.go:35`, `cmd/clawflow/commands/lang.go:70` | `TestCommand` setting and language-detection use | Do not mistake this setting for an enforced test gate |

Suggested new internal boundaries are `workflow` (graph and scheduler), `skill` (managed packages), `execution` (adapter contracts), `testing` (normalized evidence), and `store` (ledger/effect intents). These are implementation boundaries, not separately deployed services. Keep `internal/vcs` as the provider boundary and `internal/api`/`web` as views and controls over the same engine. Extract incrementally from the command orchestration instead of rewriting it wholesale.

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
    mode: through-merge
    max_repair_attempts: 3
```

The resolved snapshot pins immutable digests even when a human-readable version selector is used. Workflow graph definitions live in the bound workflow package, not duplicated in this binding file.

## Runtime model

| Record | Key information |
|---|---|
| Project binding | Provider/repository identity, package pins, environment bindings, autonomy policy |
| Task | Source issue/event, goal revision, acceptance criteria, dependencies, selected endpoint |
| Workflow run | Task generation, compiled graph, configuration snapshot, aggregate status |
| Step attempt | Step, attempt number, input digest, lease epoch, workspace, executor session, timestamps, result |
| Evidence | Code/integration SHA, configuration digest, environment identity, check identity, status, artifacts |
| Effect intent | Desired external write, idempotency key, preconditions, external identity, reconciliation status |
| Event | Provider delivery identity or synthetic deduplication key, source revision, timestamp, normalized payload |

Use a transactional local store, preferably SQLite, for the first managed runtime. Persist transitions and pending effect intents in one transaction. Retain existing human-readable logs as views or artifacts rather than maintaining competing scheduling authorities. Local disk durability and backup remain deployment responsibilities; copying a database does not create a safe second scheduler.

Suggested step states: `pending`, `ready`, `running`, `waiting_external`, `succeeded`, `failed`, `blocked`, `cancelled`. An attempt is immutable after termination. A retry creates another attempt and preserves prior evidence. Run status is derived from required steps and the selected endpoint; it is not inferred from a label.

## Event ingestion and scheduling

Normalize polling observations and, later, webhook deliveries into one inbox. Polling remains a reconciliation mechanism even if webhooks are enabled. Keep durable per-source cursors, paginate reads, and apply backoff and rate-limit budgets. Do not infer a successful remote write from a successful HTTP request without checking the provider's documented result semantics.

Duplicate delivery maps to the existing task generation. A changed issue goal or source revision can request a new generation; it must not silently mutate a running attempt's input. Merge/close/cancel observations reconcile the task rather than creating conflicting work.

The scheduler chooses ready steps subject to dependencies, project fairness, concurrency limits, and usage limits. A durable lease with heartbeat and fencing epoch prevents an expired worker from committing results. Labels such as `agent-running` can mirror activity but are not locks. The first release supports one active scheduler per store; worker processes must lose write capability when their attempt is fenced or cancelled.

Cancellation stops new dispatch, terminates the active execution where supported, revokes effect authorization, and then reconciles partial effects. Resume re-evaluates preconditions; it does not blindly rerun the last command.

## Workspace and execution boundary

Allocate an isolated worktree/branch for each task, with bounded per-attempt modifications and a recorded base SHA. Worktrees isolate Git edits, not arbitrary executable code: projects that execute untrusted code need a container or equivalent restricted executor with scoped credentials and network access. Command adapters use structured executable/argument/environment inputs.

An execution request contains the pinned skill, inputs, workspace, deadline, resource budget, capabilities, and artifact locations. Agent sessions can be attached to an attempt for diagnosis or continuation, but a resumed conversation cannot bypass the current lease or input revision.

The structured result contains status, outputs, artifact references, evidence references, requested effects, and a failure category. A zero process exit code is necessary for some adapters but never sufficient to certify task completion. Invalid or absent output becomes a protocol failure.

Agents may edit their workspace and request effects. Pushes, PR creation, labels/comments, merge, and task closure go through the effect gateway. Existing skills that directly write VCS state need an explicit legacy execution mode during migration; managed mode must not claim the stronger guarantee until those paths are removed or constrained.

## External effects and crash recovery

External systems and the local store cannot share a transaction. Use durable intent plus reconciliation, with at-least-once delivery and idempotent effects where possible; do not promise exactly-once execution.

- Before dispatch: persist the effect intent with task/run/step identity, precondition SHA, and deterministic operation key.
- Before a write: check the lease and verify source state and configured permissions.
- After a write: persist the provider object ID/result; if the response is lost, read and reconcile using deterministic branch names, PR source branches, or comment markers.
- When uniqueness cannot be established: stop the effect as ambiguous and investigate; never blindly recreate it.
- After restart: reconcile outstanding effects and external CI jobs, then reclaim expired attempts. Reattach to a known CI run instead of dispatching another one.

Record partial effects even when the step ultimately fails. A stale agent result is retained for diagnosis but cannot advance the run. Every effect gateway operation must recheck fencing; advisory prompting is insufficient.

## Test configuration workflow

Use a separate configuration run so incomplete onboarding cannot be treated as a ready development project:

`discovered → proposed → preflight → validated → activated`, with explicit `blocked` and `rejected` outcomes.

Discovery inspects repository manifests, existing CI definitions, test commands, runner requirements, services, and reports. It returns findings with confidence and missing inputs. Setup proposes a test package and any necessary CI change as artifacts/PR changes. It preserves existing required checks by default and requests missing secret bindings by name, never by embedding their values.

Preflight verifies commands, working directories, tool versions, dependency access, secret references, runner availability, report parsing, and at least one baseline execution. A pre-existing failing baseline is recorded as a blocker or an explicit policy exception, not silently accepted. Activation binds the validated package digest to the project and records who or what policy authorized it.

Configuration changes do not loosen an active run's requirements. A configuration PR runs the current required checks and validates the candidate profile. A code task changing tests or CI cannot approve its own lower gate; policy changes need the configured separate authorization path. Restarting a run against a new profile produces a new auditable generation.

## Test execution and evidence

A test package describes check identities, required/optional status, commands or CI job selectors, environment requirements, timeouts, report parsers, artifact collection, and any supported scope selection. Secrets are environment bindings outside the package. Begin with generic command execution and existing provider CI adapters rather than one framework implementation per language.

Normalize results to `passed`, `failed`, `pending`, `cancelled`, `infra_error`, or `unknown`. Store skip counts and collected test counts; required checks with no usable evidence cannot become `passed` merely because a process exited zero. Explicit exemptions are policy records with reason and scope.

Evidence key: repository + code or integration SHA + test-profile digest + relevant environment identity + check identity. CI evidence also records the provider run/job ID and attempt. Results from another SHA, cancelled runs, stale reports, or similarly named unrelated jobs cannot satisfy a gate. Dependency/environment changes can invalidate reuse even when the source SHA is unchanged.

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

Review is a separate attempt bound to the current diff and policy snapshot. It can request changes, provide evidence, or block. Required provider approvals remain authoritative and cannot be replaced with an agent-written approval comment.

Merge eligibility requires current head/integration revision evidence, required review, successful required tests, resolved conflicts, permitted action, and satisfied provider protections. Use provider-supported conditional merge or an equivalent merge queue. Recheck the target base; if it changes, recompute the integration candidate and rerun required integration checks. If the provider cannot enforce the checked candidate, do not enable unattended merge on that capability path.

After merging, record the actual resulting target SHA and run or observe target-branch checks. Squash/rebase may change the SHA; pre-merge evidence is not automatically target-branch evidence. A failing verification creates a recovery task and marks the original workflow accordingly. Revert is a distinct authorized effect, not an unconditional error handler.

## Migration and framework validation

Introduce managed mode alongside the existing label-driven path. Map old trigger/outcome definitions into a compatibility adapter and preserve their documented behavior. New managed packages require strict declared results; permissive legacy outcome labels must not bypass new gates. Prevent both schedulers from owning the same issue concurrently.

Migrate one built-in flow end to end before converting every skill. Keep historical records readable. Rollback stops managed dispatch and reconciles in-flight effects before returning a repository to legacy ownership; do not delete the ledger or replay pending writes under another scheduler.

Framework acceptance checks should include:

- Duplicate event and poll replay produce one logical task generation.
- Restart before/after PR creation or comment write reconciles the same effect.
- Expired worker results and cancelled attempts cannot merge or advance work.
- Missing, stale, skipped, or wrong-SHA test evidence blocks completion.
- CI infrastructure failure and code failure take different bounded paths.
- Changed base, changed head, and changed test profile invalidate the appropriate evidence.
- Existing required checks cannot be removed by a task's proposed test plan.
- Both provider adapters return explicit unsupported-capability failures.
- The full repair-and-merge scenario succeeds in a disposable repository without manual step advancement.

Use unit tests for transition and policy evaluation, adapter contract tests for provider normalization, and crash-injection integration tests for the ledger/effect boundary. Real-provider smoke tests require designated test repositories and isolated credentials. Validate the product's own test suite separately from the user-project tests it orchestrates.
