// Package vcs defines the platform-agnostic VCS client interface.
package vcs

import "errors"

// ErrNotSupported is returned by platform implementations for operations
// that have no equivalent on that platform (e.g. sub-issues on GitLab).
var ErrNotSupported = errors.New("operation not supported on this platform")

// IssueComment represents a single comment on an issue.
type IssueComment struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at,omitempty"`
}

// Issue represents a VCS issue/ticket.
type Issue struct {
	ID        int64    `json:"id,omitempty"` // platform internal ID (GitHub: id, GitLab: id); used for sub-issue API
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Labels    []string `json:"labels"`
	State     string   `json:"state"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	// ClosedAt is populated only when State == "closed". GitHub /
	// GitLab both return null when open; clients normalize to "" here.
	// Used by Pilot's issue_digest to count "closed in last 24h"
	// without abusing CapturedAt (which the scanner rewrites every
	// pass and thus is not a real close timestamp).
	ClosedAt string `json:"closed_at,omitempty"`
	// SubIssuesTotal / SubIssuesCompleted mirror GitHub's
	// sub_issues_summary on the issue list response: how many sub-issues
	// this issue has, and how many of them are closed. Both zero when the
	// issue has no sub-issues, or on platforms without native sub-issues
	// (GitLab). Lets the dashboard render a parent's ring progress without
	// an extra round-trip per issue.
	SubIssuesTotal     int `json:"sub_issues_total,omitempty"`
	SubIssuesCompleted int `json:"sub_issues_completed,omitempty"`
}

// IssueUpdate holds editable issue fields. Nil means "leave unchanged"; a
// pointer to an empty string intentionally clears that field.
type IssueUpdate struct {
	Title *string
	Body  *string
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
	Number     int    `json:"number"`
	Title      string `json:"title"`
	HeadBranch string `json:"head_branch"`
	Body       string `json:"body"`
	State      string `json:"state"`
	MergedAt   string `json:"merged_at,omitempty"`
	URL        string `json:"url,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
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
	UpdateIssue(repo string, issueNumber int, update IssueUpdate) (Issue, error)
	PostIssueComment(repo string, issueNumber int, body string) error
	DeleteIssueComment(repo string, issueNumber int, commentID int64) error
	// ListIssuesByBodyKeyword returns open issues whose body contains keyword.
	ListIssuesByBodyKeyword(repo string, keyword string) ([]Issue, error)
	// SearchIssues runs a keyword search across title + body for the
	// given repo. state is one of "open"/"closed"/"all". limit caps the
	// returned slice (clamp to a sane max in implementations). Used by
	// `clawflow issue search` so operators / PM can pull historical
	// related issues into evaluation context.
	SearchIssues(repo, query, state string, limit int) ([]Issue, error)

	// Sub-issues (GitHub native; GitLab returns ErrNotSupported).
	// AddSubIssue links subIssueID (Issue.ID, the platform internal id)
	// as a child of parentNumber.
	AddSubIssue(repo string, parentNumber int, subIssueID int64) error
	// ListSubIssues returns all sub-issues of the given issue.
	ListSubIssues(repo string, issueNumber int) ([]Issue, error)
	// GetParentIssue returns the parent issue of the given issue, or
	// ErrNotSupported / nil if there is no parent.
	GetParentIssue(repo string, issueNumber int) (*Issue, error)

	// Attachments (GitLab native; GitHub returns ErrNotSupported).
	// UploadAttachment uploads the local file at filePath to the repo's
	// attachment storage and returns the ready-to-embed image markdown
	// (e.g. "![file](/uploads/<hash>/file.png)"). GitHub has no public
	// issue-attachment upload API, so its implementation returns
	// ErrNotSupported — callers should tell the user to paste images via
	// the web UI.
	UploadAttachment(repo string, filePath string) (markdown string, err error)

	// Labels
	GetIssueLabels(repo string, issueNumber int) ([]string, error)
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

	// Branches
	// DeleteBranch removes a branch from the remote. Implementations should
	// treat "branch already gone" as a non-error (idempotent).
	DeleteBranch(repo string, branch string) error
}

// MergeStatus represents whether a PR can be merged.
type MergeStatus string

const (
	MergeStatusClean    MergeStatus = "clean"    // ready to merge
	MergeStatusConflict MergeStatus = "conflict" // has conflicts
	MergeStatusPending  MergeStatus = "pending"  // mergeability not yet computed
	MergeStatusUnknown  MergeStatus = "unknown"
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
