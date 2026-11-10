package candidate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolatedSource clones the working tree HEAD into a temp repository with the
// current uncommitted edits applied as one commit, so the test builds exactly
// the code under test, and returns the repository and the commit.
func isolatedSource(t *testing.T, mutate func(dir string)) (repo, commit string) {
	t.Helper()
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	root := strings.TrimSpace(string(top))
	repo = filepath.Join(t.TempDir(), "src")
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(t.TempDir(), "clone", "--quiet", "--local", root, repo)
	// Copy the tracked and new files of the working tree over the clone.
	files, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range strings.Split(string(files), "\x00") {
		if f == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			continue // deleted in the working tree
		}
		dst := filepath.Join(repo, f)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if mutate != nil {
		mutate(repo)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "--quiet", "--allow-empty", "-m", "isolated run fixture")
	return repo, run(repo, "rev-parse", "HEAD")
}

func isolatedInput(t *testing.T, repo, commit string) IsolatedInput {
	t.Helper()
	root := filepath.Join(t.TempDir(), "stable")
	for _, d := range []string{"config", "data"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stable := Stable{Root: root, Ports: []int{8090, 8093, 5432}, LabelPrefix: "com.zhoushoujianwork.clawflow."}
	modCache, _ := exec.Command("go", "env", "GOMODCACHE").Output()
	return IsolatedInput{
		Commit: commit, Source: repo, Base: filepath.Join(t.TempDir(), "cands"), EvidenceDir: t.TempDir(),
		Stable: stable, Probe: StableProbe{}, GoCache: os.Getenv("CLAWFLOW_TEST_GOCACHE"),
		ModCache: strings.TrimSpace(string(modCache)), TaskTimeout: 90 * time.Second,
	}
}

func TestIsolatedRunCompletesARealTask(t *testing.T) {
	if testing.Short() || os.Getenv("CLAWFLOW_E2E_ISOLATED") != "1" {
		t.Skip("set CLAWFLOW_E2E_ISOLATED=1 and omit -short: builds and runs a candidate")
	}
	repo, commit := isolatedSource(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	res, err := RunIsolated(ctx, isolatedInput(t, repo, commit))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed() {
		for k, v := range res.Evidence.Task.Diagnostics {
			t.Logf("diagnostic %s:\n%s", k, v)
		}
		t.Logf("residual locks: %v", res.Evidence.Task.ResidualLocks)
		t.Logf("residual processes: %v", res.Evidence.Task.ResidualProcesses)
		t.Fatalf("run did not pass: %s", res.Evidence.Task.Summary())
	}
	task := res.Evidence.Task
	for _, s := range task.Steps {
		t.Logf("%-13s passed=%v %s", s.Name, s.Passed, s.Evidence)
	}
	if len(task.ResidualProcesses) != 0 || len(task.ResidualLocks) != 0 {
		t.Fatalf("residue: %v %v", task.ResidualProcesses, task.ResidualLocks)
	}
}

// A defect that appears only when the program really runs must fail the run.
// Each fault below passes every unit test of the surrounding packages: nothing
// in the build or the code review shows it.
func TestIsolatedRunFailsOnDefectsOnlyARealRunShows(t *testing.T) {
	if testing.Short() || os.Getenv("CLAWFLOW_E2E_ISOLATED") != "1" {
		t.Skip("set CLAWFLOW_E2E_ISOLATED=1 and omit -short: builds and runs a candidate")
	}
	cases := []struct {
		name  string
		agent func(string) string
		step  string
		want  func(*TaskRun) bool
	}{
		{"a background writer is left running", AgentLeavingBackground, "", func(r *TaskRun) bool { return len(r.ResidualProcesses) > 0 }},
		{"the write-back never reaches the forge", AgentSkippingWriteBack, "written-back", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, commit := isolatedSource(t, nil)
			in := isolatedInput(t, repo, commit)
			in.Agent = tc.agent
			in.TaskTimeout = 60 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
			defer cancel()
			res, err := RunIsolated(ctx, in)
			if err != nil {
				t.Fatalf("a defective commit must be judged, not reported as an environment error: %v", err)
			}
			if res.Passed() {
				t.Fatalf("a run with an injected defect passed: %s", res.Evidence.Task.Summary())
			}
			task := res.Evidence.Task
			t.Log(task.Summary())
			if tc.want != nil && !tc.want(task) {
				t.Fatalf("the defect was not the recorded reason: %s", task.Summary())
			}
			if tc.step != "" {
				failed := false
				for _, s := range task.Steps {
					failed = failed || (s.Name == tc.step && !s.Passed)
				}
				if !failed && task.Failure == "" {
					t.Fatalf("step %q was not recorded as failed: %s", tc.step, task.Summary())
				}
			}
			if err := CheckEvidence(*res.Evidence, commit, true); err == nil {
				t.Fatal("evidence of a defective run verified")
			}
		})
	}
}
