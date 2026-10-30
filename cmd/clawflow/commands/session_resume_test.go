package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestSessionResumeEligibility(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	original := snapshot.RunMeta{Status: "failed", DevelopmentSession: snapshot.DevelopmentSession{SessionID: "native", Profile: "dev", Backend: "codex", Role: config.RoleOperator, Workdir: dir, ErrorKind: "timeout"}}
	for _, tc := range []struct {
		name, want string
		mutate     func(*snapshot.RunMeta)
		count      int
		repair     bool
	}{
		{"timeout", "", func(*snapshot.RunMeta) {}, 0, false},
		{"writeback", "", func(m *snapshot.RunMeta) { m.Status = "success"; m.WritebackError = "network" }, 0, false},
		{"blocked released", "", func(m *snapshot.RunMeta) {
			m.Status = "success"
			m.Outcome = "agent-failed"
			m.Summary = "BLOCKED pending decision"
		}, 0, false},
		{"acceptance repair", "fresh_repair", func(m *snapshot.RunMeta) { m.Status = "success" }, 0, true},
		{"missing session", "no_session", func(m *snapshot.RunMeta) { m.DevelopmentSession.SessionID = "" }, 0, false},
		{"role", "role_changed", func(m *snapshot.RunMeta) { m.DevelopmentSession.Role = "acceptance" }, 0, false},
		{"profile", "profile_changed", func(m *snapshot.RunMeta) { m.DevelopmentSession.Profile = "old" }, 0, false},
		{"backend", "backend_changed", func(m *snapshot.RunMeta) { m.DevelopmentSession.BackendChanged = true }, 0, false},
		{"chain cap", "resume_limit", func(m *snapshot.RunMeta) { m.DevelopmentSession.ResumeCount = 2 }, 0, false},
		{"second resume", "", func(m *snapshot.RunMeta) { m.DevelopmentSession.ResumeCount = 1 }, 0, false},
		{"missing worktree", "worktree_missing", func(m *snapshot.RunMeta) { m.DevelopmentSession.Workdir = "gone" }, 0, false},
		{"output limit", "context_exhausted", func(m *snapshot.RunMeta) { m.Status = "output-limit" }, 0, false},
		{"context exhausted", "context_exhausted", func(m *snapshot.RunMeta) { m.DevelopmentSession.ErrorKind = "context_exhausted" }, 0, false},
		{"base moved", "base_moved", func(*snapshot.RunMeta) {}, 21, false},
		{"base unknown", "base_unknown", func(*snapshot.RunMeta) {}, -1, false},
		{"cancelled", "not_resumable", func(m *snapshot.RunMeta) { m.Status = "cancelled" }, 0, false},
		{"completed", "not_resumable", func(m *snapshot.RunMeta) { m.Status = "success" }, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := original
			tc.mutate(&m)
			if got := sessionResumeReason(m, config.RoleOperator, "dev", dir, tc.count, tc.repair); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	if err := os.Remove(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	if got := sessionResumeReason(original, config.RoleOperator, "dev", dir, 0, false); got != "worktree_missing" {
		t.Fatal(got)
	}
}

func resumeGitFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	run("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	run("remote", "add", "origin", dir)
	run("fetch", "origin", "main:refs/remotes/origin/main")
	return dir, run("rev-parse", "HEAD")
}

func TestDevelopmentDeltaAndPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "dev")
	dir, sha := resumeGitFixture(t)
	start := time.Now().UTC().Add(-time.Hour)
	prev := snapshot.RunMeta{Repo: "o/r", IssueNumber: 9, Operator: "implement", StartedAt: start, Status: "failed", DevelopmentSession: snapshot.DevelopmentSession{SessionID: "native", Profile: "dev", Backend: "codex", Role: config.RoleOperator, Workdir: dir, BaseSHA: sha, ErrorKind: "timeout"}}
	runDir := snapshot.RunDir("o/r", 9, start)
	if err := snapshot.WriteRunMeta(runDir, prev); err != nil {
		t.Fatal(err)
	}
	// A later reviewer run must never become the development resume source.
	reviewer := prev
	reviewer.Operator = "acceptance"
	reviewer.StartedAt = start.Add(time.Minute)
	reviewer.DevelopmentSession.SessionID = "reviewer"
	if err := snapshot.WriteRunMeta(snapshot.RunDir("o/r", 9, reviewer.StartedAt), reviewer); err != nil {
		t.Fatal(err)
	}
	if got := previousDevelopmentRun("o/r", 9, time.Now()); got.DevelopmentSession.SessionID != "native" {
		t.Fatalf("wrong source: %+v", got)
	}
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "acceptance"}[repair], func(t *testing.T) {
			j := &runJob{repo: "o/r", repoCfg: config.Repo{BaseBranch: "main"}, sub: &operator.Subject{Number: 9, Body: "Acceptance: targeted checks pass"}, op: &operator.Operator{Name: "implement"}}
			repairContext := ""
			if repair {
				j.op.Name = "repair-ci"
				j.repair = &ciRepairContext{PR: vcs.PR{Number: 11, HeadRepo: "o/r", HeadSHA: sha, HeadBranch: "fix/issue-9"}, Acceptance: &acceptanceFindings{Verdict: "failed", Summary: "missing boundary check"}}
				repairContext = j.repair.prompt("")
			}
			current := snapshot.RunMeta{Repo: "o/r", IssueNumber: 9, Operator: j.op.Name, StartedAt: time.Now()}
			outDir := snapshot.RunDir("o/r", 9, current.StartedAt)
			opts := developmentResumeOptions(context.Background(), j, prev, worktreeResult{Path: dir, Resumed: true}, []vcs.IssueComment{{Body: "old decision", CreatedAt: start.Add(-time.Minute).Format(time.RFC3339)}, {Body: "released decision", CreatedAt: start.Add(time.Minute).Format(time.RFC3339)}}, repairContext, &current, func() {
				if err := snapshot.WriteRunMeta(outDir, current); err != nil {
					t.Fatal(err)
				}
			})
			if !repair && (opts.SessionKey != "native" || !strings.Contains(opts.ResumePrompt, "timeout") || !strings.Contains(opts.ResumePrompt, "released decision") || strings.Contains(opts.ResumePrompt, "old decision")) {
				t.Fatalf("bad delta: %s", opts.ResumePrompt)
			}
			if repair && (opts.SessionKey != "" || opts.ResumePrompt != "") {
				t.Fatal("repair inherited the development session")
			}
			opts.Workdir, opts.Role = dir, config.RoleOperator
			opts.ResumeContext = repairContext
			opts.ExecuteFunc = func(_ context.Context, prompt string, request execution.Options) (execution.Result, error) {
				if repair {
					if request.Resume || request.SessionKey != "" || !strings.Contains(prompt, "missing boundary check") || !strings.Contains(prompt, "UNTRUSTED") {
						t.Fatal("repair was not delivered to a fresh session with findings")
					}
					return execution.Result{NativeSessionID: "fresh-repair", Profile: "dev", Backend: "new-backend", Text: "partial work"}, context.DeadlineExceeded
				}
				if !request.Resume || request.SessionKey != "native" || prompt != opts.ResumePrompt {
					t.Fatal("delta was not delivered to the native session")
				}
				return execution.Result{NativeSessionID: "native", Profile: "dev", Backend: "new-backend", Text: "partial work"}, context.DeadlineExceeded
			}
			if _, _, err := operator.Run(context.Background(), j.op, j.sub, nil, opts); err == nil {
				t.Fatal("expected interrupted execution")
			}
			data, err := os.ReadFile(filepath.Join(outDir, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved snapshot.RunMeta
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			s := saved.DevelopmentSession
			wantSession, wantCount := "native", 1
			if repair {
				wantSession, wantCount = "fresh-repair", 0
			}
			if s.SessionID != wantSession || s.Profile != "dev" || s.Backend != "new-backend" || !s.BackendChanged || s.ResumeCount != wantCount {
				t.Fatalf("identity lost: %+v", s)
			}
			if got := sessionResumeReason(saved, config.RoleOperator, "dev", dir, 0, false); got != "backend_changed" {
				t.Fatal(got)
			}
			opts.OnExecutionResult(execution.Result{NativeSessionID: "fresh", Profile: "dev", Backend: "codex"}, nil, false)
			if current.DevelopmentSession.ResumeCount != 0 {
				t.Fatal("fresh fallback did not reset chain")
			}
		})
	}
}

func TestRejectedResumeEvidenceIsSeparated(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.WriteString("rejected resume evidence\n")
	if err := archiveRejectedResume(dir, f); err != nil {
		t.Fatal(err)
	}
	events := []execution.Event{{Version: 1, Type: "start", Seq: 1, RunID: "fresh-run", CallerRunID: "caller", Session: "fresh-session", Timestamp: time.Now().UTC().Format(time.RFC3339), Backend: "codex", Model: "model"}, {Version: 1, Type: "final", Seq: 2, RunID: "fresh-run", CallerRunID: "caller", Session: "fresh-session", Timestamp: time.Now().UTC().Format(time.RFC3339), Backend: "codex", Model: "model", Status: "ok"}}
	for _, event := range events {
		if err := json.NewEncoder(f).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	id, err := developmentIdentity(dir)
	if err != nil || id.SessionID != "fresh-session" {
		t.Fatalf("identity=%+v err=%v", id, err)
	}
	archived, err := os.ReadFile(filepath.Join(dir, "resume-rejected.jsonl"))
	if err != nil || string(archived) != "rejected resume evidence\n" {
		t.Fatalf("archive=%q err=%v", archived, err)
	}
}

func TestRepairReusesOnlyExactCleanDeveloperCheckout(t *testing.T) {
	dir, sha := resumeGitFixture(t)
	if out, err := exec.Command("git", "-C", dir, "remote", "set-url", "origin", "https://github.com/o/r.git").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	prev := snapshot.RunMeta{DevelopmentSession: snapshot.DevelopmentSession{Workdir: dir}}
	pr := vcs.PR{HeadSHA: sha, HeadBranch: "main"}
	wt, ok := reusableRepairWorktree(context.Background(), prev, "o/r", pr)
	if !ok || !wt.Resumed || wt.Path != dir {
		t.Fatal("intact checkout not reused")
	}
	pr.HeadSHA = strings.Repeat("a", 40)
	if _, ok := reusableRepairWorktree(context.Background(), prev, "o/r", pr); ok {
		t.Fatal("stale head reused")
	}
	pr.HeadSHA = sha
	if err := os.WriteFile(filepath.Join(dir, "partial"), []byte("wip"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := reusableRepairWorktree(context.Background(), prev, "o/r", pr); ok {
		t.Fatal("dirty checkout reused for exact-head repair")
	}
	if _, ok := reusableDevelopmentWorktree(context.Background(), prev, "o/r"); !ok {
		t.Fatal("implementation lost partial checkout")
	}
}

func TestRepairPreservesPartialWorkAtSameSubmittedHead(t *testing.T) {
	dir, sha := resumeGitFixture(t)
	if out, err := exec.Command("git", "-C", dir, "remote", "set-url", "origin", "https://github.com/o/r.git").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	prev := snapshot.RunMeta{Operator: "repair-ci", DevelopmentSession: snapshot.DevelopmentSession{Workdir: dir, RepairHeadSHA: sha}}
	if err := os.WriteFile(filepath.Join(dir, "partial"), []byte("wip"), 0600); err != nil {
		t.Fatal(err)
	}
	pr := vcs.PR{HeadSHA: sha, HeadBranch: "main"}
	if _, ok := reusableRepairWorktree(context.Background(), prev, "o/r", pr); !ok {
		t.Fatal("same-head repair lost partial work")
	}
	pr.HeadSHA = strings.Repeat("a", 40)
	if _, ok := reusableRepairWorktree(context.Background(), prev, "o/r", pr); ok {
		t.Fatal("changed PR head reused stale repair")
	}
}

type retryEvidenceClient struct {
	repairClient
	requestedHead string
}

func (c *retryEvidenceClient) GetFailedJobEvidence(_ string, head string, _ int64) (string, error) {
	c.requestedHead = head
	return "bounded failing job excerpt", nil
}

func TestRetryDeltaUsesExistingBoundedCIEvidence(t *testing.T) {
	c := &retryEvidenceClient{repairClient: repairClient{pr: vcs.PR{Number: 11, Body: "Fixes #9", HeadSHA: "exact-head"}, status: vcs.CIStatusFailure}}
	j := &runJob{repo: "o/r", sub: &operator.Subject{Number: 9}, client: c}
	got := retryCIContext(j)
	for _, want := range []string{"failure", "bounded failing job excerpt", "UNTRUSTED CI LOG DATA"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if c.requestedHead != "exact-head" {
		t.Fatal("unbound CI evidence")
	}
}

func TestResumedWorktreeCleanupLeavesPrimaryCheckout(t *testing.T) {
	dir, _ := resumeGitFixture(t)
	wt := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", "--detach", wt, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	resumedWorktreeCleanup(dir, wt)()
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("completed retry checkout not cleaned: %v", err)
	}
	resumedWorktreeCleanup(dir, dir)()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("primary checkout changed: %v", err)
	}
}
