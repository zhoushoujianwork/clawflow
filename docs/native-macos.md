# Native macOS dashboard

ClawFlow.app is a SwiftUI/AppKit status viewer for macOS 14 or later. One window contains **Tasks**, **Workers** and **Projects**; a menu-bar summary shares the same status stream. When configured for localhost, it ensures installed local host services are started once at app launch. Closing or quitting it leaves the controller running.

## Build

```sh
make native
```

The app is at `macos/dist/ClawFlow.app`, with a bundled CLI. The build signs it locally and validates its plist; it does not open, install or replace an app. Swift and Go are required; Node is not. This is a locally signed build, not a notarized distribution.

## Standard macOS host directories

After building, preview migration with `python3 macos/install-host.py`; apply it to an idle host with `python3 macos/install-host.py --apply`. The installer refuses active coordinator tasks or conflicting destination directories. It stops the app and installed host agents, moves the existing data rather than copying a second configuration, updates agent paths and app connection preferences, installs the app/CLI, and reloads services. An interrupted installation can be inspected and rerun; it does not merge conflicting data directories. LaunchAgent backups are saved alongside the originals.

| Content | Installed location |
|---|---|
| Shared configuration | `~/Library/Application Support/ClawFlow/config/config.yaml` |
| Controller state, projects, PostgreSQL and independent worker state | `~/Library/Application Support/ClawFlow/` |
| Stable service CLI | `~/Library/Application Support/ClawFlow/bin/clawflow` |
| Service logs | `~/Library/Logs/ClawFlow/` |
| Login startup definitions | `~/Library/LaunchAgents/com.zhoushoujianwork.clawflow.*.plist` |
| App executable and bundled helper | `/Applications/ClawFlow.app` |

The old `~/.clawflow` path remains a compatibility symlink to Application Support, and its `logs` entry points to Library/Logs. Existing historical paths and CLI scripts remain usable. Config, runtime and credential lookup resolve the same migrated root; explicit independent-worker state overrides remain supported. Non-migrated CLI installations retain their existing paths. Credentials required by unattended CLI workers remain in protected files inside the private data directory; this migration does not convert them to Keychain items.

Opening the app with an explicit localhost controller connection starts missing installed database, controller and worker agents once, in that order. Loaded agents are reused, and readiness failures are retried by launchd. Remote viewer connections do not start local services. Stopping a service in the current app session keeps it stopped until explicitly started, the app is reopened, or the next login starts its agent. Quitting the app leaves services running. Host installation still requires database/execution configuration; app startup does not invent credentials or install unknown services.

Validated on 2026-10-30: the existing idle host was migrated, all four LaunchAgent path references were updated, both workers reconnected, and app preferences matched the CLI token path. Unloading the controller and reopening the app started it again without creating a second worker identity. The PostgreSQL-backed full Go race suite, 14 Swift tests, vet, build and app signature checks passed. No computer reboot was performed.

## Controller setup

Reuse your existing PostgreSQL and worker configuration. The new `controller` command uses the same coordinator HTTPS listener, scheduler, execution profiles and delivery gates as the previous service.

```yaml
settings:
  coordinator:
    enabled: true
    listen: ":8093"
```

On a host currently running the old service, stop it before starting the new one. During the chosen maintenance window:

```sh
clawflow web service uninstall   # only if the old LaunchAgent is installed
clawflow db migrate
clawflow controller service install
clawflow controller service status
```

For a foreground controller on macOS or Linux, use `clawflow controller`. A shared process lock prevents the new controller and the compatibility Web server from owning the scheduler simultaneously. Existing worker tokens, bindings/tags and active task records remain. Migration 10 adds invitations and a paused flag; existing workers keep their previous eligibility.

On macOS, installed user LaunchAgents start after login following a reboot. They do not run before login. Install the controller from a stable CLI location such as `~/.clawflow/bin/clawflow`; avoid a temporary worktree binary. The controller uses `RunAtLoad` and unconditional `KeepAlive`, so launchd restarts it after an unexpected process exit, including a clean exit. Explicit Stop unloads the agent and prevents that restart for the current login session; its plist remains for the next login. Database and worker agents need their own startup entries. Database readiness failures cause controller startup to fail and retry; no dependency ordering is assumed. The menu-bar app can be launched at login independently, and closing it never stops scheduling.

Create a separate admin identity for the viewer, saving its output to a private file (the compatibility path resolves to Application Support after migration):

```sh
(umask 077; clawflow worker token create --name native-viewer --scope admin > ~/.clawflow/native-viewer.token)
```

Record the printed certificate SHA-256 fingerprint. In the app's **Settings**, enter the controller's reachable HTTPS address, admin token file path and fingerprint, then choose **Test and Save**. The private token file must exist on the viewer Mac. Use a new identity name if one already exists; the command never replaces an existing token.

The app stores connection metadata, not token contents, in preferences. The CLI bridge reuses the existing authenticated, pinned platform client, rejects redirects and emits JSON only. Leave the address blank to reuse an existing `clawflow platform login` configuration. When viewing a controller on its own host, use explicit app connection fields; do not configure that host as a platform client, which intentionally disables local scheduling.

## Offload readiness

Settings shows **Offload execution** from `clawflow execution status --json`, and the menu-bar item gains an attention mark when any local worker lacks a usable Offload. Worker rows under **On this Mac** repeat the same one-line reason. The app polls the CLI's cached result every 15 seconds and runs no probe of its own; **Recheck Offload** is the explicit action after you install it. Viewer-only Macs show that no local executor is required. See [execution readiness](execution-capability-check.md#readiness-status). Native visual behavior is not covered by the headless tests.

## Daily use

The menu-bar panel and Settings show **On this Mac** with installed ClawFlow worker/controller LaunchAgents, process status and Start / Restart / Stop shortcuts. Stop unloads the selected service but retains its plist so Start can load it again. Restart/Stop require confirmation because they interrupt local work. These controls never manage the remote controller. Quit App closes only the viewer. Service commands report acceptance; the next status refresh confirms the process state.

- Tasks shows active attempts first, with attention before running and waiting; History contains settled attempts. Search and project filtering do not change execution. Select a row for its reason, worker, issue/PR link and on-demand coordinator activity log. Attempt completion does not imply CI, acceptance or merge passed.
- Workers shows identity, tags, roles, active/capacity and last contact. Stop new assignments drains a worker; it keeps renewing and completing existing leases. Removal requires a paused worker with no unsettled leases and revokes its credential while retaining history.
- Projects groups repositories and displays independent CI, acceptance and merge settings. Pause/Resume changes repository monitoring only. Queued or active worker tasks are unaffected; use worker drain when you need to stop new assignments. Advanced policy editing stays in the CLI.
- The menu-bar popover answers the daily questions without the dashboard: overall state (disconnected/unknown, needs attention, running, waiting, idle) with a count and text/VoiceOver label, a one-line explanation of why work is idle or waiting, controller connectivity and data age, worker readiness (ready, offline, not accepting new assignments), bounded lists of attention, running and queued tasks with links, then repositories grouped as monitoring or paused with running/queued counts and separate CI, acceptance and auto-merge labels. The content scrolls under a fixed height; Open Dashboard is a separate button. Pause/Resume uses the existing `disable`/`enable` action and changes monitoring only; the popover says that queued and running tasks and workers are unaffected. Opening it only reads the shared snapshot. On disconnect it keeps last-known rows labelled with their age and disables controls; counts the app cannot know are dashes. Contract gaps: the controller exposes no per-project dispatch pause, no per-repository last-scan time, and queued counts cover coordinator tasks only, not issues not yet enqueued; the popover does not invent these.
- Lost connections retain visibly stale data and disable mutations. Failed history alone does not light the current attention indicator. Unknown status is a dash, never zero or success.

The viewer polls once every five seconds through a bounded CLI invocation; all views share that result. It does not add a second event or execution protocol. It shows coordinator attempts rather than claiming a complete issue-to-delivery pipeline. Manual retry/cancel, raw execution-log streaming, notifications and advanced project-policy editing are not exposed in this first native release.

## LAN worker onboarding

Choose **Workers → Add Worker**, enter a unique worker name, and generate a ten-minute, one-use invitation. The sheet supplies a join command and a starter worker command. The invitation is pasted into stdin rather than placed in shell arguments or history. Set the actual project tags and roles in the starter command before running it.

The worker exchanges the invitation over pinned HTTPS for a new credential, saved with mode `0600` without overwriting any existing file. Start it with its existing local VCS credentials, Offload profile and repository configuration. Its registration appears with assignments paused. Inspect the tags/roles, then select **Resume assignments**. Existing token-based worker startup remains supported. See [worker configuration](worker.md).

An invitation is not a worker/admin token and cannot claim tasks. Reuse and expiry are rejected. An interrupted exchange can consume an invitation without delivering the credential; create a new identity/invitation rather than replaying a potentially successful action. Worker lease fencing and current retry policy remain authoritative. A disconnected worker is not assumed to have stopped.

## Compatibility and validation

Default CLI builds contain a small Web migration page. The old API surface remains available under `clawflow web`; `make legacy-web` explicitly builds the old frontend when needed during migration. Its source is retained for rollback. No running installation is changed by building the native app.

Run `swift test --package-path macos` for native model/process checks and the Go race suite with an isolated `CLAWFLOW_TEST_DATABASE_DSN` for protocol/storage checks. CI has a dedicated macOS test/build job. Real menu-bar behavior, accessibility and desktop layout require a separately agreed desktop acceptance window. A successful build or headless check is not visual acceptance.

Design: [interaction](native-macos-design.md) · [implementation notes](native-macos-design-detail.md).
