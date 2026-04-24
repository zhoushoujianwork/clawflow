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

	// Merge
	MergePR(repo string, prNumber int) error
	GetPRMergeability(repo string, prNumber int) (MergeStatus, error)
}

// MergeStatus represents whether a PR can be merged.
type MergeStatus string

const (
	MergeStatusClean      MergeStatus = "clean"      // ready to merge
	MergeStatusConflict   MergeStatus = "conflict"   // has conflicts
	MergeStatusPending    MergeStatus = "pending"    // mergeability not yet computed
	MergeStatusUnknown    MergeStatus = "unknown"
)

// ClawFlowLabels are the standard labels ClawFlow requires on every monitored
// repo. Two buckets: trigger labels gate which operator fires; state labels
// are written back by operators to record progress.
var ClawFlowLabels = []Label{
	// Trigger labels
	{"bug", "D73A4A", "Bug report — triggers evaluate-bug operator"},
	{"feat", "0E8A16", "Feature request — triggers evaluate-feat operator (planned)"},
	{"ready-for-agent", "00FF00", "Owner approved — triggers implement operator"},
	{"agent-mentioned", "BFD4F2", "Issue mentioned @agent — triggers reply-comment operator"},
	// State labels
	{"agent-running", "FFA500", "An operator is running on this subject (concurrency lock)"},
	{"agent-evaluated", "0075CA", "An evaluate-* operator has posted its assessment"},
	{"agent-skipped", "BDBDBD", "Operator declined — confidence too low or info missing"},
	{"agent-implemented", "6E7681", "implement operator finished — PR opened"},
	{"agent-failed", "FF0000", "An operator errored; see failure comment"},
	{"agent-replied", "E99695", "reply-comment operator has responded to a mention"},
}
