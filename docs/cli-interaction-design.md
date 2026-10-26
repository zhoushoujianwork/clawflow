# CLI interaction for managed development

Status: Proposed, with incremental implementation · 2026-10-26

This extends the [managed development design](managed-development-design.md). The CLI is the primary control surface for both users and external AI skills; the Web interface should call the same application services. [Contract details](cli-interaction-design-detail.md) define output and validation boundaries.

## User journey

The normal path is **inspect → configure → check → preview → execute → observe → recover**. An external AI should complete it using documented commands and structured results, without editing internal JSON/YAML state or scraping dashboard pages.

These are capabilities, not mandatory human checkpoints. Under the unattended policy, project setup authorizes the delivery endpoint once; each eligible issue proceeds directly to implementation and test/repair. Preview remains an optional inspection tool, and execution plans can be produced and consumed internally. No separate evaluation score or manual approval label is required for each issue.

Skills teach the workflow and project-specific choices. The CLI owns validation, persistence, execution identifiers and side effects. A skill must not claim a command exists before it is implemented.

## Interaction rules

- Terminal output explains the result and next useful action. `--json` returns one versioned JSON document on stdout; diagnostics go to stderr. No prompts, colors, progress bars or explanatory prose belong in JSON output.
- Inspection commands are read-only by default. Network checks are explicit. Checking prerequisites never starts an agent, initializes labels, enables automation or runs project tests.
- Writes are explicit verbs. Commands that only launch work return an identifier and an accepted/running state, never an unearned success result.
- Expected states such as a disabled repository, pending CI or a missing test profile have stable machine-readable identifiers. An AI should not have to parse English error text to choose the next action.
- Long operations support bounded waits and eventual status retrieval. Disconnecting the client and cancelling the task are distinct actions.
- Automation settings are visible and independently controlled: monitoring, issue approval, merge, project Pilot, and config sync. One toggle must not silently authorize the others.

## Command surface and delivery order

| Step | Interface | Delivery status/scope |
|---|---|---|
| Inspect | `repo show`, `project show`, `sync status`, operator diagnostics | Existing; consistent JSON coverage is incremental |
| Register | `repo add --disabled`, project membership and primary-project commands | Implemented lifecycle foundation |
| Bind execution host | `repo set --bound-machine`, `project automation bind` | Explicit host selection; configuring a binding does not enable automation |
| Check | `project check <name> --json` | Implemented and checked against offload; local readiness only |
| Configure project knowledge | Proposed `project document get/set` with file/stdin input | Next slice; replaces the current interactive-only gap |
| Configure tests | Proposed skill binding and test-profile validate/activate commands | Requires the test-profile contracts in the managed development design |
| Preview execution | Proposed `task plan` returning selected steps, revision and effects | Requires a side-effect-free planner; current `run` is not a preview |
| Execute | Existing bounded `run --repo --issue`; proposed `task start --plan` | Existing runner retains documented side effects; future start binds to a plan |
| Observe/recover | Proposed `task list/show/logs/wait/cancel/retry` | Requires durable task IDs and application-service extraction |
| Reset | `reset` preview and `reset --yes --json` | Implemented archive-based offline maintenance |

Command names marked proposed are design vocabulary, not usable commands. Preserve existing `run` behavior during migration rather than presenting it as a durable task API.

## First implementation and validation

Implement local project readiness inspection and correct operator diagnostics first. The readiness result should expose repository bindings, checkout identity/base availability, worktree cleanliness, project instructions, executables, operator validity, sync state and automation controls. It must distinguish blocking failures from warnings and explicitly state that remote authentication, provider execution and project tests were not verified.

Validate against the registered `offload` project and temporary fixture repositories. Its current checkout has unrelated work in progress, so no automated checkout reset, branch switch, commit or repair may touch those changes. Use a separate revision-pinned workspace for subsequent code-test verification.

The first acceptance condition is that a fresh external AI can invoke the skill, discover the commands, read readiness JSON, and identify the next action without an agent invocation or remote write. An issue-to-PR smoke run follows only after project context, test instructions and execution scope are ready. A passing local check is not proof that this remote workflow has passed.

[Validation evidence](cli-validation-2026-10-26.md) records the implemented CLI checks, isolated offload test baseline, and remaining acceptance work.

## Subsequent priorities

1. Complete noninteractive project-document and skill-binding configuration.
2. Add execution planning with a revision/configuration fingerprint and explicit effects.
3. Expose durable run/task status, logs, waiting, cancellation and bounded retry through the CLI.
4. Run a designated offload issue through test, repair and review; add merge only when the corresponding policy and evidence gate are available.

Preserve compatibility with existing terminal output. Introduce structured contracts command by command and update the external skill with each delivery. Do not add a generic `--json` flag that merely wraps mixed human output.
