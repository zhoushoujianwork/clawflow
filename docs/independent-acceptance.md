# Independent acceptance

ClawFlow can require an independent review before merging a managed GitHub PR. Development and acceptance use separate native sessions. Acceptance first records a plan from the requirements without source access, then reviews a detached checkout of the submitted commit with read-only permissions.

This is a local reference implementation for [platform issue #349](https://github.com/zhoushoujianwork/clawflow/issues/349). The broader [platform design](managed-development-design.md) describes the distributed workflow target. [Technical details](independent-acceptance-detail.md) describe evidence and limits.

## Usage

After building this revision, enroll a registered repository:

```bash
clawflow repo set owner/repo --acceptance-required on
clawflow acceptance run --repo owner/repo --pr 123 --timeout 30m
clawflow acceptance status --repo owner/repo --pr 123 --json
```

Enrollment preserves monitoring and automatic-merge settings. The scan sweep never launches a reviewer; it only reports missing evidence. `clawflow acceptance run` stays available for explicit runs.

## Automatic harvest

`clawflow run` has an acceptance phase after the operator phase. It needs `auto_merge` on and `acceptance_required` on, and it skips single-issue runs (`--issue`).

- Per repo and pass, at most one acceptance runs, for the first open PR that passes the sweep's filters, has successful required CI and has no evidence for its current contract.
- A passing result goes straight to the conditional merge path with the accepted head SHA.
- A failed or blocked verdict posts one comment on the issue with the status and reviewer summary. Later passes find the recorded verdict and stay silent.
- Setup or provider errors post nothing, are logged, and make `clawflow run` exit non-zero. They are retried after 15 minutes, up to 3 attempts per head. After that one comment points to `clawflow acceptance run`.
- There is no repair loop yet. A non-pass PR waits for a new head or a human.

The PR must be open, non-draft, from the same repository, and reference an open issue with a closing keyword. A successful managed development run must have captured that issue's requirements and trustworthy native execution events. Historical runs missing the requirements snapshot need a new managed development run. The configured acceptance profile must support the existing Offload execution protocol.

The latest completed acceptance attempt must pass for the current requirements, developer identity, head, base and policy. New submissions or changed requirements/configuration need fresh acceptance. A newer failed or cancelled attempt supersedes an older pass. Both automatic merge and explicit `clawflow pr merge` enforce the policy for enrolled repositories, along with required CI and provider protections.

## Scope

This gate reviews the submitted head and rechecks the base. It does not establish tested integration-commit provenance, an atomic merge queue, target-branch verification, distributed fencing, or 10,000-worker capacity. GitHub Actions evidence integration remains [#350](https://github.com/zhoushoujianwork/clawflow/issues/350); durable scheduling remains [#351](https://github.com/zhoushoujianwork/clawflow/issues/351).

Local fixture tests do not establish a live provider or GitHub Actions result. See the [pilot execution record](platform-pilot-report.md) for earlier work and recorded validation.
