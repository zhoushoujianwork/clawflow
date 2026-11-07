package commands

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// localWorkerStateSubdir is the state directory of the in-process local worker,
// relative to the machine state root. Its worktrees are ClawFlow-owned too.
const localWorkerStateSubdir = "local-worker"

// worktreeRoots lists the state roots whose worktrees/ directory ClawFlow owns.
func worktreeRoots(state string) []string {
	return []string{state, filepath.Join(state, localWorkerStateSubdir)}
}

// worktreeReclaim removes idle, clean issue worktrees whose HEAD is already in
// the default branch, and prunes git records of ClawFlow worktrees whose
// directory is gone. It never forces removal, never deletes branches and never
// touches worktrees outside the given state roots.
type worktreeReclaim struct {
	ctx   context.Context
	r     *housekeeping.Result
	roots []string
	grace time.Duration
	now   time.Time
	log   *clog.Logger
	// repos maps the filesystem slug (owner__repo) to its configuration.
	repos map[string]config.Repo
	// active is read lazily, only once a worktree passes the cheap checks.
	active    func(context.Context) (map[string]map[int]bool, error)
	activeSet map[string]map[int]bool // keyed by filesystem slug
	activeErr error
	activeOK  bool

	seen    map[string]bool // paths already reported in r.Paths
	commons map[string]bool // git common dirs to prune records from
}

func reclaimIssueWorktrees(ctx context.Context, r *housekeeping.Result, cfg *config.Config, roots []string, grace time.Duration, lg *clog.Logger, active func(context.Context) (map[string]map[int]bool, error)) {
	w := &worktreeReclaim{ctx: ctx, r: r, roots: roots, grace: grace, now: r.At, log: lg, active: active,
		repos: map[string]config.Repo{}, seen: map[string]bool{}, commons: map[string]bool{}}
	if w.now.IsZero() {
		w.now = time.Now()
	}
	for name, rc := range cfg.Repos {
		w.repos[config.RepoSlugFS(name)] = rc
	}
	stats := r.WorktreeStats()
	for _, p := range r.Paths {
		w.seen[p] = true
	}
	for _, root := range roots {
		w.scanRoot(root)
	}
	// Configured clones may hold records for worktrees whose directory is gone
	// even when no directory is left to discover them from.
	for _, rc := range cfg.Repos {
		if rc.LocalPath == "" {
			continue
		}
		if common := gitCommonDir(ctx, expandHomeStr(rc.LocalPath)); common != "" {
			w.commons[common] = true
		}
	}
	commons := make([]string, 0, len(w.commons))
	for c := range w.commons {
		commons = append(commons, c)
	}
	sort.Strings(commons)
	for _, c := range commons {
		if ctx.Err() != nil {
			break
		}
		w.pruneRecords(c)
	}
	w.log.Info("housekeeping/worktrees", "dry_run", r.DryRun, "removed", stats.Removed, "pruned_records", stats.Pruned,
		"skipped", stats.Skipped, "skipped_by", formatSkipReasons(stats.SkippedBy), "grace", w.grace.String())
}

func formatSkipReasons(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + ":" + strconv.Itoa(m[k])
	}
	return strings.Join(parts, ",")
}

func (w *worktreeReclaim) skip(path, reason string) {
	s := w.r.WorktreeStats()
	s.Skipped++
	if s.SkippedBy == nil {
		s.SkippedBy = map[string]int{}
	}
	s.SkippedBy[reason]++
	w.log.Info("housekeeping/worktree_kept", "path", path, "reason", reason)
}

func (w *worktreeReclaim) scanRoot(root string) {
	wtRoot := filepath.Join(root, "worktrees")
	slugs, err := os.ReadDir(wtRoot)
	if err != nil {
		return
	}
	for _, s := range slugs {
		slugDir := filepath.Join(wtRoot, s.Name())
		if !s.IsDir() || !housekeeping.SafeDir(root, slugDir) {
			continue
		}
		entries, err := os.ReadDir(slugDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if w.ctx.Err() != nil {
				return
			}
			m := issueWorktreeDirRE.FindStringSubmatch(e.Name())
			if m == nil || !e.IsDir() {
				continue
			}
			issue, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			repoSlug := reopenSlugRE.ReplaceAllString(s.Name(), "$1")
			w.consider(root, repoSlug, filepath.Join(slugDir, e.Name()), issue)
		}
	}
}

// worktreeAdminDir reads the .git pointer of a linked worktree. It returns the
// per-worktree admin dir and the repository's common git dir, or "" when the
// directory is not a linked worktree.
func worktreeAdminDir(p string) (admin, common string) {
	info, err := os.Lstat(filepath.Join(p, ".git"))
	if err != nil || !info.Mode().IsRegular() {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join(p, ".git"))
	if err != nil {
		return "", ""
	}
	after, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", ""
	}
	admin = filepath.Clean(strings.TrimSpace(after))
	if filepath.Base(filepath.Dir(admin)) != "worktrees" {
		return "", ""
	}
	return admin, filepath.Dir(filepath.Dir(admin))
}

// lastWorktreeActivity is the newest modification time of the checkout and of
// the git files that change when anything is checked out, committed or staged.
func lastWorktreeActivity(p, admin string) time.Time {
	var newest time.Time
	for _, f := range []string{p, filepath.Join(p, ".git"), admin, filepath.Join(admin, "HEAD"), filepath.Join(admin, "index"), filepath.Join(admin, "logs", "HEAD")} {
		if info, err := os.Stat(f); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

func (w *worktreeReclaim) loadActive() bool {
	if !w.activeOK {
		w.activeOK = true
		if w.active == nil {
			w.activeErr = errors.New("no active task source")
		} else {
			var byRepo map[string]map[int]bool
			byRepo, w.activeErr = w.active(w.ctx)
			w.activeSet = map[string]map[int]bool{}
			for repo, issues := range byRepo {
				w.activeSet[config.RepoSlugFS(repo)] = issues
			}
		}
		if msg := "worktrees: active task state unavailable"; w.activeErr != nil && !slices.Contains(w.r.Errors, msg) {
			w.r.Errors = append(w.r.Errors, msg)
		}
	}
	return w.activeErr == nil
}

func (w *worktreeReclaim) consider(root, slug, p string, issue int) {
	if !housekeeping.SafeDir(root, p) {
		return
	}
	admin, common := worktreeAdminDir(p)
	if admin == "" {
		w.skip(p, "not_worktree")
		return
	}
	w.commons[common] = true
	if w.now.Sub(lastWorktreeActivity(p, admin)) < w.grace {
		w.skip(p, "within_grace")
		return
	}
	if _, err := os.Lstat(filepath.Join(admin, "locked")); err == nil {
		w.skip(p, "worktree_locked")
		return
	}
	if !w.loadActive() {
		w.skip(p, "active_state_unavailable")
		return
	}
	if w.activeSet[slug][issue] {
		w.skip(p, "active_task")
		return
	}
	// Acquire, rather than probe, the execution lock through the whole check and
	// removal so a worker cannot start on this worktree in between.
	lock, err := filelock.Try(snapshot.LockPathIn(root, slug, issue), snapshot.LockInfo{PID: os.Getpid(), Repo: slug, Issue: issue, Operator: "housekeeping"})
	if err != nil {
		w.skip(p, "issue_locked")
		return
	}
	defer filelock.Release(lock)

	// --no-optional-locks keeps the status probe from refreshing the index and
	// therefore from renewing the worktree's own activity time.
	status, err := exec.CommandContext(w.ctx, "git", "--no-optional-locks", "-C", p, "status", "--porcelain").Output()
	if err != nil {
		w.skip(p, "git_error")
		return
	}
	if len(status) != 0 {
		w.skip(p, "dirty")
		return
	}
	ref := w.defaultBranchRef(common, w.repos[slug].BaseBranch)
	if ref == "" {
		w.skip(p, "no_default_branch")
		return
	}
	switch err := exec.CommandContext(w.ctx, "git", "-C", p, "merge-base", "--is-ancestor", "HEAD", ref).Run(); {
	case err == nil:
	case exitCode(err) == 1:
		w.skip(p, "unmerged")
		return
	default:
		w.skip(p, "git_error")
		return
	}
	n, _ := housekeeping.Size(p)
	if !w.r.DryRun {
		// Never force: git also protects locked worktrees and submodules. The
		// branch, if any, is left alone.
		if out, err := exec.CommandContext(w.ctx, "git", "-C", common, "worktree", "remove", p).CombinedOutput(); err != nil {
			w.r.Errors = append(w.r.Errors, "worktree removal failed: "+p)
			w.skip(p, "remove_failed")
			w.log.Warn("housekeeping/worktree_remove_failed", "path", p, "err", strings.TrimSpace(string(out)))
			return
		}
	}
	if !w.seen[p] {
		w.seen[p] = true
		w.r.Paths = append(w.r.Paths, p)
		w.r.Bytes += n
	}
	w.r.WorktreeStats().Removed++
	w.log.Info("housekeeping/worktree_removed", "path", p, "repo", slug, "issue", issue, "bytes", n, "dry_run", w.r.DryRun)
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// defaultBranchRef resolves the ref HEAD must be contained in: the configured
// base branch when set, otherwise origin's default branch.
func (w *worktreeReclaim) defaultBranchRef(common, base string) string {
	candidates := []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master"}
	if base != "" {
		candidates = []string{"refs/remotes/origin/" + base, "refs/heads/" + base}
	}
	for _, ref := range candidates {
		if exec.CommandContext(w.ctx, "git", "-C", common, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run() == nil {
			return ref
		}
	}
	return ""
}

// gitCommonDir returns the absolute common git dir of the repository at dir.
func gitCommonDir(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type staleRecord struct {
	path string // the worktree directory that no longer exists
	ours bool
}

// underRoots reports whether p is inside one of the ClawFlow worktrees dirs.
// The state root is canonicalized (not the worktrees dir, which may be gone) so
// a /var vs /private/var alias cannot make an owned path look foreign.
func (w *worktreeReclaim) underRoots(p string) bool {
	p = filepath.Clean(p)
	for _, root := range w.roots {
		bases := []string{filepath.Join(root, "worktrees")}
		if canonical, err := filepath.EvalSymlinks(root); err == nil {
			bases = append(bases, filepath.Join(canonical, "worktrees"))
		}
		for _, b := range bases {
			if strings.HasPrefix(p, b+string(os.PathSeparator)) {
				return true
			}
		}
	}
	return false
}

// reopenSlugRE matches the directory a reopened-issue cycle uses
// (<owner__repo>__reopen-<token>). Locks and tasks use the real repository.
var reopenSlugRE = regexp.MustCompile(`^(.+)__reopen-[0-9a-f]{24}$`)

// staleRecords lists the records git would prune now: unlocked, directory
// missing and idle for longer than the grace period (git compares the admin
// index time). Unreadable records count as foreign so they are never touched.
func (w *worktreeReclaim) staleRecords(common string) (ours []staleRecord, foreign bool) {
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil {
		return nil, false
	}
	cutoff := w.now.Add(-w.grace)
	for _, e := range entries {
		admin := filepath.Join(common, "worktrees", e.Name())
		if !e.IsDir() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(admin, "locked")); err == nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil {
			foreign = true
			continue
		}
		gitFile := strings.TrimSpace(string(raw))
		if _, err := os.Lstat(gitFile); !os.IsNotExist(err) {
			continue // still present, or unknown: keep
		}
		if idx, err := os.Stat(filepath.Join(admin, "index")); err == nil && idx.ModTime().After(cutoff) {
			continue
		}
		wt := filepath.Dir(gitFile)
		if w.underRoots(wt) {
			ours = append(ours, staleRecord{path: wt, ours: true})
		} else {
			foreign = true
		}
	}
	return ours, foreign
}

// pruneRecords runs `git worktree prune` for records of ClawFlow worktrees
// whose directory is gone. git prunes every stale record of a repository, so
// when a stale record outside the ClawFlow data dirs exists the ClawFlow ones
// are removed one by one instead and the outside record is left untouched.
func (w *worktreeReclaim) pruneRecords(common string) {
	ours, foreign := w.staleRecords(common)
	if len(ours) == 0 {
		return
	}
	stats := w.r.WorktreeStats()
	expire := "--expire=" + strconv.FormatInt(int64(w.grace/time.Second), 10) + ".seconds.ago"
	if !foreign {
		args := []string{"-C", common, "worktree", "prune", expire}
		if w.r.DryRun {
			args = append(args, "--dry-run")
		}
		if out, err := exec.CommandContext(w.ctx, "git", args...).CombinedOutput(); err != nil {
			w.r.Errors = append(w.r.Errors, "worktree prune failed: "+common)
			w.log.Warn("housekeeping/worktree_prune_failed", "git_dir", common, "err", strings.TrimSpace(string(out)))
			return
		}
		for _, rec := range ours {
			stats.Pruned++
			w.log.Info("housekeeping/worktree_record_pruned", "path", rec.path, "dry_run", w.r.DryRun)
		}
		return
	}
	for _, rec := range ours {
		if !w.r.DryRun {
			if out, err := exec.CommandContext(w.ctx, "git", "-C", common, "worktree", "remove", rec.path).CombinedOutput(); err != nil {
				w.r.Errors = append(w.r.Errors, "worktree record removal failed: "+rec.path)
				w.log.Warn("housekeeping/worktree_prune_failed", "path", rec.path, "err", strings.TrimSpace(string(out)))
				continue
			}
		}
		stats.Pruned++
		w.log.Info("housekeeping/worktree_record_pruned", "path", rec.path, "dry_run", w.r.DryRun, "mode", "single")
	}
}
