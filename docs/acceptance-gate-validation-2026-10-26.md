# Independent acceptance gate validation — 2026-10-26

Scope: bounded local head-review gate for [#349](https://github.com/zhoushoujianwork/clawflow/issues/349). [Usage](independent-acceptance.md) and [technical limits](independent-acceptance-detail.md) distinguish this implementation from the distributed platform target.

## Submitted changes

| Commit | Change |
|---|---|
| `4c4f8f5` | Integrate the existing Offload execution branch with the platform baseline |
| `849c82e` | Conditional GitHub merge at the expected head; validate confirmed merge results |
| `5c40ef0` | Independent planning/review contract, per-attempt private evidence and latest-result lookup |
| `94777d9` | Explicit acceptance commands, repository enrollment, manual/automatic merge gate and requirements snapshots |
| `a29f755` | Reject conflicting or incorrectly cased JSON schema fields |
| `9d9068f` | Usage and technical documentation |
| `34662e2` | Accept production early events before native session binding; preserve terminal provenance checks |

Enrollment defaults false. No repository monitoring, automatic merge, runtime configuration or running service was changed by this slice. Changes remain local; no push, PR creation or merge was performed.

## Parent validation

The full headless suite passed 1,254 tests before the exact-schema correction. After that correction, `go test -race ./...` passed 1,272 tests across 27 packages. Web production build and TypeScript checking passed during integration. CLI build and acceptance command help checks passed. Documentation links/fences and `git diff --check` passed.

After the session-binding correction, `go test -race ./cmd/clawflow/commands ./internal/acceptance ./internal/execution` passed 583 tests across the three affected packages. No desktop interaction was used.

## Independent findings and repair loop

Independent reviewer session `01a17788-4e12-7e12-a6e2-615bd90cd96c` recorded its adversarial plan before source inspection.

1. Initial core review: FAIL. Case-insensitive struct decoding could let `Status` override `status`. The provider conditional-merge slice passed. Fixed in `a29f755` with exact-schema and conflicting-case regressions.
2. Full candidate review: FAIL. Acceptance rejected sessionless early events permitted by production execution. Fixed in `34662e2` with production-shaped success and invalid final/changed-session cases.
3. The second review also reported an incompatible generic PATH Offload binary. A separate narrow review corrected the scope: the actual configured command is `/Users/mikas/.clawflow/runtime/offload-ae202ff`, and its help recognizes the required protocol flags. Help exits 2 with `flag: help requested`; no provider worker was launched. This establishes argument availability, not live execution.

Final verdict: **PASS for the bounded local head-review gate** at `34662e2d1b1634c54ea9610f5478547d4eb8fa6b`. The independent reviewer confirmed both original defects were fixed and earlier protections remained unchanged. It reran acceptance/provider tests and uncached command acceptance tests (6.472 s), all exit 0. This supersedes the earlier FAIL verdicts for the fixed scope; it is not production-readiness approval. No live-provider or GitHub Actions success is claimed.

## Worker evidence

All sessions below used `codex-gpt-6.1-sol-medium-fast` / `gpt-6.1-sol`, exit 0 for the worker process. A successful worker process can report a FAIL verdict. Cost is an estimated API-equivalent worker cost, not billing, total parent cost or measured savings. Times include backend waiting.

| Assignment | Session suffix | Tool calls | Elapsed seconds | Estimated USD |
|---|---|---:|---:|---:|
| Integration feasibility | `01a1775d…` | 6 | 750.030 | 0.3599 |
| Fixture startup correction | `01a17783…` | 8 | 436.620 | 0.1449 |
| Acceptance core | `01a17786…` | 24 | 694.501 | 0.7029 |
| Core retry/provenance corrections | `01a17786…` | 14 | 789.015 | 0.4958 |
| Conditional provider implementation | `01a1775d…` | 8 | 443.296 | 0.4172 |
| Command integration | `01a1775d…` | 27 | 877.857 | 1.5529 |
| Manual gate/requirements/lock corrections | `01a1775d…` | 15 | 164.232 | 1.4311 |
| Independent acceptance plan | `01a17788…` | 0 | 98.257 | 0.0545 |
| Initial independent review: FAIL core | `01a17788…` | 17 | 109.818 | 0.3116 |
| Exact-schema repair | `01a17786…` | 5 | 76.185 | 0.4339 |
| Full independent review: FAIL events | `01a17788…` | 24 | 359.184 | 0.5150 |
| Runtime finding correction | `01a17788…` | 4 | 62.537 | 0.2920 |
| Session-binding repair | `01a1775d…` | 4 | 69.520 | 0.2615 |
| Final independent reacceptance: PASS | `01a17788…` | 4 | 62.798 | 0.2412 |

Full session IDs: development/provider `01a1775d-259f-7a23-af17-ff0cf723c5c1`; core `01a17786-47ec-7d83-a62c-0e146754b5b3`; fixture `01a17783-30be-7263-a749-62dec85e9fa4`; independent acceptance `01a17788-4e12-7e12-a6e2-615bd90cd96c`.

## Remaining acceptance boundaries

Read-only enforcement depends on the selected backend and same-host trust. Local PID locking and private JSON evidence do not supply distributed fencing or a transactional effect ledger. Head-conditional merging does not atomically pin the base or verify the resulting target branch. Actions provenance, automated acceptance dispatch and scale validation remain subsequent issues. Legacy development records without captured requirements are intentionally ineligible.
