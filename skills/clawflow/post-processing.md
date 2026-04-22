# Phase 5 — Post-processing (Smoke Test → Merge → Cleanup)

After the sub-agent returns a PR URL, proceed through the following steps. **Regardless of success or failure, the worktree must always be cleaned up.**

---

## Step 5.0 — Handle split_done (Auto-close Main Issue)

The `split_done` list in `clawflow harvest` output contains main issues where all sub-issues have been closed.
For each main issue in `split_done`:

```bash
# 1. Summarize sub-issue list (extract from main issue comments)
clawflow issue comment-list --repo {owner}/{repo} --issue {number}

# 2. Post summary comment and close main issue
clawflow issue comment --repo {owner}/{repo} --issue {number} \
  --body "## ✅ All Sub-tasks Completed

The following sub-issues have all been closed — closing main issue automatically:

{sub-issue list, one per line: - #{n}: {title}}

---
🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow)"

# 3. Close main issue
clawflow issue close --repo {owner}/{repo} --issue {number}
```

---

## Step 5.5 — Smoke Test

> ClawFlow only runs smoke tests: build passes + unit tests within the change impact scope.
> E2E / integration / real-world scenario testing requires manual owner verification.

```bash
# 1. Detect language and test commands
LANG_INFO=$(clawflow lang detect --repo {owner}/{repo} --issue {number})
BUILD_CMD=$(echo "$LANG_INFO" | jq -r '.build_cmd')
TEST_CMD=$(echo "$LANG_INFO" | jq -r '.test_cmd')

# 2. Run build in worktree
cd {worktree_path}
eval "$BUILD_CMD"
```

**Build failure**: sub-agent fixes within the worktree, max 2 retries; if still failing, go to failure handling.

```bash
# 3. Run impact-scope tests
eval "$TEST_CMD"
```

**Test failure**: sub-agent fixes within the worktree, max 2 retries; if still failing, go to failure handling.

Proceed to Step 5.6 after smoke tests pass.

---

## Step 5.6 — Conflict Detection and Rebase

```bash
MERGE_STATUS=$(clawflow pr view --repo {owner}/{repo} --pr {pr_number} | grep mergeable || echo "unknown")
```

**When conflicts exist** (max 2 retries):

```bash
clawflow pr rebase --repo {owner}/{repo} --issue {number}
```

After successful rebase, re-run smoke tests (back to Step 5.5).

If rebase fails more than 2 times:

```bash
clawflow pr comment --repo {owner}/{repo} --pr {pr_number} \
  --body "⚠️ Automatic rebase failed due to complex conflicts. Please resolve manually."
```

Then proceed to failure handling (keep the PR open).

---

## Step 5.7 — CI Wait

```bash
clawflow pr ci-wait --repo {owner}/{repo} --pr {pr_number} --timeout 600
```

| CI Result | Action |
|-----------|--------|
| Pass | Continue to Step 5.8 |
| Fail | Go to CI failure handling |
| No CI configured | Proceed directly to Step 5.8 |

---

## Step 5.8 — Auto Merge

Only executes when the repo config has `auto_merge: true`:

```bash
clawflow pr merge --repo {owner}/{repo} --pr {pr_number}
```

### Success Cleanup (both auto_merge=true and false)

```bash
clawflow memory write --repo {owner}/{repo} --issue {number} --status success --pr-url {pr_url}
clawflow label remove --repo {owner}/{repo} --issue {number} --label in-progress
clawflow label remove --repo {owner}/{repo} --issue {number} --label ready-for-agent
clawflow label remove --repo {owner}/{repo} --issue {number} --label agent-queued
clawflow worktree remove --repo {owner}/{repo} --issue {number}
```

> When `auto_merge: false` (default), PR awaits owner review before manual merge.

---

## Failure Handling

```bash
# 1. Write to memory
clawflow memory write --repo {owner}/{repo} --issue {number} --status failed --reason "{error}"

# 2. Add agent-failed label, remove in-progress
clawflow label add    --repo {owner}/{repo} --issue {number} --label agent-failed
clawflow label remove --repo {owner}/{repo} --issue {number} --label in-progress

# 3. Comment failure reason on issue
clawflow issue comment --repo {owner}/{repo} --issue {number} \
  --body "ClawFlow agent failed: {error}"

# 4. Clean up worktree (must execute, even on failure)
clawflow worktree remove --repo {owner}/{repo} --issue {number}
```

## CI Failure Handling

```bash
# 1. Write to memory
clawflow memory write --repo {owner}/{repo} --issue {number} --status ci-failed --reason "{ci_error}"

# 2. Add agent-failed label, remove in-progress
clawflow label add    --repo {owner}/{repo} --issue {number} --label agent-failed
clawflow label remove --repo {owner}/{repo} --issue {number} --label in-progress

# 3. Comment CI failure reason on PR
clawflow pr comment --repo {owner}/{repo} --pr {pr_number} \
  --body "⚠️ CI checks failed. Please review manually: {ci_error}"

# 4. Clean up worktree (must execute)
clawflow worktree remove --repo {owner}/{repo} --issue {number}
```

> PR is kept open so the owner can review CI logs and decide whether to fix manually.

## LLM Quota Exhaustion Handling

**Detection conditions (any one triggers):**
- API returns `overloaded_error` / `rate_limit_error` / `quota_exceeded`
- Output contains `Credit balance is too low` / `Usage limit reached` / `insufficient_quota`

```bash
# 1. Write to memory
clawflow memory write --repo {owner}/{repo} --issue {number} --status failed --reason "LLM quota exhausted"

# 2. Remove in-progress, add agent-failed
clawflow label remove --repo {owner}/{repo} --issue {number} --label in-progress
clawflow label add    --repo {owner}/{repo} --issue {number} --label agent-failed

# 3. Notify owner via comment
clawflow issue comment --repo {owner}/{repo} --issue {number} \
  --body "⚠️ ClawFlow paused: LLM API quota exhausted. Please top up your quota and run \`clawflow retry --repo {owner}/{repo} --issue {number}\` to re-trigger."

# 4. Clean up worktree
clawflow worktree remove --repo {owner}/{repo} --issue {number}
```

**Critical**: Once quota exhaustion is detected, immediately terminate the entire harvest round — do not continue processing other queued issues.
