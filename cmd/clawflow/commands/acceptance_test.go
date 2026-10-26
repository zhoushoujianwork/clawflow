package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
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

type acceptanceFake struct {
	*sweepFake
	pr          vcs.PR
	issue       *vcs.Issue
	expected    []string
	conditional func() error
}

func (f *acceptanceFake) GetPR(_ string, _ int) (vcs.PR, error)        { return f.pr, nil }
func (f *acceptanceFake) GetIssue(_ string, _ int) (*vcs.Issue, error) { return f.issue, nil }
func (f *acceptanceFake) MergePRAtHead(_ string, _ int, expected string) (string, error) {
	f.expected = append(f.expected, expected)
	if f.conditional != nil {
		if err := f.conditional(); err != nil {
			return "", err
		}
	}
	return strings.Repeat("c", 40), nil
}

func acceptanceFixture(t *testing.T) (*acceptanceFake, config.Repo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", "/usr/bin/true")
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "acceptance-profile")
	t.Setenv("CLAWFLOW_OFFLOAD_CODEX_SANDBOX", "")
	rc := config.Repo{Platform: "github", BaseBranch: "main", LocalPath: t.TempDir(), AcceptanceRequired: true, CIRequired: true, AutoMerge: true}
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": rc}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	f := &acceptanceFake{sweepFake: &sweepFake{ciStatus: vcs.CIStatusSuccess}, pr: vcs.PR{Number: 11, Title: "Implementation", Body: "Fixes #42", State: "open", HeadRepo: "o/r", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), BaseBranch: "main", HeadBranch: "fix/issue-42"}, issue: &vcs.Issue{Number: 42, Title: "Required behavior", Body: "Meet the original goal", State: "open"}}
	f.openPRs = []vcs.PR{f.pr}
	oldGit, oldNative, oldFactory := acceptanceGit, acceptanceNativeRun, acceptanceClientFactory
	t.Cleanup(func() { acceptanceGit, acceptanceNativeRun, acceptanceClientFactory = oldGit, oldNative, oldFactory })
	acceptanceGit = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "config":
			return "https://github.com/o/r.git", nil
		case "rev-parse":
			return f.pr.HeadSHA, nil
		case "status":
			return "", nil
		default:
			return "", nil
		}
	}
	acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return f, nil }
	calls := 0
	acceptanceNativeRun = func(_ context.Context, c execution.Config, r execution.Request, _ []string, _ io.Writer, _ func(execution.Observation)) (execution.Result, error) {
		calls++
		if c.Profile != "acceptance-profile" || r.Resume || r.SessionKey != "" || r.PermissionMode != "default" {
			t.Fatalf("unpinned or writable native request: %+v %+v", c, r)
		}
		result := execution.Result{Status: "ok", ExecutionPolicy: "default", Profile: c.Profile, Backend: "fixture", Model: "fixture-model", NativeSessionID: execution.NewSessionKey(), RunID: execution.NewSessionKey(), Text: "Independent plan"}
		if !r.ToolsDisabled {
			result.Text = `{"status":"passed","summary":"Independent checks complete","evidence":["Source fulfills the plan"]}`
		}
		return result, nil
	}
	writeAcceptanceDevelopment(t, "success", time.Now().Add(-time.Minute), nil)
	return f, rc
}

func writeAcceptanceDevelopment(t *testing.T, status string, started time.Time, mutate func([]execution.Event) []execution.Event) string {
	t.Helper()
	dir := snapshot.RunDir("o/r", 42, started)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ended := started.Add(time.Second)
	meta := snapshot.RunMeta{Operator: "implement", Repo: "o/r", IssueNumber: 42, IssueTitle: "Required behavior", IssueBody: "Meet the original goal", StartedAt: started, EndedAt: &ended, Status: status}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	events := []execution.Event{{Version: 1, Type: "start", Seq: 1, Session: "dev-session", RunID: "dev-run", CallerRunID: "dev-caller", Timestamp: started.Format(time.RFC3339Nano)}, {Version: 1, Type: "final", Seq: 2, Session: "dev-session", RunID: "dev-run", CallerRunID: "dev-caller", Status: "ok", Timestamp: ended.Format(time.RFC3339Nano)}}
	if mutate != nil {
		events = mutate(events)
	}
	var output bytes.Buffer
	for _, e := range events {
		_ = json.NewEncoder(&output).Encode(e)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func produceAcceptance(t *testing.T, f *acceptanceFake) acceptance.Record {
	t.Helper()
	r, err := runAcceptance(context.Background(), f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAcceptanceRunSeparatePlanningAndPinnedReview(t *testing.T) {
	f, _ := acceptanceFixture(t)
	baseRun := acceptanceNativeRun
	var requests []execution.Request
	var gitArgs [][]string
	baseGit := acceptanceGit
	acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		gitArgs = append(gitArgs, append([]string{dir}, args...))
		return baseGit(ctx, dir, args...)
	}
	acceptanceNativeRun = func(ctx context.Context, c execution.Config, req execution.Request, env []string, out io.Writer, observe func(execution.Observation)) (execution.Result, error) {
		requests = append(requests, req)
		if req.ToolsDisabled {
			entries, err := os.ReadDir(req.Workspace)
			if err != nil || len(entries) != 0 {
				t.Fatal("planner scratch is not empty")
			}
			for _, forbidden := range []string{f.pr.HeadSHA, "dev-session", "dev-run", "Implementation", "Source fulfills"} {
				if strings.Contains(req.Prompt, forbidden) {
					t.Fatalf("planner received development input %q", forbidden)
				}
			}
		} else {
			entries, _ := os.ReadDir(acceptanceStorePath("o/r", 11))
			if len(entries) == 0 {
				t.Fatal("review started before plan persistence")
			}
			if !strings.Contains(req.Prompt, f.pr.HeadSHA) || !strings.Contains(req.Prompt, "Independent plan") {
				t.Fatal("review lacks pinned source/plan")
			}
		}
		return baseRun(ctx, c, req, env, out, observe)
	}
	r := produceAcceptance(t, f)
	if len(requests) != 2 || requests[0].Workspace == requests[1].Workspace || requests[0].RequestID == requests[1].RequestID || !requests[0].ToolsDisabled || requests[1].ToolsDisabled || r.Contract.IntegrationSHA != "" {
		t.Fatalf("bad phases: %+v", requests)
	}
	for _, req := range requests {
		if _, err := os.Stat(req.Workspace); !os.IsNotExist(err) {
			t.Fatal("temporary workspace was not cleaned")
		}
	}
	detached := false
	for _, args := range gitArgs {
		if len(args) > 3 && args[1] == "checkout" && args[2] == "--detach" && args[3] == f.pr.HeadSHA {
			detached = true
		}
	}
	if !detached {
		t.Fatal("review did not checkout exact detached head")
	}
	if _, _, err := currentAcceptance(context.Background(), f, "o/r", 11); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptanceGateRejectsMissingAndStaleEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "head", "base", "goal", "policy", "profile", "provenance", "failed", "selfreview"} {
		t.Run(mode, func(t *testing.T) {
			f, rc := acceptanceFixture(t)
			if mode != "missing" {
				produceAcceptance(t, f)
			}
			switch mode {
			case "head":
				f.pr.HeadSHA = strings.Repeat("d", 40)
			case "base":
				f.pr.BaseSHA = strings.Repeat("d", 40)
			case "goal":
				f.issue.Body += " changed"
			case "policy":
				cfg, _ := config.Load()
				r := cfg.Repos["o/r"]
				r.CIRequired = false
				cfg.Repos["o/r"] = r
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
			case "profile":
				t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "changed-profile")
			case "provenance":
				writeAcceptanceDevelopment(t, "failed", time.Now(), nil)
			case "failed", "selfreview":
				baseRun := acceptanceNativeRun
				acceptanceNativeRun = func(ctx context.Context, c execution.Config, req execution.Request, env []string, w io.Writer, observe func(execution.Observation)) (execution.Result, error) {
					r, err := baseRun(ctx, c, req, env, w, observe)
					if !req.ToolsDisabled {
						if mode == "failed" {
							r.Text = `{"status":"failed","summary":"Tests fail","evidence":["failure"]}`
						} else {
							r.NativeSessionID = "dev-session"
						}
					}
					return r, err
				}
				_, _ = runAcceptance(context.Background(), f, "o/r", 11)
			}
			if attemptAutoMerge(autoMergeAttempt{ctx: context.Background(), client: f, repo: "o/r", repoCfg: rc, prNum: 11, quiet: true}) || len(f.expected) != 0 || len(f.merged) != 0 {
				t.Fatal("unaccepted evidence merged")
			}
		})
	}
}

func TestAcceptanceGateInlineAndSweepUseConditionalMerge(t *testing.T) {
	for _, mode := range []string{"inline", "sweep"} {
		t.Run(mode, func(t *testing.T) {
			withRunLog(t)
			f, rc := acceptanceFixture(t)
			produceAcceptance(t, f)
			if mode == "sweep" {
				sweepAutoMergePRs(context.Background(), f, "o/r", rc)
			} else {
				runPostAutomation(context.Background(), &runJob{repo: "o/r", repoCfg: rc, client: f, op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 42}}, "agent-implemented", "https://github.com/o/r/pull/11", "test")
			}
			if !reflect.DeepEqual(f.expected, []string{f.pr.HeadSHA}) || len(f.merged) != 0 || len(f.deletedBranches) != 1 {
				t.Fatalf("conditional=%v legacy=%v deleted=%v", f.expected, f.merged, f.deletedBranches)
			}
		})
	}
}

func TestAcceptanceGateRetryCIAndCapability(t *testing.T) {
	for _, mode := range []string{"retry-head", "ci", "unsupported", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, rc := acceptanceFixture(t)
			produceAcceptance(t, f)
			var client vcs.Client = f
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "retry-head":
				f.conditional = func() error { f.pr.HeadSHA = strings.Repeat("d", 40); return errors.New("Base branch was modified") }
			case "ci":
				f.ciStatus = vcs.CIStatusFailure
			case "unsupported":
				client = struct{ vcs.Client }{f}
			case "cancelled":
				cancel()
			}
			if attemptAutoMerge(autoMergeAttempt{ctx: ctx, client: client, repo: "o/r", repoCfg: rc, prNum: 11, quiet: true}) {
				t.Fatal("merge succeeded despite blocker")
			}
			want := 0
			if mode == "retry-head" {
				want = 1
			}
			if len(f.expected) != want || len(f.merged) != 0 || len(f.deletedBranches) != 0 {
				t.Fatalf("conditional=%v legacy=%v cleanup=%v", f.expected, f.merged, f.deletedBranches)
			}
		})
	}
}

func TestAcceptanceDevelopmentFailsClosed(t *testing.T) {
	for _, mode := range []string{"absent", "failed", "running", "changed-session", "duplicate-final", "no-final", "missing-exit"} {
		t.Run(mode, func(t *testing.T) {
			_, _ = acceptanceFixture(t)
			parent := filepath.Dir(snapshot.RunDir("o/r", 42, time.Now()))
			if mode == "absent" {
				_ = os.RemoveAll(parent)
			} else {
				status := "success"
				if mode == "failed" || mode == "running" {
					status = mode
				}
				dir := writeAcceptanceDevelopment(t, status, time.Now(), func(es []execution.Event) []execution.Event {
					switch mode {
					case "changed-session":
						es[1].Session = "other"
					case "duplicate-final":
						es = append(es, es[1])
					case "no-final":
						es = es[:1]
					}
					return es
				})
				if mode == "missing-exit" {
					p := filepath.Join(dir, "events.jsonl")
					b, _ := os.ReadFile(p)
					b = bytes.ReplaceAll(b, []byte(`,"exit_code":0`), nil)
					if err := os.WriteFile(p, b, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := acceptanceDevelopment("o/r", 42, "Required behavior\n\nMeet the original goal"); err == nil {
				t.Fatal("invalid provenance accepted")
			}
		})
	}
}

func TestAcceptanceStatusReadOnly(t *testing.T) {
	f, _ := acceptanceFixture(t)
	produceAcceptance(t, f)
	before := acceptanceTree(t, snapshot.DataDir())
	acceptanceNativeRun = func(context.Context, execution.Config, execution.Request, []string, io.Writer, func(execution.Observation)) (execution.Result, error) {
		t.Fatal("status launched agent")
		return execution.Result{}, nil
	}
	baseGit := acceptanceGit
	acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] != "config" {
			t.Fatalf("status performed Git mutation/fetch: %v", args)
		}
		return baseGit(ctx, dir, args...)
	}
	var output bytes.Buffer
	cmd := newAcceptanceCmd()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"status", "--repo", "o/r", "--pr", "11", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"status":"passed"`) || !reflect.DeepEqual(before, acceptanceTree(t, snapshot.DataDir())) {
		t.Fatal("status changed evidence or omitted result")
	}
}

func acceptanceTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			tree[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestRepoAcceptanceRequiredCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"on", "off"} {
		cmd := newRepoSetCmd()
		cmd.SetArgs([]string{"o/r", "--acceptance-required", v})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		r := cfg.Repos["o/r"]
		if r.AcceptanceRequired != (v == "on") || r.AutoMerge || r.Enabled {
			t.Fatalf("unexpected policy: %+v", r)
		}
	}
	for _, v := range []string{"true", "", "yes"} {
		cmd := newRepoSetCmd()
		cmd.SetArgs([]string{"o/r", "--acceptance-required=" + v})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted invalid value %q", v)
		}
	}
}

func TestAcceptanceRunBlocksChangedInputsAndDirtySource(t *testing.T) {
	for _, mode := range []string{"fetched-head", "dirty", "changed-base", "changed-goal", "no-provenance"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := acceptanceFixture(t)
			baseGit := acceptanceGit
			acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
				if mode == "fetched-head" && args[0] == "rev-parse" && args[1] == "FETCH_HEAD" {
					return strings.Repeat("e", 40), nil
				}
				if mode == "dirty" && args[0] == "status" {
					return " M code.go", nil
				}
				return baseGit(ctx, dir, args...)
			}
			baseRun := acceptanceNativeRun
			calls := 0
			acceptanceNativeRun = func(ctx context.Context, c execution.Config, req execution.Request, env []string, w io.Writer, observe func(execution.Observation)) (execution.Result, error) {
				calls++
				if !req.ToolsDisabled {
					if mode == "changed-base" {
						f.pr.BaseSHA = strings.Repeat("e", 40)
					}
					if mode == "changed-goal" {
						f.issue.Body += " new requirements"
					}
				}
				return baseRun(ctx, c, req, env, w, observe)
			}
			if mode == "no-provenance" {
				_ = os.RemoveAll(filepath.Dir(snapshot.RunDir("o/r", 42, time.Now())))
			}
			r, err := runAcceptance(context.Background(), f, "o/r", 11)
			if err == nil || r.Verdict == acceptance.Passed {
				t.Fatalf("accepted %s: %+v", mode, r)
			}
			if (mode == "fetched-head" || mode == "no-provenance") && calls != 0 {
				t.Fatal("agent launched without immutable source/provenance")
			}
			if mode == "dirty" && calls != 1 {
				t.Fatal("review launched against dirty source")
			}
		})
	}
}

func TestAcceptanceExecutionRolePrecedence(t *testing.T) {
	_, _ = acceptanceFixture(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.Execution = config.ExecutionSettings{Command: "/usr/bin/true", Profile: "base", RoleProfiles: map[string]string{"acceptance": "review-role"}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("CLAWFLOW_OFFLOAD_PROFILE"); err != nil {
		t.Fatal(err)
	}
	_, ec, err := acceptanceConfiguration("o/r")
	if err != nil || ec.Profile != "review-role" || ec.RoleProfiles != nil {
		t.Fatalf("role resolution %+v %v", ec, err)
	}
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "environment-profile")
	_, ec, err = acceptanceConfiguration("o/r")
	if err != nil || ec.Profile != "environment-profile" {
		t.Fatalf("environment resolution %+v %v", ec, err)
	}
}

func TestAcceptanceRunCommandTimeoutAndArguments(t *testing.T) {
	_, _ = acceptanceFixture(t)
	baseRun := acceptanceNativeRun
	calls := 0
	acceptanceNativeRun = func(ctx context.Context, c execution.Config, req execution.Request, env []string, w io.Writer, observe func(execution.Observation)) (execution.Result, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok || req.TimeoutMS <= 0 || req.TimeoutMS > int64(time.Minute/time.Millisecond) {
			t.Fatal("CLI did not bound total phase execution")
		}
		return baseRun(ctx, c, req, env, w, observe)
	}
	var output bytes.Buffer
	cmd := newAcceptanceCmd()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"run", "--repo", "o/r", "--pr", "11", "--timeout", "1m"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(output.String(), "passed") {
		t.Fatalf("calls=%d output=%s", calls, output.String())
	}
	for _, args := range [][]string{{"run", "--repo", "o/r", "--pr", "0"}, {"run", "--repo", "o/r", "--pr", "11", "--timeout", "0s"}, {"run", "--repo", "o/r", "--pr", "11", "--timeout", "25h"}, {"run", "--repo", "o/r", "--pr", "11", "--development-session", "arbitrary"}} {
		cmd := newAcceptanceCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	if calls != 2 {
		t.Fatal("invalid arguments launched a reviewer")
	}
}

func TestAcceptanceManualMergeEnrollment(t *testing.T) {
	for _, mode := range []string{"missing", "valid", "ci", "mergeability", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := acceptanceFixture(t)
			cfg, _ := config.Load()
			r := cfg.Repos["o/r"]
			r.AutoMerge = false
			cfg.Repos["o/r"] = r
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			if mode != "missing" && mode != "unreadable" {
				produceAcceptance(t, f)
			}
			if mode == "ci" {
				f.ciStatus = vcs.CIStatusFailure
			}
			if mode == "mergeability" {
				f.mergeability = map[int]vcs.MergeStatus{11: vcs.MergeStatusConflict}
			}
			if mode == "unreadable" {
				if err := os.WriteFile(config.ConfigPath(), []byte("[malformed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			old := acceptanceMergeClient
			t.Cleanup(func() { acceptanceMergeClient = old })
			acceptanceMergeClient = func(string) (vcs.Client, string, config.Repo, error) { return f, "o/r", r, nil }
			cmd := newPRMergeCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--repo", "o/r", "--pr", "11", "--no-delete-branch"})
			err := cmd.Execute()
			if (err == nil) != (mode == "valid") || len(f.merged) != 0 {
				t.Fatalf("mode=%s err=%v legacy=%v", mode, err, f.merged)
			}
			want := 0
			if mode == "valid" {
				want = 1
			}
			if len(f.expected) != want {
				t.Fatalf("conditional=%v", f.expected)
			}
		})
	}
}

func TestAcceptanceDevelopmentRequirementsSnapshot(t *testing.T) {
	for _, mode := range []string{"changed-goal", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := acceptanceFixture(t)
			if mode == "changed-goal" {
				f.issue.Body += " changed after development"
			} else {
				parent := filepath.Dir(snapshot.RunDir("o/r", 42, time.Now()))
				entries, _ := os.ReadDir(parent)
				path := filepath.Join(parent, entries[0].Name(), "meta.json")
				b, _ := os.ReadFile(path)
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(b, &fields)
				delete(fields, "issue_body")
				b, _ = json.Marshal(fields)
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runAcceptance(context.Background(), f, "o/r", 11); err == nil || !strings.Contains(err.Error(), "new managed development run") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestAcceptanceLocalReviewMergeExclusion(t *testing.T) {
	f, rc := acceptanceFixture(t)
	produceAcceptance(t, f)
	unlock, err := acceptanceLock("o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runAcceptance(context.Background(), f, "o/r", 11); err == nil {
		t.Fatal("concurrent review admitted")
	}
	if err := explicitAcceptanceMerge(context.Background(), f, "o/r", 11); err == nil {
		t.Fatal("concurrent manual merge admitted")
	}
	if attemptAutoMerge(autoMergeAttempt{ctx: context.Background(), client: f, repo: "o/r", repoCfg: rc, prNum: 11}) {
		t.Fatal("concurrent automatic merge admitted")
	}
	if len(f.expected) != 0 {
		t.Fatal("locked merge reached provider")
	}
	unlock()
	if err := explicitAcceptanceMerge(context.Background(), f, "o/r", 11); err != nil {
		t.Fatal(err)
	}
}
