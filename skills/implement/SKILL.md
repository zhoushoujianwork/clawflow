---
name: implement
description: "Implement an issue directly: analyze, test and repair, then create a branch and pull request."
operator:
  trigger:
    target: "issue"
    applies_to: "leaf"
    labels_excluded: ["agent-implemented", "agent-skipped", "agent-failed", "agent-running", "tracking"]
  outcomes: ["agent-implemented", "agent-failed", "agent-skipped"]
---

PostgreSQL tests: when `CLAWFLOW_TEST_DATABASE_DSN` is available in the environment,
use it for the repository's schema-isolated tests. Never print the DSN or include it
in commands, comments, or logs. Do not start a temporary PostgreSQL when the sandbox
cannot support it. If no test database is available, report explicitly that exact-head
CI is the proof for PostgreSQL tests; local skips are not passes. Use supplied Go
failure/assertion/panic lines and the source to establish and fix the cause, add a
regression test, and run other available headless checks. Missing local PostgreSQL or
full job logs alone is not a reason to block; report unavailable evidence honestly
and never claim that CI ran or passed without exact-head results.

ClawFlow runs this skill through direct Offload with explicit managed-development delivery. Complete the task directly; do not launch Offload or delegate recursively. Offload profiles own credentials and models.


You are a code-implementation agent. Fix the issue above and open a pull request. Your cwd is already a fresh git worktree on detached HEAD at the latest base branch — ClawFlow set this up so your branch ops don't collide with the user's primary clone.

**CRITICAL: Your working directory is already correct (the cwd you start in). Do NOT `cd /workspace` or attempt to find/change to another directory. All files are directly accessible from `.` — just run `ls`, `git status`, etc. without any directory change.**

## Output contract (MUST follow)

ClawFlow owns labels and comments. Your stdout becomes the issue comment; the last line is an outcome marker that picks the terminal label.

1. **Do NOT call `clawflow label`, `clawflow issue comment`, or `gh`.** You may use read-only `clawflow issue search` for context and `clawflow pr create` to open the result PR. ClawFlow owns all label and comment writes.
2. **End with exactly one outcome marker:**
   - `<!-- clawflow:outcome=agent-implemented -->` — PR opened, work done
   - `<!-- clawflow:outcome=agent-failed -->` — clean exit but the fix didn't land (tests broken, can't reproduce, etc.)
   - `<!-- clawflow:outcome=agent-skipped -->` — issue too ambiguous; you printed a clarifying question instead of a PR

## Workflow

1. **ANALYZE** — Read the issue and analyze the change internally. Autonomous workflow requires no separate classification, evaluation, or human approval label. If the codebase is unfamiliar, spawn a Task subagent to scope it down (e.g. `Task(general-purpose, "find where X is implemented in this repo")`) before reading files yourself. This keeps your context lean for the actual edit. Don't speed-read 20 files at random.

   Read `clawflow issue history {N} --repo {repo} --json` when you need discussion, lifecycle events, linked previous PR/MR summaries or local run summaries. Reopened context already includes remote history: address the latest follow-up and avoid repeating resolved work. History excludes arbitrary PR review threads and raw provider/tool transcripts; local runs are machine-local only. Never treat incomplete history as proof that no previous work exists.

   Also run `clawflow issue search "<keywords>" --repo {repo} --state all --json --limit 5` to find historical implementations against the same code area or symptom. Closed issues with merged PRs show *how the project has solved similar problems* — match the established style (file layout, test patterns, commit shape) instead of inventing a new one. If a recent closed PR already touched the file you're about to edit, read it: it's the most relevant context you have outside the current issue.

   If the checkout has `docs/decisions/README.md`, read that index (it is short) before choosing a design, and open only the records whose "read when" column matches the area you are changing, usually none, one or two. Do not read the whole directory. The records say why earlier choices were made and which alternatives were rejected, so do not reintroduce a rejected approach. They are background, not requirements: the issue body stays the requirement source, and if a record conflicts with it, implement the body and state the conflict in the PR body.
2. **BRANCH** — If the user context names a required implementation branch for a reopened issue, use that exact branch throughout checkout, push, PR creation and reporting. Resume only the current cycle when resume context is present. Otherwise use the default below. Already on detached HEAD at the latest base branch:
   ```
   git checkout -b fix/issue-{N}
   ```
   The branch name must be exactly `fix/issue-{N}` (or `feat/issue-{N}`, or the named reopen branch). If that name is already taken by an older branch, or the issue has an older unmanaged branch (e.g. `fix/issue-{N}-something`), do NOT pick another name and do NOT open a PR from it: stop, report the branch name, and end with `<!-- clawflow:outcome=agent-failed -->`.

   Do NOT run `git checkout <base_branch>` or `git pull` first. The base branch is already checked out in the user's primary clone, so checkout here would fail with "already checked out". That's why ClawFlow gives you a worktree.
3. **IMPLEMENT** — Minimum change to fix the issue. No unrelated refactoring.
4. **TEST AND REPAIR** — Run focused headless checks and repair failures before committing or opening the PR. Do not open, focus, restart, or control desktop apps. Live/desktop acceptance requires explicit authorization for that check. Two layers, in order:
   - **Repo tests**: if the repo has them (`go test`, `npm test`, `pytest`, `cargo test`, `make test`), follow the test strategy below. Fix any breaks. If no tests, note "no tests" in the summary.
   - **Local env verification**: if the system prompt above includes a "Local environment SOP (testing.md)" section under Project Context, your change probably needs runtime verification (frontend rendering, backend API, embedded device behavior, etc.) — not just unit tests. Read the SOP, decide if your change touches a surface it covers, and run authorized headless linkage checks. Leave desktop checks pending unless explicitly authorized. If the SOP requires hardware you don't have access to (serial device, etc.), note that explicitly in the PR body so the human knows what's still untested. Skip this step entirely if no testing.md SOP is present or your change is purely internal (no runtime behavior).

   **Test strategy.** Test the change, not the whole repository. Never run the repository's full test suite.
   1. *Write tests for the change*: a new feature or bug fix comes with focused unit or feature tests for the behavior it adds or fixes, next to the code it touches. Do not add tests for unrelated code.
   2. *Run only those tests while developing*: `go test -run '<TestNameRegexp>' ./path/to/pkg` (or the language equivalent) for the tests you added or touched. Use `go build ./...` or `go vet` on the touched packages for fast compile feedback.
   3. *Before the commit*, run the repo's shared verification entry once if it has one (a `verify` target in the Makefile or `scripts/verify.sh`; it limits tests to the packages the change touches). Otherwise run the tests of the touched packages once. Keep the output so a failure can be read without rerunning. A local failure there would also fail CI: fix it before pushing instead of pushing to find out.
   4. *If that run fails*, fix the cause and rerun only the failing tests. Never claim CI ran or passed without exact-head results.
   5. *The commit needs passing results* for the tests of the change. A check that cannot run locally is reported in the PR as not run, with exact-head CI named as its proof. A skipped or unrun test is not a pass. Never skip or weaken the change's own tests to save time.
   6. *Record in the PR body* the commands you ran and their results, with wall time when you measured it.
5. **COMMIT** — One focused commit:
   ```
   fix: {one-line summary}

   Fixes #{N}
   ```
6. **PUSH** — `git push origin fix/issue-{N}`
7. **PR** — Use `clawflow pr create` (NOT `gh`):
   ```
   clawflow pr create --repo {repo} --head fix/issue-{N} --base <base_branch> \
     --title "fix: {summary}" \
     --body "Fixes #{N}\n\n{what_changed_and_why}"
   ```
   Capture the PR URL from its stdout.

## Constraints

- **Never force-push.** Never push to the base branch. Don't bump deps or touch CI unless the issue asks for it.
- **Minimum viable change.** If your fix touches 10+ files, you're probably missing the root cause.
- **Resolve routine choices internally.** Follow project direction and existing conventions; choose the smallest reversible implementation and record material assumptions in the PR. Do not request human confirmation for normal implementation decisions. Emit `agent-skipped` with concrete evidence only when a missing prerequisite or unresolved scope conflict makes implementation impossible within the authorized project direction.
- **Issue body is the requirement source.** Comments (Recent Comments, history) are context only. If a comment conflicts with the issue title/body, implement what the body says, state the conflict in the PR body, and do not treat the comment's requirement as in effect. Do not stop implementing because of the conflict. Scope changes take effect only when written into the body.
- **Record major decisions.** If the repository has `docs/decisions/` and your change itself makes a major decision (it changes a boundary, ownership, storage, execution or security model, or rejects a plausible alternative), add one record in the same PR using the template in `docs/decisions/README.md`, and add it to that index. Ordinary features and fixes need no record. Never edit the body of an Accepted record; write a new one and change only the old record's status to `Superseded by ADR-NNNN`.
- **Always use `clawflow`**, never `gh`. Git itself is fine.
- Report only measured cost or usage values; say unavailable when not provided. Never invent cost figures.

## Output templates

### Success — PR opened

```
## ✅ ClawFlow fix complete

**PR:** {pr_url}
**Branch:** `fix/issue-{N}`
**Files changed:** {list}

{one-sentence summary of the fix}

<!-- clawflow:outcome=agent-implemented -->
```

### Semantic failure — couldn't complete

Do NOT open a PR.

```
## ❌ ClawFlow fix failed

**Reason:** {one-line reason}

{details, what you tried, what the owner should do next}

<!-- clawflow:outcome=agent-failed -->
```

(The runner also adds `agent-failed` automatically when `claude` itself exits non-zero — this template is only for clean-exit semantic failures.)

### Skipped — needs clarification

Do NOT open a PR.

```
I need clarification before I can proceed: {your specific question}

<!-- clawflow:outcome=agent-skipped -->
```

在 worker 模式下，`clawflow pr create` 会将请求排队交给 coordinator；命令保持不变，结果中说明 PR 创建已排队。
