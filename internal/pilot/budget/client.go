package budget

import "github.com/zhoushoujianwork/clawflow/internal/vcs"

// WrapClient returns a vcs.Client decorator that calls Reserve before
// each VCS write method. Read methods pass through via interface
// embedding, so future read additions to vcs.Client are picked up
// automatically without changes here.
//
// Activation is gated by the CLAWFLOW_PILOT_BUDGET_PATH env var at
// Reserve() time, so wrapping a client when the env var is unset
// is harmless — every write becomes a no-op pass-through.
func WrapClient(inner vcs.Client) vcs.Client {
	return &budgetClient{Client: inner}
}

type budgetClient struct {
	vcs.Client
}

// --- Issue writes ---

func (b *budgetClient) CreateIssue(repo, title, body string) (vcs.Issue, error) {
	if err := Reserve("CreateIssue"); err != nil {
		return vcs.Issue{}, err
	}
	return b.Client.CreateIssue(repo, title, body)
}

func (b *budgetClient) CloseIssue(repo string, n int) error {
	if err := Reserve("CloseIssue"); err != nil {
		return err
	}
	return b.Client.CloseIssue(repo, n)
}

func (b *budgetClient) PostIssueComment(repo string, n int, body string) error {
	if err := Reserve("PostIssueComment"); err != nil {
		return err
	}
	return b.Client.PostIssueComment(repo, n, body)
}

func (b *budgetClient) DeleteIssueComment(repo string, n int, commentID int64) error {
	if err := Reserve("DeleteIssueComment"); err != nil {
		return err
	}
	return b.Client.DeleteIssueComment(repo, n, commentID)
}

func (b *budgetClient) AddSubIssue(repo string, parent int, sub int64) error {
	if err := Reserve("AddSubIssue"); err != nil {
		return err
	}
	return b.Client.AddSubIssue(repo, parent, sub)
}

// --- Labels ---

func (b *budgetClient) AddLabel(repo string, n int, labels ...string) error {
	if err := Reserve("AddLabel"); err != nil {
		return err
	}
	return b.Client.AddLabel(repo, n, labels...)
}

func (b *budgetClient) RemoveLabel(repo string, n int, labels ...string) error {
	if err := Reserve("RemoveLabel"); err != nil {
		return err
	}
	return b.Client.RemoveLabel(repo, n, labels...)
}

func (b *budgetClient) InitLabels(repo string, labels []vcs.Label) error {
	if err := Reserve("InitLabels"); err != nil {
		return err
	}
	return b.Client.InitLabels(repo, labels)
}

// --- PR writes ---

func (b *budgetClient) CreatePR(repo string, opts vcs.PRCreateOpts) (vcs.PR, error) {
	if err := Reserve("CreatePR"); err != nil {
		return vcs.PR{}, err
	}
	return b.Client.CreatePR(repo, opts)
}

func (b *budgetClient) PostPRComment(repo string, n int, body string) error {
	if err := Reserve("PostPRComment"); err != nil {
		return err
	}
	return b.Client.PostPRComment(repo, n, body)
}

// --- Merge / Branch (destructive) ---

func (b *budgetClient) MergePR(repo string, n int) error {
	if err := Reserve("MergePR"); err != nil {
		return err
	}
	return b.Client.MergePR(repo, n)
}

func (b *budgetClient) DeleteBranch(repo, branch string) error {
	if err := Reserve("DeleteBranch"); err != nil {
		return err
	}
	return b.Client.DeleteBranch(repo, branch)
}
