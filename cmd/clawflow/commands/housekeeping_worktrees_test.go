package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type reclaimEnv struct {
	t     *testing.T
	state string // ClawFlow data dir
	local string // configured clone
	out   string // directory outside the data dir
	cfg   *config.Config
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newReclaimEnv builds a clone whose origin/main has two commits.
func newReclaimEnv(t *testing.T) *reclaimEnv {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	e := &reclaimEnv{t: t, state: t.TempDir(), local: filepath.Join(t.TempDir(), "clone"), out: t.TempDir()}
	t.Setenv("CLAWFLOW_STATE_DIR", e.state)
	origin := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", origin).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := exec.Command("git", "init", "-q", "-b", "main", e.local).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	gitRun(t, e.local, "remote", "add", "origin", origin)
	gitRun(t, e.local, "commit", "-q", "--allow-empty", "-m", "first")
	gitRun(t, e.local, "commit", "-q", "--allow-empty", "-m", "second")
	gitRun(t, e.local, "push", "-q", "origin", "main")
	e.cfg = &config.Config{Repos: map[string]config.Repo{"o/r": {BaseBranch: "main", LocalPath: e.local}}}
	return e
}

func (e *reclaimEnv) parent() string {
	return filepath.Join(e.state, "worktrees", config.RepoSlugFS("o/r"))
}

// add creates a worktree named issue-<n>-<ts> under dir at rev; branch=="" is detached.
func (e *reclaimEnv) add(dir string, n int, rev, branch string) string {
	e.t.Helper()
	p := filepath.Join(dir, "issue-"+strconv.Itoa(n)+"-2026-01-01T00-00-00Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if branch == "" {
		gitRun(e.t, e.local, "worktree", "add", "-q", "--detach", p, rev)
	} else {
		gitRun(e.t, e.local, "worktree", "add", "-q", "-b", branch, p, rev)
	}
	return p
}

// age backdates every file the grace check and git's own pruning look at.
func age(t *testing.T, p string, by time.Duration) {
	t.Helper()
	when := time.Now().Add(-by)
	admin, _ := worktreeAdminDir(p)
	for _, f := range []string{p, filepath.Join(p, ".git"), admin, filepath.Join(admin, "HEAD"), filepath.Join(admin, "index"), filepath.Join(admin, "logs", "HEAD")} {
		if f == "" {
			continue
		}
		if _, err := os.Lstat(f); err == nil {
			if err := os.Chtimes(f, when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// ageRecord backdates the admin dir of a worktree whose directory is gone.
func ageRecord(t *testing.T, local, name string, by time.Duration) {
	t.Helper()
	when := time.Now().Add(-by)
	admin := filepath.Join(local, ".git", "worktrees", name)
	for _, f := range []string{admin, filepath.Join(admin, "index"), filepath.Join(admin, "gitdir")} {
		if _, err := os.Lstat(f); err == nil {
			if err := os.Chtimes(f, when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func (e *reclaimEnv) run(dry bool, active map[string]map[int]bool) housekeeping.Result {
	e.t.Helper()
	r := housekeeping.Result{At: time.Now().UTC(), DryRun: dry, Paths: []string{}}
	reclaimIssueWorktrees(context.Background(), &r, e.cfg, worktreeRoots(e.state), 24*time.Hour, runLog,
		func(context.Context) (map[string]map[int]bool, error) { return active, nil })
	return r
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func worktreeRecords(t *testing.T, local string) string {
	t.Helper()
	return gitRun(t, local, "worktree", "list", "--porcelain")
}

func TestReclaimIssueWorktrees(t *testing.T) {
	e := newReclaimEnv(t)
	lg, err := clog.Open("run")
	if err != nil {
		t.Fatal(err)
	}
	old := runLog
	runLog = lg
	t.Cleanup(func() { runLog = old; lg.Close() })

	parent := e.parent()
	merged := e.add(parent, 1, "origin/main", "")                                      // clean, at origin/main
	behind := e.add(parent, 2, "origin/main~1", "")                                    // clean, older ancestor of origin/main
	branched := e.add(parent, 3, "origin/main", "fix/issue-3")                         // clean, on a branch that must survive
	dirty := e.add(parent, 4, "origin/main", "")                                       // untracked file
	unmerged := e.add(parent, 5, "origin/main", "")                                    // commit not in origin/main
	fresh := e.add(parent, 6, "origin/main", "")                                       // clean but inside the grace period
	activeWT := e.add(parent, 7, "origin/main", "")                                    // queued/leased task
	locked := e.add(parent, 8, "origin/main", "")                                      // issue lock held
	gitLocked := e.add(parent, 9, "origin/main", "")                                   // `git worktree lock`
	outside := e.add(filepath.Join(e.out, "worktrees", "o__r"), 10, "origin/main", "") // not under the data dir

	if err := os.WriteFile(filepath.Join(dirty, "wip.txt"), []byte("wip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unmerged, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, unmerged, "add", "f.txt")
	gitRun(t, unmerged, "commit", "-q", "-m", "local only")
	gitRun(t, e.local, "worktree", "lock", gitLocked)
	if err := snapshot.AcquireLock("o/r", 8, "implement"); err != nil {
		t.Fatal(err)
	}
	defer snapshot.ReleaseLock("o/r", 8)

	for _, p := range []string{merged, behind, branched, dirty, unmerged, activeWT, locked, gitLocked, outside} {
		age(t, p, 72*time.Hour)
	}
	_ = fresh // deliberately left with current timestamps

	active := map[string]map[int]bool{"o/r": {7: true}}

	// A preview reports the same decisions without touching anything.
	dry := e.run(true, active)
	for _, p := range []string{merged, behind, branched} {
		if !exists(p) {
			t.Fatalf("dry run removed %s", p)
		}
	}
	if dry.Worktrees == nil || dry.Worktrees.Removed != 3 || len(dry.Paths) != 3 {
		t.Fatalf("dry run result: %+v paths=%v", dry.Worktrees, dry.Paths)
	}

	r := e.run(false, active)
	for name, p := range map[string]string{"merged": merged, "behind": behind, "branched": branched} {
		if exists(p) {
			t.Errorf("%s worktree should be removed: %s", name, p)
		}
	}
	for name, p := range map[string]string{"dirty": dirty, "unmerged": unmerged, "grace": fresh, "active": activeWT, "issue lock": locked, "git lock": gitLocked, "outside": outside} {
		if !exists(p) {
			t.Errorf("%s worktree must be kept: %s", name, p)
		}
	}
	if !strings.Contains(gitRun(t, e.local, "branch", "--list", "fix/issue-3"), "fix/issue-3") {
		t.Error("branch of a reclaimed worktree was deleted")
	}
	if got := worktreeRecords(t, e.local); !strings.Contains(got, outside) && !strings.Contains(got, filepath.Base(outside)) {
		t.Errorf("outside worktree record vanished:\n%s", got)
	}
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	s := r.Worktrees
	if s == nil || s.Removed != 3 || s.Skipped != 6 {
		t.Fatalf("stats: %+v", s)
	}
	want := map[string]int{"dirty": 1, "unmerged": 1, "within_grace": 1, "active_task": 1, "issue_locked": 1, "worktree_locked": 1}
	for k, v := range want {
		if s.SkippedBy[k] != v {
			t.Errorf("skipped_by[%s]=%d want %d (%v)", k, s.SkippedBy[k], v, s.SkippedBy)
		}
	}

	// The log names what was removed and how many were kept.
	data, err := os.ReadFile(filepath.Join(clog.LogsDir(), "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	logText := string(data)
	for _, p := range []string{merged, behind, branched} {
		if !strings.Contains(logText, "housekeeping/worktree_removed path="+p) {
			t.Errorf("log lacks removal of %s", p)
		}
	}
	if !strings.Contains(logText, "housekeeping/worktrees dry_run=false removed=3 pruned_records=0 skipped=6") {
		t.Errorf("log lacks summary:\n%s", logText)
	}
	if strings.Contains(logText, outside+" ") && strings.Contains(logText, "worktree_removed path="+outside) {
		t.Error("outside worktree logged as removed")
	}
}

func TestReclaimWorkerStateRoot(t *testing.T) {
	e := newReclaimEnv(t)
	p := e.add(filepath.Join(e.state, localWorkerStateSubdir, "worktrees", config.RepoSlugFS("o/r")), 11, "origin/main", "")
	age(t, p, 48*time.Hour)
	r := e.run(false, nil)
	if exists(p) || r.Worktrees == nil || r.Worktrees.Removed != 1 {
		t.Fatalf("local worker worktree not reclaimed: %+v", r.Worktrees)
	}
}

func TestReclaimKeepsEverythingWhenTaskStateUnknown(t *testing.T) {
	e := newReclaimEnv(t)
	p := e.add(e.parent(), 12, "origin/main", "")
	age(t, p, 48*time.Hour)
	r := housekeeping.Result{At: time.Now().UTC(), Paths: []string{}}
	reclaimIssueWorktrees(context.Background(), &r, e.cfg, worktreeRoots(e.state), 24*time.Hour, runLog,
		func(context.Context) (map[string]map[int]bool, error) { return nil, os.ErrInvalid })
	if !exists(p) || r.Worktrees.SkippedBy["active_state_unavailable"] != 1 || len(r.Errors) != 1 {
		t.Fatalf("%+v errors=%v", r.Worktrees, r.Errors)
	}
}

func TestReclaimUsesConfiguredBaseBranch(t *testing.T) {
	e := newReclaimEnv(t)
	gitRun(t, e.local, "push", "-q", "origin", "main:release")
	e.cfg.Repos["o/r"] = config.Repo{BaseBranch: "release", LocalPath: e.local}
	p := e.add(e.parent(), 13, "origin/release", "")
	gitRun(t, p, "commit", "-q", "--allow-empty", "-m", "ahead of release but could be in main")
	gitRun(t, e.local, "push", "-q", "origin", gitRun(t, p, "rev-parse", "HEAD")+":main")
	age(t, p, 48*time.Hour)
	r := e.run(false, nil)
	if !exists(p) || r.Worktrees.SkippedBy["unmerged"] != 1 {
		t.Fatalf("HEAD only in main must not count as merged into release: %+v", r.Worktrees)
	}
}

func TestPruneRecordsOfMissingDirectories(t *testing.T) {
	e := newReclaimEnv(t)
	gone := e.add(e.parent(), 14, "origin/main", "")
	recent := e.add(e.parent(), 15, "origin/main", "")
	kept := e.add(e.parent(), 16, "origin/main", "") // present, must be left alone
	age(t, kept, 1*time.Hour)
	for _, p := range []string{gone, recent} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	ageRecord(t, e.local, filepath.Base(gone), 48*time.Hour)

	dry := e.run(true, nil)
	if dry.Worktrees == nil || dry.Worktrees.Pruned != 1 || !strings.Contains(worktreeRecords(t, e.local), filepath.Base(gone)) {
		t.Fatalf("dry run must report but keep the record: %+v", dry.Worktrees)
	}
	r := e.run(false, nil)
	records := worktreeRecords(t, e.local)
	if strings.Contains(records, filepath.Base(gone)) {
		t.Errorf("stale record remains:\n%s", records)
	}
	if !strings.Contains(records, filepath.Base(recent)) {
		t.Errorf("record inside the grace period was pruned:\n%s", records)
	}
	if !strings.Contains(records, filepath.Base(kept)) || !exists(kept) {
		t.Error("existing worktree was affected")
	}
	if r.Worktrees.Pruned != 1 || len(r.Errors) != 0 {
		t.Fatalf("%+v %v", r.Worktrees, r.Errors)
	}
}

func TestPruneRecordsLeavesRecordsOutsideDataDir(t *testing.T) {
	e := newReclaimEnv(t)
	inside := e.add(e.parent(), 17, "origin/main", "")
	outside := e.add(filepath.Join(e.out, "worktrees", "o__r"), 18, "origin/main", "")
	for _, p := range []string{inside, outside} {
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
	}
	ageRecord(t, e.local, filepath.Base(inside), 48*time.Hour)
	// Both directories are named issue-N-<same ts>; git gives the second a suffix.
	entries, err := os.ReadDir(filepath.Join(e.local, ".git", "worktrees"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("setup: %v %d", err, len(entries))
	}
	for _, en := range entries {
		ageRecord(t, e.local, en.Name(), 48*time.Hour)
	}
	r := e.run(false, nil)
	records := worktreeRecords(t, e.local)
	if strings.Contains(records, inside) || !strings.Contains(records, outside) {
		t.Fatalf("expected only the in-data-dir record to be pruned:\n%s", records)
	}
	if r.Worktrees.Pruned != 1 {
		t.Fatalf("%+v", r.Worktrees)
	}
}

func TestHousekeepingLimitsWorktreeGrace(t *testing.T) {
	if got := (config.HousekeepingSettings{}).Limits().WorktreeGraceHours; got != 24 {
		t.Fatalf("default grace %d", got)
	}
	if got := (config.HousekeepingSettings{WorktreeGraceHours: 6}).Limits().WorktreeGraceHours; got != 6 {
		t.Fatalf("custom grace %d", got)
	}
	if got := (config.HousekeepingSettings{WorktreeGraceHours: -3}).Limits().WorktreeGraceHours; got != 24 {
		t.Fatalf("invalid grace %d", got)
	}
}

// Removal must never be forced: git itself is the last guard against losing
// work that appeared after the cleanliness check.
func TestReclaimNeverForcesRemoval(t *testing.T) {
	src, err := os.ReadFile("housekeeping_worktrees.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{`"--force"`, `"-f"`, `"-ff"`, `"branch"`, "RemoveAll"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("housekeeping_worktrees.go must not use %s", banned)
		}
	}
}
