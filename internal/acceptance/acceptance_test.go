package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

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

func TestEvaluate(t *testing.T) {
	if err := Evaluate(contract(), passing()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Record){
		"record version":         func(r *Record) { r.Version = 2 },
		"contract version":       func(r *Record) { r.Contract.Version = 2 },
		"digest":                 func(r *Record) { r.ContractDigest = "wrong" },
		"generation":             func(r *Record) { r.Contract.Generation++ },
		"head":                   func(r *Record) { r.Contract.HeadSHA = "stale" },
		"base":                   func(r *Record) { r.Contract.BaseSHA = "stale" },
		"integration":            func(r *Record) { r.Contract.IntegrationSHA = "stale" },
		"policy":                 func(r *Record) { r.Contract.PolicyDigest = "stale" },
		"requirements":           func(r *Record) { r.Contract.Requirements = "other" },
		"task":                   func(r *Record) { r.Contract.TaskID = "other" },
		"repository":             func(r *Record) { r.Contract.Repository = "other" },
		"PR":                     func(r *Record) { r.Contract.PRNumber++ },
		"no attempt":             func(r *Record) { r.AttemptID = "" },
		"role":                   func(r *Record) { r.Role = "development" },
		"failed":                 func(r *Record) { r.Verdict = Failed },
		"blocked":                func(r *Record) { r.Verdict = Blocked },
		"cancelled":              func(r *Record) { r.Cancelled = true },
		"incomplete":             func(r *Record) { r.Completed = false },
		"missing report":         func(r *Record) { r.Report = "" },
		"missing evidence":       func(r *Record) { r.Evidence = nil },
		"empty evidence":         func(r *Record) { r.Evidence = []string{" "} },
		"planner self session":   func(r *Record) { r.Plan.Planner.SessionID = r.Contract.Development.SessionID },
		"planner self run":       func(r *Record) { r.Plan.Planner.RunID = r.Contract.Development.RunID },
		"reviewer self session":  func(r *Record) { r.Reviewer.SessionID = r.Contract.Development.SessionID },
		"reviewer self run":      func(r *Record) { r.Reviewer.RunID = r.Contract.Development.RunID },
		"reused planner session": func(r *Record) { r.Reviewer.SessionID = r.Plan.Planner.SessionID },
		"reused planner run":     func(r *Record) { r.Reviewer.RunID = r.Plan.Planner.RunID },
		"missing native session": func(r *Record) { r.Reviewer.SessionID = "" },
		"missing native run":     func(r *Record) { r.Reviewer.RunID = "" },
		"failed native":          func(r *Record) { r.Reviewer.Status = "cancelled" },
		"native exit":            func(r *Record) { r.Reviewer.ExitCode = 1 },
		"native error":           func(r *Record) { r.Reviewer.Error = "failure" },
		"native error kind":      func(r *Record) { r.Reviewer.ErrorKind = "canceled" },
		"native policy":          func(r *Record) { r.Reviewer.Policy = "default" },
		"no command evidence":    func(r *Record) { r.Evidence = []string{"test evidence"} },
		"bare command prefix":    func(r *Record) { r.Evidence = []string{"command:  "} },
		"planner policy":         func(r *Record) { r.Plan.Planner.Policy = "managed-development" },
		"plan missing":           func(r *Record) { r.Plan.Text = "" },
		"plan digest":            func(r *Record) { r.Plan.Digest = "wrong" },
		"plan contract":          func(r *Record) { r.Plan.ContractDigest = "wrong" },
		"plan timing":            func(r *Record) { r.Plan.CompletedAt = r.ReviewStartedAt.Add(time.Second) },
		"start timing":           func(r *Record) { r.StartedAt = r.Plan.StartedAt.Add(time.Second) },
		"end timing":             func(r *Record) { r.CompletedAt = r.ReviewStartedAt.Add(-time.Second) },
		"dirty before":           func(r *Record) { r.Before.Clean = false },
		"dirty after":            func(r *Record) { r.After.Clean = false },
		"source mutation":        func(r *Record) { r.After.HeadSHA = "changed" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := passing()
			change(&r)
			if Evaluate(contract(), r) == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
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

func TestStore(t *testing.T) {
	s := openTestStore(t)
	r := passing()
	c := r.Contract
	if err := s.SavePlan(c, r.Plan); err != nil {
		t.Fatal(err)
	}
	p, err := s.LoadPlan(c, r.Plan.AttemptID)
	if err != nil || p.Digest != r.Plan.Digest {
		t.Fatalf("load plan: %v", err)
	}
	p.Text = "replacement"
	p.Digest = digest(p.Text)
	if s.SavePlan(c, p) == nil {
		t.Fatal("overwrote immutable plan")
	}
	if err := s.SaveRecord(r); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadRecord(c, r.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(c, loaded); err != nil {
		t.Fatal(err)
	}
	if s.SaveRecord(r) == nil {
		t.Fatal("overwrote immutable record")
	}
	name, _ := planName(c, r.Plan.AttemptID)
	st, err := os.Stat(filepath.Join(s.path, name, "data.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("private file: %v %v", st, err)
	}
	entries, _ := os.ReadDir(s.path)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatal("temporary file leaked")
		}
	}
	if err := os.WriteFile(filepath.Join(s.path, name, "data.json"), []byte(`{"version":1,"digest":"bad","data":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadPlan(c, r.Plan.AttemptID); err == nil {
		t.Fatal("accepted corrupt plan")
	}
}

func TestStorePathSafety(t *testing.T) {
	s := openTestStore(t)
	r := passing()
	c := r.Contract
	c.Repository = "../../escape"
	r.Contract = c
	r.ContractDigest, _ = c.Digest()
	r.Plan.ContractDigest = r.ContractDigest
	r.AttemptID = "../../escape"
	r.Plan.AttemptID = r.AttemptID
	if err := s.SaveRecord(r); err != nil {
		t.Fatal(err)
	}
	name, _ := recordName(c, r.AttemptID)
	if strings.ContainsAny(name, "/\\") {
		t.Fatal("unhashed storage key")
	}
	if _, err := s.LoadRecord(c, r.AttemptID); err != nil {
		t.Fatal(err)
	}
	name, _ = planName(c, r.Plan.AttemptID)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(s.path, name)); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePlan(c, r.Plan); err == nil {
		t.Fatal("overwrote symlink")
	}
	if _, err := s.LoadPlan(c, r.Plan.AttemptID); err == nil {
		t.Fatal("loaded symlink")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "untouched" {
		t.Fatal("modified symlink target")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(s.path, link); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenStore(link); err == nil {
		bad.Close()
		t.Fatal("accepted symlink store root")
	}
	if _, err := OpenStore("relative"); err == nil {
		t.Fatal("accepted relative store root")
	}
}

func TestStoreConcurrentPlanPublication(t *testing.T) {
	s := openTestStore(t)
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.SavePlan(contract(), passing().Plan) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("immutable publication successes: %d", successes.Load())
	}
	if _, err := s.LoadPlan(contract(), "attempt"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreBoundsAndContentDigests(t *testing.T) {
	for _, mode := range []string{"oversize", "plan digest", "contract digest", "record digest"} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			r := passing()
			name, _ := recordName(r.Contract, r.AttemptID)
			if mode == "oversize" {
				if err := os.Mkdir(filepath.Join(s.path, name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(s.path, name, "data.json"), make([]byte, maxFileBytes+1), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if mode == "plan digest" {
					r.Plan.Digest = "corrupt"
				}
				if mode == "contract digest" {
					r.ContractDigest = "corrupt"
				}
				if mode == "record digest" {
					if err := s.SaveRecord(r); err != nil {
						t.Fatal(err)
					}
					b, _ := os.ReadFile(filepath.Join(s.path, name, "data.json"))
					b = []byte(strings.Replace(string(b), "verified", "tampered", 1))
					if err := os.WriteFile(filepath.Join(s.path, name, "data.json"), b, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := s.write(name, r); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := s.LoadRecord(r.Contract, r.AttemptID); err == nil {
				t.Fatal("accepted corrupt/oversized record")
			}
		})
	}
}

func setupRunner(t *testing.T) (Runner, string, string) {
	t.Helper()
	s := openTestStore(t)
	scratch := t.TempDir()
	workspace := t.TempDir()
	scratch, _ = filepath.EvalSymlinks(scratch)
	workspace, _ = filepath.EvalSymlinks(workspace)
	c := contract()
	r := Runner{Store: s, Inspect: func(context.Context, string) (Source, error) {
		return Source{c.HeadSHA, c.BaseSHA, c.IntegrationSHA, true}, nil
	}}
	r.Execute = func(_ context.Context, prompt string, o execution.Options) (execution.Result, error) {
		if o.Role != "acceptance" || o.Resume || o.SessionKey != "" {
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
		if !strings.Contains(prompt, p.Text) || !strings.Contains(prompt, c.HeadSHA) || strings.Contains(prompt, c.Development.SessionID) {
			t.Fatal("review input mismatch")
		}
		result := reviewerResult("reviewer", "review-run")
		result.Text = `{"status":"passed","summary":"verified","evidence":["behavior check passed","` + testCommandEvidence + `"]}`
		return result, nil
	}
	return r, scratch, workspace
}

func TestRunnerPassAndPhaseOrder(t *testing.T) {
	r, scratch, workspace := setupRunner(t)
	base := r.Execute
	calls := 0
	inspect := r.Inspect
	inspections := 0
	r.Inspect = func(ctx context.Context, w string) (Source, error) {
		inspections++
		_ = savedTestPlan(t, r.Store, contract())
		return inspect(ctx, w)
	}
	r.Execute = func(ctx context.Context, p string, o execution.Options) (execution.Result, error) {
		calls++
		return base(ctx, p, o)
	}
	record, err := r.Run(context.Background(), contract(), scratch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("executions: %d", calls)
	}
	if inspections != 2 {
		t.Fatalf("source inspections: %d", inspections)
	}
	if err := Evaluate(contract(), record); err != nil {
		t.Fatal(err)
	}
	stored, err := r.Store.LoadRecord(contract(), record.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Evaluate(contract(), stored); err != nil {
		t.Fatal(err)
	}
}

func TestContractRequiredInputsAndDigest(t *testing.T) {
	valid := contract()
	d, err := valid.Digest()
	if err != nil || len(d) != 64 {
		t.Fatalf("digest: %q %v", d, err)
	}
	again, _ := valid.Digest()
	if d != again {
		t.Fatal("unstable contract digest")
	}
	valid.IntegrationSHA = ""
	if err := valid.Validate(); err != nil {
		t.Fatal("optional integration SHA rejected")
	}
	changes := []func(*Contract){
		func(c *Contract) { c.Version = 0 }, func(c *Contract) { c.Generation = 0 }, func(c *Contract) { c.PRNumber = 0 },
		func(c *Contract) { c.TaskID = "" }, func(c *Contract) { c.Repository = "" }, func(c *Contract) { c.HeadSHA = "" },
		func(c *Contract) { c.BaseSHA = "" }, func(c *Contract) { c.PolicyDigest = " " }, func(c *Contract) { c.Requirements = "" },
		func(c *Contract) { c.Development.SessionID = "" }, func(c *Contract) { c.Development.RunID = "" },
	}
	for _, change := range changes {
		c := contract()
		change(&c)
		if _, err := c.Digest(); err == nil {
			t.Fatalf("accepted invalid contract: %+v", c)
		}
	}
}

func TestRunnerAdmissionFailures(t *testing.T) {
	for _, mode := range []string{"scratch source", "overlap", "missing inspector", "cancelled", "relative"} {
		t.Run(mode, func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			calls := 0
			r.Execute = func(context.Context, string, execution.Options) (execution.Result, error) {
				calls++
				return execution.Result{}, errors.New("unexpected execution")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "scratch source":
				if err := os.WriteFile(filepath.Join(scratch, "source.go"), []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
			case "overlap":
				scratch = workspace
			case "missing inspector":
				r.Inspect = nil
			case "cancelled":
				cancel()
			case "relative":
				scratch = "relative"
			}
			record, err := r.Run(ctx, contract(), scratch, workspace)
			if err == nil || calls != 0 || record.Verdict != Blocked {
				t.Fatalf("admission failed open: %v %d", err, calls)
			}
		})
	}
}

func TestRunnerFailsClosed(t *testing.T) {
	cases := []string{"malformed", "trailing", "duplicate", "identity injection", "missing evidence", "empty evidence", "null", "failed native", "exit", "policy", "no command evidence", "missing session", "missing run", "self session", "self run", "planner self", "planner failure", "planner empty", "reused session", "reused run", "mutation", "dirty before", "inspect error", "cancel planner", "cancel review", "execution error"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			base := r.Execute
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.Execute = func(ctx context.Context, p string, o execution.Options) (execution.Result, error) {
				result, err := base(ctx, p, o)
				if err != nil {
					return result, err
				}
				if o.ToolsDisabled {
					switch mode {
					case "planner self":
						result.NativeSessionID = contract().Development.SessionID
					case "planner failure":
						result.Status = "error"
					case "planner empty":
						result.Text = " "
					case "cancel planner":
						cancel()
					}
					return result, nil
				}
				switch mode {
				case "malformed":
					result.Text = "not JSON"
				case "trailing":
					result.Text += " {}"
				case "duplicate":
					result.Text = `{"status":"failed","status":"passed","summary":"ok","evidence":["ok"]}`
				case "identity injection":
					result.Text = `{"status":"passed","summary":"ok","evidence":["ok"],"session":"trusted"}`
				case "missing evidence":
					result.Text = `{"status":"passed","summary":"ok"}`
				case "empty evidence":
					result.Text = `{"status":"passed","summary":"ok","evidence":[""]}`
				case "null":
					result.Text = "null"
				case "failed native":
					result.Status = "error"
				case "exit":
					result.ExitCode = 1
				case "policy":
					result.ExecutionPolicy = "default"
				case "no command evidence":
					result.Text = `{"status":"passed","summary":"verified","evidence":["read the code"]}`
				case "missing session":
					result.NativeSessionID = ""
				case "missing run":
					result.RunID = ""
				case "self session":
					result.NativeSessionID = contract().Development.SessionID
				case "self run":
					result.RunID = contract().Development.RunID
				case "reused session":
					result.NativeSessionID = "planner"
				case "reused run":
					result.RunID = "plan-run"
				case "cancel review":
					cancel()
				case "execution error":
					return result, errors.New("execution failed")
				}
				return result, nil
			}
			inspect := r.Inspect
			inspections := 0
			r.Inspect = func(ctx context.Context, w string) (Source, error) {
				inspections++
				s, err := inspect(ctx, w)
				switch mode {
				case "mutation":
					if inspections == 2 {
						s.HeadSHA = "mutated"
					}
				case "dirty before":
					s.Clean = false
				case "inspect error":
					return s, errors.New("unavailable")
				}
				return s, err
			}
			record, err := r.Run(ctx, contract(), scratch, workspace)
			if err == nil {
				t.Fatal("expected failure")
			}
			if record.Verdict != Blocked || Evaluate(contract(), record) == nil {
				t.Fatal("failure satisfied gate")
			}
			stored, loadErr := r.Store.LoadRecord(contract(), record.AttemptID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if Evaluate(contract(), stored) == nil {
				t.Fatal("stored failure satisfied gate")
			}
			if strings.HasPrefix(mode, "cancel") && !record.Cancelled {
				t.Fatal("cancellation missing")
			}
		})
	}
}

func TestRunnerNonpassingVerdicts(t *testing.T) {
	for _, status := range []Verdict{Failed, Blocked} {
		t.Run(string(status), func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			base := r.Execute
			r.Execute = func(ctx context.Context, p string, o execution.Options) (execution.Result, error) {
				result, err := base(ctx, p, o)
				if !o.ToolsDisabled {
					b, _ := json.Marshal(map[string]any{"status": status, "summary": "not accepted", "evidence": []string{"reason"}})
					result.Text = string(b)
				}
				return result, err
			}
			record, err := r.Run(context.Background(), contract(), scratch, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if record.Verdict != status || Evaluate(contract(), record) == nil {
				t.Fatal("nonpassing verdict satisfied gate")
			}
		})
	}
}

func TestPlannerIsolationFallback(t *testing.T) {
	for _, kind := range []string{"", "unsupported_tools", "auth", "protocol_invalid_result", "fallback failure", "dirty scratch"} {
		t.Run(kind, func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			if err := os.Remove(workspace); err != nil {
				t.Fatal(err)
			}
			prepared, calls := false, 0
			r.Prepare = func(context.Context) (string, error) {
				_ = savedTestPlan(t, r.Store, contract())
				prepared = true
				return workspace, os.Mkdir(workspace, 0700)
			}
			base := r.Execute
			r.Execute = func(ctx context.Context, prompt string, o execution.Options) (execution.Result, error) {
				calls++
				if o.Workspace == scratch {
					if prepared {
						t.Fatal("checkout prepared during planning")
					}
					if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("review checkout exists during planning")
					}
					entries, err := os.ReadDir(scratch)
					if err != nil || len(entries) != 0 {
						t.Fatal("planner scratch is not empty")
					}
					if calls == 1 && kind != "" {
						k := kind
						if kind == "fallback failure" || kind == "dirty scratch" {
							k = "unsupported_tools"
						}
						if kind == "dirty scratch" {
							if err := os.WriteFile(filepath.Join(scratch, "unexpected"), []byte("data"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						return execution.Result{Status: "error", ErrorKind: k}, &execution.Error{Kind: k}
					}
					if calls == 2 {
						if o.ToolsDisabled || len(o.AllowedTools) != 0 || len(o.DisallowedTools) != 0 || o.PermissionMode != "default" ||
							!strings.Contains(prompt, "Do not read files outside your working directory or any repository") {
							t.Fatalf("unsafe fallback: %+v", o)
						}
						if kind == "fallback failure" {
							return execution.Result{Status: "error", ErrorKind: "unsupported_tools"}, &execution.Error{Kind: "unsupported_tools"}
						}
						result := successful("planner", "plan-run")
						result.Text = "independent test plan"
						return result, nil
					}
				}
				return base(ctx, prompt, o)
			}
			record, err := r.Run(context.Background(), contract(), scratch, "")
			wantCalls, mode := 2, ToolsDisabled
			if kind == "unsupported_tools" {
				wantCalls, mode = 3, IsolatedReadonly
			} else if kind != "" {
				wantCalls = 1
				if kind == "fallback failure" {
					wantCalls = 2
				}
				if err == nil || prepared || record.Verdict != Blocked || calls != wantCalls {
					t.Fatalf("failure did not stop: calls=%d prepared=%v err=%v", calls, prepared, err)
				}
				return
			}
			if err != nil || !prepared || calls != wantCalls || record.Plan.PlannerIsolation != mode {
				t.Fatalf("isolation result: calls=%d prepared=%v mode=%s err=%v", calls, prepared, record.Plan.PlannerIsolation, err)
			}
			stored, err := r.Store.LoadRecord(contract(), record.AttemptID)
			if err != nil || stored.Plan.PlannerIsolation != mode || Evaluate(contract(), stored) != nil {
				t.Fatalf("invalid persisted isolation: %+v %v", stored.Plan, err)
			}
		})
	}
}

func TestPlanIsolationValidation(t *testing.T) {
	for _, mode := range []string{ToolsDisabled, IsolatedReadonly, "", "unknown"} {
		for _, policy := range []string{PlannerPolicy, ReviewerPolicy} {
			p := passing().Plan
			p.PlannerIsolation, p.Planner.Policy = mode, policy
			want := (mode == ToolsDisabled || mode == IsolatedReadonly) && policy == PlannerPolicy
			if (p.validate(contract()) == nil) != want {
				t.Fatalf("mode=%q policy=%q", mode, policy)
			}
		}
	}
}
