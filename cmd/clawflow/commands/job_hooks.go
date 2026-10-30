package commands

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
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

// jobProvenance deliberately excludes workdirs, prompts, credentials and raw events.
// Keep this wire schema separate from extensible local execution records.
type jobProvenance struct {
	Machine         string `json:"machine,omitempty"`
	Profile         string `json:"profile,omitempty"`
	Backend         string `json:"backend,omitempty"`
	Model           string `json:"model,omitempty"`
	OffloadRunID    string `json:"offload_run_id,omitempty"`
	CallerRunID     string `json:"caller_run_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ExitCode        *int   `json:"exit_code"`
	ErrorKind       string `json:"error_kind,omitempty"`
	ElapsedMs       *int64 `json:"elapsed_ms"`
	ExecutionStatus string `json:"execution_status,omitempty"`
}

func provenanceFromRun(m snapshot.RunMeta) *jobProvenance {
	return &jobProvenance{Machine: m.Machine, Profile: m.Profile, Backend: m.Backend, Model: m.Model,
		OffloadRunID: m.OffloadRunID, CallerRunID: m.CallerRunID, SessionID: m.SessionID,
		ExitCode: m.ExitCode, ErrorKind: m.ErrorKind, ElapsedMs: m.ElapsedMs, ExecutionStatus: m.ExecutionStatus}
}

var provenanceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)

// normalize keeps advisory metadata from invalidating completed work. Warnings
// name fields only; never echo untrusted values into diagnostics.
func (p *jobProvenance) normalize() []string {
	if p == nil {
		return nil
	}
	var warnings []string
	for _, field := range []struct {
		name       string
		value      *string
		identifier bool
	}{
		{"machine", &p.Machine, false}, {"profile", &p.Profile, false},
		{"backend", &p.Backend, false}, {"model", &p.Model, false},
		{"offload_run_id", &p.OffloadRunID, true}, {"caller_run_id", &p.CallerRunID, true},
		{"session_id", &p.SessionID, true}, {"error_kind", &p.ErrorKind, false},
		{"execution_status", &p.ExecutionStatus, false},
	} {
		original := *field.value
		value := strings.TrimSpace(original)
		if field.identifier {
			// Do not remove unsafe characters or truncate identifiers into other links.
			if value != "" && !provenanceID.MatchString(value) {
				value = ""
			}
		} else {
			value = strings.TrimSpace(strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, value))
			runes := []rune(value)
			if len(runes) > 256 {
				value = string(runes[:256])
			}
		}
		*field.value = value
		if value != original {
			warnings = append(warnings, "provenance."+field.name+": normalized or dropped invalid metadata")
		}
	}
	if p.ElapsedMs != nil && *p.ElapsedMs < 0 {
		p.ElapsedMs = nil
		warnings = append(warnings, "provenance.elapsed_ms: dropped negative duration")
	}
	return warnings
}

type jobResult struct {
	SOPDigest         string          `json:"sop_digest,omitempty"`
	NextAction        string          `json:"next_action,omitempty"`
	NextOwner         string          `json:"next_owner,omitempty"`
	Provenance        *jobProvenance  `json:"provenance,omitempty"`
	DevelopmentEvents string          `json:"development_events,omitempty"`
	Status            string          `json:"status"`
	Outcome           string          `json:"outcome,omitempty"`
	Output            string          `json:"output,omitempty"`
	Error             string          `json:"error,omitempty"`
	Usage             *snapshot.Usage `json:"usage,omitempty"`
	Cost              float64         `json:"cost"`
	FailingCheck      string          `json:"failing_check,omitempty"`
	InfraKind         string          `json:"infra_kind,omitempty"`
	PRNumber          int             `json:"pr_number,omitempty"`
	PRURL             string          `json:"pr_url,omitempty"`
	Branch            string          `json:"branch,omitempty"`
	HeadSHA           string          `json:"head_sha,omitempty"`
	RateLimited       bool            `json:"rate_limited"`
	CostLimited       bool            `json:"cost_limited"`
	Effects           []jobEffect     `json:"effects,omitempty"`
}

type jobEffect = coordinator.EffectRequest

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
func (c *fencedClient) CreatePR(repo string, opts vcs.PRCreateOpts) (vcs.PR, error) {
	return vcs.PR{}, c.record(jobEffect{Kind: "pr-create", Repo: repo, Head: opts.Head, Base: opts.Base, Title: opts.Title, Body: opts.Body})
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

// Unsupported writes never fall through to the embedded worker VCS client.
func (c *fencedClient) CreateIssue(string, string, string) (vcs.Issue, error) {
	return vcs.Issue{}, errors.New("worker cannot create issues")
}
func (c *fencedClient) UpdateIssue(string, int, vcs.IssueUpdate) (vcs.Issue, error) {
	return vcs.Issue{}, errors.New("worker cannot edit issues")
}
func (c *fencedClient) DeleteIssueComment(string, int, int64) error {
	return errors.New("worker cannot delete comments")
}
func (c *fencedClient) AddSubIssue(string, int, int64) error {
	return errors.New("worker cannot change issue hierarchy")
}
func (c *fencedClient) UploadAttachment(string, string) (string, error) {
	return "", errors.New("worker cannot upload attachments")
}
func (c *fencedClient) InitLabels(string, []vcs.Label) error {
	return errors.New("worker cannot initialize labels")
}
func (c *fencedClient) ClosePR(string, int) error { return errors.New("worker cannot close PRs") }
func (c *fencedClient) DeleteBranch(string, string) error {
	return errors.New("worker cannot delete remote branches")
}
