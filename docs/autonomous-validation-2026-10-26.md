# Autonomous workflow validation

Date: 2026-10-26. Live repository: `zhoushoujianwork/offload`.

## Implemented and verified

- ClawFlow commit `8021edc` defaults to direct autonomous issue implementation. Explicit `legacy` retains the earlier pipeline.
- Independent headless checks: `go test ./internal/config ./internal/operator ./cmd/clawflow/commands` passed 602 tests; matching `go vet` and `git diff --check` passed.
- Installed CLI and external AI skills; enabled autonomous dispatch, auto-merge and required CI for offload. Auto-approve and Pilot remain off; sync remains paused.
- Started background scheduler on localhost:8090 without opening a browser. A scheduled tick recognized the active scoped run and skipped duplicate execution.
- Issue [#10](https://github.com/zhoushoujianwork/offload/issues/10) selected `implement` directly at 03:34:23 UTC without adding a ready label. Worktree started at synchronized main `ff0a109`.
- Baseline [CI run](https://github.com/zhoushoujianwork/offload/actions/runs/40264042854) exposed a promptui masking-test data race; evidence was supplied to the implementation agent for automatic repair.

## Live acceptance

- Implementation produced [PR #12](https://github.com/zhoushoujianwork/offload/pull/12) with separate commits `3445ad6` (prompt race repair) and `c2a0322` (cost explanation).
- [PR CI](https://github.com/zhoushoujianwork/offload/actions/runs/40265421894) passed formatting, vet and `go test -race ./...`.
- ClawFlow automatically merged it at 03:44:41 UTC as `a2a7775f46707ee5780b0e2637de1cd589fddc51`; issue #10 closed at 03:44:43 UTC. No manual ready label or merge command was used.
- Independent local merged-source checks passed 17 cost/list/history/masked-secret tests with `-race`; built and installed offload `a2a7775`. Installed `offload ls` now explains unavailable Codex USD cost.
- Target-branch [CI](https://github.com/zhoushoujianwork/offload/actions/runs/40265567308) passed formatting, vet and race tests at 03:46:06 UTC.
- The additional bounded CI-repair operator passed 684 independent tests across config/operator/VCS/commands, matching vet and diff checks. Its post-PR failure dispatch is covered with isolated fixtures, not a deliberately failing live PR. The observed live repair occurred inside initial implementation.

ClawFlow implementation runtime reported model `claude-sonnet-5`, 28 turns, duration 510.103 seconds and estimated cost USD 2.418972; this is not verified billing. Session: `110e1fc2-705e-4ad1-88fd-09147b44f25e`. The run metadata spans 03:34:23–03:43:02 UTC; CI waiting and merging happened afterward.

A leftover missing-ready-label cleanup warning in the first live run was harmless and has been removed from autonomous dispatch, with regression coverage.

## Supporting worker evidence

Costs below are estimated worker cost only, not verified billing or total coordinating-agent cost. No missing cost is treated as zero.

| Task | Status / exit | Profile / model | Elapsed | Estimated USD | Session |
|---|---|---|---|---|---|
| Direct-dispatch investigation | ok / 0 | codex-gpt-6.1-sol-medium-fast / gpt-6.1-sol | 117.901 s | unavailable | 01a1763d-74c0-7860-8464-edf313e6a2d6 |
| Direct-dispatch implementation | ok / 0 | codex-gpt-6.1-sol-medium-fast / gpt-6.1-sol | 325.704 s | unavailable | 01a1763d-74c0-7860-8464-edf313e6a2d6 |
| CI repair implementation | ok / 0 | codex-gpt-6.1-sol-medium-fast / gpt-6.1-sol | 399.254 s | unavailable | 01a17646-5eb5-7b91-b15b-92eda9a329e8 |
| CI repair feasibility | ok / 0; missing contract identified | codex-gpt-6.1-sol-medium-fast / gpt-6.1-sol | 77.968 s | unavailable | 01a17646-5eb5-7b91-b15b-92eda9a329e8 |

The repair design deliberately uses local test reproduction and immutable PR identity, without claiming CI-log ingestion or general GitLab repair support.

## Continuing operation

ClawFlow runtime `2fd3a0c` and updated external skills are installed. The localhost scheduler restarted successfully at 03:46:31 UTC without GUI interaction. The preceding scheduled pass independently picked up open unlabeled issue #11 at 03:44:53 UTC; that implementation remains active and was preserved across the service restart. A follow-up scoped command correctly skipped because the active run owns the global lock. Completion of issue #11 is outside this issue #10 acceptance record.

Source commits: `8021edc` (direct autonomous dispatch) and `2fd3a0c` (bounded CI repair plus obsolete-label cleanup). Local source commits were not pushed to the ClawFlow origin. offload main is synchronized to merged `a2a7775`, and its installed CLI reports that version.
