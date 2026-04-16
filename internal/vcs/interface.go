// Package vcs defines the platform-agnostic VCS client interface.
package vcs

// IssueComment represents a single comment on an issue.
type IssueComment struct {
	ID     int64
	Author string
	Body   string
}

// Issue represents a VCS issue/ticket.
type Issue struct {
	Number int
	Title  string
	Body   string
	Labels []string
	State  string // "open" | "closed"
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
	State      string // "open" | "closed" | "merged"
	MergedAt   string // ISO timestamp, empty if not merged
	URL        string // HTML URL
}

// IsMerged reports whether the PR was merged (not just closed).
func (p PR) IsMerged() bool {
	return p.State == "merged" || p.MergedAt != ""
}

// PRCreateOpts holds parameters for creating a PR/MR.
type PRCreateOpts struct {
	Title string
	Body  string
	Head  string // source branch
	Base  string // target branch
}

// CIStatus represents the aggregate CI result for a PR.
type CIStatus string

const (
	CIStatusPending CIStatus = "pending"
	CIStatusSuccess CIStatus = "success"
	CIStatusFailure CIStatus = "failure"
	CIStatusNone    CIStatus = "no-checks"
)

// Label represents a repository label definition.
type Label struct {
	Name  string
	Color string // hex without #, e.g. "FF0000"
	Desc  string
}

// Client is the platform-agnostic interface for VCS operations.
type Client interface {
	// Issues
	ListOpenIssues(repo string) ([]Issue, error)
	ListIssues(repo string, state string, labels []string) ([]Issue, error)
	ListIssueComments(repo string, issueNumber int) ([]string, error)
	ListIssueCommentsDetail(repo string, issueNumber int) ([]IssueComment, error)
	CloseIssue(repo string, issueNumber int) error
	CreateIssue(repo string, title, body string) (Issue, error)
	PostIssueComment(repo string, issueNumber int, body string) error
	DeleteIssueComment(repo string, issueNumber int, commentID int64) error
	// ListIssuesByBodyKeyword returns open issues whose body contains keyword.
	ListIssuesByBodyKeyword(repo string, keyword string) ([]Issue, error)

	// Labels
	AddLabel(repo string, issueNumber int, labels ...string) error
	RemoveLabel(repo string, issueNumber int, labels ...string) error
	InitLabels(repo string, labels []Label) error

	// PRs / MRs
	ListOpenPRs(repo string) ([]PR, error)
	ListPRs(repo string, state string) ([]PR, error)
	PRExistsForIssue(repo string, issueNumber int) (bool, error)
	CreatePR(repo string, opts PRCreateOpts) (PR, error)
	GetPR(repo string, prNumber int) (PR, error)
	PostPRComment(repo string, prNumber int, body string) error

	// CI
	GetCIStatus(repo string, prNumber int) (CIStatus, error)
}

// ClawFlowLabels are the standard labels ClawFlow requires on every monitored repo.
var ClawFlowLabels = []Label{
	{"ready-for-agent", "00FF00", "Owner approved — triggers ClawFlow fix pipeline"},
	{"agent-evaluated", "0075CA", "ClawFlow has assessed this issue and posted a proposal"},
	{"in-progress", "FFA500", "Agent is actively working on this issue"},
	{"agent-skipped", "BDBDBD", "Low confidence — needs more information"},
	{"agent-failed", "FF0000", "Agent attempted but failed"},
	{"blocked", "E4E669", "Waiting on dependency issues to be resolved"},
	{"agent-split", "8B5CF6", "Issue split into sub-issues; main issue closed when all sub-issues close"},
}
