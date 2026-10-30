# Local worker claim recovery validation

Observed on the local macOS host on 2026-10-30, approximately 22:46–23:05 +08.
Operational reference: [durable worker runbook](resilient-execution-runbook.md).

## Failure and fixes

The controller and PostgreSQL were available, but the launchd worker repeatedly
exited with `retained claim epoch mismatch`. A saved claim referred to a newer
controller-authorized attempt while the task's local journal retained an older,
acknowledged attempt. A second controller-managed worker had the same problem.

Commit `75828c3` permits a newer authorized attempt only after checking task
identity, prior acknowledgement, remaining execution authority, and exclusive
executor/session locks. It archives prior evidence and removes the old active
checkpoint before preparing a fresh retry. Lock inodes, worktrees and referenced
native artifacts remain intact. This is a new attempt, not same-session recovery
of an acknowledged result.

Live checking then exposed a second defect: infrastructure retry retained the old
report digest. An identical report from the next attempt could receive HTTP 200
without committing a current result. Commit `84f23f0` requires a committed result
for current receipt matching and clears that digest on infrastructure retry.
Historical receipts retain their attempt-specific epoch.

## Operational actions and evidence

- Installed the fixes in the separate service executable, retaining binary backups.
- Rolled the controller-managed worker and allowed the launchd worker to restart.
- Reported one expired, never-started claim as an infrastructure launch failure
  through the authenticated worker API after checking controller ownership,
  retained journals and exclusive locks. Retry limits were preserved.
- Backed up the controller-managed worker's invalid `queue.json` (`not json`)
  and atomically restored an empty version-1 ranking file under the snapshot lock.
- Gracefully restarted the controller with SIGTERM after verifying durable
  protocol 1 and independent executor lifetime. PostgreSQL was not restarted.
- Replayed the exact falsely acknowledged report through its authenticated worker
  API after verifying task, owner and current epoch. The controller recorded
  `result-accepted`, then `result-applied`; the next attempt used a newer epoch.
- The launchd worker remained at PID 35581 after recovery. Issue #615 continued
  executing at epoch 4 with executor PID 35595. Its native session and worktree
  were unchanged across controller replacement (PID 30349 to 41494), and its
  checkpoint and controller heartbeat continued advancing.
- `clawflow db status` reported connectivity OK and migrations 001–011 applied.
  The original PostgreSQL PID 61176 remained running.
- After controller recovery, the scheduler automatically fired both configured
  repositories at 23:04:35, with the configured two-minute interval.

Private binary backups and reconciliation request/response evidence are under
the host's `ClawFlow/backups` directory. Secrets and native output were not added
to this repository. The native dashboard was not manipulated.

## Headless checks

- Focused worker replay/recovery checks: 27 passed.
- Race-enabled worker replay/admission checks: 24 passed.
- Coordinator tests against a temporary, isolated PostgreSQL instance: 91 passed;
  the fixture was stopped afterward.
- Initial full suite: 3050 passed, 101 skipped, one API adapter timing failure.
  That test passed on an isolated rerun. Two help-probe timeout failures in an
  earlier package run also passed on isolated rerun.
- Final full suite (`go test ./...`): 3052 passed across 45 packages.

## Remaining task-level limits

The recovered runtime does not establish completion of the issues it executes.
Coordination reassessment still blocks some queued work, including #612/#613 and
Offload #63/#65. PR #617 lacks private acceptance evidence, and Offload PR #67 has
a merge conflict. CI, independent acceptance, merge policy and retry budgets were
not relaxed to remove these blockers.
