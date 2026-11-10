package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// Shared fixtures for the isolated-run tests. The legacy suite that used to
// provide them was removed, so the tests of this change carry what they use.

func contract() Contract {
	return Contract{Version: Version, TaskID: "task349", Generation: 1, Repository: "owner/repo", PRNumber: 349, HeadSHA: "head", BaseSHA: "base", IntegrationSHA: "integration", PolicyDigest: "policy", Requirements: "Required behavior", Development: Identity{"dev-session", "dev-run"}}
}

func successful(session, run string) execution.Result {
	return execution.Result{Status: "ok", NativeSessionID: session, RunID: run, ExecutionPolicy: "default", Profile: "test-profile", Backend: "test-backend", Model: "test-model"}
}

// reviewerResult is a successful reviewer execution under the command-capable policy.
func reviewerResult(session, run string) execution.Result {
	r := successful(session, run)
	r.ExecutionPolicy = ReviewerPolicy
	return r
}

const testCommandEvidence = "command: go test ./... => ok"

func passing() Record {
	c := contract()
	d, _ := c.Digest()
	now := time.Now().UTC()
	plan := Plan{PlannerIsolation: ToolsDisabled, Version: Version, ContractDigest: d, AttemptID: "attempt", Text: "independent plan", Planner: native(successful("plan-session", "plan-run")), StartedAt: now.Add(time.Second), CompletedAt: now.Add(2 * time.Second)}
	plan.Digest = digest(plan.Text)
	source := Source{c.HeadSHA, c.BaseSHA, c.IntegrationSHA, true}
	return Record{Version: Version, Contract: c, ContractDigest: d, Plan: plan, AttemptID: "attempt", Role: "acceptance", Reviewer: native(reviewerResult("review-session", "review-run")), Verdict: Passed, Report: "verified", Evidence: []string{"test evidence", testCommandEvidence}, Before: source, After: source, Completed: true, StartedAt: now, ReviewStartedAt: now.Add(3 * time.Second), CompletedAt: now.Add(4 * time.Second)}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func savedTestPlan(t *testing.T, s *Store, c Contract) Plan {
	t.Helper()
	k, err := key(c)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var latest Plan
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), k+".") || !strings.HasSuffix(entry.Name(), ".plan") {
			continue
		}
		var p Plan
		if err := s.read(entry.Name(), &p); err != nil {
			t.Fatal(err)
		}
		p, err = s.LoadPlan(c, p.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if latest.CompletedAt.IsZero() || latest.CompletedAt.Before(p.CompletedAt) {
			latest = p
		}
	}
	if latest.AttemptID == "" {
		t.Fatal("source exposed before durable plan")
	}
	return latest
}

func setupRunner(t *testing.T) (Runner, string, string) {
	t.Helper()
	return setupRunnerFor(t, contract())
}

// setupRunnerFor is setupRunner for a specific contract, whose plan key differs
// when its digest does.
func setupRunnerFor(t *testing.T, c Contract) (Runner, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := openTestStore(t)
	scratch := t.TempDir()
	workspace := t.TempDir()
	scratch, _ = filepath.EvalSymlinks(scratch)
	workspace, _ = filepath.EvalSymlinks(workspace)
	r := Runner{Store: s, Inspect: func(context.Context, string) (Source, error) {
		return Source{c.HeadSHA, c.BaseSHA, c.IntegrationSHA, true}, nil
	}}
	r.Execute = func(_ context.Context, prompt string, o execution.Options) (execution.Result, error) {
		if o.Role != "acceptance" || (o.Resume && o.SessionKey != "reviewer") || (!o.Resume && o.SessionKey != "") {
			t.Fatal("unsafe execution options")
		}
		if o.Workspace == scratch {
			if o.PermissionMode != "default" || !o.ToolsDisabled || len(o.AllowedTools) != 0 {
				t.Fatal("planner tools enabled")
			}
			for _, forbidden := range []string{workspace, s.path, c.HeadSHA, c.BaseSHA, c.IntegrationSHA, c.Development.SessionID, c.Development.RunID, c.TaskID, c.Repository} {
				if strings.Contains(prompt, forbidden) {
					t.Fatalf("planner received source/identity: %s", forbidden)
				}
			}
			result := successful("planner", "plan-run")
			result.Text = "independent test plan"
			return result, nil
		}
		if o.Workspace != workspace || o.ToolsDisabled || o.PermissionMode != "bypassPermissions" {
			t.Fatal("reviewer workspace/tools/policy mismatch")
		}
		p := savedTestPlan(t, s, c)
		if !o.Resume && (!strings.Contains(prompt, p.Text) || !strings.Contains(prompt, c.HeadSHA) || strings.Contains(prompt, c.Development.SessionID)) {
			t.Fatal("review input mismatch")
		}
		result := reviewerResult("reviewer", "review-run")
		if o.Resume {
			result.RunID = "correction-run"
		}
		result.Text = `{"status":"passed","summary":"verified","evidence":["behavior check passed","` + testCommandEvidence + `"]}`
		return result, nil
	}
	return r, scratch, workspace
}

func structuredRecord() Record {
	r := passing()
	r.Contract.EvidenceSchema = 1
	r.ContractDigest, _ = r.Contract.Digest()
	r.Plan.ContractDigest = r.ContractDigest
	r.Plan.Criteria = []Criterion{{"R1", "Required behavior", "execution"}}
	b, _ := json.Marshal(r.Plan.Criteria)
	r.Plan.Text, r.Plan.Digest = string(b), digest(string(b))
	r.Checks = []RequirementCheck{{ID: "R1", Status: Passed, Evidence: []RequirementEvidence{{Type: "unit", Check: "TestRequiredBehavior covers required behavior", Reference: "CI run 7/job 9/TestRequiredBehavior: PASS", Commit: r.Contract.HeadSHA, Result: "passed"}}}}
	r.NextOwner, r.NextAction = "runner", "Apply CI and merge gates"
	r.Evidence = []string{"Reused existing test result; no duplicate execution"}
	return r
}
