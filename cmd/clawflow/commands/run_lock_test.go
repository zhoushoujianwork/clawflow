package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// stubRepoLocks replaces the run-lock hooks. failures maps a repo to how many
// acquire attempts fail before one succeeds (a large value never succeeds).
func stubRepoLocks(t *testing.T, failures map[string]int) (calls map[string]int, released func() []string) {
	t.Helper()
	var mu sync.Mutex
	calls = map[string]int{}
	var rel []string
	prevA, prevR, prevWait, prevPoll := acquireRepoRunLock, releaseRepoRunLock, targetedLockWait, runLockPoll
	acquireRepoRunLock = func(repo, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		calls[repo]++
		if calls[repo] <= failures[repo] {
			return errors.New("another clawflow run is active")
		}
		return nil
	}
	releaseRepoRunLock = func(repo string) {
		mu.Lock()
		defer mu.Unlock()
		rel = append(rel, repo)
	}
	targetedLockWait, runLockPoll = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() {
		acquireRepoRunLock, releaseRepoRunLock, targetedLockWait, runLockPoll = prevA, prevR, prevWait, prevPoll
	})
	return calls, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), rel...)
	}
}

func TestUntargetedRepoRunSkipsOnLockContention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls, _ := stubRepoLocks(t, map[string]int{"o/a": 1000})
	pl := &passLocks{}
	skip, err := acquireRepoLockForPass(context.Background(), nil, pl, "o/a", false)
	if !skip || err != nil || calls["o/a"] != 1 || pl.has("o/a") {
		t.Fatalf("skip=%v err=%v calls=%v held=%v", skip, err, calls, pl.has("o/a"))
	}
}

func TestTargetedRunWaitsOnOwnRepoLockOnly(t *testing.T) {
	calls, _ := stubRepoLocks(t, map[string]int{"o/a": 3, "o/other": 1000})
	pl := &passLocks{}
	skip, err := acquireRepoLockForPass(context.Background(), nil, pl, "o/a", true)
	if skip || err != nil || calls["o/a"] != 4 || !pl.has("o/a") {
		t.Fatalf("skip=%v err=%v calls=%v", skip, err, calls)
	}
	if calls["o/other"] != 0 {
		t.Fatalf("another repo's lock was touched: %v", calls)
	}
}

func TestTargetedRunFailsAfterLockWaitTimeout(t *testing.T) {
	stubRepoLocks(t, map[string]int{"o/a": 1000})
	skip, err := acquireRepoLockForPass(context.Background(), nil, &passLocks{}, "o/a", true)
	if skip || err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
}

func TestTargetedRunLockWaitHonorsCancellation(t *testing.T) {
	stubRepoLocks(t, map[string]int{"o/a": 1000})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireRepoLockForPass(ctx, nil, &passLocks{}, "o/a", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestLockUnscopedReposKeepsOnlyAcquired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, released := stubRepoLocks(t, map[string]int{"o/b": 1000})
	pl := &passLocks{}
	got := lockUnscopedRepos(nil, pl, []string{"o/a", "o/b", "o/c"})
	if strings.Join(got, ",") != "o/a,o/c" || !pl.has("o/a") || pl.has("o/b") || !pl.has("o/c") {
		t.Fatalf("got=%v", got)
	}
	pl.releaseAll()
	pl.releaseAll() // idempotent
	if rel := released(); len(rel) != 2 {
		t.Fatalf("released = %v, want the 2 acquired repos once each", rel)
	}
}

func TestRunOnceUnscopedProcessesOnlyUnlockedRepos(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, func(r *config.Repo) { r.Enabled = true })
	// A live process (this test) holds o/r's run lock via the real lock files.
	if err := snapshot.AcquireRepoRunLock("o/r", "test"); err != nil {
		t.Fatal(err)
	}
	// Re-acquiring in the same pid fails (PID alive), like a competing pass.
	targetedVCS(t, []map[string]any{targetedIssue(7)})
	dispatched := recordDispatch(t)
	var err error
	out := captureStderr(t, func() { err = runOnce(context.Background(), "", 0, time.Minute) })
	if err != nil {
		t.Fatalf("runOnce must exit 0 when every repo is locked: %v", err)
	}
	if len(dispatched()) != 0 {
		t.Fatalf("dispatched %v for a locked repo", dispatched())
	}
	if !strings.Contains(out, "o/r") {
		t.Fatalf("stderr lacks skipped repo: %s", out)
	}
	logData, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".clawflow", "logs", "run.log"))
	if !strings.Contains(string(logData), "reason=repo_locked") || !strings.Contains(string(logData), "repo=o/r") {
		t.Fatalf("run.log lacks repo_locked skip:\n%s", logData)
	}
	// The pre-existing lock is not ours to release.
	if _, statErr := os.Stat(snapshot.RepoRunLockPath("o/r")); statErr != nil {
		t.Fatalf("holder's lock disappeared: %v", statErr)
	}
}

func TestRunOnceReleasesRepoLockOnExit(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, func(r *config.Repo) { r.Enabled = true })
	targetedVCS(t, []map[string]any{targetedIssue(7)})
	recordDispatch(t)
	if err := runOnce(context.Background(), "", 0, time.Minute); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if _, err := os.Stat(snapshot.RepoRunLockPath("o/r")); !os.IsNotExist(err) {
		t.Fatalf("repo lock left behind: %v", err)
	}
}

func TestRunOnceSkipsEveryRepoWhileLegacyGlobalLockIsLive(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, func(r *config.Repo) { r.Enabled = true })
	if err := snapshot.AcquireRunLock("old-binary"); err != nil {
		t.Fatal(err)
	}
	dispatched := recordDispatch(t)
	if err := runOnce(context.Background(), "o/r", 0, time.Minute); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if err := runOnce(context.Background(), "", 0, time.Minute); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if len(dispatched()) != 0 {
		t.Fatalf("dispatched %v under legacy lock", dispatched())
	}
	if _, err := os.Stat(snapshot.RunLockPath()); err != nil {
		t.Fatalf("legacy lock was removed: %v", err)
	}
}
