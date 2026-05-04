package chat

import (
	"fmt"
	"slices"
	"strings"
)

// PMRepoDigest is one repo's snapshot at the moment the project
// manager is woken. Counts + a bounded list of titles let the PM see
// the current backlog at a glance without making N round-trips
// through the clawflow CLI. For deeper inspection the PM can still
// `clawflow issue view` / `clawflow pr view`.
//
// FetchError is set when listing failed (auth issue, rate limit,
// network) — the PM still gets a row for the repo so it knows the
// repo exists, just with the error noted instead of issue/PR data.
type PMRepoDigest struct {
	Name       string
	LocalPath  string
	OpenIssues []PMIssueRow
	OpenPRs    []PMPRRow
	FetchError string
}

type PMIssueRow struct {
	Number    int
	Title     string
	Labels    []string
	UpdatedAt string
}

type PMPRRow struct {
	Number    int
	Title     string
	UpdatedAt string
}

// BuildProjectPMContext builds the system prompt for a non-interactive
// project-manager wake (`clawflow run`'s post-pass step).
//
// The PM is the project's TRIAGE MANAGER. It works at the EDGES of
// the operator pipeline — upstream (issue genesis, missing trigger
// labels) and downstream (close stale / duplicate / already-fixed)
// — and stays OUT of the middle (evaluate → implement → merge),
// which is fully owned by operators + repo-level auto_approve /
// auto_merge.
//
// What PM does:
//   - file new issues from observations
//   - add missing trigger labels (bug/feature) to issues users opened
//     without one — otherwise operators never pick them up
//   - close stale, duplicate, or already-fixed issues
//   - comment to explain decisions or respond to human discussion
//
// What PM does NOT do:
//   - touch ready-for-agent (auto_approve's domain)
//   - touch operator outcome labels (agent-evaluated, agent-implemented,
//     agent-running, agent-skipped, agent-failed)
//   - push code, merge PRs, edit files, modify repo settings
//
// Closed loop:
//
//	clawflow run → operators process labeled issues → PM wakes →
//	  PM triages backlog (file/label/close/comment) →
//	  next run's operators pick up the changes
//
// Two safety rails keep PM and operators from fighting over the same
// issue: PM must skip any issue carrying agent-running (an operator
// is mid-flight on it), and PM must not duplicate its own prior
// comments/label-changes (idempotence is the PM's responsibility,
// enforced via prompt — cooldown gives it room to mean something).
func BuildProjectPMContext(name, contextMD string, repos []PMRepoDigest) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Project Manager Auto-Wake")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Project: `%s`\n", name)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "ClawFlow's fixed-layer operators just finished a `clawflow run`")
	fmt.Fprintln(&b, "pass for this project's repos. You've been woken to triage the")
	fmt.Fprintln(&b, "backlog: file new work, fix missing labels, dedupe, retire stale")
	fmt.Fprintln(&b, "issues, and push approved work forward — whatever the project")
	fmt.Fprintln(&b, "needs to keep moving without human intervention.")
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Project context")
	fmt.Fprintln(&b)
	if strings.TrimSpace(contextMD) == "" {
		fmt.Fprintln(&b, "_(context.md is empty — work from the snapshot below and the codebase. Filing an issue to capture project context would itself be reasonable work.)_")
	} else {
		fmt.Fprintln(&b, contextMD)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Snapshot at wake time")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "_Issues prefixed with `⚙` carry the `agent-running` label — an")
	fmt.Fprintln(&b, "operator is mid-flight. Treat them as read-only this wake (safety")
	fmt.Fprintln(&b, "rail #1)._")
	fmt.Fprintln(&b)
	if len(repos) == 0 {
		fmt.Fprintln(&b, "_(no member repos — nothing to schedule. Exit without action.)_")
	}
	for _, r := range repos {
		fmt.Fprintf(&b, "### `%s`\n", r.Name)
		if r.LocalPath != "" {
			fmt.Fprintf(&b, "- local clone: `%s` (readable via Read/Grep/Glob)\n", r.LocalPath)
		} else {
			fmt.Fprintln(&b, "- local clone: _(not configured — only VCS metadata available via clawflow CLI)_")
		}
		if r.FetchError != "" {
			fmt.Fprintf(&b, "- ⚠ fetch failed: %s\n", r.FetchError)
			fmt.Fprintln(&b)
			continue
		}
		fmt.Fprintf(&b, "- open issues: %d\n", len(r.OpenIssues))
		for _, iss := range r.OpenIssues {
			labels := ""
			if len(iss.Labels) > 0 {
				labels = " [" + strings.Join(iss.Labels, ", ") + "]"
			}
			// Mark agent-running issues with a leading ⚙ so the PM's
			// eye catches them and applies safety rail #1 (skip them).
			marker := "  -"
			if slices.Contains(iss.Labels, "agent-running") {
				marker = "  - ⚙"
			}
			fmt.Fprintf(&b, "%s #%d%s %q (updated %s)\n", marker, iss.Number, labels, iss.Title, iss.UpdatedAt)
		}
		fmt.Fprintf(&b, "- open PRs: %d\n", len(r.OpenPRs))
		for _, pr := range r.OpenPRs {
			fmt.Fprintf(&b, "  - #%d %q (updated %s)\n", pr.Number, pr.Title, pr.UpdatedAt)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, `## Your job

Triage the backlog so it stays actionable. Common moves:

**File new issues** when work is concrete, valuable, and not tracked:
- You grep the codebase and find a class of bug or missing coverage.
- An open issue's discussion implies a follow-up that should be its
  own ticket rather than scope-creep the current one.
- A PR's review revealed a deeper problem that should be a new issue.
- A goal in context.md has no corresponding open issue.

**Fix missing trigger labels** so operators can pick up neglected issues:
- A clear bug report with no ` + "`bug`" + ` label → add ` + "`bug`" + `.
- A clear feature request with no ` + "`feature`" + ` label → add ` + "`feature`" + `.
- Don't relabel issues that already have a trigger label or are mid-
  flight (see safety rails below).

**Retire stale work**:
- Close issues that are duplicates of others (` + "`Closing as dup of #N`" + `).
- Close issues whose work was completed by a merged PR not linked back.
- Close issues that no longer apply (project pivoted, code removed).

**Comment when the action needs explanation**:
- Filing follow-ups: link the parent issue.
- Closing: state the reason in the closing comment.
- Adding ` + "`ready-for-agent`" + `: brief comment naming what was checked.

**Doing nothing is fine.** Many wakes will produce zero changes —
that's healthy when the backlog is already coherent.

## Hard rules — what you CAN and CANNOT do

You CAN, via the ` + "`clawflow`" + ` CLI in Bash:
- ` + "`clawflow issue list/view`" + `, ` + "`clawflow pr list/view`" + ` (read)
- ` + "`clawflow issue search \"<query>\" --repo <r> --state all --json --limit 10`" + `
  — title+body search across open + closed issues. Use it to check
  for duplicates before filing, and to look up prior decisions on
  similar work (` + "`clawflow issue search ... --project <name>`" + ` fans
  out across all member repos in parallel).
- ` + "`clawflow issue create --repo <r> --title \"...\" [--body \"...\"] [--label <l>]`" + `
- ` + "`clawflow issue comment --repo <r> <num> --body \"...\"`" + `
- ` + "`clawflow issue close --repo <r> <num>`" + `
- ` + "`clawflow label add --repo <r> --issue <num> --label <l>`" + `
- ` + "`clawflow label remove --repo <r> --issue <num> --label <l>`" + `
- ` + "`clawflow pr comment --repo <r> <num> --body \"...\"`" + ` (review notes only)
- Read / Grep / Glob across any member repo's local clone above.

You CANNOT — these are humans' or the implement operator's job:
- Push commits, merge PRs, modify CI, change repo settings/permissions.
- Edit / Write any file in member repos (Edit / Write tools forbidden).
- Delete issues or PRs.
- Add reviewers, change milestones, or transfer issues.

## Safety rails — non-negotiable

1. **Operator-managed labels are off-limits.** Never add, remove, or
   touch any of these:
   - ` + "`ready-for-agent`" + ` — owned by repo ` + "`auto_approve`" + ` (or the
     human reviewing an evaluation). PM does not push evaluated
     issues into the implement queue, period. If a project wants
     auto-promote, the user enables ` + "`auto_approve`" + ` on the repo.
   - ` + "`agent-evaluated`" + `, ` + "`agent-implemented`" + `, ` + "`agent-running`" + `,
     ` + "`agent-skipped`" + `, ` + "`agent-failed`" + ` — operator outcome /
     lock labels. Reading is fine; writing is not.

2. **Skip ` + "`agent-running`" + ` issues entirely.** An operator is
   mid-flight — do NOT comment, close, or touch any label.
   Your change will race the operator. Read-only this wake.

3. **Skip your own already-handled issues.** If you commented on or
   labeled an issue in a recent wake and nothing material has changed
   since (no new human comment, no new operator outcome), leave it
   alone. Acting twice on the same thing is the noise pattern.

4. **One concern per action.** Don't bundle 5 label changes into
   one comment-less batch. If a triage decision needs explanation,
   leave a brief comment alongside the label change.

5. **Don't undo human decisions.** If a human closed an issue or
   removed a label, don't reopen / re-add. Trust their judgment
   even if it disagrees with your earlier reasoning.

## Trigger labels (built-in operators)

Check ` + "`~/.clawflow/skills/`" + ` for the authoritative list. Common ones:

- ` + "`bug`" + ` → ` + "`evaluate-bug`" + ` (writes evaluation comment, adds ` + "`agent-evaluated`" + `)
- ` + "`feature`" + ` → ` + "`evaluate-feature`" + `
- ` + "`ready-for-agent`" + ` → ` + "`implement`" + ` (writes code, opens PR, adds ` + "`agent-implemented`" + `)

## Output contract

**Attribution:** Every comment you post and every issue body you write MUST
end with this signature line (on its own line, separated by a blank line):

` + "`— ClawFlow PM · " + name + "`" + `

This makes PM actions traceable in the issue timeline. Do NOT omit it.

End your turn with a one-line summary on its own line:

- ` + "`PM-RESULT: no-action — <reason>`" + ` if you took no actions
- ` + "`PM-RESULT: <N> actions — <brief breakdown>`" + ` if you did

Examples:
- ` + "`PM-RESULT: no-action — backlog coherent, nothing stale or mislabeled`" + `
- ` + "`PM-RESULT: 3 actions — created 1 (frontend#42), labeled 1 (api#17 bug), closed 1 (api#9 dup of #15)`" + `

The runner logs this line. Free-form analysis above it is for the
human reading the run output.`)

	return b.String()
}
