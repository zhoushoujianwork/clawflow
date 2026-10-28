package commands

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Hooks transfer shared-state ownership to the caller. Missing worker hooks
// are no-ops, never fallbacks to machine-local locks or repair ledgers.
type jobHooks struct {
	AcquireLock   func(context.Context, *runJob) error
	ReleaseLock   func(*runJob)
	ReserveRepair func(context.Context, *runJob, time.Time) error
	ReleaseRepair func(*runJob, string, time.Time) error
	AfterRun      func(*jobResult)
	SkipIndex     bool
}

type jobResult struct {
	DevelopmentEvents string          `json:"development_events,omitempty"`
	Status            string          `json:"status"`
	Outcome           string          `json:"outcome,omitempty"`
	Output            string          `json:"output,omitempty"`
	Error             string          `json:"error,omitempty"`
	Usage             *snapshot.Usage `json:"usage,omitempty"`
	Cost              float64         `json:"cost"`
	InfraKind         string          `json:"infra_kind,omitempty"`
	PRNumber          int             `json:"pr_number,omitempty"`
	PRURL             string          `json:"pr_url,omitempty"`
	Branch            string          `json:"branch,omitempty"`
	HeadSHA           string          `json:"head_sha,omitempty"`
	RateLimited       bool            `json:"rate_limited"`
	CostLimited       bool            `json:"cost_limited"`
	Effects           []jobEffect     `json:"effects,omitempty"`
}

type jobEffect struct {
	Kind   string   `json:"kind"`
	Repo   string   `json:"repo"`
	Number int      `json:"number"`
	Body   string   `json:"body,omitempty"`
	RunID  string   `json:"run_id,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// fencedClient only buffers coordination writes. A cancelled lease cannot
// append effects; merging is exclusively a coordinator responsibility.
type fencedClient struct {
	vcs.Client
	ctx    context.Context
	result *jobResult
	mu     sync.Mutex
}

func (c *fencedClient) record(e jobEffect) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.result.Effects = append(c.result.Effects, e)
	return nil
}
func (c *fencedClient) PostIssueComment(repo string, n int, body string) error {
	return c.record(jobEffect{Kind: "issue-comment", Repo: repo, Number: n, Body: body})
}
func (c *fencedClient) PostIssueCommentIdempotent(repo string, n int, body, runID string) error {
	return c.record(jobEffect{Kind: "issue-comment", Repo: repo, Number: n, Body: body, RunID: runID})
}
func (c *fencedClient) PostPRComment(repo string, n int, body string) error {
	return c.record(jobEffect{Kind: "pr-comment", Repo: repo, Number: n, Body: body})
}
func (c *fencedClient) AddLabel(repo string, n int, labels ...string) error {
	return c.record(jobEffect{Kind: "add-label", Repo: repo, Number: n, Labels: append([]string(nil), labels...)})
}
func (c *fencedClient) RemoveLabel(repo string, n int, labels ...string) error {
	return c.record(jobEffect{Kind: "remove-label", Repo: repo, Number: n, Labels: append([]string(nil), labels...)})
}
func (c *fencedClient) CloseIssue(repo string, n int) error {
	return c.record(jobEffect{Kind: "close-issue", Repo: repo, Number: n})
}
func (c *fencedClient) MergePR(string, int) error { return errors.New("worker cannot merge") }
func (c *fencedClient) MergePRAtHead(string, int, string) (string, error) {
	return "", errors.New("worker cannot merge")
}
func (j *runJob) acquireLock(ctx context.Context) error {
	if j.hooks != nil {
		if j.hooks.AcquireLock != nil {
			return j.hooks.AcquireLock(ctx, j)
		}
		return nil
	}
	return snapshot.AcquireLock(j.repo, j.sub.Number, j.op.Name)
}
func (j *runJob) releaseLock() {
	if j.hooks != nil {
		if j.hooks.ReleaseLock != nil {
			j.hooks.ReleaseLock(j)
		}
		return
	}
	snapshot.ReleaseLock(j.repo, j.sub.Number)
}
func (j *runJob) releaseRepair(kind string, now time.Time) error {
	if j.result != nil {
		j.result.InfraKind = kind
	}
	if j.hooks != nil {
		if j.hooks.ReleaseRepair != nil {
			return j.hooks.ReleaseRepair(j, kind, now)
		}
		return nil
	}
	return releaseCIRepair(snapshot.DataDir(), j.repo, j.repair.PR, j.repair.Attempt, kind, now)
}

// Preserve the optional diagnostic reader through the wrapper.
func (c *fencedClient) GetFailedJobEvidence(repo, head string, runID int64) (string, error) {
	if reader, ok := c.Client.(interface {
		GetFailedJobEvidence(string, string, int64) (string, error)
	}); ok {
		return reader.GetFailedJobEvidence(repo, head, runID)
	}
	return "", vcs.ErrNotSupported
}

func (c *fencedClient) ListIssueEvents(repo string, n int) ([]vcs.IssueEvent, error) {
	if reader, ok := c.Client.(vcs.IssueEventLister); ok {
		return reader.ListIssueEvents(repo, n)
	}
	return nil, vcs.ErrNotSupported
}
func (c *fencedClient) GetCIEvidence(repo string, n int) (vcs.CIEvidence, error) {
	if reader, ok := c.Client.(vcs.CIEvidenceProvider); ok {
		return reader.GetCIEvidence(repo, n)
	}
	return vcs.CIEvidence{}, vcs.ErrNotSupported
}
