# Native macOS dashboard — implementation notes

Companion to the [product and interaction design](native-macos-design.md), which controls scope. Proposed contracts below are not existing commands or APIs.

## Reference and current baseline

Reference inspected in the adjacent Offload checkout: `docs/menubar-status-design.md`, `macos/Sources/OffloadMonitor/DashboardWindow.swift` and the Swift source layout. Reuse its status hierarchy, explicit freshness, native window lifecycle and shared model pattern. Do not carry over its chat, cost views or multiple auxiliary windows. This is reference reading only; no Offload worker is launched.

ClawFlow currently has Go CLI/application modules, a React interface, local run/history/lock data, Offload execution profiles and `bound_machine` configuration. The managed-development design proposes durable distributed execution. This inspection did not establish a working LAN registration/lease protocol; retain the worker requirement while implementing the missing service rather than presenting local hostname binding as remote registration.

## Ownership and transport

- `macos/`: proposed SwiftUI app with AppKit menu-bar/window integration. One shared observable store serves the window and menu-bar summary. Pure state reducers handle labels, freshness and enabled actions.
- Go controller: owns scheduling, task history, worker registry, project eligibility and validated mutations. Extract lifecycle from `clawflow web` before retiring its frontend. The app never starts scheduling merely by refreshing a view.
- Worker agent: owns machine-local repository checkout, Offload profiles and execution. It connects to the controller outbound; it needs no inbound listener. UI exit is unrelated to task cancellation.
- Use a versioned authenticated service API with snapshot plus resumable event updates. One native client stream updates all views; reconnect refreshes the snapshot if the event cursor has expired. Polling is a bounded fallback. No database/file scraping by the app.
- Local connections use a restricted local endpoint. LAN connections use TLS and controller identity pinning established during pairing. Enabling LAN access is explicit; never expose existing unauthenticated Web routes on the LAN.

The initial topology is one controller with a durable store and a small LAN worker pool. No distributed-controller or 10,000-worker capacity promise. The local host participates as a registered worker when enabled.

## Minimum read model

| Entity | Required information |
|---|---|
| Snapshot | Schema version, controller identity, revision, server time, freshness and capability flags |
| Task/attempt | Stable IDs, project and issue/PR links, current state/step, actionable reason, worker identity, timestamps, evidence/result links, supported actions |
| Worker | Stable identity, display name, platform/version, capabilities, capacity, assignment state, last heartbeat, active attempts, eligible projects |
| Project | Stable identity, repositories, task counts, execution binding and independent automation/CI/acceptance/merge policies |

Historical records without durable identity remain read-only. Missing cost, progress or evidence is unavailable, never zero or passed. Controller task state and verified evidence determine completion; an Offload process exit or PR creation alone cannot show Delivered.

Use UI categories Attention, Running, Waiting and History while preserving the precise backend reason. Waiting for CI is waiting, not a stalled worker. Waiting for an eligible worker shows that reason. Menu-bar precedence is disconnected/stale, actionable attention, running, waiting, idle; its count always means running tasks. Unknown data uses a dash. VoiceOver supplies full counts and freshness; symbols and text convey state without relying on color.

## Registration and execution safety

Invitation issuance requires an authenticated controller operator. Invitations are single-use, expire quickly, carry controller identity and are redacted from logs/history. The copyable command should prompt securely for its secret rather than place the secret in shell history. A successful exchange issues a revocable worker credential scoped to that worker. macOS viewer secrets belong in Keychain; worker credentials use platform-appropriate protected storage.

Registration is idempotent for a persisted worker identity. Reconnect updates the existing row; hostname is only a display value. Capability/readiness checks verify the selected execution profile, repositories and project eligibility before dispatch. Authentication failures and incompatible versions surface as Needs setup with an actionable explanation.

Before enabling remote dispatch, implement durable attempt IDs, renewable leases, fencing and idempotent result acceptance. The controller grants at most one current lease for an attempt. Losing contact produces an uncertain attempt; lease expiry is not proof that its process stopped. Reconcile worker execution and external effects before allowing retry/reassignment. Workers stop new effects when authority expires; late results cannot advance a newer attempt. Retain existing CI, acceptance, retry and merge gates.

Commands carry request IDs and expected entity revisions. A timeout means outcome unknown: refetch the operation instead of blindly repeating it. Cancellation and drain remain pending until acknowledged. Revoking a worker cannot guarantee termination on a disconnected machine; retain uncertain attempts and fence subsequent writes. Never silently reset locks or retry ledgers to make the dashboard appear healthy.

## Migration and acceptance

Keep the [CLI contract](cli-interaction-design-detail.md), [execution adapter](offload-execution-design-detail.md) and [durable engine direction](managed-development-design-detail.md). Import existing read-only history; map host bindings explicitly to stable worker identities without broadening eligibility. Snapshot configuration before conversion. Disable the old scheduler owner before starting the extracted controller; rollback must also keep exactly one owner.

Headless acceptance covers status precedence/freshness, task filters, unknown values, action availability, snapshot/event reconciliation, single-use/expired invitations, unauthorized access, duplicate registration, incompatible versions, drain, disconnect, lease expiry, late results, cancellation races and app-independent service lifetime. Exercise one controller with two isolated worker fixtures, including restart during an active attempt. Existing CLI/CI policy tests remain required for shared service changes.

Native acceptance, during an explicitly agreed testing window, covers menu-bar anchoring, keyboard navigation, VoiceOver, light/dark appearance, narrow/wide layout, text truncation, reconnect banners and window-close/app-quit semantics. A headless pass does not establish visual acceptance. Retire Web assets only after native parity for this document's scope and LAN acceptance pass; update packaging, release checks and migration instructions together.
