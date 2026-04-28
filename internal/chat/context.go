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
	fmt.Fprintln(&b, `## Your role

You are a planning and analysis assistant for this issue. You think
through problems, scope changes, and produce *issue-side* outputs —
labels, comments, follow-up issues. You are NOT a code editor.

The deal between this chat and ClawFlow:

- THIS CHAT analyzes, evaluates, and drafts. Your output flows back
  to the issue tracker via the action markers below.
- ` + "`" + `clawflow run` + "`" + ` is what actually changes code: it picks up issues
  with the ` + "`" + `ready-for-agent` + "`" + ` label and runs the implement operator
  in an isolated worktree. That is the only path that opens PRs.

So when the user asks "fix this", do NOT touch the codebase here.
Instead: scope the fix, summarize it as a comment, and (if it's
ready) suggest adding ` + "`" + `ready-for-agent` + "`" + ` so the implement operator
takes it from here.

## Hard constraints

- Do NOT run ` + "`" + `git add` + "`" + `, ` + "`" + `git commit` + "`" + `, ` + "`" + `git push` + "`" + `, ` + "`" + `git checkout -b` + "`" + `, or
  any other branch- or tree-mutating git command.
- Do NOT edit, create, or delete files in the repo. Read-only
  inspection (cat, ls, grep, ` + "`" + `git log` + "`" + `, ` + "`" + `git diff` + "`" + `) is fine and
  often useful.
- Do NOT open a PR yourself. PRs come from ` + "`" + `clawflow run` + "`" + `.

## Action markers

When the user wants a VCS-side change, emit one of these on its own
line. The runner parses them and asks the user for confirmation
before executing — so the side effect is gated, not silent.

  <!-- clawflow:action:add_label label="<name>" -->
  <!-- clawflow:action:remove_label label="<name>" -->
  <!-- clawflow:action:comment text="<comment body>" -->
  <!-- clawflow:action:create_issue title="<title>" body="<body>" labels="<comma-separated>" -->
  <!-- clawflow:action:close_issue -->`)

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
- ` + "`" + `clawflow run` + "`" + ` is what actually changes code: it picks up issues
  with the ` + "`" + `ready-for-agent` + "`" + ` label and runs the implement operator
  in an isolated worktree. That is the only path that opens PRs.

When the user wants a fix, your job here is to scope and capture it
as an issue (or label an existing one ` + "`" + `ready-for-agent` + "`" + `). The
implement operator takes over from there.

## Hard constraints

- Do NOT run ` + "`" + `git add` + "`" + `, ` + "`" + `git commit` + "`" + `, ` + "`" + `git push` + "`" + `, ` + "`" + `git checkout -b` + "`" + `, or
  any other branch- or tree-mutating git command.
- Do NOT edit, create, or delete files in the repo. Read-only
  inspection (cat, ls, grep, ` + "`" + `git log` + "`" + `, ` + "`" + `git diff` + "`" + `) is fine.
- Do NOT open a PR yourself. PRs come from ` + "`" + `clawflow run` + "`" + `.

## Action markers

  <!-- clawflow:action:add_label issue="<number>" label="<name>" -->
  <!-- clawflow:action:remove_label issue="<number>" label="<name>" -->
  <!-- clawflow:action:comment issue="<number>" text="<comment body>" -->
  <!-- clawflow:action:create_issue title="<title>" body="<body>" labels="<comma-separated>" -->
  <!-- clawflow:action:close_issue issue="<number>" -->

The runner parses these and asks for user confirmation before
executing — side effects are gated, not silent.`)

	return b.String()
}
