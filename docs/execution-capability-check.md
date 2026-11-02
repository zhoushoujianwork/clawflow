# Execution capability check

Use an explicit local check to confirm that the configured Offload executable advertises the flags ClawFlow needs:

```bash
clawflow execution check --json
clawflow execution check --role acceptance --json
```

The check resolves the configured command and role profile, runs help in an empty temporary directory, and limits it to three seconds and 128 KiB of output. It accepts recognizable help with exit status 0 or the standard Go help status 2. It sends no task or profile argument to the child process. The existing `project check` retains its local configuration, file and Git inspections and does not launch this Offload probe.

JSON uses schema version 1 and `ready` or `blocked`. Here, `ready` means only that recognizable help declares the required protocol flags. The report leaves provider execution and permission enforcement explicitly unverified. Help does not establish credentials, provider connectivity, backend sandbox behavior or real task success.

The parser expects Go-style flag declarations with descriptions. ANSI-colored or binary help fails closed. Unusual diagnostic formatting can require manual inspection; this is an interface probe rather than a general log parser.

If blocked, inspect the reported check IDs and configure a compatible execution command. The generic `offload` on PATH and a separately configured pinned binary may have different capabilities. Use the resolved command in the report when investigating; changing PATH alone may not change ClawFlow's configured executor.

The check reads canonical configuration without migrating legacy `repos.yaml` or renaming configuration files. Legacy-only setup gets an actionable blocked result unless an explicit environment profile supplies the required settings.

The check is explicit and does not activate project automation. Independent acceptance and merge policies are described in the [acceptance guide](independent-acceptance.md).

## Readiness status

`clawflow execution status [--json] [--recheck] [--role R] [--shell]` is the shared, read-only readiness result for the Offload dependency. The CLI and background services own it; the native app only decodes `--json` output and never probes Offload itself.

- **Where it checks.** Each installed worker LaunchAgent is inspected with that service's own `EnvironmentVariables` (its `PATH`, `CLAWFLOW_OFFLOAD_COMMAND`, `CLAWFLOW_OFFLOAD_PROFILE`) and its `--roles`, not the interactive shell. A service with no `PATH` gets launchd's default (`/usr/bin:/bin:/usr/sbin:/sbin`). An embedded local worker uses the controller service's environment. `--shell` adds the current shell for setup scripts.
- **Machines without an executor.** A controller-only or viewer-only machine reports `not_required`. A blocked result names the worker that lacks Offload.
- **Reasons.** `missing_executable`, `not_executable`, `probe_timeout`, `probe_failed`, `incompatible_capabilities`, `profile_not_selected`, `profile_not_found`, `config_invalid`, `config_unavailable`. A result of `ready` always lists `live_authentication` and `provider_execution` as unverified.
- **Profiles.** Existence is verified only through Offload's own `offload profiles --json` (plus the plain listing for aliases), which never prints or runs credentials. If the installed Offload does not declare that command, the profile stays `profile_existence` unverified instead of being assumed. Each role the worker serves is checked with the same role/environment precedence as execution.
- **Installation guidance.** Taken from the Offload repository README (`go install github.com/zhoushoujianwork/offload@latest`, Go 1.24+), with the configured path and the recheck command. Installation is always an explicit user action; ClawFlow never installs, upgrades, or edits profiles or credentials.
- **Caching.** The result is shared through `~/.clawflow/run/execution-status.json` (mode 0600): 60 seconds when ready, 15 seconds when blocked, so polling does not spawn probes and a fix is noticed quickly. `--recheck` forces a probe. Workers use one in-process cache with the same lifetimes.
- **Claims.** A blocked worker keeps registering but does not claim. If the dependency disappears after a claim, the worker calls `tasks/<id>/release`: the task returns to `pending` with the attempt row removed, no result, no issue label and no `agent-failed`. Local `clawflow run` stops before any CI/acceptance repair reservation and does not feed the circuit breaker. A reservation released as `offload-unavailable` counts toward neither the three-attempt repair budget nor the infrastructure budget. Healthy workers continue to claim and active leases are untouched.
- **Notifications.** The native app raises at most one notification per distinct blocked condition and clears it on recovery. Worker logs likewise warn once per change.
