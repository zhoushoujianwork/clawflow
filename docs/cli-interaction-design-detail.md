# CLI interaction contracts

[Main design and scope](cli-interaction-design.md) is authoritative. Proposed interfaces below are not promises of current support.

## Output and exit behavior

For the first `project check` command, JSON has `schema_version`, `project`, `status`, `checks`, and an explicit list of unverified capabilities. Each check has a stable `id`, a repository or project scope, a status (`passed`, `warning`, `blocked`), a human-readable summary, and an optional next action. Check ordering is deterministic.

Overall `status` is `blocked` when any prerequisite check is blocked; otherwise it is `ready`. Warnings remain visible without changing the exit result. Ready means ready for the locally checked prerequisites only, not authorized or proven ready for an unattended merge.

Return exit 0 for a completed check without blockers; return nonzero with the same structured result for detected blockers. For malformed invocation or unrecoverable input/config errors, return nonzero and avoid a misleading ready result. Exact typed exit-code expansion should be centralized when task commands are introduced; do not impose new exit semantics on all existing commands in this slice.

JSON stdout contains exactly one JSON value. Do not include credentials, raw credential-bearing remote URLs, arbitrary logs, or provider configuration secrets. Human-readable summaries can change; automation branches on stable IDs and statuses.

## Local readiness checks

Inspect without mutation or remote access:

- Project exists; its registered repository references resolve and its primary binding is consistent.
- Each local path exists, is a Git worktree, and its origin matches configured provider, host and full namespace.
- The configured base is locally resolvable. A locally present tracking ref is not proof it is up to date; no implicit fetch is allowed.
- Checkout changes are reported as a warning with no file contents exposed. They must not be reset by validation.
- Context and testing instructions are present/nonempty, or their absence is reported with a concrete setup action.
- Required local executables such as Git and Claude are discoverable. Presence does not prove authentication or provider health.
- Embedded/user operator definitions parse and diagnostics identify actual duplicate trigger definitions without false positives from omitted trigger fields.
- Repository enablement, auto-merge, required CI, Pilot and sync state are visible. Disabled automation is expected during onboarding and is not an instruction to enable it automatically.

No `config.Load` legacy migration, metadata save, snapshot refresh, lock cleanup, label initialization, provider probe, Git fetch, project generation or test command is permitted in the inspection path. Missing configuration is a reported prerequisite, not an invitation to initialize it. Inspect authoritative config files without side effects.

Project documents are untrusted project data, not permission grants. Avoid feeding their contents into readiness output. Subsequent document setters should validate known document kinds and accept a file or stdin, write atomically, and report the resulting revision; they should not accept arbitrary paths under the managed home.

## Future execution planning

A proposed plan identifies project, source issue, input revision, workflow and skill digests, selected steps, anticipated remote actions, resource bounds and required gates. An execute request references that immutable plan. Stale inputs require replanning or a structured stale-plan response, rather than silently executing a different plan.

Task start returns a durable task/run ID before lengthy execution. Show returns aggregate state plus the active attempt and its revision; logs provide bounded pagination or explicit streaming. Wait has a client deadline and reports pending without cancelling the task. Cancel records an intent, stops dispatch and reconciles partial effects. Retry creates an attempt linked to its predecessor and obeys the workflow retry budget.

These commands should use the same services as the Web API, not make a running Web server a prerequisite for CLI automation. Existing snapshots can support read-only inspection initially, but cannot certify durable task ownership or exactly-once remote writes.

## Tests and live evidence

Contract tests should parse JSON for both ready and blocked results, assert stable check IDs, verify stdout purity and exit behavior, and assert that project/config/credential fixtures remain byte-for-byte unchanged. Use fake executables or temporary Git repositories to check absent tooling, mismatched origin, missing base, dirty worktree and invalid operator definitions. Never make unit tests depend on the user's configured repositories or real provider credentials.

Operator diagnostic regression cases should cover active built-ins and non-retired custom definitions, reordered equivalent label sets, and trigger differences in OR labels, exclusions and parent/leaf scope. Retired exact names must be rejected in user overrides without discarding other custom operators or history.

For live offload validation, record observed state and the command/build revision. A successful local readiness check does not establish remote authentication, test success, agent execution, PR creation or merge. Report each of those separately when exercised.

## Autonomous repository controls

There is one autonomous lifecycle, with no workflow mode selector. Configure unattended delivery using supported commands:

```bash
clawflow repo set <R> --auto-merge on --ci-required on
clawflow repo enable <R>
clawflow run --repo <R>
```

Merge remains separately configured; required CI accepts success only. Old config input remains readable. Explicit legacy selection emits a migration notice and proceeds autonomously. Config rewrites omit `workflow`, `auto_approve`, `auto_evaluate_all_issues` and `confidence_threshold`, retaining enabled state, bindings, merge/CI and other supported settings.

The exact built-ins `classify`, `evaluate-bug`, `evaluate-feat` and `decompose` are retired, including same-name user overrides. Other custom operators and historical records remain; the eval model role is only for custom `evaluate-*` extensions.

Regression coverage should include compatibility input and rewrite/sync omission, migration notices, direct issue dispatch, custom constraints, terminal/parent/question exclusions, fresh-label rechecks, pause/cancel/locks, bounded retries and missing-CI rejection. Same-repository GitHub CI repair reproduces failures locally, updates the existing branch without force, and permits at most three attempts with a five-minute cooldown. CI-log ingestion, forks and GitLab repair are outside this slice; other GitLab capabilities remain. These runner controls do not implement the proposed general durable task API above.
