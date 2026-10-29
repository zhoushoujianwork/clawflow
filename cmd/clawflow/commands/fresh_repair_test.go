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

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestFreshRepairInputAndProvenance(t *testing.T) {
	for _, kind := range []string{"ci", "acceptance", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "dev")
			dir, _ := resumeGitFixture(t)
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			write := func(s string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "behavior.txt"), []byte(s+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				git("add", "behavior.txt")
				git("commit", "-m", s)
			}
			write("original behavior")
			git("checkout", "-b", "fix/issue-42")
			write("required new behavior")
			head := git("rev-parse", "HEAD")
			git("checkout", "main")
			write("concurrent base behavior")
			base := git("rev-parse", "HEAD")
			git("checkout", "fix/issue-42")
			repair := &ciRepairContext{PR: vcs.PR{Number: 11, HeadRepo: "o/r", HeadBranch: "fix/issue-42", BaseBranch: "main", HeadSHA: head, BaseSHA: base}, Evidence: "test failed: missing boundary check"}
			failure := repair.Evidence
			if kind == "acceptance" {
				repair.Acceptance = &acceptanceFindings{Verdict: "failed", Summary: "review: missing boundary check"}
				failure = repair.Acceptance.Summary
			}
			if kind == "conflict" {
				repair.MergeConflict = true
				failure = "behavior.txt"
			}
			j := &runJob{repo: "o/r", repoCfg: config.Repo{BaseBranch: "main"}, op: &operator.Operator{Name: "repair-ci"}, sub: &operator.Subject{Number: 42, Title: "Required behavior", Body: "Acceptance: preserve the required behavior"}, repair: repair}
			input, err := buildRepairInput(context.Background(), dir, j)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"PR diff against base", "+required new behavior", "-original behavior", failure} {
				if !strings.Contains(input, want) {
					t.Fatalf("missing %q: %s", want, input)
				}
			}
			prev := snapshot.RunMeta{Status: "success", DevelopmentSession: snapshot.DevelopmentSession{SessionID: "original-session", Role: config.RoleOperator, Profile: "dev", Workdir: dir, BaseSHA: base}}
			start := time.Now().UTC()
			current := snapshot.RunMeta{Repo: "o/r", IssueNumber: 42, Operator: "repair-ci", StartedAt: start}
			runDir := snapshot.RunDir("o/r", 42, start)
			persist := func() {
				t.Helper()
				if err := snapshot.WriteRunMeta(runDir, current); err != nil {
					t.Fatal(err)
				}
			}
			opts := developmentResumeOptions(context.Background(), j, prev, worktreeResult{Path: dir, Resumed: true}, []vcs.IssueComment{{Body: "old implementation history"}}, input, &current, persist)
			if opts.SessionKey != "" || opts.ResumePrompt != "" {
				t.Fatal("repair resumed development")
			}
			opts.ResumeContext, opts.Role, opts.Workdir = input, config.RoleOperator, dir
			opts.ExecuteFunc = func(_ context.Context, prompt string, request execution.Options) (execution.Result, error) {
				if request.Resume || request.SessionKey != "" || strings.Contains(prompt, "old implementation history") || !strings.Contains(prompt, j.sub.Body) || !strings.Contains(prompt, input) {
					t.Fatalf("bad fresh repair request: %+v %s", request, prompt)
				}
				return execution.Result{NativeSessionID: "repair-session", RunID: "repair-run", Profile: "dev", Backend: "codex"}, nil
			}
			if _, _, err := operator.Run(context.Background(), j.op, j.sub, nil, opts); err != nil {
				t.Fatal(err)
			}
			if current.DevelopmentSession.SessionID != "repair-session" || current.DevelopmentSession.ResumeCount != 0 || current.DevelopmentSession.RepairHeadSHA != head {
				t.Fatalf("repair identity lost: %+v", current)
			}
			write("repaired behavior")
			current.HeadSHA = git("rev-parse", "HEAD")
			current.Status = "success"
			end := time.Now().UTC()
			current.EndedAt = &end
			persist()
			events := []execution.Event{{Version: 1, Type: "start", Seq: 1, Session: "repair-session", RunID: "repair-run", CallerRunID: "repair-caller", Timestamp: start.Format(time.RFC3339Nano)}, {Version: 1, Type: "final", Seq: 2, Session: "repair-session", RunID: "repair-run", CallerRunID: "repair-caller", Status: "ok", Timestamp: end.Format(time.RFC3339Nano)}}
			f, err := os.Create(filepath.Join(runDir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if err := json.NewEncoder(f).Encode(event); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			identity, err := acceptanceDevelopment("o/r", 42, current.HeadSHA)
			if err != nil || identity.SessionID != "repair-session" || identity.RunID != "repair-run" {
				t.Fatalf("fresh repair provenance rejected: %+v %v", identity, err)
			}
		})
	}
}

func TestRepairCriteriaRequireMatchingRequirements(t *testing.T) {
	f, _ := acceptanceFixture(t)
	rec := produceAcceptance(t, f)
	rec.AttemptID += "-criteria"
	rec.Plan.AttemptID = rec.AttemptID
	rec.StartedAt = rec.StartedAt.Add(time.Nanosecond)
	rec.CompletedAt = rec.CompletedAt.Add(time.Nanosecond)
	rec.Plan.Criteria = []acceptance.Criterion{{ID: "R1", Requirement: f.issue.Body, Proof: "execution"}}
	store, err := acceptance.OpenStore(acceptanceStorePath("o/r", 11))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecord(rec); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	j := &runJob{repo: "o/r", sub: &operator.Subject{Title: f.issue.Title, Body: f.issue.Body}, repair: &ciRepairContext{PR: f.pr}}
	if got := repairAcceptanceCriteria(j); !strings.Contains(got, `"id":"R1"`) || !strings.Contains(got, f.issue.Body) {
		t.Fatalf("criteria missing: %s", got)
	}
	j.sub.Body = "changed requirements"
	if got := repairAcceptanceCriteria(j); got != "" {
		t.Fatalf("stale criteria supplied: %s", got)
	}
}
