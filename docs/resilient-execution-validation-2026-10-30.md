# Local recovery cutover validation

Deployed controller/service CLI: `v0.75.0-409-gf3189289` (`f3189289`). Native app: `0.75.0-dev.20261030121821.f3189289`. Migration 11 applied to the existing dedicated PostgreSQL database. PostgreSQL itself was not restarted.

## Observed result

- Captured four legacy task/session/worktree identities and backed up the database, configuration and installed CLI. Drained both workers, sent SIGTERM to the four Offload processes, and observed all four exit without an operator-issued SIGKILL. Offload history identifies each stop as an interruption and supplies its original resumable session.
- Stopped the legacy controller and standalone worker service. All four sealed interruption reports had zero write-back effects. Offline import preserved those reports, advanced each task to epoch 2 and retained its original worktree and native session.
- Started the new controller and worker service. ClawFlow issues #612, #613, #615 and Offload issue #63 resumed with their original native session IDs and new Offload invocation IDs. Native metadata events confirmed the session identities and continued increasing.
- Gracefully restarted the new controller: PID `28670` changed to `30349`. All four Offload PIDs (`28864`, `28876`, `28877`, `28953`) and their executor processes survived. No task epoch changed during this restart.
- Replaced the local worker admissions manager using `worker roll` with the same verified build. All four execution PIDs survived. This tests manager replacement; it is not a mixed-protocol or incompatible-schema upgrade test.
- Restored the previous dispatch pause setting and resumed assignments for both previously ready workers. Both workers were live, with four running tasks on attempt 2 at the final check.
- Updated/relaunched the native app using `make dev`. Actual UI showed four running spinners, advancing elapsed durations, attempt 2 in task details, the deployed controller version, and issue queues with scheduler reasons.

Private evidence and rollback artifacts are retained under the standard ClawFlow data root at `backups/recovery-cutover-20261030-201232/`. Native session contents, tokens and database credentials are excluded from this document.

## Checks and limits

The offline importer and rejection cases passed isolated PostgreSQL race tests, including successful results, unrelated failures, existing effect intents and explicitly cancelled tasks. Targeted coordinator/worker/execution race checks passed again after integration with the concurrent dispatch-coordination change. The build and native live checks passed.

This validates real Claude-backed session continuation, controller restart survival and same-build worker-manager replacement. The four development tasks were still executing at acceptance; their eventual development, CI, independent review and merge outcomes remain separate. Live completion/result-replay after another outage, host reboot, other native backends and mixed-version protocol migrations were not tested here.

See the [operations runbook](resilient-execution-runbook.md) for supported boundaries and recovery commands.
