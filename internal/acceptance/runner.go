package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

type ExecuteFunc func(context.Context, string, execution.Options) (execution.Result, error)
type InspectFunc func(context.Context, string) (Source, error)

type Runner struct {
	Store   *Store
	Execute ExecuteFunc
	Inspect InspectFunc
}

func randomID() string { return execution.NewSessionKey() }

func containsPath(parent, child string) bool {
	r, err := filepath.Rel(parent, child)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator))
}

func directory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("acceptance workspace must be absolute")
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errors.New("acceptance workspace must be a directory")
	}
	return p, nil
}

// Run executes exactly one fresh planning call and one fresh review call. The
// caller supplies an immutable review workspace and authoritative inspection;
// this core never fetches Git state, falls back to another provider, or resumes.
func (runner Runner) Run(ctx context.Context, c Contract, scratch, workspace string) (record Record, err error) {
	d, err := c.Digest()
	if err != nil {
		return record, err
	}
	record = Record{Version: Version, Contract: c, ContractDigest: d, AttemptID: randomID(), Role: "acceptance", Verdict: Blocked, StartedAt: time.Now().UTC()}
	if runner.Store == nil {
		return record, errors.New("acceptance store required")
	}
	defer func() {
		if err != nil {
			record.Verdict = Blocked
			record.Report = err.Error()
		}
		record.Cancelled = ctx.Err() != nil
		if record.Cancelled {
			record.Verdict = Blocked
			err = errors.Join(err, ctx.Err())
		}
		record.Completed = true
		record.CompletedAt = time.Now().UTC()
		if err == nil && record.Verdict == Passed {
			if gateErr := Evaluate(c, record); gateErr != nil {
				err = gateErr
				record.Verdict = Blocked
				record.Report = gateErr.Error()
			}
		}
		if saveErr := runner.Store.SaveRecord(record); saveErr != nil {
			err = errors.Join(err, saveErr)
			record.Verdict = Blocked
		}
	}()
	if runner.Inspect == nil {
		return record, errors.New("authoritative source inspector required")
	}
	if err = ctx.Err(); err != nil {
		return record, err
	}
	scratch, err = directory(scratch)
	if err != nil {
		return record, err
	}
	workspace, err = directory(workspace)
	if err != nil {
		return record, err
	}
	paths := []string{scratch, workspace, runner.Store.path}
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if containsPath(a, b) || containsPath(b, a) {
				return record, errors.New("acceptance scratch, source and store must be separate directories")
			}
		}
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		return record, err
	}
	if len(entries) != 0 {
		return record, errors.New("planner scratch must be empty and contain no source or instructions")
	}
	execute := runner.Execute
	if execute == nil {
		execute = execution.Execute
	}
	plan := Plan{Version: Version, ContractDigest: d, AttemptID: record.AttemptID, StartedAt: time.Now().UTC()}
	// Only requirements are sent in phase one: no source locations, Git inputs,
	// developer output, contract identity, or development session identifiers.
	planned, err := execute(ctx, "Create an independent acceptance test plan for these requirements. Return the plan as nonempty text. Do not inspect source or use tools.\n\n"+c.Requirements,
		execution.Options{Workspace: scratch, Role: "acceptance", PermissionMode: "default", ToolsDisabled: true})
	if err != nil {
		return record, err
	}
	if err = ctx.Err(); err != nil {
		return record, err
	}
	plan.Planner = native(planned)
	plan.Text = planned.Text
	plan.Digest = digest(plan.Text)
	plan.CompletedAt = time.Now().UTC()
	if err = plan.validate(c); err != nil {
		return record, err
	}
	if err = runner.Store.SavePlan(c, plan); err != nil {
		return record, err
	}
	// Load the durable copy instead of exposing an unsaved in-memory plan.
	plan, err = runner.Store.LoadPlan(c, record.AttemptID)
	if err != nil {
		return record, err
	}
	record.Plan = plan
	record.Before, err = runner.Inspect(ctx, workspace)
	if err != nil {
		return record, err
	}
	if !record.Before.matches(c) {
		return record, errors.New("acceptance source is stale or dirty before review")
	}
	if err = ctx.Err(); err != nil {
		return record, err
	}
	record.ReviewStartedAt = time.Now().UTC()
	// Exclude development identity from the model prompt; the digest still pins
	// the complete contract, and native identity is verified outside the model.
	input, _ := json.Marshal(struct {
		ContractDigest string `json:"contract_digest"`
		TaskID         string `json:"task_id"`
		Generation     uint64 `json:"generation"`
		Repository     string `json:"repository"`
		PRNumber       int    `json:"pr_number"`
		HeadSHA        string `json:"head_sha"`
		BaseSHA        string `json:"base_sha"`
		IntegrationSHA string `json:"integration_sha,omitempty"`
		PolicyDigest   string `json:"policy_digest"`
		Requirements   string `json:"requirements"`
		Plan           string `json:"plan"`
		PlanDigest     string `json:"plan_digest"`
	}{d, c.TaskID, c.Generation, c.Repository, c.PRNumber, c.HeadSHA, c.BaseSHA, c.IntegrationSHA, c.PolicyDigest, c.Requirements, plan.Text, plan.Digest})
	reviewed, reviewErr := execute(ctx, "Review the immutable source against the pinned contract and independently saved plan below. Use read-only tools; never change source. Return exactly one JSON object with only status (passed, failed, or blocked), summary (nonempty string), and evidence (nonempty array of nonempty strings). Return raw JSON only, without Markdown code fences. Do not supply actor or execution identities.\n\n"+string(input),
		execution.Options{Workspace: workspace, Role: "acceptance", PermissionMode: "default"})
	record.Reviewer = native(reviewed)
	// Inspect even after execution failure/cancellation; use the caller's context
	// and fail closed if inspection itself cannot complete.
	var inspectErr error
	record.After, inspectErr = runner.Inspect(ctx, workspace)
	if err = errors.Join(reviewErr, inspectErr, ctx.Err()); err != nil {
		return record, err
	}
	if !record.After.matches(c) {
		return record, errors.New("acceptance source changed during review")
	}
	if err = record.Reviewer.validate(c.Development); err != nil {
		return record, err
	}
	if record.Reviewer.SessionID == plan.Planner.SessionID || record.Reviewer.RunID == plan.Planner.RunID {
		return record, errors.New("review requires a fresh native session and run")
	}
	var output struct {
		Status   Verdict  `json:"status"`
		Summary  string   `json:"summary"`
		Evidence []string `json:"evidence"`
	}
	if len(reviewed.Text) > maxFileBytes {
		return record, errors.New("acceptance report exceeds size limit")
	}
	if err = decodeStrict([]byte(unfenceReport(reviewed.Text)), &output); err != nil {
		return record, fmt.Errorf("invalid acceptance report: %w", err)
	}
	if (output.Status != Passed && output.Status != Failed && output.Status != Blocked) || !nonempty(output.Summary) || len(output.Evidence) == 0 {
		return record, errors.New("acceptance report requires typed verdict, summary and evidence")
	}
	for _, e := range output.Evidence {
		if !nonempty(e) {
			return record, errors.New("acceptance report contains empty evidence")
		}
	}
	record.Verdict = output.Status
	record.Report = output.Summary
	record.Evidence = output.Evidence
	return record, nil
}

// unfenceReport removes one Markdown code fence that encloses the whole
// trimmed report (models often wrap JSON in ```json). Any other shape is
// returned unchanged so strict decoding still rejects surrounding prose,
// multiple blocks and trailing data.
func unfenceReport(text string) string {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "```") || !strings.HasSuffix(t, "```") || len(t) < 6 {
		return text
	}
	open, body, ok := strings.Cut(t[3:len(t)-3], "\n")
	if !ok || (strings.TrimSpace(open) != "" && !strings.EqualFold(strings.TrimSpace(open), "json")) || strings.Contains(body, "```") {
		return text
	}
	return body
}
