package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/candidate"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// isolatedFake adds the optional changed-file listing to the acceptance fake.
type isolatedFake struct {
	*acceptanceFake
	files []string
	err   error
	calls int
}

func (f *isolatedFake) ListPRFiles(string, int) ([]string, error) {
	f.calls++
	return f.files, f.err
}

func isolatedFixture(t *testing.T, files ...string) (*isolatedFake, config.Repo) {
	t.Helper()
	// Fixtures share one head SHA; the production cache is keyed by it.
	acceptanceFileCache.Lock()
	acceptanceFileCache.files = map[string][]string{}
	acceptanceFileCache.Unlock()
	base, rc := acceptanceFixture(t)
	rc.AcceptanceIsolatedRun = true
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": rc}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	f := &isolatedFake{acceptanceFake: base, files: files}
	acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return f, nil }
	return f, rc
}

// stubIsolated replaces the candidate build with evidence of the given outcome.
func stubIsolated(t *testing.T, pass bool, calls *int, err error) {
	t.Helper()
	old := acceptanceRunIsolated
	t.Cleanup(func() { acceptanceRunIsolated = old })
	acceptanceRunIsolated = func(_ context.Context, in candidate.IsolatedInput) (candidate.IsolatedResult, error) {
		*calls++
		if err != nil {
			return candidate.IsolatedResult{}, err
		}
		plan, _ := candidate.NewPlan(in.Commit, "/tmp/c", 0, candidate.Pin{})
		ev := candidate.NewEvidence(plan, "")
		ev.TaskRequired = true
		task := &candidate.TaskRun{Commit: in.Commit, RuntimeID: plan.ID, TaskID: "task-1", State: "done", Settled: true,
			ResidualProcesses: []string{}, ResidualLocks: []string{}}
		for _, name := range []string{"claimed", "executed", "written-back", "finished"} {
			task.Steps = append(task.Steps, candidate.Step{Name: name, Passed: true, Evidence: name + " observed"})
		}
		if !pass {
			task.ResidualProcesses = []string{"sleep (pid 9)"}
		}
		ev.Task = task
		for _, l := range []string{"environment-ready", "candidate-loaded", "lifecycle"} {
			ev.Layers[l] = candidate.Passed
		}
		ev.Finish()
		return candidate.IsolatedResult{Evidence: ev, EvidencePath: "evidence.json", Digest: strings.Repeat("d", 64)}, nil
	}
}

// Only changes that can alter runtime behaviour trigger the run (docs, SKILL
// text and front-end styling do not), and a provider that cannot list files
// never silently skips it.
func TestIsolatedRunTriggerSelection(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"coordinator code", []string{"internal/coordinator/store.go"}, true},
		{"worker command", []string{"cmd/clawflow/commands/worker_run.go"}, true},
		{"storage migration among docs", []string{"docs/a.md", "internal/store/migrations.go", "README.md"}, true},
		{"documentation only", []string{"docs/a.md", "README.md", "docs/adr/0002-x.md"}, false},
		{"SKILL text only", []string{"skills/implement/SKILL.md", "skills/repair-ci/SKILL.md"}, false},
		{"front-end style only", []string{"web/src/styles.css", "web/src/App.tsx", "macos/Sources/View.swift"}, false},
		{"tests only", []string{"internal/coordinator/store_test.go", "cmd/clawflow/commands/worker_test.go"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, rc := isolatedFixture(t, tc.files...)
			got, err := isolatedRunNeeded(f, rc, "o/r", f.pr)
			if err != nil || got != tc.want {
				t.Fatalf("got %v err %v, want %v", got, err, tc.want)
			}
		})
	}
	t.Run("disabled repository", func(t *testing.T) {
		f, rc := isolatedFixture(t, "internal/coordinator/store.go")
		rc.AcceptanceIsolatedRun = false
		if got, _ := isolatedRunNeeded(f, rc, "o/r", f.pr); got || f.calls != 0 {
			t.Fatalf("a repository that did not opt in was run (%v, %d lookups)", got, f.calls)
		}
	})
	t.Run("provider cannot list files", func(t *testing.T) {
		f, rc := isolatedFixture(t)
		f.err = errors.New("rate limited")
		if got, err := isolatedRunNeeded(f, rc, "o/r", f.pr); err == nil || got {
			t.Fatalf("an unreadable file list must be an error, got %v %v", got, err)
		}
		var readErr acceptanceReadError
		if _, err := isolatedRunNeeded(f, rc, "o/r", f.pr); !errors.As(err, &readErr) {
			t.Fatalf("the failure must be a read error so callers retry: %T", err)
		}
		if _, err := isolatedRunNeeded(f.acceptanceFake, rc, "o/r", f.pr); err == nil {
			t.Fatal("a provider without file listing silently skipped the run")
		}
	})
	t.Run("inside a candidate", func(t *testing.T) {
		f, rc := isolatedFixture(t, "internal/coordinator/store.go")
		t.Setenv(devmode.EnvID, "0123456789ab")
		t.Setenv(devmode.EnvRoot, t.TempDir())
		if got, _ := isolatedRunNeeded(f, rc, "o/r", f.pr); got {
			t.Fatal("a candidate must not start another candidate")
		}
	})
}

func TestIsolatedRunFileListIsReadOncePerHead(t *testing.T) {
	f, rc := isolatedFixture(t, "internal/coordinator/store.go")
	f.pr.HeadSHA = strings.Repeat("e", 40)
	for i := 0; i < 5; i++ {
		if got, err := isolatedRunNeeded(f, rc, "o/r", f.pr); err != nil || !got {
			t.Fatal(got, err)
		}
	}
	if f.calls != 1 {
		t.Fatalf("the provider was asked %d times for one head", f.calls)
	}
	f.pr.HeadSHA = strings.Repeat("f", 40)
	if _, err := isolatedRunNeeded(f, rc, "o/r", f.pr); err != nil || f.calls != 2 {
		t.Fatalf("a new head must be listed again (%d)", f.calls)
	}
}

func TestAcceptanceOfARuntimeChangeCarriesAndCitesTheIsolatedRun(t *testing.T) {
	f, _ := isolatedFixture(t, "internal/coordinator/store.go", "docs/x.md")
	runs := 0
	stubIsolated(t, true, &runs, nil)
	r, err := runAcceptance(context.Background(), f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 || !r.Contract.IsolatedRun || r.IsolatedRun == nil {
		t.Fatalf("runs=%d contract flag=%v run=%v", runs, r.Contract.IsolatedRun, r.IsolatedRun)
	}
	if r.IsolatedRun.Status != acceptance.IsolatedPassed || r.Verdict != acceptance.Passed {
		t.Fatalf("run %s verdict %s", r.IsolatedRun.Status, r.Verdict)
	}
	if len(r.IsolatedRun.Triggers) != 1 || r.IsolatedRun.Triggers[0] != "internal/coordinator/store.go" {
		t.Fatalf("triggers %v", r.IsolatedRun.Triggers)
	}
	cited := false
	for _, e := range r.Evidence {
		cited = cited || (strings.HasPrefix(e, "isolated-run: ") && strings.Contains(e, strings.Repeat("d", 64)))
	}
	if !cited {
		t.Fatalf("the verdict does not cite the run: %v", r.Evidence)
	}
	// The stored record the merge gate reads carries the same evidence.
	if _, rec, err := currentAcceptance(context.Background(), f, "o/r", 11); err != nil || rec.IsolatedRun == nil {
		t.Fatalf("gate read: %v %+v", err, rec.IsolatedRun)
	}
}

// A change that cannot alter runtime behaviour is accepted without the run, so
// acceptance stays as fast as before for it.
func TestAcceptanceOfADocsChangeDoesNotRunIsolated(t *testing.T) {
	f, _ := isolatedFixture(t, "docs/a.md", "skills/implement/SKILL.md", "web/src/App.tsx")
	runs := 0
	stubIsolated(t, true, &runs, nil)
	r, err := runAcceptance(context.Background(), f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 0 || r.Contract.IsolatedRun || r.IsolatedRun != nil || r.Verdict != acceptance.Passed {
		t.Fatalf("a docs change ran the isolated build: runs=%d flag=%v", runs, r.Contract.IsolatedRun)
	}
}

// The run failing makes the verdict failed, and the existing repair flow picks
// it up: the failed record is classified repairable with exact evidence.
func TestAFailingIsolatedRunFailsAcceptanceAndEntersRepair(t *testing.T) {
	f, _ := isolatedFixture(t, "internal/coordinator/store.go")
	runs := 0
	stubIsolated(t, false, &runs, nil)
	r, err := runAcceptance(context.Background(), f, "o/r", 11)
	if !errors.Is(err, errAcceptanceNonpass) {
		t.Fatalf("want a recorded nonpassing verdict, got %v", err)
	}
	if r.Verdict != acceptance.Failed || r.IsolatedRun == nil || r.IsolatedRun.Status != acceptance.IsolatedFailed {
		t.Fatalf("verdict %s run %+v", r.Verdict, r.IsolatedRun)
	}
	if len(r.IsolatedRun.ResidualProcesses) == 0 {
		t.Fatal("the leftover process was not recorded")
	}
	decision, rec, _, _ := classifyAcceptanceForRepair(context.Background(), f, "o/r", 11)
	if decision != acceptanceRepairFailed {
		t.Fatalf("repair decision %v, want %v", decision, acceptanceRepairFailed)
	}
	findings := findingsFromRecord(rec)
	if len(findings.Evidence) == 0 || !strings.Contains(strings.Join(findings.Evidence, " "), "residual processes: 1") {
		t.Fatalf("the repair is not told what failed: %+v", findings)
	}
	// The merge gate refuses it.
	if _, _, gateErr := currentAcceptance(context.Background(), f, "o/r", 11); gateErr == nil {
		t.Fatal("a head whose isolated run failed passed the gate")
	}
}

// A run that could not be attempted is an infrastructure error. It must neither
// pass the head nor blame its code, and it must not consume a repair attempt.
func TestAnUnattemptedIsolatedRunIsInfrastructureNotAVerdict(t *testing.T) {
	f, _ := isolatedFixture(t, "internal/coordinator/store.go")
	runs := 0
	stubIsolated(t, true, &runs, candidate.ErrNotAttempted)
	r, err := runAcceptance(context.Background(), f, "o/r", 11)
	if err == nil || errors.Is(err, errAcceptanceNonpass) {
		t.Fatalf("want an infrastructure error, got %v", err)
	}
	if r.Verdict == acceptance.Failed || !acceptance.IsInfrastructure(r) {
		t.Fatalf("verdict %s infrastructure=%v", r.Verdict, acceptance.IsInfrastructure(r))
	}
	if decision, _, _, _ := classifyAcceptanceForRepair(context.Background(), f, "o/r", 11); decision == acceptanceRepairFailed {
		t.Fatal("an environment problem was sent to code repair")
	}
}
