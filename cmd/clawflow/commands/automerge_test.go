package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestExtractClosesIssue(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"closes", "Closes #273", 273},
		{"lowercase fixes", "some text\n\nfixes #12\n", 12},
		{"resolves", "Resolves #7", 7},
		{"fixed past tense", "Fixed #99", 99},
		{"with colon", "Closes: #42", 42},
		{"first of many wins", "Closes #5 and closes #6", 5},
		{"plain reference is not a claim", "see #123 for context", 0},
		{"empty", "", 0},
		{"no digits", "closes #", 0},
		{"word boundary", "encloses #5", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractClosesIssue(tc.body); got != tc.want {
				t.Errorf("extractClosesIssue(%q) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestIsDraftPR(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"WIP: fix thing", true},
		{"[WIP] fix thing", true},
		{"Draft: fix thing", true},
		{"wip something", true},
		{"fix: real change", false},
		{"", false},
		{"swiping the cache", false},
	} {
		if got := isDraftPR(tc.title); got != tc.want {
			t.Errorf("isDraftPR(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// sweepFake is a minimal vcs.Client for exercising sweepAutoMergePRs. Only
// the methods the sweep touches carry behaviour; the rest return
// vcs.ErrNotSupported so an accidental extra call is loud in tests.
type sweepFake struct {
	openPRs []vcs.PR
	// mergeability keyed by PR number; missing entries default to clean.
	mergeability map[int]vcs.MergeStatus
	ciStatus     vcs.CIStatus

	listOpenPRCalls  int
	mergeabilityCall int
	merged           []int
	deletedBranches  []string
	comments         []string
	existingComments []string
	mergeErr         error
}

func (f *sweepFake) ListOpenPRs(repo string) ([]vcs.PR, error) {
	f.listOpenPRCalls++
	return f.openPRs, nil
}

func (f *sweepFake) GetPRMergeability(repo string, prNumber int) (vcs.MergeStatus, error) {
	f.mergeabilityCall++
	if s, ok := f.mergeability[prNumber]; ok {
		return s, nil
	}
	return vcs.MergeStatusClean, nil
}

func (f *sweepFake) MergePR(repo string, prNumber int) error {
	if f.mergeErr != nil {
		return f.mergeErr
	}
	f.merged = append(f.merged, prNumber)
	return nil
}

func (f *sweepFake) GetPR(repo string, prNumber int) (vcs.PR, error) {
	for _, pr := range f.openPRs {
		if pr.Number == prNumber {
			return pr, nil
		}
	}
	return vcs.PR{}, errors.New("no such PR")
}

func (f *sweepFake) DeleteBranch(repo string, branch string) error {
	f.deletedBranches = append(f.deletedBranches, branch)
	return nil
}

func (f *sweepFake) PostIssueComment(repo string, issueNumber int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}

func (f *sweepFake) ListIssueComments(repo string, issueNumber int) ([]string, error) {
	return f.existingComments, nil
}

func (f *sweepFake) GetCIStatus(repo string, prNumber int) (vcs.CIStatus, error) {
	if f.ciStatus == "" {
		return vcs.CIStatusNone, nil
	}
	return f.ciStatus, nil
}

// All remaining vcs.Client methods are unsupported stubs for sweep tests.
func (f *sweepFake) ListOpenIssues(repo string) ([]vcs.Issue, error) { return nil, vcs.ErrNotSupported }
func (f *sweepFake) ListIssues(repo string, state string, labels []string) ([]vcs.Issue, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) GetIssue(repo string, number int) (*vcs.Issue, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) ListIssueCommentsDetail(repo string, issueNumber int) ([]vcs.IssueComment, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) CloseIssue(repo string, issueNumber int) error { return vcs.ErrNotSupported }
func (f *sweepFake) CreateIssue(repo string, title, body string) (vcs.Issue, error) {
	return vcs.Issue{}, vcs.ErrNotSupported
}
func (f *sweepFake) UpdateIssue(repo string, issueNumber int, update vcs.IssueUpdate) (vcs.Issue, error) {
	return vcs.Issue{}, vcs.ErrNotSupported
}
func (f *sweepFake) PostIssueCommentIdempotent(repo string, issueNumber int, body, runID string) error {
	return vcs.ErrNotSupported
}
func (f *sweepFake) DeleteIssueComment(repo string, issueNumber int, commentID int64) error {
	return vcs.ErrNotSupported
}
func (f *sweepFake) ListIssuesByBodyKeyword(repo string, keyword string) ([]vcs.Issue, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) SearchIssues(repo, query, state string, limit int) ([]vcs.Issue, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) AddSubIssue(repo string, parentNumber int, subIssueID int64) error {
	return vcs.ErrNotSupported
}
func (f *sweepFake) ListSubIssues(repo string, issueNumber int) ([]vcs.Issue, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) GetParentIssue(repo string, issueNumber int) (*vcs.Issue, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) UploadAttachment(repo string, filePath string) (string, error) {
	return "", vcs.ErrNotSupported
}
func (f *sweepFake) GetIssueLabels(repo string, issueNumber int) ([]string, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) AddLabel(repo string, issueNumber int, labels ...string) error {
	return vcs.ErrNotSupported
}
func (f *sweepFake) RemoveLabel(repo string, issueNumber int, labels ...string) error {
	return vcs.ErrNotSupported
}
func (f *sweepFake) InitLabels(repo string, labels []vcs.Label) error { return vcs.ErrNotSupported }
func (f *sweepFake) ListPRs(repo string, state string) ([]vcs.PR, error) {
	return nil, vcs.ErrNotSupported
}
func (f *sweepFake) PRExistsForIssue(repo string, issueNumber int) (bool, error) {
	return false, vcs.ErrNotSupported
}
func (f *sweepFake) CreatePR(repo string, opts vcs.PRCreateOpts) (vcs.PR, error) {
	return vcs.PR{}, vcs.ErrNotSupported
}
func (f *sweepFake) PostPRComment(repo string, prNumber int, body string) error {
	return vcs.ErrNotSupported
}
func (f *sweepFake) ClosePR(repo string, prNumber int) error { return vcs.ErrNotSupported }

func TestSweepAutoMergePRs_DisabledCostsNoAPICalls(t *testing.T) {
	f := &sweepFake{openPRs: []vcs.PR{{Number: 1, Body: "Closes #2"}}}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: false})
	if f.listOpenPRCalls != 0 {
		t.Errorf("expected 0 ListOpenPRs calls when auto_merge is off, got %d", f.listOpenPRCalls)
	}
	if len(f.merged) != 0 {
		t.Errorf("expected no merges, got %v", f.merged)
	}
}

func TestSweepAutoMergePRs_MergesCleanCandidate(t *testing.T) {
	f := &sweepFake{openPRs: []vcs.PR{
		{Number: 273, Title: "fix: thing", Body: "Closes #272", HeadBranch: "fix/issue-272"},
	}}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true, BaseBranch: "main"})
	if len(f.merged) != 1 || f.merged[0] != 273 {
		t.Fatalf("expected PR 273 merged, got %v", f.merged)
	}
	if len(f.deletedBranches) != 1 || f.deletedBranches[0] != "fix/issue-272" {
		t.Errorf("expected head branch deleted, got %v", f.deletedBranches)
	}
	if len(f.comments) != 0 {
		t.Errorf("expected no comments on success, got %v", f.comments)
	}
}

func TestSweepAutoMergePRs_DirtyIsSilentlySkipped(t *testing.T) {
	f := &sweepFake{
		openPRs:      []vcs.PR{{Number: 10, Body: "Closes #9"}},
		mergeability: map[int]vcs.MergeStatus{10: vcs.MergeStatusConflict},
	}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true})
	if len(f.merged) != 0 {
		t.Errorf("expected no merge for dirty PR, got %v", f.merged)
	}
	if len(f.comments) != 0 {
		t.Errorf("dirty PR must not be commented on every pass, got %v", f.comments)
	}
}

func TestSweepAutoMergePRs_SkipsPRsWithoutClosingKeyword(t *testing.T) {
	f := &sweepFake{openPRs: []vcs.PR{
		{Number: 1, Title: "chore: human WIP", Body: "just some work"},
		{Number: 2, Title: "WIP: not ready", Body: "Closes #3"},
	}}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true})
	if f.mergeabilityCall != 0 {
		t.Errorf("expected no mergeability calls for non-candidates, got %d", f.mergeabilityCall)
	}
	if len(f.merged) != 0 {
		t.Errorf("expected no merges, got %v", f.merged)
	}
}

func TestSweepAutoMergePRs_CapsMergesPerPass(t *testing.T) {
	f := &sweepFake{openPRs: []vcs.PR{
		{Number: 1, Body: "Closes #11", HeadBranch: "a"},
		{Number: 2, Body: "Closes #12", HeadBranch: "b"},
		{Number: 3, Body: "Closes #13", HeadBranch: "c"},
	}}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true})
	if len(f.merged) != sweepMaxMergesPerPass {
		t.Errorf("expected at most %d merge(s) per pass, got %v", sweepMaxMergesPerPass, f.merged)
	}
}

func TestSweepAutoMergePRs_FailureCommentIsDeduped(t *testing.T) {
	f := &sweepFake{
		openPRs:  []vcs.PR{{Number: 5, Body: "Closes #4"}},
		mergeErr: errors.New("HTTP 403: Resource not accessible by integration"),
		existingComments: []string{
			"🤖 Auto-merge failed for PR #5: HTTP 403: Resource not accessible by integration",
		},
	}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true})
	if len(f.comments) != 0 {
		t.Errorf("expected the duplicate failure comment to be suppressed, got %v", f.comments)
	}
}

func TestSweepAutoMergePRs_FailureCommentPostedOnce(t *testing.T) {
	f := &sweepFake{
		openPRs:  []vcs.PR{{Number: 5, Body: "Closes #4"}},
		mergeErr: errors.New("HTTP 403: Resource not accessible by integration"),
	}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true})
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "Auto-merge failed for PR #5") {
		t.Fatalf("expected one failure comment carrying the Play 4 marker, got %v", f.comments)
	}
}

// TestSweepAutoMergePRs_PendingCIDoesNotBlock guards the sweep against
// waitForCI's 30s sleep loop: a pending check must yield immediately and be
// re-examined on the next pass.
func TestSweepAutoMergePRs_PendingCIDoesNotBlock(t *testing.T) {
	f := &sweepFake{
		openPRs:  []vcs.PR{{Number: 8, Body: "Closes #7"}},
		ciStatus: vcs.CIStatusPending,
	}
	done := make(chan struct{})
	go func() {
		sweepAutoMergePRs(context.Background(), f, "o/r",
			config.Repo{AutoMerge: true, CIRequired: true, CITimeout: 600})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep blocked on pending CI — it must poll once and move on")
	}
	if len(f.merged) != 0 {
		t.Errorf("expected no merge while CI is pending, got %v", f.merged)
	}
	if len(f.comments) != 0 {
		t.Errorf("pending CI must stay silent in sweep mode, got %v", f.comments)
	}
}
