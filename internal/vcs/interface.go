// Package vcs defines the platform-agnostic VCS client interface.
package vcs

// Issue represents a VCS issue/ticket.
type Issue struct {
	Number int
	Title  string
	Body   string
	Labels []string
}

// HasLabel reports whether the issue has a given label.
func (i Issue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if l == name {
			return true
		}
	}
	return false
}

// PR represents a pull/merge request.
type PR struct {
	Number     int
	Title      string
	HeadBranch string
	Body       string
	State      string
}

// Label represents a repository label definition.
type Label struct {
	Name  string
	Color string // hex without #, e.g. "FF0000"
	Desc  string
}

// Client is the platform-agnostic interface for VCS operations.
type Client interface {
	ListOpenIssues(repo string) ([]Issue, error)
	ListOpenPRs(repo string) ([]PR, error)
	PRExistsForIssue(repo string, issueNumber int) (bool, error)
	AddLabel(repo string, issueNumber int, labels ...string) error
	RemoveLabel(repo string, issueNumber int, labels ...string) error
	PostIssueComment(repo string, issueNumber int, body string) error
	InitLabels(repo string, labels []Label) error
	CreateIssue(repo string, title, body string) (Issue, error)
}

// ClawFlowLabels are the standard labels ClawFlow requires on every monitored repo.
var ClawFlowLabels = []Label{
	{"ready-for-agent", "00FF00", "Owner approved — triggers ClawFlow fix pipeline"},
	{"agent-evaluated", "0075CA", "ClawFlow has assessed this issue and posted a proposal"},
	{"in-progress",     "FFA500", "Agent is actively working on this issue"},
	{"agent-skipped",   "BDBDBD", "Low confidence — needs more information"},
	{"agent-failed",    "FF0000", "Agent attempted but failed"},
}
