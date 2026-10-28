package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

const testFullSHA = "0123456789abcdef0123456789abcdef01234567"

// upgradeHarness wires a selfUpgrader to a real temp dir for the filesystem
// and a scripted exec seam. The "installed binary" and the "candidate" are
// plain files whose content identifies them.
type upgradeHarness struct {
	t         *testing.T
	u         *selfUpgrader
	dest      string
	calls     []string
	failOn    string // substring of "name args" that fails
	versions  map[string]string
	checkJSON string
	web       bool
	restarts  [][]string
	renameErr func(oldpath, newpath string) error
	lockHeld  bool
	released  int
	worktrees []string
}

func newUpgradeHarness(t *testing.T) *upgradeHarness {
	t.Helper()
	dir := t.TempDir()
	h := &upgradeHarness{t: t, dest: filepath.Join(dir, "bin", "clawflow"), checkJSON: `{"status":"ready"}`}
	if err := os.MkdirAll(filepath.Dir(h.dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.dest, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := newSelfUpgrader()
	u.Version = "oldver"
	u.Commit = strings.Repeat("a", 40)
	u.Executable = func() (string, error) { return h.dest, nil }
	u.WebRunning = func() bool { return h.web }
	u.StartDetached = func(name string, args ...string) error {
		h.restarts = append(h.restarts, append([]string{name}, args...))
		return nil
	}
	u.Lock = func() (func(), bool, error) {
		if h.lockHeld {
			return nil, false, nil
		}
		return func() { h.released++ }, true, nil
	}
	u.Rename = func(o, n string) error {
		if h.renameErr != nil {
			if err := h.renameErr(o, n); err != nil {
				return err
			}
		}
		return os.Rename(o, n)
	}
	u.Run = h.run
	h.u = u
	return h
}

func (h *upgradeHarness) run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	h.calls = append(h.calls, line)
	if h.failOn != "" && strings.Contains(line, h.failOn) {
		return nil, errors.New("boom")
	}
	switch {
	case name == "git" && args[0] == "describe":
		return []byte(testFullSHA[:7]), nil
	case name == "git" && args[0] == "ls-remote":
		return []byte(testFullSHA + "\trefs/heads/main"), nil
	case name == "git" && args[0] == "rev-list":
		return []byte("1"), nil
	case name == "git" && args[0] == "rev-parse":
		return []byte(testFullSHA + "\n"), nil
	case name == "git" && args[0] == "worktree" && args[1] == "add":
		h.worktrees = append(h.worktrees, args[3])
		return nil, os.MkdirAll(args[3], 0o755)
	case name == "go":
		// The candidate binary: content marks it as NEW.
		for i, a := range args {
			if a == "-o" {
				return nil, os.WriteFile(args[i+1], []byte("NEW"), 0o755)
			}
		}
	case len(args) == 1 && args[0] == "version":
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		if v, ok := h.versions[string(b)]; ok {
			return []byte(v + "\n"), nil
		}
		if string(b) == "OLD" {
			return []byte(h.u.Version + "\nchannel=source commit=" + h.u.Commit + " branch=main\n"), nil
		}
		return []byte(testFullSHA[:7] + "\n"), nil
	case len(args) >= 2 && args[0] == "execution" && args[1] == "check":
		return []byte(h.checkJSON), nil
	}
	return nil, nil
}

func (h *upgradeHarness) installed() string {
	b, err := os.ReadFile(h.dest)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func (h *upgradeHarness) called(sub string) bool {
	for _, c := range h.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func selfUpgradeRepo(t *testing.T) config.Repo {
	return config.Repo{SelfUpgrade: true, BaseBranch: "main", LocalPath: t.TempDir()}
}

func TestSelfUpgradeSuccess(t *testing.T) {
	h := newUpgradeHarness(t)
	repo := selfUpgradeRepo(t)
	out := h.u.Upgrade(context.Background(), repo)
	if out.Result != selfUpgradeUpgraded || out.To != testFullSHA[:7] || out.From != "oldver" {
		t.Fatalf("outcome = %+v", out)
	}
	if h.installed() != "NEW" {
		t.Fatalf("installed = %q, want NEW", h.installed())
	}
	prev, err := os.ReadFile(h.dest + ".prev")
	if err != nil || string(prev) != "OLD" {
		t.Fatalf(".prev = %q err=%v", prev, err)
	}
	if info, _ := os.Stat(h.dest); info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed binary not executable: %v", info.Mode())
	}
	for _, leftover := range []string{h.dest + ".tmp", h.dest + ".new"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("leftover %s", leftover)
		}
	}
	if h.released != 1 {
		t.Errorf("lock released %d times, want 1", h.released)
	}
	if len(h.restarts) != 0 {
		t.Errorf("web not running but restart invoked: %v", h.restarts)
	}
}

func TestSelfUpgradeBuildsAtFetchedSHAInTempWorktree(t *testing.T) {
	h := newUpgradeHarness(t)
	repo := selfUpgradeRepo(t)
	h.u.Upgrade(context.Background(), repo)
	if !h.called("git fetch origin main") {
		t.Errorf("no fetch of origin/main: %v", h.calls)
	}
	if len(h.worktrees) != 1 {
		t.Fatalf("worktrees = %v", h.worktrees)
	}
	wt := h.worktrees[0]
	if strings.HasPrefix(wt, repo.LocalPath) {
		t.Errorf("worktree %q is inside the user's checkout %q", wt, repo.LocalPath)
	}
	if !h.called("git worktree add --detach " + wt + " " + testFullSHA) {
		t.Errorf("worktree not detached at fetched SHA: %v", h.calls)
	}
	if !h.called("pnpm install --frozen-lockfile") || !h.called("pnpm build") {
		t.Errorf("web bundle not built: %v", h.calls)
	}
	if !h.called("-X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.Version=" + testFullSHA[:7]) {
		t.Errorf("version ldflag missing: %v", h.calls)
	}
	if !h.called("git worktree remove --force " + wt) {
		t.Errorf("worktree not removed: %v", h.calls)
	}
	if _, err := os.Stat(filepath.Dir(wt)); err == nil {
		t.Errorf("temp dir %s not removed", filepath.Dir(wt))
	}
	// Only git commands that never modify the checkout run in local_path.
	for _, c := range h.calls {
		if strings.HasPrefix(c, "git checkout") || strings.HasPrefix(c, "git reset") || strings.HasPrefix(c, "git pull") || strings.HasPrefix(c, "git merge ") {
			t.Errorf("modifies user's checkout: %s", c)
		}
	}
}

func TestSelfUpgradeFailuresLeaveBinaryUntouched(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *upgradeHarness)
	}{
		{"fetch fails", func(h *upgradeHarness) { h.failOn = "git fetch" }},
		{"worktree add fails", func(h *upgradeHarness) { h.failOn = "git worktree add" }},
		{"pnpm install fails", func(h *upgradeHarness) { h.failOn = "pnpm install" }},
		{"pnpm build fails", func(h *upgradeHarness) { h.failOn = "pnpm build" }},
		{"go build fails", func(h *upgradeHarness) { h.failOn = "go build" }},
		{"candidate version mismatch", func(h *upgradeHarness) { h.versions = map[string]string{"NEW": "deadbee"} }},
		{"candidate version errors", func(h *upgradeHarness) { h.failOn = " version" }},
		{"execution check blocked", func(h *upgradeHarness) { h.checkJSON = `{"status":"blocked"}` }},
		{"execution check unreadable", func(h *upgradeHarness) { h.checkJSON = `not json` }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpgradeHarness(t)
			tc.mutate(h)
			out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
			if out.Result != selfUpgradeFailed || out.Reason == "" {
				t.Fatalf("outcome = %+v, want failed with reason", out)
			}
			if h.installed() != "OLD" {
				t.Errorf("installed binary changed to %q", h.installed())
			}
			if _, err := os.Stat(h.dest + ".prev"); err == nil {
				t.Errorf(".prev written before the candidate was verified")
			}
			if h.released != 1 {
				t.Errorf("lock released %d times", h.released)
			}
			for _, wt := range h.worktrees {
				if !h.called("git worktree remove --force " + wt) {
					t.Errorf("temp worktree %s not removed after failure", wt)
				}
			}
			if len(h.restarts) != 0 {
				t.Errorf("web restarted despite failure")
			}
		})
	}
}

func TestSelfUpgradeSkipsWhenVersionMatches(t *testing.T) {
	h := newUpgradeHarness(t)
	h.u.Version = testFullSHA[:7]
	out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
	if out.Result != selfUpgradeSkipped {
		t.Fatalf("outcome = %+v", out)
	}
	if h.called("worktree add") || h.called("go build") || h.installed() != "OLD" {
		t.Errorf("same-version skip still built: %v", h.calls)
	}
}

func TestSelfUpgradeLockPreventsConcurrentUpgrade(t *testing.T) {
	h := newUpgradeHarness(t)
	h.lockHeld = true
	out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
	if out.Result != selfUpgradeSkipped || !strings.Contains(out.Reason, "in progress") {
		t.Fatalf("outcome = %+v", out)
	}
	if len(h.calls) != 0 {
		t.Errorf("work done without the lock: %v", h.calls)
	}
}

func TestSelfUpgradeRestartsWebDetachedOnlyWhenRunning(t *testing.T) {
	h := newUpgradeHarness(t)
	h.web = true
	if out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t)); out.Result != selfUpgradeUpgraded {
		t.Fatalf("outcome = %+v", out)
	}
	if len(h.restarts) != 1 || h.restarts[0][0] != h.dest || strings.Join(h.restarts[0][1:], " ") != "web restart" {
		t.Fatalf("restarts = %v", h.restarts)
	}
}

func TestSelfUpgradeRollsBackWhenInstalledBinaryFailsVerification(t *testing.T) {
	h := newUpgradeHarness(t)
	// The candidate verifies; the installed copy then reports a wrong version.
	n := 0
	inner := h.u.Run
	h.u.Run = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		if len(args) == 1 && args[0] == "version" && name == h.dest {
			n++
			if n > 1 {
				return []byte("wrongsh\n"), nil
			}
		}
		return inner(ctx, dir, name, args...)
	}
	out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
	if out.Result != selfUpgradeFailed || !strings.Contains(out.Reason, "rolled back") {
		t.Fatalf("outcome = %+v", out)
	}
	if n != 2 {
		t.Fatalf("installed version checked %d times, want before and after replacement", n)
	}
	if h.installed() != "OLD" {
		t.Errorf("installed = %q, want OLD restored", h.installed())
	}
}

func TestSelfUpgradeRollsBackWhenWebRestartFails(t *testing.T) {
	h := newUpgradeHarness(t)
	h.web = true
	h.u.StartDetached = func(string, ...string) error { return errors.New("spawn failed") }
	out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
	if out.Result != selfUpgradeFailed || !strings.Contains(out.Reason, "rolled back") {
		t.Fatalf("outcome = %+v", out)
	}
	if h.installed() != "OLD" {
		t.Errorf("installed = %q, want OLD restored", h.installed())
	}
}

func TestSelfUpgradeReplaceFailureKeepsOriginal(t *testing.T) {
	h := newUpgradeHarness(t)
	h.renameErr = func(_, n string) error {
		if n == h.dest {
			return errors.New("rename denied")
		}
		return nil
	}
	out := h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
	if out.Result != selfUpgradeFailed {
		t.Fatalf("outcome = %+v", out)
	}
	if h.installed() != "OLD" {
		t.Errorf("installed = %q, want OLD", h.installed())
	}
	if _, err := os.Stat(h.dest + ".new"); err == nil {
		t.Errorf("staged file left behind")
	}
}

func TestSelfUpgradeReplacementIsRenameOfSiblingTempFile(t *testing.T) {
	h := newUpgradeHarness(t)
	var renames [][2]string
	prev := h.u.Rename
	h.u.Rename = func(o, n string) error {
		renames = append(renames, [2]string{o, n})
		return prev(o, n)
	}
	h.u.Upgrade(context.Background(), selfUpgradeRepo(t))
	var found bool
	for _, r := range renames {
		if r[1] == h.dest {
			found = true
			if filepath.Dir(r[0]) != filepath.Dir(h.dest) {
				t.Errorf("staged file %s not on the same filesystem as %s", r[0], h.dest)
			}
		}
	}
	if !found {
		t.Fatalf("executable not replaced by rename: %v", renames)
	}
}

func TestSelfUpgradeLockFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rel, ok, err := acquireSelfUpgradeLock()
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok2, err := acquireSelfUpgradeLock(); err != nil || ok2 {
		t.Fatalf("second acquire while held: ok=%v err=%v", ok2, err)
	}
	rel()
	rel2, ok3, err := acquireSelfUpgradeLock()
	if err != nil || !ok3 {
		t.Fatalf("acquire after release: ok=%v err=%v", ok3, err)
	}
	rel2()
	// A lock owned by a dead PID is reclaimed.
	if err := os.WriteFile(selfUpgradeLockPath(), []byte("2147483646\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel3, ok4, err := acquireSelfUpgradeLock()
	if err != nil || !ok4 {
		t.Fatalf("stale reclaim: ok=%v err=%v", ok4, err)
	}
	rel3()
}

func TestSelfUpgradeLockSerializesGoroutines(t *testing.T) {
	for _, initial := range []string{"missing", "stale", "corrupt"} {
		t.Run(initial, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			if initial != "missing" {
				if err := os.MkdirAll(filepath.Dir(selfUpgradeLockPath()), 0o755); err != nil {
					t.Fatal(err)
				}
				data := "not a PID"
				if initial == "stale" {
					data = "2147483646\n"
					if pidAlive(2147483646) {
						t.Skip("test PID is alive")
					}
				}
				if err := os.WriteFile(selfUpgradeLockPath(), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			const contenders = 20
			var attempted sync.WaitGroup
			attempted.Add(contenders)
			releases := make(chan func(), contenders)
			start := make(chan struct{})
			for i := 0; i < contenders; i++ {
				go func() {
					defer attempted.Done()
					<-start
					release, ok, err := acquireSelfUpgradeLock()
					if err != nil {
						t.Errorf("acquire: %v", err)
					}
					if ok {
						releases <- release
					}
				}()
			}
			close(start)
			attempted.Wait()
			close(releases)
			// Keep every successful acquisition held until all attempts finish.
			winners := 0
			for release := range releases {
				winners++
				release()
			}
			if winners != 1 {
				t.Fatalf("%d goroutines acquired the lock while held, want 1", winners)
			}
			release, ok, err := acquireSelfUpgradeLock()
			if err != nil || !ok {
				t.Fatalf("acquire after release: ok=%v err=%v", ok, err)
			}
			release()
		})
	}
}

// phaseHarness drives runSelfUpgradePhase with a counting upgrader.
func phaseUpgrader(t *testing.T, _ *upgradeHarness) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	lg, _ := clog.Open("run")
	prevLog := runLog
	runLog = lg
	t.Cleanup(func() { runLog = prevLog; lg.Close() })
}

func TestSelfUpgradePhaseBranchAdvanceWithoutMerge(t *testing.T) {
	h := newUpgradeHarness(t)
	phaseUpgrader(t, h)
	confirmedMerges.reset()
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": selfUpgradeRepo(t)}}
	if failed := runSelfUpgradePhase(context.Background(), cfg, h.u); len(failed) != 0 {
		t.Fatalf("failed = %v", failed)
	}
	if h.installed() != "NEW" {
		t.Fatalf("branch advance did not upgrade: %v", h.calls)
	}
}

func TestSelfUpgradePhaseMergeTriggersUpgrade(t *testing.T) {
	h := newUpgradeHarness(t)
	phaseUpgrader(t, h)
	confirmedMerges.reset()
	t.Cleanup(confirmedMerges.reset)
	confirmedMerges.note("o/r")
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": selfUpgradeRepo(t)}}
	if failed := runSelfUpgradePhase(context.Background(), cfg, h.u); len(failed) != 0 {
		t.Fatalf("failed = %v", failed)
	}
	if h.installed() != "NEW" {
		t.Fatalf("binary not upgraded")
	}
}

func TestSelfUpgradePhaseIgnoresReposWithoutOptIn(t *testing.T) {
	h := newUpgradeHarness(t)
	phaseUpgrader(t, h)
	confirmedMerges.reset()
	t.Cleanup(confirmedMerges.reset)
	confirmedMerges.note("o/r")
	repo := selfUpgradeRepo(t)
	repo.SelfUpgrade = false
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": repo}}
	runSelfUpgradePhase(context.Background(), cfg, h.u)
	if len(h.calls) != 0 {
		t.Fatalf("upgrade ran without opt-in: %v", h.calls)
	}
}

func TestSelfUpgradePhaseFailureCountsTowardExitStatus(t *testing.T) {
	h := newUpgradeHarness(t)
	phaseUpgrader(t, h)
	h.failOn = "go build"
	confirmedMerges.reset()
	t.Cleanup(confirmedMerges.reset)
	confirmedMerges.note("o/r")
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": selfUpgradeRepo(t)}}
	failed := runSelfUpgradePhase(context.Background(), cfg, h.u)
	if len(failed) != 1 || failed[0].Repo != "o/r" || failed[0].Operator != "self_upgrade" {
		t.Fatalf("failed = %v", failed)
	}
	if err := failedRunsError(failed); err == nil || !strings.Contains(err.Error(), "o/r#0 self_upgrade") {
		t.Fatalf("exit error = %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".clawflow", "logs", "run.log"))
	if !strings.Contains(string(data), "WARN  run/self_upgrade") || !strings.Contains(string(data), "result=failed") {
		t.Errorf("run.log lacks WARN run/self_upgrade: %q", data)
	}
	if h.installed() != "OLD" {
		t.Errorf("binary changed on failure")
	}
}

func TestSelfUpgradePhaseSkipDoesNotFail(t *testing.T) {
	h := newUpgradeHarness(t)
	phaseUpgrader(t, h)
	h.u.Version = testFullSHA[:7]
	confirmedMerges.reset()
	t.Cleanup(confirmedMerges.reset)
	confirmedMerges.note("o/r")
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": selfUpgradeRepo(t)}}
	if failed := runSelfUpgradePhase(context.Background(), cfg, h.u); len(failed) != 0 {
		t.Fatalf("skip counted as failure: %v", failed)
	}
}

func sweepWith(t *testing.T, repoCfg config.Repo, pr vcs.PR) *sweepFake {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := (&config.Config{Repos: map[string]config.Repo{"o/r": {}}}).Save(); err != nil {
		t.Fatal(err)
	}
	confirmedMerges.reset()
	t.Cleanup(confirmedMerges.reset)
	f := &sweepFake{openPRs: []vcs.PR{pr}}
	sweepAutoMergePRs(context.Background(), f, "o/r", repoCfg, 0)
	return f
}

func TestConfirmedMergeIsRecordedForSelfUpgradeRepo(t *testing.T) {
	pr := vcs.PR{Number: 5, Title: "fix", Body: "Closes #4", HeadBranch: "fix/issue-4", BaseBranch: "main"}
	f := sweepWith(t, config.Repo{AutoMerge: true, SelfUpgrade: true, BaseBranch: "main"}, pr)
	if len(f.merged) != 1 {
		t.Fatalf("merged = %v", f.merged)
	}
	if got := confirmedMerges.names(); len(got) != 1 || got[0] != "o/r" {
		t.Fatalf("confirmed merges = %v", got)
	}
}

func TestMergeNotRecordedWithoutSelfUpgradeOrForOtherBase(t *testing.T) {
	pr := vcs.PR{Number: 5, Title: "fix", Body: "Closes #4", HeadBranch: "fix/issue-4", BaseBranch: "main"}
	if sweepWith(t, config.Repo{AutoMerge: true, BaseBranch: "main"}, pr); len(confirmedMerges.names()) != 0 {
		t.Errorf("recorded without self_upgrade: %v", confirmedMerges.names())
	}
	pr.BaseBranch = "release"
	f := sweepWith(t, config.Repo{AutoMerge: true, SelfUpgrade: true, BaseBranch: "main"}, pr)
	if len(f.merged) != 1 || len(confirmedMerges.names()) != 0 {
		t.Errorf("merge into non-base branch recorded: merged=%v names=%v", f.merged, confirmedMerges.names())
	}
}

func TestUnmergedPassRecordsNothing(t *testing.T) {
	// A dirty PR is skipped, so no merge is confirmed and no upgrade can follow.
	t.Setenv("HOME", t.TempDir())
	confirmedMerges.reset()
	f := &sweepFake{openPRs: []vcs.PR{{Number: 5, Title: "fix", Body: "Closes #4", BaseBranch: "main"}}, mergeability: map[int]vcs.MergeStatus{5: vcs.MergeStatusConflict}}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true, SelfUpgrade: true, BaseBranch: "main"}, 0)
	if len(f.merged) != 0 || len(confirmedMerges.names()) != 0 {
		t.Fatalf("merged=%v names=%v", f.merged, confirmedMerges.names())
	}
}

func TestSelfUpgradeSkipsWithoutLocalPath(t *testing.T) {
	h := newUpgradeHarness(t)
	out := h.u.Upgrade(context.Background(), config.Repo{SelfUpgrade: true, BaseBranch: "main"})
	if out.Result != selfUpgradeSkipped || len(h.calls) != 0 {
		t.Fatalf("outcome = %+v calls=%v", out, h.calls)
	}
}
