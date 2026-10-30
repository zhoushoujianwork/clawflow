# Native macOS dashboard

Status: First native implementation available · 2026-10-30

The [implementation and migration guide](native-macos.md) records the delivered scope: native status views, independent controller, LAN invitations and worker drain/resume. It uses the latest existing worker protocol, a shared five-second polling bridge and repository monitoring controls. Manual task retry/cancel, notifications, raw execution-log streaming and advanced policy editors remain outside this first release. Desktop acceptance remains pending.

ClawFlow becomes a small native macOS app for answering three questions: **What is running? What needs me? Which workers are available?** The Go service continues scheduling work independently. LAN workers remain part of the product; closing or quitting the dashboard does not stop them.

This replaces the Web-first interface direction in the [CLI design](cli-interaction-design.md). CLI and execution policies remain. [Technical notes](native-macos-design-detail.md) distinguish existing foundations from new work.

## One window, three destinations

Use SwiftUI with AppKit integration, system typography, standard lists, a narrow sidebar and an inline detail pane. No embedded browser. Open Tasks by default and remember selection. Avoid metric cards, charts, chat panels and nested administration pages.

```text
ClawFlow                 Main controller · Connected        Search
─────────────────────────────────────────────────────────────────
Tasks          2         Needs attention 1 · Running 2 · Waiting 3
Workers        3         [Active | History]          All projects ▾
Projects       4         ─────────────────────────────────────────
                         ! Fix login       Needs input    mac-mini
                         ↻ Add export      Implementing   linux-01
                         ◷ Fix search      Waiting for CI       —
                         ─────────────────────────────────────────
                         Fix login · app#42
                         Needs input: test account is missing
                         Last update 2 minutes ago · mac-mini
                         [Open issue]                More…
                         Progress · Result · Logs
─────────────────────────────────────────────────────────────────
Settings                 Updated just now
```

The sketch shows content hierarchy; in a wide window, details sit to the right. In a narrow window, selecting a row opens details with a Back button. There is only one dashboard window.

| Destination | Default content | Main interaction |
|---|---|---|
| Tasks | Attention first, then running and waiting; history is a separate segment | Select a task to read its current step, reason, worker and result |
| Workers | Name, availability, active/capacity and last contact | Add worker; select one to inspect tasks or stop new assignments |
| Projects | Name, active task count and automation state | Filter Tasks to a project; open a compact settings sheet |

Task rows show title, project/issue, state, worker and elapsed time. No invented completion percentages. Details start with a plain-language reason and the most useful action. Logs stay collapsed until requested. Search finds task titles, issue references and worker names in the selected destination.

## Daily use

1. **Glance:** the menu bar shows the ClawFlow mark, a status symbol and running count. Click for a small summary: attention, running, waiting, worker availability and Open Dashboard. No duplicate navigation.
2. **Inspect:** select an attention row. Read the reason before opening logs. Open the existing issue or PR to supply context or review code; the app does not add a chat workspace.
3. **Recover:** the task menu offers only supported actions. Cancel shows “Cancellation requested” until acknowledged. Retry creates a linked attempt after the previous attempt is settled and policy permits it. Pausing a project stops new dispatch; active tasks continue.
4. **Leave:** closing the window keeps the menu-bar view; quitting the app disconnects the viewer. The controller and workers continue independently.

Unknown or disconnected data is never displayed as zero work or success. Keep last-known rows with a visible timestamp; disable execution controls until the controller is reachable. Failed history alone does not keep the current attention indicator lit. Notifications are optional and limited to new actionable blockers, with deduplication.

## Add a LAN worker

In **Workers → Add Worker**, generate an expiring, one-use invitation. The sheet shows the controller address and a copyable join command. Run it on the worker host; the worker connects outbound and appears as Checking, then Ready, or Needs setup with a concrete reason. No browser or manual YAML editing is required for registration. Existing worker credentials remain local.

Registration establishes identity and availability; project eligibility is selected separately in worker details. An unassigned worker receives no work. Existing project bindings are preserved. A worker may be macOS or Linux; only the viewer requires macOS.

Worker details contain its identity, version, capabilities, assigned projects and active tasks. **Stop new assignments** lets current work finish; **Resume assignments** restores eligibility. **Remove worker** revokes access after draining. An offline worker stays visible, and its unfinished tasks show “Connection lost”; they are not automatically duplicated on another host.

## Setup and scope

First launch offers **Connect to controller** or **Set up on this Mac**. Remote setup takes an address and pairing credential; local setup explicitly installs/starts the background service. Show connection/readiness results before opening Tasks. Settings holds the controller connection, notifications and launch-at-login. One controller is active at a time in the first release.

Project information retains independent monitoring, required CI, acceptance and merge settings. Compact presentation must not combine these permissions into one switch. Advanced operator/profile configuration stays in the existing CLI initially. The retired per-project Pilot controls are not reintroduced. No new workflow editor, analytics suite or cloud account.

Delivery order:

1. Extract the background controller from Web lifecycle ownership; provide authoritative task/worker status and preserve existing CLI behavior.
2. Ship the native Tasks view, details and menu-bar summary against that service.
3. Complete LAN invitation, registration, heartbeat and safe dispatch; add Workers and compact Projects controls.
4. Validate reconnect/recovery and native accessibility, then retire the Web UI and its build dependency. Keep its compatibility entry point only for migration; never run two schedulers.

The first complete release includes LAN worker onboarding. Native screen checks require an agreed desktop testing window; headless model and service checks run first. The interaction direction below includes later refinements; use the implementation guide above for current capabilities.
