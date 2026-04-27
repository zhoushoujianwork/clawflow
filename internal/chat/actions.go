package chat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Action represents a parsed VCS action from AI output.
type Action struct {
	Type        string // "add_label", "remove_label", "comment", "create_issue", "close_issue"
	IssueNumber int    // 0 means current issue (from chat context)
	Label       string
	Title       string
	Body        string
	Labels      []string
	Text        string // comment text
}

var actionRE = regexp.MustCompile(`<!--\s*clawflow:action:(\w+)\s+(.*?)\s*-->`)
var paramRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// ParseActions extracts action markers from AI output.
func ParseActions(text string) []Action {
	matches := actionRE.FindAllStringSubmatch(text, -1)
	var actions []Action
	for _, m := range matches {
		a := Action{Type: m[1]}
		params := paramRE.FindAllStringSubmatch(m[2], -1)
		for _, p := range params {
			switch p[1] {
			case "label":
				a.Label = p[2]
			case "issue":
				a.IssueNumber, _ = strconv.Atoi(p[2])
			case "title":
				a.Title = p[2]
			case "body":
				a.Body = p[2]
			case "labels":
				for _, l := range strings.Split(p[2], ",") {
					if t := strings.TrimSpace(l); t != "" {
						a.Labels = append(a.Labels, t)
					}
				}
			case "text":
				a.Text = p[2]
			}
		}
		actions = append(actions, a)
	}
	return actions
}

// StripActions removes action markers from text for display.
func StripActions(text string) string {
	return strings.TrimSpace(actionRE.ReplaceAllString(text, ""))
}

// Describe returns a human-readable description of the action.
func (a Action) Describe(currentIssue int) string {
	issue := a.IssueNumber
	if issue == 0 {
		issue = currentIssue
	}
	switch a.Type {
	case "add_label":
		return fmt.Sprintf("Add label %q to #%d", a.Label, issue)
	case "remove_label":
		return fmt.Sprintf("Remove label %q from #%d", a.Label, issue)
	case "comment":
		preview := a.Text
		if len(preview) > 60 {
			preview = preview[:60] + "..."
		}
		return fmt.Sprintf("Post comment on #%d: %s", issue, preview)
	case "create_issue":
		return fmt.Sprintf("Create issue: %s", a.Title)
	case "close_issue":
		return fmt.Sprintf("Close issue #%d", issue)
	default:
		return fmt.Sprintf("Unknown action: %s", a.Type)
	}
}

// Execute runs the action against the VCS client.
func (a Action) Execute(client vcs.Client, repo string, currentIssue int) error {
	issue := a.IssueNumber
	if issue == 0 {
		issue = currentIssue
	}
	switch a.Type {
	case "add_label":
		return client.AddLabel(repo, issue, a.Label)
	case "remove_label":
		return client.RemoveLabel(repo, issue, a.Label)
	case "comment":
		return client.PostIssueComment(repo, issue, a.Text)
	case "create_issue":
		_, err := client.CreateIssue(repo, a.Title, a.Body)
		return err
	case "close_issue":
		return client.CloseIssue(repo, issue)
	default:
		return fmt.Errorf("unknown action type: %s", a.Type)
	}
}
