package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// Self-upgrade checks opted-in source branches after every run pass, including
// advances merged outside ClawFlow. The final step builds a verified descendant
// of the installed commit from the fetched branch and replaces the
// running executable. The user's checkout is never built in, switched or
// modified: the build happens in a temporary detached worktree. Every step
// before the replacement leaves the installed binary untouched, and a failure
// after the replacement restores <path>.prev.

const (
	selfUpgradeTimeout = 30 * time.Minute
)

var fullSHARE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// selfUpgradeResult values logged under run/self_upgrade.
const (
	selfUpgradeUpgraded = "upgraded"
	selfUpgradeSkipped  = "skipped"
	selfUpgradeFailed   = "failed"
)

// selfUpgradeOutcome is the logged summary of one upgrade attempt.
type selfUpgradeOutcome struct {
	From, To string
	Result   string
	Reason   string
}

// mergeLedger records, per pass, the repositories where ClawFlow confirmed a
// merge into the configured base branch and that opted into self-upgrade.
type mergeLedger struct {
	mu    sync.Mutex
	repos map[string]bool
}

var confirmedMerges mergeLedger

func (l *mergeLedger) reset() {
	l.mu.Lock()
	l.repos = nil
	l.mu.Unlock()
}

func (l *mergeLedger) note(repo string) {
	l.mu.Lock()
	if l.repos == nil {
		l.repos = map[string]bool{}
	}
	l.repos[repo] = true
	l.mu.Unlock()
}

func (l *mergeLedger) names() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.repos))
	for r := range l.repos {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// selfUpgrader holds the injectable seams. Production values come from
// newSelfUpgrader; tests replace individual fields.
type selfUpgrader struct {
	// Version is the running binary's version string.
	Version string
	Commit  string
	// Executable returns the resolved path of the running executable.
	Executable func() (string, error)
	// Run executes name with args in dir and returns stdout. Errors carry a
	// bounded stderr tail.
	Run func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
	// StartDetached starts name in its own session without waiting for it.
	StartDetached func(name string, args ...string) error
	// WebRunning reports whether a local `clawflow web` instance is running.
	WebRunning func() bool
	// Lock takes the local upgrade lock; ok=false means another upgrade holds it.
	Lock func() (release func(), ok bool, err error)

	MkdirTemp func(dir, pattern string) (string, error)
	RemoveAll func(path string) error
	Stat      func(path string) (os.FileInfo, error)
	Copy      func(src, dst string, mode os.FileMode) error
	Rename    func(oldpath, newpath string) error
	Remove    func(path string) error
}

func newSelfUpgrader() *selfUpgrader {
	return &selfUpgrader{
		Version: Version,
		Commit:  installedCommit(),
		Executable: func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return resolveBinaryPath(exe), nil
		},
		Run:           runSelfUpgradeCmd,
		StartDetached: startDetached,
		WebRunning:    localWebRunning,
		Lock:          acquireSelfUpgradeLock,
		MkdirTemp:     os.MkdirTemp,
		RemoveAll:     os.RemoveAll,
		Stat:          os.Stat,
		Copy:          copyExecutable,
		Rename:        os.Rename,
		Remove:        os.Remove,
	}
}

// runSelfUpgradeCmd is the production exec seam. git gets the hardened
// non-interactive environment.
func runSelfUpgradeCmd(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	if name == "git" {
		c.Env = hardenedGitEnv(nil)
	}
	c.WaitDelay = 10 * time.Second
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, tail(stderr.String(), 400))
	}
	return stdout.Bytes(), nil
}

// tail returns the last n bytes of s as a single trimmed line.
func tail(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}

// startDetached launches name in a new session (own process group) so that
// stopping the web instance it replaces cannot take this run down with it.
func startDetached(name string, args ...string) error {
	c := exec.Command(name, args...) //nolint:gosec
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

// localWebRunning reports whether the pid file names a live clawflow process.
func localWebRunning() bool {
	info, ok := readWebPid()
	return ok && pidAlive(info.PID) && isClawflowProcess(info.PID)
}

func selfUpgradeLockPath() string {
	return filepath.Join(snapshot.LockDir(), "self-upgrade.lock")
}

// selfUpgradeLockMu serializes create/write/reclaim within this process.
// O_EXCL alone exposes an empty file before its owner PID has been written.
var selfUpgradeLockMu sync.Mutex

// acquireSelfUpgradeLock takes an O_EXCL lockfile holding the owner PID.
// A lock whose owner is dead is reclaimed.
func acquireSelfUpgradeLock() (func(), bool, error) {
	selfUpgradeLockMu.Lock()
	defer selfUpgradeLockMu.Unlock()
	path := selfUpgradeLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := fmt.Fprintf(f, "%d\n", os.Getpid())
			cerr := f.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(path)
				return nil, false, errors.Join(werr, cerr)
			}
			return func() {
				selfUpgradeLockMu.Lock()
				defer selfUpgradeLockMu.Unlock()
				_ = os.Remove(path)
			}, true, nil
		}
		if !os.IsExist(err) {
			return nil, false, err
		}
		data, rerr := os.ReadFile(path)
		pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
		if rerr == nil && perr == nil && pidAlive(pid) {
			return nil, false, nil
		}
		_ = os.Remove(path) // stale or unreadable: reclaim and retry once
	}
	return nil, false, nil
}

// copyExecutable copies src to dst with mode and fsyncs it.
func copyExecutable(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Chmod(dst, mode)
}

// Upgrade verifies ancestry and builds one source upgrade.
// It reports its result instead of returning errors.
func (u *selfUpgrader) Upgrade(ctx context.Context, repoCfg config.Repo) (out selfUpgradeOutcome) {
	out = selfUpgradeOutcome{From: u.Version, Result: selfUpgradeFailed}
	fail := func(format string, a ...any) selfUpgradeOutcome {
		out.Result, out.Reason = selfUpgradeFailed, fmt.Sprintf(format, a...)
		return out
	}
	skip := func(reason string) selfUpgradeOutcome {
		out.Result, out.Reason = selfUpgradeSkipped, reason
		return out
	}

	localPath := expandHomeStr(repoCfg.LocalPath)
	if localPath == "" {
		// Synced configs reach machines with no clone; nothing to build from.
		return skip("repository has no local_path on this machine")
	}
	base := baseBranchOrMain(repoCfg)

	release, ok, err := u.Lock()
	if err != nil {
		return fail("acquire upgrade lock: %v", err)
	}
	if !ok {
		return skip("another upgrade is in progress")
	}
	defer release()

	// The lock may have been held by an upgrade from another process. Read the
	// destination now: this process's build metadata may already be obsolete.
	dest, err := u.Executable()
	if err != nil {
		return fail("locate running executable: %v", err)
	}
	installed, err := u.Run(ctx, filepath.Dir(dest), dest, "version")
	if err != nil {
		return fail("read installed version: %v", err)
	}
	versionLine, metadata, _ := strings.Cut(strings.TrimSpace(string(installed)), "\n")
	installedVersion := strings.TrimSpace(versionLine)
	commit := ""
	for _, field := range strings.Fields(metadata) {
		if value, ok := strings.CutPrefix(field, "commit="); ok {
			if value != "" && !fullSHARE.MatchString(value) {
				return fail("installed commit metadata is invalid")
			}
			commit = value
			break
		}
	}
	// Keep status checks on the running process unchanged; only this locked
	// replacement attempt uses the freshly read destination identity.
	current := *u
	current.Version = installedVersion
	current.Commit = commitForVersion(installedVersion, commit)
	u = &current
	out.From = installedVersion

	// 1. Fetch the base and resolve its SHA without touching refs or the
	// working tree of the user's checkout (FETCH_HEAD only).
	if _, err := u.Run(ctx, localPath, "git", "fetch", "origin", base); err != nil {
		return fail("fetch origin/%s: %v", base, err)
	}
	shaOut, err := u.Run(ctx, localPath, "git", "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return fail("resolve fetched SHA: %v", err)
	}
	sha := strings.TrimSpace(string(shaOut))
	if !fullSHARE.MatchString(sha) {
		return fail("unexpected fetched SHA %q", tail(sha, 80))
	}
	short := sha[:7]
	out.To = short
	if u.Version == short {
		return skip("installed version already equals target")
	}

	distance, err := u.sourceDistance(ctx, localPath, sha)
	if err != nil {
		return fail("%v", err)
	}
	if distance == 0 {
		return skip("installed commit already equals target")
	}
	described, err := u.Run(ctx, localPath, "git", "describe", "--tags", "--always", sha)
	if err != nil {
		return fail("describe target: %v", err)
	}
	version := strings.TrimSpace(string(described))
	if version == "" {
		return fail("empty target version")
	}

	info, err := u.Stat(dest)
	if err != nil {
		return fail("stat running executable: %v", err)
	}
	mode := info.Mode().Perm() | 0o111

	tmp, err := u.MkdirTemp("", "clawflow-selfupgrade-*")
	if err != nil {
		return fail("create temp dir: %v", err)
	}
	wt := filepath.Join(tmp, "src")
	worktreeAdded := false
	defer func() {
		// Step 6: always remove the temporary worktree.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if worktreeAdded {
			if _, err := u.Run(cctx, localPath, "git", "worktree", "remove", "--force", wt); err != nil {
				_, _ = u.Run(cctx, localPath, "git", "worktree", "prune")
			}
		}
		_ = u.RemoveAll(tmp)
	}()

	if _, err := u.Run(ctx, localPath, "git", "worktree", "add", "--detach", wt, sha); err != nil {
		return fail("create worktree at %s: %v", short, err)
	}
	worktreeAdded = true

	// 2. Web bundle (embedded by go:embed all:web/dist) and the binary.
	webDir := filepath.Join(wt, "web")
	if _, err := u.Run(ctx, webDir, "pnpm", "install", "--frozen-lockfile"); err != nil {
		return fail("web install: %v", err)
	}
	if _, err := u.Run(ctx, webDir, "pnpm", "build"); err != nil {
		return fail("web build: %v", err)
	}
	candidate := filepath.Join(tmp, "clawflow-candidate")
	ldflags := sourceBuildFlags(version, sha, base)
	if _, err := u.Run(ctx, wt, "go", "build", "-ldflags", ldflags, "-o", candidate, "./cmd/clawflow"); err != nil {
		return fail("go build: %v", err)
	}

	// 3. Verify the candidate before it gets anywhere near the install path.
	if err := u.verifyVersion(ctx, candidate, version); err != nil {
		return fail("candidate: %v", err)
	}
	checkOut, checkErr := u.Run(ctx, tmp, candidate, "execution", "check", "--json")
	var report execution.Report
	if jerr := json.Unmarshal(checkOut, &report); jerr != nil {
		return fail("candidate execution check: unreadable report (%v, run error: %v)", jerr, checkErr)
	}
	if report.Status != execution.StatusReady {
		return fail("candidate execution check is %q, not ready", report.Status)
	}
	if checkErr != nil {
		return fail("candidate execution check: %v", checkErr)
	}

	// 4. Replace atomically; keep the previous binary as <path>.prev.
	prev := dest + ".prev"
	staged := dest + ".new"
	if err := u.stageCopy(dest, prev, mode); err != nil {
		return fail("keep previous binary: %v", err)
	}
	if err := u.stageCopy(candidate, staged, mode); err != nil {
		return fail("stage candidate: %v", err)
	}
	if err := u.Rename(staged, dest); err != nil {
		_ = u.Remove(staged)
		return fail("replace executable: %v", err)
	}

	// Anything failing from here on restores the previous binary.
	rollback := func(cause error) selfUpgradeOutcome {
		if rerr := u.stageCopy(prev, dest, mode); rerr != nil {
			return fail("%v; rollback FAILED, restore manually from %s: %v", cause, prev, rerr)
		}
		return fail("%v; rolled back to previous binary", cause)
	}
	if err := u.verifyVersion(ctx, dest, version); err != nil {
		return rollback(fmt.Errorf("installed binary: %w", err))
	}

	// 5. Restart a running local web instance, detached from this process.
	if u.WebRunning() {
		if err := u.StartDetached(dest, "web", "restart"); err != nil {
			return rollback(fmt.Errorf("restart web: %w", err))
		}
	}

	out.Result, out.Reason = selfUpgradeUpgraded, ""
	return out
}

// verifyVersion checks that `<bin> version` prints want on its first line.
func (u *selfUpgrader) verifyVersion(ctx context.Context, bin, want string) error {
	vout, err := u.Run(ctx, filepath.Dir(bin), bin, "version")
	if err != nil {
		return fmt.Errorf("version: %w", err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(vout)), "\n")
	if strings.TrimSpace(first) != want {
		return fmt.Errorf("reports version %q, want %q", tail(first, 80), want)
	}
	return nil
}

// stageCopy atomically writes a copy of src at dst: copy to dst+".tmp" in the
// same directory, then rename. A failed copy never disturbs dst.
func (u *selfUpgrader) stageCopy(src, dst string, mode os.FileMode) error {
	tmp := dst + ".tmp"
	if err := u.Copy(src, tmp, mode); err != nil {
		_ = u.Remove(tmp)
		return err
	}
	if err := u.Rename(tmp, dst); err != nil {
		_ = u.Remove(tmp)
		return err
	}
	return nil
}

// runSelfUpgradePhase is the final step of a pass. It upgrades at most once,
// for an opted-in repository with a verified branch advance. Legacy release
// installs retain the confirmed-merge trigger. Failures contribute to exit status.
func runSelfUpgradePhase(ctx context.Context, cfg *config.Config, u *selfUpgrader) []firedKey {
	if cfg == nil || u == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	names := confirmedMerges.names()
	if buildChannel() == "source" {
		for name, repo := range cfg.Repos {
			if repo.SelfUpgrade && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
	}
	for _, repo := range names {
		repoCfg, ok := cfg.Repos[repo]
		if !ok || !repoCfg.SelfUpgrade {
			continue
		}
		if ctx.Err() != nil {
			return nil
		}
		uctx, cancel := context.WithTimeout(ctx, selfUpgradeTimeout)
		if buildChannel() == "source" {
			repoCfg.BaseBranch = trackedBranch()
			_, behind, err := u.SourceStatus(uctx, repoCfg)
			if err != nil || behind == 0 {
				cancel()
				if err != nil {
					runLog.Warn("run/self_upgrade", "repo", repo, "reason", err.Error())
				}
				continue
			}
		}
		res := u.Upgrade(uctx, repoCfg)
		cancel()

		fields := []any{"repo", repo, "from", res.From, "to", res.To, "result", res.Result}
		if res.Reason != "" {
			fields = append(fields, "reason", res.Reason)
		}
		switch res.Result {
		case selfUpgradeFailed:
			runLog.Warn("run/self_upgrade", fields...)
			fmt.Fprintf(os.Stderr, "[%s] ⚠ self-upgrade failed: %s\n", repo, res.Reason)
			return []firedKey{{Repo: repo, Operator: "self_upgrade"}}
		case selfUpgradeSkipped:
			runLog.Info("run/self_upgrade", fields...)
			fmt.Fprintf(os.Stderr, "[%s] · self-upgrade skipped: %s\n", repo, res.Reason)
		default:
			runLog.Info("run/self_upgrade", fields...)
			fmt.Fprintf(os.Stderr, "[%s] ✓ self-upgraded %s → %s\n", repo, res.From, res.To)
			return nil
		}
	}
	return nil
}
