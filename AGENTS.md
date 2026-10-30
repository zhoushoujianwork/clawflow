# Repository agent instructions

## Native dashboard updates and validation

- The macOS ClawFlow app is a dashboard and control client. The controller/scheduler, workers, and database run as separate services; quitting or restarting the app leaves active execution running.
- The user authorizes agents to build, install/update, quit, and relaunch the ClawFlow dashboard app as needed to complete and validate app changes, without asking again. This is an explicit exception to the default restriction on restarting desktop apps for routine validation. Keep desktop interactions scoped to ClawFlow.
- Complete app delivery by updating the installed app and checking the changed screen when needed; do not leave installation or app restart pending solely because the app is running.
- App-only updates must not stop/restart the controller, workers, or database, replace their installed CLI executable, or run a host migration installer that reloads services. Updating the bundled dashboard CLI helper does not update the independently installed service executable.
- Core service updates are a separate deployment step. Inspect active tasks and service state first, preserve running work, and use the service's supported drain/recovery process when a restart is needed. App restart authorization does not authorize interrupting active tasks.
- On an explicit local connection, app startup may start installed services that are not loaded. Loaded services are reused. Account for this existing recovery behavior when validating app startup; do not describe app launch as having no possible service side effects.

## Local native development

- Use `make dev` to build a timestamped dev app, install it at `/Applications/ClawFlow.app`, and relaunch it. Use `make dev-build` for build-only validation; override `CLAWFLOW_DEV_INSTALL_DIR` for an isolated install. `make cli-dev` retains the separate CLI installation workflow.
- Dev bundles label the app and bundled CLI with the same version. Settings exposes a read-only stable release check and release link; dev builds continue updating locally with `make dev`. Do not run CLI self-update against a signed app bundle or confuse app updates with controller upgrades.

## Dashboard interaction and status reference

- Use GitLab Runner/job monitoring as the reference for concise execution status: recognizable pending/running/success/failure states, a spinning indicator during live execution, and an elapsed duration alongside each running task and in its details.
- Show the task activity (development, PR review, repair, reply), worker, issue/PR links, and queue/blocking reasons without requiring users to inspect logs.
- Duration belongs to the current execution attempt and starts at the controller-recorded claim/start time, never the latest heartbeat or app launch. Retries reset to the new attempt; unknown timestamps are explicitly unavailable. Completed durations stop at the recorded end time.
- Keep connection freshness visible. On stale/disconnected snapshots, stop presenting live animation and advancing elapsed time; retain last-known data. Task completion must not imply CI, review, or merge completion.
- Follow this reference in the native macOS app; keep the interaction simple and avoid adding a web dashboard dependency.
