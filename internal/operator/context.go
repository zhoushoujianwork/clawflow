package operator

import (
	"fmt"
	"strings"
)

// BuildPrompt constructs the full prompt handed to `claude -p`. It prepends a
// Context block with the issue/PR details so the operator's SKILL.md body can
// reason about "this issue" without calling out to the VCS API itself.
//
// comments is optional; pass nil to skip the "Recent Comments" section.
func BuildPrompt(op *Operator, sub *Subject, repo string, comments []string) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Context")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Repo: %s\n", repo)

	subjType := "Issue"
	if sub.IsPR {
		subjType = "Pull Request"
	}
	fmt.Fprintf(&b, "%s Number: #%d\n", subjType, sub.Number)
	fmt.Fprintf(&b, "%s Title: %s\n", subjType, sub.Title)
	fmt.Fprintf(&b, "Current Labels: %v\n", sub.Labels)
	if sub.HeadBranch != "" {
		fmt.Fprintf(&b, "Head Branch: %s\n", sub.HeadBranch)
	}
	if sub.URL != "" {
		fmt.Fprintf(&b, "URL: %s\n", sub.URL)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Body")
	fmt.Fprintln(&b)
	if strings.TrimSpace(sub.Body) == "" {
		fmt.Fprintln(&b, "_(empty)_")
	} else {
		fmt.Fprintln(&b, sub.Body)
	}

	if len(comments) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Recent Comments")
		fmt.Fprintln(&b)
		for i, c := range comments {
			fmt.Fprintf(&b, "### Comment %d\n\n%s\n\n", i+1, c)
		}
	}

	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "# Your Task (Operator: %s)\n\n", op.Name)
	fmt.Fprint(&b, op.Prompt)
	return b.String()
}
