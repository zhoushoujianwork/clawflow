# Native dashboard validation — 2026-10-30

Implementation starts from fetched `origin/main` `df62382f`, in the isolated `clawflow-native` worktree. The previously committed interaction design was carried onto that baseline. Existing controller/worker behavior was reused after inspecting the newer source.

## Headless evidence

- `swift test --package-path macos`: 10 tests passed. Covers unknown/stale/failed status, historical failure classification, lease loss, safe links, argument handling, concurrent stream draining, timeout and nonzero process exit.
- PostgreSQL-backed invitation tests passed under `-race`: single-use and expiration, concurrent exchange, distinct issued credentials, registration preserving pause, drain preserving heartbeats/completion, removal blocked by active leases, revocation and credential-free audit records.
- `TestNativeBridgeLANLifecycle` passed under `-race`: real pinned TLS listener, CLI snapshot, invitation exchange through stdin, private token file, registration, claim blocked while paused, resume/claim, drain/heartbeat, snapshot/activity, completion/removal and policy-preserving repository pause. Uses loopback fixtures, not a production LAN host or execution provider.
- Migration tests passed: upgrade from schema 9, existing-worker eligibility unchanged, and migration idempotency through the normal store suite.
- Full `go test -race ./...` ran with an isolated PostgreSQL server. All packages except `internal/api` passed on that run. Its existing `TestProjectPipelineExecutionsUsesAdapterWithoutMutatingCache` hit the adapter's three-second deadline during the concurrent run. The unchanged test passed in isolation, then the entire `internal/api` package passed under `-race`. No timeout or assertion was weakened.
- Subsequent focused command/worker/scheduler tests passed after the final controller-context and bridge changes. `go vet ./...`, local documentation links and `git diff --check` passed.
- `make native` built the app and bundled CLI without Node. Plist validation and `codesign --verify --deep --strict` passed. CLI help checks passed for controller, dashboard and worker join.

## Acceptance boundaries

No desktop application was opened, focused or restarted. No existing controller, worker or production database was changed. No real provider job, repository delivery or cross-machine LAN rollout was performed. The temporary PostgreSQL instance was used only for isolated test schemas.

Native menu-bar anchoring, resizing/accessibility and visual appearance remain pending an agreed desktop testing window. The app is locally signed, not notarized. The delivered UI and its explicit scope are documented in the [setup guide](native-macos.md).
