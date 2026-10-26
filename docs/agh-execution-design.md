# AGH execution

ClawFlow now sends every AI request through `agent-host execute`. Operators, Pilot, document generation, provider probes and terminal conversations use one structured client. There is no direct native execution backend, automatic provider replay or fallback.

Configure one dedicated Agent-Host execution runtime before running AI work:

```yaml
settings:
  execution:
    command: agent-host
    config_path: /absolute/path/execution.acp.json
    native_command: /absolute/path/native-executable
    native_profile: /absolute/path/native-profile
```

The config must describe an execution-capable ACP runtime. The native profile owns native authentication unless an explicit ClawFlow provider supplies auth environment overrides. ClawFlow does not discover another Host home or read its credentials. Existing provider entries select model/auth and support explicit connectivity probes; the first enabled entry is used once.

For isolated invocations, override these fields with `CLAWFLOW_AGH_COMMAND`, `CLAWFLOW_AGH_CONFIG`, `CLAWFLOW_AGH_NATIVE`, and `CLAWFLOW_AGH_PROFILE`. Paths must be absolute and available. These machine-local settings are excluded from config sync.

`clawflow chat`, `clawflow project chat`, and `clawflow feedback` provide a plain text loop: one message per line, `/exit` or EOF to leave. Later turns resume the same logical session. Ctrl-C cancels the owned execution tree. Native terminal UI and image paste are not reproduced.

The AGH CLI keeps native auth and ACP policy behind a separate process boundary. ACP currently supports Claude only, does not report usage, and rejects budget caps before native execution. ClawFlow forwards requested budgets and tool restrictions unchanged; unsupported policies must fail rather than weaken the request. Budgeted Pilot runs therefore require AGH budget support or an explicitly unbudgeted configuration.

Issue/worktree locks, CI repair bounds and VCS writeback remain owned by ClawFlow. This implementation has headless fake-AGH validation; it does not claim a live runtime switch or multi-engine acceptance.

See [protocol and implementation details](agh-execution-design-detail.md).
See the [acceptance record](agh-execution-validation-2026-10-26.md) for checks and rollout limits.
The [Offload live acceptance](agh-offload-live-acceptance-2026-10-26.md) records a real read-only ACP run and the active-issue lock behavior.
