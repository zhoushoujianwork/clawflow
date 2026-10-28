package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRepoRunLockPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := filepath.Base(RepoRunLockPath("owner/name")); got != "run-owner__name.lock" {
		t.Fatalf("path = %s", got)
	}
	if got := filepath.Base(RepoRunLockPath("grp/sub/name")); got != "run-grp__sub__name.lock" {
		t.Fatalf("subgroup path = %s", got)
	}
}

func TestRepoRunLock_DifferentReposBothAcquire(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AcquireRepoRunLock("o/a", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := AcquireRepoRunLock("o/b", "v1"); err != nil {
		t.Fatalf("second repo must not be blocked: %v", err)
	}
	if err := AcquireRepoRunLock("o/a", "v1"); err == nil {
		t.Fatal("same repo acquired twice")
	}
	info, err := ReadRepoRunLock("o/a")
	if err != nil || info.PID != os.Getpid() || info.Repo != "o/a" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	ReleaseRepoRunLock("o/a")
	if _, err := os.Stat(RepoRunLockPath("o/a")); err != nil || LockHeld(RepoRunLockPath("o/a")) {
		t.Fatal("lock must persist unlocked")
	}
	if err := AcquireRepoRunLock("o/a", "v1"); err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	ReleaseRepoRunLock("o/a")
	ReleaseRepoRunLock("o/b")
}

func TestRepoRunLock_ReclaimsStaleAndCorrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if processAlive(2999999) {
		t.Skip("test PID is alive")
	}
	for name, payload := range map[string]string{"stale": `{"pid":2999999}`, "corrupt": "not json"} {
		t.Run(name, func(t *testing.T) {
			path := RepoRunLockPath("o/stale")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := AcquireRepoRunLock("o/stale", "v1"); err != nil {
				t.Fatalf("reclaim: %v", err)
			}
			if info, err := ReadRepoRunLock("o/stale"); err != nil || info.PID != os.Getpid() {
				t.Fatalf("info=%+v err=%v", info, err)
			}
			ReleaseRepoRunLock("o/stale")
		})
	}
}

func TestRepoRunLock_ConcurrentExclusion(t *testing.T) {
	testConcurrentLock(t, func() string { return RepoRunLockPath("o/r") },
		func() error { return AcquireRepoRunLock("o/r", "v1") },
		func() { ReleaseRepoRunLock("o/r") })
}

func TestLegacyGlobalLockBlocksEveryRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AcquireRunLock("old"); err != nil {
		t.Fatal(err)
	}
	if err := AcquireRepoRunLock("o/a", "v1"); err == nil {
		t.Fatal("repo lock acquired under a live legacy lock")
	}
	if h := RepoRunLockHolder("o/zzz"); h == nil || h.PID != os.Getpid() {
		t.Fatalf("holder = %+v", h)
	}
	if held, _ := AnyRunLockHeld(); !held {
		t.Fatal("AnyRunLockHeld missed the legacy lock")
	}
	if held, _ := AnyRunLockHeldExcept(os.Getpid()); held {
		t.Fatal("own lock must be ignored")
	}
	ReleaseRunLock()
	if held, _ := AnyRunLockHeld(); held {
		t.Fatal("nothing should be held")
	}
}

func TestAnyRunLockHeld_PerRepoAndStale(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if held, _ := AnyRunLockHeld(); held {
		t.Fatal("empty dir reported held")
	}
	if !processAlive(2999999) {
		path := RepoRunLockPath("o/dead")
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte(`{"pid":2999999}`), 0o644)
		if held, _ := AnyRunLockHeld(); held {
			t.Fatal("dead holder counted")
		}
	}
	if err := AcquireRepoRunLock("o/a", "v1"); err != nil {
		t.Fatal(err)
	}
	if held, info := AnyRunLockHeld(); !held || info.Repo != "o/a" {
		t.Fatalf("held=%v info=%+v", held, info)
	}
	// CleanStaleLocks must keep the live per-repo lock.
	CleanStaleLocks()
	if _, err := os.Stat(RepoRunLockPath("o/a")); err != nil {
		t.Fatalf("live repo lock removed: %v", err)
	}
	ReleaseRepoRunLock("o/a")
}

func TestSnapshotLock_ExclusionAndTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := AcquireSnapshotLock(); err != nil {
		t.Fatal(err)
	}
	old := snapshotLockWait
	snapshotLockWait = 100 * time.Millisecond
	defer func() { snapshotLockWait = old }()
	err := AcquireSnapshotLock()
	if err == nil || !strings.Contains(err.Error(), "still held") {
		t.Fatalf("err = %v", err)
	}
	ReleaseSnapshotLock()
	if err := AcquireSnapshotLock(); err != nil {
		t.Fatal(err)
	}
	ReleaseSnapshotLock()
}

func issueAt(repo string, n int, at time.Time) IssueEntry {
	return IssueEntry{Repo: repo, IssueNumber: n, State: "open", CapturedAt: at}
}

func readIssues(t *testing.T) map[string][]int {
	t.Helper()
	got := map[string][]int{}
	for _, e := range readIssueFile(t) {
		got[e.Repo] = append(got[e.Repo], e.IssueNumber)
	}
	return got
}

func TestWriteIssuesMerged_PreservesOtherRepos(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC()
	if err := WriteIssues([]IssueEntry{issueAt("o/a", 1, now), issueAt("o/b", 7, now)}); err != nil {
		t.Fatal(err)
	}
	// A pass that only holds A rewrites A and leaves B alone.
	if err := WriteIssuesMerged([]IssueEntry{issueAt("o/a", 2, now.Add(time.Second))}, map[string]bool{"o/b": true}); err != nil {
		t.Fatal(err)
	}
	got := readIssues(t)
	if len(got["o/a"]) != 1 || got["o/a"][0] != 2 || len(got["o/b"]) != 1 || got["o/b"][0] != 7 {
		t.Fatalf("issues = %v", got)
	}
	// A repo no longer enabled (not preserved, not scanned) is dropped as before.
	if err := WriteIssuesMerged([]IssueEntry{issueAt("o/a", 2, now)}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readIssues(t); len(got["o/b"]) != 0 {
		t.Fatalf("unpreserved repo kept: %v", got)
	}
}

func TestWritePendingMerged_PreservesOtherRepos(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WritePending([]PendingEntry{{Repo: "o/a", IssueNumber: 1, Operator: "x"}, {Repo: "o/b", IssueNumber: 5, Operator: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := WritePendingMerged([]PendingEntry{{Repo: "o/a", IssueNumber: 2, Operator: "x"}}, map[string]bool{"o/b": true}); err != nil {
		t.Fatal(err)
	}
	got := readPendingFile(t)
	if len(got) != 2 || got[0].Repo != "o/a" || got[0].IssueNumber != 2 || got[1].Repo != "o/b" || got[1].IssueNumber != 5 {
		t.Fatalf("pending = %+v", got)
	}
}

func TestSnapshotMerge_ConcurrentPassesBothLand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		for _, repo := range []string{"o/a", "o/b"} {
			other := map[string]bool{"o/a": true, "o/b": true}
			delete(other, repo)
			wg.Add(1)
			go func(repo string, n int) {
				defer wg.Done()
				if err := WriteIssuesMerged([]IssueEntry{issueAt(repo, n, now)}, other); err != nil {
					t.Error(err)
				}
				if err := WritePendingMerged([]PendingEntry{{Repo: repo, IssueNumber: n, Operator: "x"}}, other); err != nil {
					t.Error(err)
				}
			}(repo, i)
		}
	}
	wg.Wait()
	// Each repo's final state is one of its own writes, and neither repo vanished.
	got := readIssues(t)
	if len(got["o/a"]) == 0 || len(got["o/b"]) == 0 {
		t.Fatalf("a repo's entries were lost: %v", got)
	}
	pending := readPendingFile(t)
	seen := map[string]bool{}
	for _, p := range pending {
		seen[p.Repo] = true
	}
	if !seen["o/a"] || !seen["o/b"] {
		t.Fatalf("pending lost a repo: %+v", pending)
	}
}

func readIssueFile(t *testing.T) []IssueEntry {
	t.Helper()
	var out []IssueEntry
	readJSONFile(t, "issues.json", &out)
	return out
}

func readPendingFile(t *testing.T) []PendingEntry {
	t.Helper()
	var out []PendingEntry
	readJSONFile(t, "pending.json", &out)
	return out
}

func readJSONFile(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(DataDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}
