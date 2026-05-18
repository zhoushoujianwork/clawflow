# ClawFlow SaaS Worker Architecture

## Background

ClawFlow currently works as a local-first CLI: `clawflow run` scans enabled
repositories from `~/.clawflow/config/config.yaml`, matches issues and PRs
against local `SKILL.md` operators, and writes state back through VCS labels
and comments. Multi-machine configuration is handled by private GitHub Gist
sync.

The SaaS version replaces Gist as the primary source of shared configuration.
Cloud config becomes the source of truth for projects, repositories, machines,
bindings, jobs, and run history. Local machines become registered workers that
pull jobs from the cloud, execute operators locally, and report results.

The core ClawFlow contract stays the same: operators still communicate through
VCS labels, comments, PRs, and the outcome marker protocol.

## Target Architecture

The first SaaS implementation uses a Go server in this repository, a cloud API,
and pull-based workers.

```text
Web / CLI
   |
   v
ClawFlow SaaS API  -> Cloud DB
   |                     |
   |                     v
   |                Jobs / Runs / Config
   |
   v
Registered Workers on user machines
   |
   v
GitHub / GitLab + local clone + Codex/operator runtime
```

Cloud responsibilities:

- Store user-visible configuration for workspaces, projects, repos, machines,
  bindings, jobs, and runs.
- Receive GitHub App webhooks and produce deduplicated jobs.
- Expose worker register, heartbeat, lease, event, and finish APIs.
- Expose cloud config CRUD APIs for Web and CLI clients.
- Show the same project/repo configuration from any machine.

Worker responsibilities:

- Register a stable machine identity and a per-process worker identity.
- Send heartbeats and capability information.
- Pull jobs assigned to the current machine.
- Execute operators locally using the existing runner path.
- Write VCS side effects through the existing label/comment/PR clients.
- Stream run events and final status back to the cloud.

## Data Model

The SaaS database should include these first-class concepts:

- `users`: authenticated users.
- `workspaces`: personal or small-team spaces.
- `vcs_connections`: GitHub App installations and GitLab host metadata.
- `projects`: project-level configuration and automation settings.
- `repos`: repository configuration such as platform, owner/name, base branch,
  labels, operator settings, and project membership.
- `machines`: stable registered machines with hostname, display name,
  capabilities, and `last_seen_at`.
- `workers`: running worker processes attached to a machine.
- `bindings`: project/repo to machine assignment.
- `jobs`: cloud-scheduled operator tasks with lease state.
- `runs`: concrete execution attempts for jobs, including logs and errors.

Suggested job lifecycle:

```text
pending -> leased -> running -> succeeded
                  -> failed
                  -> cancelled
                  -> expired -> pending
```

Every job must have a dedupe key, for example:

```text
github:owner/repo:issue:123:operator:evaluate-bug
```

## API Protocol

Worker APIs:

- `POST /api/worker/register`
  - Input: hostname, version, capabilities, optional display name.
  - Output: machine ID, worker ID, worker token.
- `POST /api/worker/heartbeat`
  - Input: machine ID, worker ID, status, capacity, active run IDs.
  - Output: server time and desired config version.
- `POST /api/worker/lease`
  - Input: machine ID, worker ID, capabilities, available capacity.
  - Output: zero or one job spec.
- `POST /api/worker/runs/{run_id}/events`
  - Input: append-only run event batch.
- `POST /api/worker/runs/{run_id}/finish`
  - Input: final status, outcome label, summary, and error.

Cloud config APIs:

- `GET /api/cloud/config`
- `POST /api/cloud/repos`
- `PATCH /api/cloud/repos/{id}`
- `POST /api/cloud/projects`
- `PATCH /api/cloud/bindings`
- `GET /api/cloud/machines`
- `GET /api/cloud/jobs`
- `GET /api/cloud/runs`

## CLI Changes

New cloud commands:

```bash
clawflow cloud login
clawflow cloud pull
clawflow cloud push
```

New worker commands:

```bash
clawflow worker register
clawflow worker start
clawflow worker status
```

Run command compatibility:

```bash
clawflow run          # keeps current local behavior for now
clawflow run --local  # explicitly selects local scan mode
```

Once cloud mode is complete, `clawflow run` may warn cloud-enabled users to run
`clawflow worker start`, while `clawflow run --local` remains the escape hatch.

## Migration Strategy

Gist sync is retained as a legacy path during migration.

- Existing `clawflow sync push/pull` commands continue to work.
- Cloud migration is performed through `clawflow cloud push`.
- New machines restore shared config through `clawflow cloud pull`.
- Web settings should label Gist sync as legacy once cloud mode is available.

The first implementation should avoid deleting or rewriting the existing Gist
sync code. It should add cloud mode beside it and move defaults only after the
worker flow is proven.

## Security Boundary

- GitHub uses GitHub App installation authorization.
- GitLab tokens stay on the worker machine in local credentials for the first
  SaaS release.
- The cloud does not execute Codex or operators in the first release.
- The cloud should not store local clone paths, local GitLab tokens, or
  provider API keys needed only for local execution.
- Worker tokens are machine-scoped and revocable.

## Implementation Phases

1. Add this architecture document, cloud client types, credential fields, and
   command shells.
2. Extract a reusable job execution adapter from the existing local runner.
3. Implement `worker start` as a pull loop with heartbeat, lease, execute, and
   finish reporting.
4. Add the SaaS API server, DB schema, worker endpoints, and config endpoints.
5. Add GitHub App webhook job generation and GitLab worker-local scanning.
6. Update the Web UI to show cloud config, machines, bindings, jobs, and runs.

## Acceptance Checklist

- Two machines logged into the same workspace see the same repo/project config.
- A repo bound to machine A is not leased by machine B.
- A GitHub label event creates one deduplicated job.
- A worker leases the job, runs the existing operator path, and reports finish.
- GitLab repo execution uses the local worker token and does not upload it.
- `clawflow run --local` continues to work without cloud credentials.
