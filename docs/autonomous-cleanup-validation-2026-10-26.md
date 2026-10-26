# Autonomous lifecycle cleanup validation

Date: 2026-10-26. Branch: `refactor/autonomous-only`.

## Scope

Retire the built-in classify/evaluate/decompose/approval pipeline and its runtime, CLI, API, UI and documentation controls. Keep a single autonomous lifecycle with parent tracking, question/comment handling, custom non-retired operators, project/GitLab capabilities, operational controls and history. Old configuration must preserve enablement, bindings, merge and CI requirements.

The updated [product design](managed-development-design.md) describes supported scope and future work. Historical acceptance reports remain historical evidence.

## Validation

- Web build and TypeScript check passed in the worker; independent TypeScript check passed.
- Maintained documentation: 19 local link targets checked; scoped diff checks passed.
- Visual validation was not run; no desktop apps were opened or controlled.
- Full independent `go test -race -json ./...` passed: 996 passing test events across 21 packages, zero failures. The test process used an isolated HOME; raw log: `/var/folders/64/xyvthqtj4xqb6xp70kw9hgf80000gq/T/clawflow-cleanup-validation-_89m0jtt/go-race.jsonl`.
- Independent `go vet ./...`, build and diff checks passed.
- Isolated CLI smoke passed: exactly five operators, supported help, retired flags rejected, legacy config notice, and save preserving disabled state, merge=false, CI=true and machine binding while omitting retired fields.
- Lifecycle acceptance covers unlabeled dispatch, fake provider, comment/marker write-back and CI-gated merge. Successful CI merges; failed/missing CI does not. No paid provider or live PR was used for that test.
- Installed-runtime validation is pending.

## Supporting workers

Every worker used profile `codex-gpt-6.1-sol-medium-fast`, model `gpt-6.1-sol`. Estimated USD cost was unavailable for every run; these are not verified billing or total coordinating-agent costs.

| Task | Status / exit | Calls | Measured elapsed | Session |
|---|---|---|---|---|
| Inventory | ok / 0 | 28 | 120.581 s | 01a176a7-d51a-7333-8a47-dee4bce81bd9 |
| Web cleanup | ok / 0 | 10 | 122.685 s | 01a176aa-d703-7352-bc62-af08875a31c7 |
| Web review correction | ok / 0 | 4 | 52.208 s | 01a176aa-d703-7352-bc62-af08875a31c7 |
| Documentation and external skill | ok / 0 | 13 | 179.339 s | 01a176aa-dc70-7913-b036-092177123f10 |

Core implementation worker: status ok / worker exit 0, 104 calls, elapsed 1073.102 s, session `01a176a7-d51a-7333-8a47-dee4bce81bd9`, same profile/model and unavailable estimated cost. The surrounding shell exited 1 because a quoting error truncated the brief and attempted invalid trailing shell text. No successful extra action was reported. The remaining requirements were resubmitted with shell-safe quoting in the same session; worker edits were preserved. Its selected core tests/build passed, but socket-dependent tests and race acceptance still require the independent full check.

Core completion follow-up: status ok / exit 0, 32 calls, 313.527 s, session `01a176a7-d51a-7333-8a47-dee4bce81bd9`, same profile/model and unavailable estimated cost. It completed override migration/diagnostics, optional category guidance and isolated lifecycle acceptance. No persisted Operators config map exists, so no additional map migration was added.
