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
	fmt.Fprintln(&b, `You are a helpful assistant discussing this issue. You can help the user:
- Analyze and evaluate this issue
- Suggest labels, priorities, and next steps
- Draft updates or new comments
- Create related issues

When the user asks to perform a VCS action, output an action marker on its own line:
  <!-- clawflow:action:add_label label="<name>" -->
  <!-- clawflow:action:remove_label label="<name>" -->
  <!-- clawflow:action:comment text="<comment body>" -->
  <!-- clawflow:action:create_issue title="<title>" body="<body>" labels="<comma-separated>" -->
  <!-- clawflow:action:close_issue -->

The CLI will parse these markers and ask the user for confirmation before executing.`)

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
	fmt.Fprintln(&b, `You are a helpful assistant for this repository. You can help the user:
- Discuss and triage issues
- Create new issues
- Evaluate existing issues
- Manage labels

When the user asks to perform a VCS action, output an action marker on its own line:
  <!-- clawflow:action:add_label issue="<number>" label="<name>" -->
  <!-- clawflow:action:remove_label issue="<number>" label="<name>" -->
  <!-- clawflow:action:comment issue="<number>" text="<comment body>" -->
  <!-- clawflow:action:create_issue title="<title>" body="<body>" labels="<comma-separated>" -->
  <!-- clawflow:action:close_issue issue="<number>" -->

The CLI will parse these markers and ask the user for confirmation before executing.`)

	return b.String()
}
