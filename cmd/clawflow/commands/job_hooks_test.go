package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestFencedClientEffectsAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &jobResult{}
	c := &fencedClient{Client: &commentsFake{labels: []string{"bug"}}, ctx: ctx, result: r}
	labels, err := c.GetIssueLabels("o/r", 1)
	if err != nil || !reflect.DeepEqual(labels, []string{"bug"}) {
		t.Fatal(labels, err)
	}
	calls := []func() error{
		func() error { return c.PostIssueComment("o/r", 1, "first") },
		func() error { return c.PostIssueCommentIdempotent("o/r", 1, "second", "run") },
		func() error { return c.AddLabel("o/r", 1, "done") },
		func() error { return c.RemoveLabel("o/r", 1, "pending") },
		func() error { return c.PostPRComment("o/r", 2, "review") },
		func() error { return c.CloseIssue("o/r", 1) },
	}
	for _, call := range calls {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	var kinds []string
	for _, e := range r.Effects {
		kinds = append(kinds, e.Kind)
	}
	if !reflect.DeepEqual(kinds, []string{"issue-comment", "issue-comment", "add-label", "remove-label", "pr-comment", "close-issue"}) {
		t.Fatal(kinds)
	}
	if r.Effects[1].RunID != "run" {
		t.Fatal(r.Effects)
	}
	if c.MergePR("o/r", 2) == nil {
		t.Fatal("merge allowed")
	}
	if _, err := c.MergePRAtHead("o/r", 2, "sha"); err == nil {
		t.Fatal("conditional merge allowed")
	}
	cancel()
	for _, call := range calls {
		if !errors.Is(call(), context.Canceled) {
			t.Fatal("write allowed after cancellation")
		}
	}
	if len(r.Effects) != len(calls) {
		t.Fatal(r.Effects)
	}
}

func TestWorkerJobLifecycle(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lease-lost"}[cancelled], func(t *testing.T) {
			withRunLog(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := runJobOperator
			t.Cleanup(func() { runJobOperator = original })
			client := &commentsFake{labels: []string{"bug"}}
			job := &runJob{repo: "o/r", client: client, repoCfg: config.Repo{AutoMerge: true},
				op:  &operator.Operator{Name: "inspect", Trigger: operator.Trigger{Target: "issue", LabelsRequired: []string{"bug"}}},
				sub: &operator.Subject{Number: 5, State: "open", Labels: []string{"bug"}}}
			acquired, released, after := 0, 0, 0
			job.hooks = &jobHooks{SkipIndex: true,
				AcquireLock: func(context.Context, *runJob) error { acquired++; return nil },
				ReleaseLock: func(*runJob) { released++ },
				AfterRun: func(r *jobResult) {
					after++
					if r != job.result || r.Output != "body" || r.Outcome != "done" {
						t.Fatalf("incomplete result: %+v", r)
					}
					if cancelled && r.Status != "lease-lost" {
						t.Fatal(r.Status)
					}
					if !cancelled && r.Status != "success" {
						t.Fatal(r.Status)
					}
				},
			}
			runJobOperator = func(ctx context.Context, op *operator.Operator, sub *operator.Subject, v operator.VCS, opts operator.RunOptions) (string, string, error) {
				if job.repoCfg.AutoMerge {
					t.Fatal("worker auto merge enabled")
				}
				// A competing local lock must remain available while a worker runs.
				if err := snapshot.AcquireLock(job.repo, sub.Number, op.Name); err != nil {
					t.Fatal(err)
				}
				snapshot.ReleaseLock(job.repo, sub.Number)
				if err := v.PostIssueComment("o/r", 5, "body"); err != nil {
					t.Fatal(err)
				}
				if cancelled {
					cancel()
					if !errors.Is(v.AddLabel("o/r", 5, "late"), context.Canceled) {
						t.Fatal("late effect accepted")
					}
					return "body", "done", ctx.Err()
				}
				if err := v.AddLabel("o/r", 5, "done"); err != nil {
					t.Fatal(err)
				}
				return "body", "done", nil
			}
			fired, limited, failed := runOneOperator(ctx, job, time.Second)
			if limited || failed || fired == cancelled {
				t.Fatalf("returns %v %v %v", fired, limited, failed)
			}
			if acquired != 1 || released != 1 || after != 1 {
				t.Fatalf("hooks %d %d %d", acquired, released, after)
			}
			if job.client != client || !job.repoCfg.AutoMerge {
				t.Fatal("job policy/client not restored")
			}
			want := 2
			if cancelled {
				want = 1
			}
			if len(job.result.Effects) != want {
				t.Fatal(job.result.Effects)
			}
			if _, err := os.Stat(filepath.Join(snapshot.DataDir(), "runs.json")); !os.IsNotExist(err) {
				t.Fatalf("index created: %v", err)
			}
		})
	}
}

func TestWorkerRepairHooksDoNotWriteLedger(t *testing.T) {
	withRunLog(t)
	j := &runJob{repo: "o/r", repair: &ciRepairContext{PR: vcs.PR{Number: 8, HeadSHA: "abc"}, Attempt: 2}, result: &jobResult{}, hooks: &jobHooks{}}
	now := time.Now()
	if err := reserveJobRepair(context.Background(), j, now); err != nil {
		t.Fatal(err)
	}
	if j.repair.Attempt != 2 {
		t.Fatal("payload reservation lost")
	}
	if err := j.releaseRepair("capacity", now); err != nil {
		t.Fatal(err)
	}
	reserved, released := 0, 0
	j.hooks.ReserveRepair = func(context.Context, *runJob, time.Time) error { reserved++; return nil }
	j.hooks.ReleaseRepair = func(_ *runJob, kind string, _ time.Time) error {
		released++
		if kind != "capacity" {
			t.Fatal(kind)
		}
		return nil
	}
	if err := reserveJobRepair(context.Background(), j, now); err != nil {
		t.Fatal(err)
	}
	if err := j.releaseRepair("capacity", now); err != nil {
		t.Fatal(err)
	}
	if reserved != 1 || released != 1 || j.result.InfraKind != "capacity" {
		t.Fatal(reserved, released, j.result)
	}
	// Hooks never create the local data directory.
	if _, err := os.Stat(snapshot.DataDir()); !os.IsNotExist(err) {
		t.Fatalf("local ledger touched: %v", err)
	}
}

func TestAcceptanceFindingsContractDigestRoundTrip(t *testing.T) {
	data, err := json.Marshal(acceptanceFindings{ContractDigest: "sha256:contract"})
	if err != nil {
		t.Fatal(err)
	}
	var got acceptanceFindings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ContractDigest != "sha256:contract" {
		t.Fatalf("digest lost: %s", data)
	}
}

func TestWorkerGitSetupCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := setupWorktree(ctx, config.Repo{}, "o/r", 1, time.Now(), ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := runGitWithRetryContext(ctx, func() *exec.Cmd { return exec.CommandContext(ctx, "sleep", "10") })
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("git not cancelled promptly: %v, %s", err, time.Since(start))
	}
}

func TestWorkerJobTerminalResults(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		status     string
		rate, cost bool
	}{
		{"failure", errors.New("executor failed"), "failed", false, false},
		{"rate", operator.ErrRateLimit, "rate-limited", true, false},
		{"cost", operator.ErrCostLimit, "cost-limit", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withRunLog(t)
			original := runJobOperator
			t.Cleanup(func() { runJobOperator = original })
			runJobOperator = func(context.Context, *operator.Operator, *operator.Subject, operator.VCS, operator.RunOptions) (string, string, error) {
				return "", "", tc.err
			}
			after := 0
			j := &runJob{repo: "o/r", client: &commentsFake{}, op: &operator.Operator{Name: "inspect", Trigger: operator.Trigger{Target: "issue"}}, sub: &operator.Subject{Number: 1, State: "open"}, hooks: &jobHooks{SkipIndex: true, AfterRun: func(r *jobResult) {
				after++
				if r.Status != tc.status || r.Error == "" || r.RateLimited != tc.rate || r.CostLimited != tc.cost {
					t.Fatalf("%+v", r)
				}
			}}}
			_, limited, failed := runOneOperator(context.Background(), j, time.Second)
			if !failed || limited != (tc.rate || tc.cost) || after != 1 {
				t.Fatal(limited, failed, after)
			}
			if len(j.result.Effects) != 0 {
				t.Fatal("worker circuit breaker wrote effects")
			}
		})
	}
}

func TestWorkerJobEarlyFailureResult(t *testing.T) {
	withRunLog(t)
	called := 0
	j := &runJob{repo: "o/r", client: &commentsFake{}, op: &operator.Operator{Name: "inspect", Trigger: operator.Trigger{Target: "issue"}}, sub: &operator.Subject{Number: 1, State: "open", Body: "ClawFlow-Base-Branch: ../evil"}, hooks: &jobHooks{SkipIndex: true, AfterRun: func(r *jobResult) {
		called++
		if r.Status != "failed" || r.Error == "" {
			t.Fatalf("%+v", r)
		}
	}}}
	_, _, failed := runOneOperator(context.Background(), j, time.Second)
	if !failed || called != 1 {
		t.Fatal(failed, called)
	}
}

type workerPRClient struct{ commentsFake }

func (*workerPRClient) GetPR(string, int) (vcs.PR, error) {
	return vcs.PR{Number: 12, URL: "https://github.com/o/r/pull/12", HeadBranch: "fix/issue-1", HeadSHA: "published"}, nil
}

func TestWorkerJobUsageAndPRResult(t *testing.T) {
	withRunLog(t)
	original := runJobOperator
	t.Cleanup(func() { runJobOperator = original })
	runJobOperator = func(_ context.Context, _ *operator.Operator, _ *operator.Subject, _ operator.VCS, opts operator.RunOptions) (string, string, error) {
		_, err := io.WriteString(opts.EventWriter, `{"type":"result","total_cost_usd":0.5,"usage":{"input_tokens":900,"output_tokens":90}}`+"\n")
		if err != nil {
			t.Fatal(err)
		}
		return "https://github.com/o/r/pull/12", "done", nil
	}
	j := &runJob{repo: "o/r", client: &workerPRClient{}, op: &operator.Operator{Name: "inspect", Trigger: operator.Trigger{Target: "issue"}}, sub: &operator.Subject{Number: 1, State: "open"}, hooks: &jobHooks{SkipIndex: true}}
	runOneOperator(context.Background(), j, time.Second)
	r := j.result
	if r.Usage == nil || r.Cost != 0.5 || r.PRNumber != 12 || r.PRURL != "https://github.com/o/r/pull/12" || r.Branch != "fix/issue-1" || r.HeadSHA != "published" {
		t.Fatalf("%+v", r)
	}
}

func TestWorkerWorktreeFetchCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec sleep 10\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := setupWorktree(ctx, config.Repo{LocalPath: t.TempDir()}, "o/r", 1, time.Now(), "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("setup cancellation: %v (%s)", err, time.Since(start))
	}
}
