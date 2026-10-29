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
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type ExecuteFunc func(context.Context, string, execution.Options) (execution.Result, error)
type InspectFunc func(context.Context, string) (Source, error)

type Runner struct {
	Store   *Store
	Execute ExecuteFunc
	Inspect InspectFunc
	// Prepare creates the review checkout only after the plan is durably saved.
	Prepare func(context.Context) (string, error)
	// TestCommand is the repository's configured test command; the reviewer is
	// told to run it. Empty means the reviewer discovers the standard command.
	TestCommand string
	CIRequired  bool
	// CI is queried immediately before review, after independent planning.
	CI func() (vcs.CIEvidence, error)
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

// Run executes fresh planning (with at most one isolation fallback) and review calls. The
// caller supplies a deferred workspace preparer (or an existing workspace) and
// authoritative inspection;
// this core never fetches Git state, falls back to another provider, or resumes.
func (runner Runner) Run(ctx context.Context, c Contract, scratch, workspace string) (record Record, err error) {
	d, err := c.Digest()
	if err != nil {
		return record, err
	}
	record = Record{ReviewMode: FullSuite, Version: Version, Contract: c, ContractDigest: d, AttemptID: randomID(), Role: "acceptance", Verdict: Blocked, StartedAt: time.Now().UTC()}
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
	if containsPath(scratch, runner.Store.path) || containsPath(runner.Store.path, scratch) {
		return record, errors.New("acceptance scratch and store must be separate directories")
	}
	if runner.Prepare == nil {
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
	planInstruction := ""
	if c.EvidenceSchema == 1 {
		planInstruction = criteriaInstruction
	}
	plan := Plan{PlannerIsolation: ToolsDisabled, Version: Version, ContractDigest: d, AttemptID: record.AttemptID, StartedAt: time.Now().UTC()}
	// Only requirements are sent in phase one: no source locations, Git inputs,
	// developer output, contract identity, or development session identifiers.
	planned, err := runner.executeRecorded(ctx, c, "acceptance-plan", execute, "Create an independent acceptance test plan for these requirements. Return the plan as nonempty text. Do not inspect source or use tools.\n\n"+planInstruction+c.Requirements,
		execution.Options{Workspace: scratch, Role: "acceptance", PermissionMode: "default", ToolsDisabled: true})
	record.Plan.PlannerIsolation = ToolsDisabled
	if err != nil && execution.IsKind(err, "unsupported_tools") && planned.ErrorKind == "unsupported_tools" {
		if err = ctx.Err(); err != nil {
			return record, err
		}
		entries, readErr := os.ReadDir(scratch)
		if readErr != nil || len(entries) != 0 {
			return record, errors.New("planner scratch must remain empty before isolation fallback")
		}
		plan.PlannerIsolation = IsolatedReadonly
		record.Plan.PlannerIsolation = IsolatedReadonly
		planned, err = runner.executeRecorded(ctx, c, "acceptance-plan", execute, "Create an independent acceptance test plan for these requirements. Return the plan as nonempty text. Do not read files outside your working directory or any repository. Do not inspect source.\n\n"+planInstruction+c.Requirements,
			execution.Options{Workspace: scratch, Role: "acceptance", PermissionMode: "default"})
	}
	if err != nil {
		return record, err
	}
	if err = ctx.Err(); err != nil {
		return record, err
	}
	plan.Planner = native(planned)
	plan.Text = planned.Text
	if c.EvidenceSchema == 1 {
		if err = decodeStrict([]byte(unfenceReport(plan.Text)), &plan.Criteria); err != nil {
			return record, err
		}
	}
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
	if runner.Prepare != nil {
		if err = ctx.Err(); err != nil {
			return record, err
		}
		workspace, err = runner.Prepare(ctx)
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
	}
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
	if runner.CIRequired && runner.CI != nil {
		ci, ciErr := runner.CI()
		if ciErr == nil && ci.SuccessfulFor(c.HeadSHA) {
			record.ReviewMode = CIVerified
			record.CI = &ci
		}
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
	prompt := "Review mode: full-suite. " + runner.reviewPrompt()
	if record.ReviewMode == CIVerified {
		evidence, _ := json.Marshal(record.CI)
		prompt = strings.Replace(runner.reviewPrompt(), runner.fullSuiteInstruction(), "Required CI succeeded on the exact contract head. Review mode: ci-verified. Run requirement-targeted tests and review the source instead of re-running the full suite. Authoritative CI check identities: "+string(evidence)+". ", 1)
	}
	if c.EvidenceSchema == 1 {
		prompt = "Review the immutable submitted checkout against the approved requirements and saved criteria. Do not edit tracked source, commit, change HEAD or publish comments. The plan is not authority to expand scope. Run only checks needed to close evidence gaps. " + checksInstruction + " Return raw JSON with status, summary, evidence (nonempty string array), findings (kind: requirement|defect|advisory, summary, requirement, defect, evidence (string), observation for failing findings, optional locations [{path: repository-relative, line: positive head-revision line, side: head}]), checks, next_action, next_owner. Failing findings require concrete evidence; advisory findings never fail acceptance. "
		if record.CI != nil {
			ci, _ := json.Marshal(record.CI)
			prompt += " Authoritative exact-head CI observation (establish relevant coverage before reuse): " + string(ci)
		}
		if runner.TestCommand != "" {
			prompt += " Configured test command, use only as needed: " + runner.TestCommand
		}
	}
	reviewed, reviewErr := runner.executeRecorded(ctx, c, "acceptance-review", execute, prompt+string(input),
		execution.Options{Workspace: workspace, Role: "acceptance", PermissionMode: "bypassPermissions"})
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
	if err = record.Reviewer.validate(c.Development, ReviewerPolicy); err != nil {
		return record, err
	}
	if record.Reviewer.SessionID == plan.Planner.SessionID || record.Reviewer.RunID == plan.Planner.RunID {
		return record, errors.New("review requires a fresh native session and run")
	}
	var output struct {
		Checks     []RequirementCheck `json:"checks"`
		NextAction string             `json:"next_action"`
		NextOwner  string             `json:"next_owner"`
		Status     Verdict            `json:"status"`
		Findings   []Finding          `json:"findings"`
		Summary    string             `json:"summary"`
		Evidence   []string           `json:"evidence"`
	}
	if len(reviewed.Text) > maxFileBytes {
		return record, errors.New("acceptance report exceeds size limit")
	}
	if err = decodeStrict([]byte(unfenceReport(reviewed.Text)), &output); err != nil {
		return record, fmt.Errorf("invalid acceptance report: %w", err)
	}
	if output.Status == "infrastructure_error" {
		return record, errors.New("review infrastructure failure: " + output.Summary)
	}
	if (output.Status != Passed && output.Status != Failed && output.Status != Blocked) || !nonempty(output.Summary) || len(output.Evidence) == 0 {
		return record, errors.New("acceptance report requires typed verdict, summary and evidence")
	}
	for _, e := range output.Evidence {
		if !nonempty(e) {
			return record, errors.New("acceptance report contains empty evidence")
		}
	}
	failing, findingErr := validateFindings(output.Findings)
	if findingErr != nil {
		return record, findingErr
	}
	if output.Status == Failed && len(output.Findings) == 0 {
		return record, errors.New("failed verdict requires requirement or defect findings")
	}
	if c.EvidenceSchema != 1 && output.Status != Blocked {
		if failing {
			output.Status = Failed
		} else {
			output.Status = Passed
		}
	}
	record.Findings = output.Findings
	record.Checks, record.NextAction, record.NextOwner = output.Checks, output.NextAction, output.NextOwner
	record.Verdict = output.Status
	record.Report = output.Summary
	if err = ValidateChecks(c, record); err != nil {
		return record, err
	}
	record.Findings = DeduplicateFindings(record.Findings)
	record.Evidence = output.Evidence
	return record, nil
}

func (runner Runner) fullSuiteInstruction() string {
	test := "the repository's standard test command"
	if nonempty(runner.TestCommand) {
		test = "the configured test command `" + strings.TrimSpace(runner.TestCommand) + "`"
	}
	return "You must run " + test + " and also tests targeted at the requirements. "
}

// reviewPrompt permits commands in the disposable checkout of the pinned head.
func (runner Runner) reviewPrompt() string {
	return "Review the source against the pinned contract below. The independently saved plan is a review aid, not a requirement source. The review checkout is a disposable detached copy of the PR head; you may build and run tests in it, but do not change tracked source, commit, or switch HEAD, and you have no remote or credentials. " +
		runner.fullSuiteInstruction() +
		"Use blocked only when requirements cannot be evaluated, such as missing or contradictory requirements or unavailable source. If the configured test command, targeted tests, and source review produced the required evidence, inability to create additional isolation or tooling beyond that command is a limitation, not grounds for blocked. Return passed or failed based on the requirements and record those limitations in evidence. " +
		"A failed verdict requires at least one finding tied to a requirement in the contract issue body or a concrete correctness/safety defect in changed code, even outside the plan. Uncovered plan items without a requirement violation or demonstrated defect are advisory only and never cause failed on their own. Each failing finding must name the violated requirement or defect and concrete evidence. Return passed when only advisory findings remain and evidence suffices. Return exactly one JSON object with only status (passed, failed, or blocked), summary (nonempty string), evidence (nonempty array of nonempty strings), and findings (array, empty if none). Every failed finding that points at code must carry locations: an array of objects with path (repository-relative), line (1-based line in the head revision), and side (exactly head). Never infer locations from base-revision lines. Each finding has kind (requirement, defect, or advisory), summary (nonempty string), requirement (contract requirement text for kind requirement), defect (correctness/safety violation for kind defect), and evidence (concrete evidence required for either failing kind). " +
		"Record every command you executed as one evidence entry of the form `" + EvidenceCommandPrefix + " <exact command> => <result>`; a passed verdict whose evidence lists no executed command is rejected. " +
		"Return raw JSON only, without Markdown code fences. Do not supply actor or execution identities.\n\n"
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
