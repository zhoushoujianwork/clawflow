package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestFinishedWorktreeProtection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	state, local := config.StateDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", local}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init")
	parent := filepath.Join(state, "worktrees", config.RepoSlugFS("o/r"))
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	prs := []vcs.PR{}
	for i, name := range []string{"merged", "open", "leased", "locked", "closed", "dirty", "unknown", "reused"} {
		issue := i + 1
		p := filepath.Join(parent, "issue-"+string(rune('1'+i))+"-2026-01-01T00-00-00Z")
		git("worktree", "add", "-b", name, p)
		state := "merged"
		if name == "open" {
			state = "open"
		}
		if name == "closed" {
			state = "closed"
		}
		if name != "unknown" {
			prs = append(prs, vcs.PR{Number: issue + 10, HeadBranch: name, State: state})
		}
		if name == "reused" {
			prs = append(prs, vcs.PR{HeadBranch: name, State: "open"})
		}
		if name == "dirty" {
			if err := os.WriteFile(filepath.Join(p, "wip"), []byte("wip"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := snapshot.AcquireLock("o/r", 4, "implement"); err != nil {
		t.Fatal(err)
	}
	defer snapshot.ReleaseLock("o/r", 4)
	for _, dry := range []bool{true, false} {
		r := housekeeping.Result{At: time.Now(), DryRun: dry}
		pruneFinishedWorktrees(context.Background(), &r, state, "o/r", local, prs, map[int]bool{3: true})
		if len(r.Paths) != 2 || len(r.Errors) != 0 {
			t.Fatalf("%+v", r)
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatal(err)
		}
		want := 6
		if dry {
			want = 8
		}
		if len(entries) != want {
			t.Fatalf("dry=%v worktrees=%d want=%d", dry, len(entries), want)
		}
	}
}

func TestHousekeepingLimits(t *testing.T) {
	defaults := (config.HousekeepingSettings{}).Limits()
	if defaults.TempMaxAgeHours != 24 || defaults.ReviewCacheMaxBytes != 2<<30 || defaults.MinFreeBytes != 2<<30 || defaults.DiskWarningPercent != 85 {
		t.Fatalf("%+v", defaults)
	}
	custom := config.HousekeepingSettings{TempMaxAgeHours: 48, ReviewCacheMaxBytes: 1024, MinFreeBytes: 2048, DiskWarningPercent: 90}
	invalid := config.HousekeepingSettings{TempMaxAgeHours: 1<<31 - 1, DiskWarningPercent: 101}.Limits()
	if invalid.TempMaxAgeHours != 24 || invalid.DiskWarningPercent != 85 {
		t.Fatalf("unsafe limits: %+v", invalid)
	}
	if custom.Limits() != custom {
		t.Fatal("custom limits lost")
	}
}

func TestHousekeepingActiveGateAndPreview(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	path := filepath.Join(os.TempDir(), "clawflow-op-old")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := housekeeping.Gate(context.Background(), os.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	r := runHousekeeping(context.Background(), &config.Config{}, false)
	release()
	if r.Skipped != "active_tasks_or_housekeeping" || len(r.Paths) != 0 {
		t.Fatalf("%+v", r)
	}
	r = runHousekeeping(context.Background(), &config.Config{}, true)
	if len(r.Paths) != 1 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("preview removed directory", err)
	}
	r = runHousekeeping(context.Background(), &config.Config{}, false)
	if len(r.Paths) != 1 {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("old directory remains", err)
	}
	if _, err := os.Stat(housekeepingResultPath()); err != nil {
		t.Fatal("result not persisted", err)
	}
}
