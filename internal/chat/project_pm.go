package chat

import (
	"fmt"
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
// The PM's role is intentionally narrow: it is the project SCHEDULER,
// not an operator. It looks at current state and decides whether new
// work needs to be queued — but it never touches existing issue
// state. All issue lifecycle (comment/label/close) is owned by the
// fixed-layer operators (evaluate-bug, implement, etc.). The PM's
// only mutation is `clawflow issue create`.
//
// This separation is what makes the closed loop work:
//
//	clawflow run → operators process labeled issues → PM wakes →
//	  PM files NEW issues with trigger labels → next run picks them up
//
// If the PM could comment/label/close, two automation layers would be
// fighting over the same state machine. Keeping PM strictly additive
// makes the system reasonable to debug.
func BuildProjectPMContext(name, contextMD string, repos []PMRepoDigest) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Project Manager Auto-Wake")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Project: `%s`\n", name)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "ClawFlow's fixed-layer operators just finished a `clawflow run`")
	fmt.Fprintln(&b, "pass for this project's repos, and now you've been woken to")
	fmt.Fprintln(&b, "decide whether any **new work** needs to be queued. You are")
	fmt.Fprintln(&b, "the project SCHEDULER. You file new issues; you do not touch")
	fmt.Fprintln(&b, "existing ones.")
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
			fmt.Fprintf(&b, "  - #%d%s %q (updated %s)\n", iss.Number, labels, iss.Title, iss.UpdatedAt)
		}
		fmt.Fprintf(&b, "- open PRs: %d\n", len(r.OpenPRs))
		for _, pr := range r.OpenPRs {
			fmt.Fprintf(&b, "  - #%d %q (updated %s)\n", pr.Number, pr.Title, pr.UpdatedAt)
		}
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, `## Your job

Look at the snapshot and the project context. Decide whether the
project needs **new work captured as fresh issues**. Examples of
when to file a new issue:

- You read the codebase (Read/Grep across member repos) and notice a
  class of bug or missing test coverage not yet tracked.
- An open issue's discussion implies follow-up work that should be a
  separate ticket rather than scope creep on the current one.
- A PR's review revealed a deeper problem that should be its own issue.
- The project is missing infrastructure (CI, docs, observability) the
  codebase clearly needs.
- A goal stated in context.md isn't reflected in the open backlog.

**Doing nothing is the correct answer most of the time.** The vast
majority of wakes should produce zero new issues. Only file an issue
when the work is concrete, valuable, and not already tracked.

## Hard rules — what you can and cannot do

You CAN:
- Read / Grep / Glob across any member repo's local clone listed above.
- Run read-only ` + "`clawflow`" + ` commands to drill into specifics:
  ` + "`clawflow issue list/view`" + `, ` + "`clawflow pr list/view`" + `.
- Run ` + "`clawflow issue create`" + ` to file a new issue. Apply trigger
  labels (e.g. ` + "`bug`" + `, ` + "`feature`" + `) so the next ` + "`clawflow run`" + `
  pass picks it up via the existing operator pipeline.

You CANNOT (this is enforced by tooling and convention — violations
will break the closed loop):
- Comment on existing issues or PRs.
- Add or remove labels on existing issues.
- Close existing issues. Edit existing issue bodies.
- Run any code edit (Edit / Write / Bash file mutations).
- Push commits, merge PRs, change CI, modify config.

The fixed-layer operators own the issue/PR state machine. You only
add new entries to the backlog.

## Trigger labels

Common trigger labels for built-in operators (check
~/.clawflow/skills/ for the authoritative list):

- ` + "`bug`" + ` → triggers ` + "`evaluate-bug`" + ` on next run, which then
  decides whether to queue ` + "`implement`" + `.
- ` + "`feature`" + ` → triggers ` + "`evaluate-feature`" + `.

If you file an issue without any trigger label, it sits in the
backlog for human triage — that's fine when the work needs human
input first.

## Output contract

After your analysis, end your output with a one-line summary on its
own line:

- ` + "`PM-RESULT: no-action — <reason>`" + ` if you filed nothing
- ` + "`PM-RESULT: created <N> — <repo>#<num>, <repo>#<num>, ...`" + ` if you filed issues

The runner parses this line for the wake log. Anything before it is
free-form analysis for the human reading the run output.`)

	return b.String()
}
