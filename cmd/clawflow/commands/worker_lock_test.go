package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestWorkerTaskHostIssueLock(t *testing.T) {
	for _, mode := range []string{"success", "operator-failure", "lock-held", "early-return"} {
		t.Run(mode, func(t *testing.T) {
			withRunLog(t)
			const repo, issue = "o/r", 562
			job := &runJob{
				repo: repo, client: &commentsFake{labels: []string{"bug"}},
				op:  &operator.Operator{Name: "inspect", Trigger: operator.Trigger{Target: "issue", LabelsRequired: []string{"bug"}}},
				sub: &operator.Subject{Number: issue, State: "open", Labels: []string{"bug"}},
			}
			if mode == "early-return" {
				job.sub.Body = "ClawFlow-Base-Branch: ../invalid"
			}
			if mode == "lock-held" {
				if err := snapshot.AcquireLock(repo, issue, "existing-run"); err != nil {
					t.Fatal(err)
				}
				defer snapshot.ReleaseLock(repo, issue)
			}

			// A fresh, edit-free worktree must survive reconcile during execution.
			var wt string
			var pruneCfg *config.Config
			if mode == "success" {
				local := newIssue329Repo(t)
				root := filepath.Join(snapshot.StateDir(), "worktrees", "o__r")
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				wt = filepath.Join(root, "issue-562-2026-10-29T00-00-00Z")
				if out, err := exec.Command("git", "-C", local, "worktree", "add", "--detach", wt, "origin/main").CombinedOutput(); err != nil {
					t.Fatalf("worktree add: %v\n%s", err, out)
				}
				pruneCfg = &config.Config{Repos: map[string]config.Repo{repo: {Enabled: true, LocalPath: local, BaseBranch: "main"}}}
			}

			oldRun, oldOperator := runOneOperatorFn, runJobOperator
			t.Cleanup(func() { runOneOperatorFn, runJobOperator = oldRun, oldOperator })
			insideRun, executed := false, false
			runOneOperatorFn = func(ctx context.Context, j *runJob, timeout time.Duration) (bool, bool, bool) {
				insideRun = true
				defer func() { insideRun = false }()
				return runOneOperator(ctx, j, timeout)
			}
			runJobOperator = func(_ context.Context, _ *operator.Operator, _ *operator.Subject, _ operator.VCS, _ operator.RunOptions) (string, string, error) {
				executed = true
				if !insideRun || !snapshot.IsLocked(repo, issue) {
					t.Fatal("worker operator executed without the host issue lock")
				}
				info, err := snapshot.ReadLock(snapshot.LockPath(repo, issue))
				if err != nil || info.Operator != job.op.Name {
					t.Fatalf("lock metadata: %+v, %v", info, err)
				}
				if pruneCfg != nil {
					if n := pruneEmptyIssueWorktrees(pruneCfg); n != 0 {
						t.Fatalf("pruned %d active worktrees", n)
					}
					if _, err := os.Stat(filepath.Join(wt, ".git")); err != nil {
						t.Fatalf("active worktree removed: %v", err)
					}
				}
				if mode == "operator-failure" {
					return "", "", errors.New("operator failed")
				}
				return "done", "done", nil
			}

			type report struct {
				result jobResult
				action string
			}
			reports := make(chan report, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Result jobResult `json:"result"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				reports <- report{input.Result, r.URL.Path}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			api := &workerAPI{base: server.URL, client: server.Client()}
			task := &coordinator.Task{ID: "host-lock", Repo: repo, Issue: issue, Operator: "inspect", Kind: "implement"}
			err := runWorkerTask(context.Background(), api, workerOptions{RunUntagged: true, Roles: []string{"operator"}}, task, time.Hour,
				func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) { return job, time.Second, nil })
			if err != nil {
				t.Fatal(err)
			}
			received := <-reports
			result, action := received.result, received.action
			if mode == "lock-held" {
				if executed || result.Status != "failed" || result.InfraKind != "launch" || result.Error == "" || action != "/worker/v1/tasks/host-lock/fail" {
					t.Fatalf("contention was not a launch failure: executed=%v action=%s result=%+v", executed, action, result)
				}
				if !snapshot.IsLocked(repo, issue) {
					t.Fatal("worker released the competing run's lock")
				}
				return
			}
			if snapshot.IsLocked(repo, issue) {
				t.Fatal("worker left the host issue lock held")
			}
			if executed != (mode != "early-return") {
				t.Fatalf("unexpected execution: %v", executed)
			}
			want := "failed"
			if mode == "success" {
				want = "success"
			}
			if result.Status != want {
				t.Fatalf("result: %+v", result)
			}
			if pruneCfg != nil {
				if n := pruneEmptyIssueWorktrees(pruneCfg); n != 1 {
					t.Fatalf("expected empty worktree to be pruned after unlock, got %d", n)
				}
			}
		})
	}
}
