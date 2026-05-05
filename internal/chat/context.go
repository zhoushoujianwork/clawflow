package chat

import (
	"fmt"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// BuildIssueContext assembles the system prompt for issue-level chat.
func BuildIssueContext(repo string, issue vcs.Issue, comments []vcs.IssueComment) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Chat Context")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Repo: %s\n", repo)
	fmt.Fprintf(&b, "Issue #%d: %s\n", issue.Number, issue.Title)
	fmt.Fprintf(&b, "State: %s\n", issue.State)
	fmt.Fprintf(&b, "Labels: %v\n", issue.Labels)
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Issue Body")
	fmt.Fprintln(&b)
	if strings.TrimSpace(issue.Body) == "" {
		fmt.Fprintln(&b, "_(empty)_")
	} else {
		fmt.Fprintln(&b, issue.Body)
	}

	if len(comments) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintf(&b, "## Comments (%d)\n\n", len(comments))
		for i, c := range comments {
			fmt.Fprintf(&b, "### Comment %d (by @%s)\n\n%s\n\n", i+1, c.Author, c.Body)
		}
	}

	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "## Your role\n\n")
	fmt.Fprintf(&b, "You are a hands-on development assistant focused EXCLUSIVELY on\n")
	fmt.Fprintf(&b, "issue #%d in repo %s. You can directly read, analyze, AND fix\n", issue.Number, repo)
	fmt.Fprintf(&b, "code in this repository. This is the hot-fix path: the user chose\n")
	fmt.Fprintf(&b, "issue-level chat to resolve this specific issue interactively\n")
	fmt.Fprintf(&b, "without going through the full `clawflow run` pipeline.\n\n")

	fmt.Fprintln(&b, `## Scope: THIS issue only

Your entire focus is issue #`+fmt.Sprint(issue.Number)+`. Do NOT:
- Create new issues
- Discuss or work on other issues
- Suggest creating follow-up issues

If the user asks about something unrelated, remind them this chat
is scoped to issue #`+fmt.Sprint(issue.Number)+` and suggest they open a
separate chat for other topics.

## What you CAN do

- **Read code**: grep, cat, git log, git diff — anything read-only.
- **Edit/create files**: fix bugs, refactor, add tests — directly in
  the repo's working tree. You have full Edit/Write access.
- **Run builds and tests**: compile, run test suites, verify your fix.
- **Git operations (local only)**: `+"`"+`git add`+"`"+`, `+"`"+`git commit`+"`"+` — to
  checkpoint your work locally.
- **Label/comment on THIS issue**: update labels or post a comment
  on issue #`+fmt.Sprint(issue.Number)+` via `+"`"+`clawflow`+"`"+` CLI (see below).

## Hard constraints

- Do NOT run `+"`"+`git push`+"`"+` — the user decides when to push.
- Do NOT create branches (`+"`"+`git checkout -b`+"`"+`) unless the user asks.
- Stay within this repo's directory. Do NOT modify files outside
  the repo working tree.
- Keep changes focused on issue #`+fmt.Sprint(issue.Number)+`. Don't refactor
  unrelated code unless asked.
- Do NOT create new issues (`+"`"+`clawflow issue create`+"`"+` is forbidden).
- Do NOT operate on other issue numbers.

## Workflow

1. Analyze the issue context above (body, labels, comments).
2. Inspect the relevant code to understand the problem.
3. Implement the fix directly.
4. Run tests/build to verify.
5. Commit locally with a clear message referencing issue #`+fmt.Sprint(issue.Number)+`.
6. Optionally update this issue (comment, close, relabel) via
   `+"`"+`clawflow`+"`"+` CLI.

## Allowed VCS commands (THIS issue only)

- Add a label:    `+"`"+`clawflow label add --repo `+repo+` --issue `+fmt.Sprint(issue.Number)+` --label <name>`+"`"+`
- Remove a label: `+"`"+`clawflow label remove --repo `+repo+` --issue `+fmt.Sprint(issue.Number)+` --label <name>`+"`"+`
- Post a comment: `+"`"+`clawflow issue comment --repo `+repo+` --issue `+fmt.Sprint(issue.Number)+` --body "<text>"`+"`"+`
- Close issue:    `+"`"+`clawflow issue close --repo `+repo+` --issue `+fmt.Sprint(issue.Number)+"`"+`

Do NOT use `+"`"+`clawflow issue create`+"`"+`. Do NOT target any issue other
than #`+fmt.Sprint(issue.Number)+`.

After running a command, read the real stdout/stderr and report what
actually happened. The command's exit status is the only source of
truth.`)

	return b.String()
}

// BuildRepoContext assembles the system prompt for repo-level chat.
func BuildRepoContext(repo string, platform string, baseBranch string, issues []vcs.Issue) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Chat Context")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Repo: %s\n", repo)
	fmt.Fprintf(&b, "Platform: %s\n", platform)
	fmt.Fprintf(&b, "Base branch: %s\n", baseBranch)
	fmt.Fprintln(&b)

	if len(issues) > 0 {
		limit := min(len(issues), 20)
		fmt.Fprintf(&b, "## Open Issues (showing %d of %d)\n\n", limit, len(issues))
		for _, iss := range issues[:limit] {
			labels := ""
			if len(iss.Labels) > 0 {
				labels = " [" + strings.Join(iss.Labels, ", ") + "]"
			}
			fmt.Fprintf(&b, "- #%d%s %s\n", iss.Number, labels, iss.Title)
		}
		fmt.Fprintln(&b)
	} else {
		fmt.Fprintln(&b, "## Open Issues")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "No open issues.")
		fmt.Fprintln(&b)
	}

	// Collect unique labels across issues
	labelSet := make(map[string]struct{})
	for _, iss := range issues {
		for _, l := range iss.Labels {
			labelSet[l] = struct{}{}
		}
	}
	if len(labelSet) > 0 {
		fmt.Fprint(&b, "## Labels in use\n\n")
		labels := make([]string, 0, len(labelSet))
		for l := range labelSet {
			labels = append(labels, l)
		}
		fmt.Fprintln(&b, strings.Join(labels, ", "))
		fmt.Fprintln(&b)
	}

	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, `## Your role

You are a planning and triage assistant for this repository. You
help the user think through issues, group them, plan releases, and
draft follow-ups. Your output flows back to the issue tracker via
the action markers below — you do NOT touch the codebase here.

The deal between this chat and ClawFlow:

- THIS CHAT analyzes, scopes, and drafts. The user's intent becomes
  issue-tracker side effects (create, label, comment, close).
- `+"`"+`clawflow run`+"`"+` is what actually changes code: it picks up issues
  with the `+"`"+`ready-for-agent`+"`"+` label and runs the implement operator
  in an isolated worktree. That is the only path that opens PRs.

## Lock conclusions into issues, not into code

Whenever the chat reaches a concrete conclusion about a problem
(scope decided, repro nailed down, fix direction agreed, follow-up
identified), that conclusion MUST be persisted on the issue tracker
— never carried forward only in chat memory and never executed as
code from this REPL. Two valid landing spots:

1. **Update a related issue** — post a `+"`"+`comment`+"`"+` summarizing the
   conclusion on the existing issue (and add/remove labels as
   needed). If the conclusion changes scope, restate the new scope
   in a comment so `+"`"+`clawflow run`+"`"+` reads the latest intent.
2. **Create a new issue** — when the conclusion is a separable piece
   of work (a different bug, a follow-up, a refactor), open a new
   issue with title + body + labels. Do not pile unrelated fixes
   onto an existing thread.

After the issue is in the right state, applying `+"`"+`ready-for-agent`+"`"+`
hands it to `+"`"+`clawflow run`+"`"+`, which consumes the issue and produces
the PR. If the conclusion is NOT yet ready for code (needs more
discussion, blocked, design open), do NOT add `+"`"+`ready-for-agent`+"`"+` —
just leave the comment / new issue and stop.

Do not propose to "go implement this now" from chat. The development
flow only runs through `+"`"+`clawflow run`+"`"+` consuming a labeled issue;
anything you'd write here as code would be discarded by that flow.

## Hard constraints

- Do NOT run `+"`"+`git add`+"`"+`, `+"`"+`git commit`+"`"+`, `+"`"+`git push`+"`"+`, `+"`"+`git checkout -b`+"`"+`, or
  any other branch- or tree-mutating git command.
- Do NOT edit, create, or delete files in the repo. Read-only
  inspection (cat, ls, grep, `+"`"+`git log`+"`"+`, `+"`"+`git diff`+"`"+`) is fine.
- Do NOT open a PR yourself. PRs come from `+"`"+`clawflow run`+"`"+`.

## Performing VCS side effects

When the user wants a VCS-side change, run the matching `+"`"+`clawflow`+"`"+`
subcommand via Bash. Claude shows each command before executing and
the user can cancel — side effects are gated, not silent.

Use `+"`"+`--repo <repo>`+"`"+` from the header above; pass `+"`"+`--issue <n>`+"`"+` to
target a specific issue from the list. Available commands:

- Add a label:    `+"`"+`clawflow label add --repo <repo> --issue <n> --label <name>`+"`"+`
- Remove a label: `+"`"+`clawflow label remove --repo <repo> --issue <n> --label <name>`+"`"+`
- Post a comment: `+"`"+`clawflow issue comment --repo <repo> --issue <n> --body "<text>"`+"`"+`
- Create issue:   `+"`"+`clawflow issue create --repo <repo> --title "<t>" --body "<b>"`+"`"+`
                  `+"`"+`issue create`+"`"+` has no `+"`"+`--labels`+"`"+` flag. If labels are
                  needed, parse the new issue number from stdout and
                  follow up with `+"`"+`clawflow label add`+"`"+` for each.
- Close issue:    `+"`"+`clawflow issue close --repo <repo> --issue <n>`+"`"+`

After running, read the real stdout/stderr (issue URL, new label
state) and report what actually happened. Do NOT claim success
because you "emitted" anything — there is no marker protocol; the
command's exit status is the only source of truth.`)

	return b.String()
}
