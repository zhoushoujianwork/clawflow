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
