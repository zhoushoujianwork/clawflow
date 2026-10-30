# Native macOS dashboard

ClawFlow.app is a SwiftUI/AppKit status viewer for macOS 14 or later. One window contains **Tasks**, **Workers** and **Projects**; a menu-bar summary shares the same status stream. It does not start a worker or scheduler when opened. Closing or quitting it leaves the controller running.

## Build

```sh
make native
```

The app is at `macos/dist/ClawFlow.app`, with a bundled CLI. The build signs it locally and validates its plist; it does not open, install or replace an app. Swift and Go are required; Node is not. This is a locally signed build, not a notarized distribution.

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

Create a separate admin identity for the viewer, saving its output to a private file:

```sh
(umask 077; clawflow worker token create --name native-viewer --scope admin > ~/.clawflow/native-viewer.token)
```

Record the printed certificate SHA-256 fingerprint. In the app's **Settings**, enter the controller's reachable HTTPS address, admin token file path and fingerprint, then choose **Test and Save**. The private token file must exist on the viewer Mac. Use a new identity name if one already exists; the command never replaces an existing token.

The app stores connection metadata, not token contents, in preferences. The CLI bridge reuses the existing authenticated, pinned platform client, rejects redirects and emits JSON only. Leave the address blank to reuse an existing `clawflow platform login` configuration. When viewing a controller on its own host, use explicit app connection fields; do not configure that host as a platform client, which intentionally disables local scheduling.

## Daily use

- Tasks shows active attempts first, with attention before running and waiting; History contains settled attempts. Search and project filtering do not change execution. Select a row for its reason, worker, issue/PR link and on-demand coordinator activity log. Attempt completion does not imply CI, acceptance or merge passed.
- Workers shows identity, tags, roles, active/capacity and last contact. Stop new assignments drains a worker; it keeps renewing and completing existing leases. Removal requires a paused worker with no unsettled leases and revokes its credential while retaining history.
- Projects groups repositories and displays independent CI, acceptance and merge settings. Pause/Resume changes repository monitoring only. Queued or active worker tasks are unaffected; use worker drain when you need to stop new assignments. Advanced policy editing stays in the CLI.
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
