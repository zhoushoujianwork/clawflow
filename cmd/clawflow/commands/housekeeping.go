package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewHousekeepingCmd() *cobra.Command {
	var dry bool
	cmd := &cobra.Command{Use: "housekeeping", Short: "清理闲置的 ClawFlow 临时目录、已结束工作树及验收构建缓存", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(runHousekeeping(cmd.Context(), cfg, dry))
		}}
	cmd.Flags().BoolVar(&dry, "dry-run", false, "预览可清理路径，不删除文件")
	return cmd
}

func housekeepingResultPath() string { return filepath.Join(snapshot.DataDir(), "housekeeping.json") }

func runHousekeeping(ctx context.Context, cfg *config.Config, dry bool) (r housekeeping.Result) {
	r = housekeeping.Result{At: time.Now().UTC(), DryRun: dry, Paths: []string{}}
	defer func() {
		if !dry {
			if err := os.MkdirAll(snapshot.DataDir(), 0755); err == nil {
				data, _ := json.Marshal(r)
				tmp, err := os.CreateTemp(snapshot.DataDir(), ".housekeeping-")
				if err == nil {
					name := tmp.Name()
					_, err = tmp.Write(data)
					closeErr := tmp.Close()
					if err == nil && closeErr == nil {
						err = os.Rename(name, housekeepingResultPath())
					}
					_ = os.Remove(name)
				}
				if err != nil {
					r.Errors = append(r.Errors, "cannot save housekeeping result")
				}
			}
		}
	}()

	limits := cfg.Settings.Housekeeping.Limits()
	if limits.Disabled {
		r.Skipped = "disabled"
		return r
	}
	release, err := housekeeping.Gate(ctx, os.TempDir(), true)
	if err != nil {
		r.Skipped = "active_tasks_or_housekeeping"
		return r
	}
	defer release()
	// Legacy binaries do not hold the maintenance gate. Retain all temp/cache
	// data whenever a legacy issue lock is held or its state is unreadable.
	legacyActive := false
	err = filepath.WalkDir(snapshot.LockDir(), func(p string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "issue-") && filelock.Held(p) {
			legacyActive = true
		}
		return nil
	})
	if err != nil {
		legacyActive = true
	}
	if !legacyActive {
		r.Temps(os.TempDir(), time.Duration(limits.TempMaxAgeHours)*time.Hour, func(string) bool { return false })
		r.ReviewTemps(os.TempDir(), filepath.Join(os.TempDir(), "clawflow-review-go", "tmp"), time.Duration(limits.TempMaxAgeHours)*time.Hour)
		cache := filepath.Join(os.TempDir(), "clawflow-review-go", "build")
		if housekeeping.SafeDir(os.TempDir(), cache) {
			r.Cache(os.TempDir(), cache, limits.ReviewCacheMaxBytes)
		}
	}
	// Database/query failures retain worktrees. Active includes queued work as
	// well as held leases, protecting the window before a worker takes its lock.
	active, err := activeCoordinatorIssues(ctx)
	if err != nil {
		r.Errors = append(r.Errors, "worktrees: active task state unavailable")
	} else {
		for repo, rc := range cfg.Repos {
			if rc.LocalPath == "" || !housekeeping.SafeDir(snapshot.StateDir(), filepath.Join(snapshot.StateDir(), "worktrees", config.RepoSlugFS(repo))) {
				continue
			}
			client, err := newVCSClient(rc)
			if err != nil {
				r.Errors = append(r.Errors, "worktrees: VCS unavailable for "+repo)
				continue
			}
			if c, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
				c.SetRequestContext(ctx)
			}
			prs, err := client.ListPRs(repo, "all")
			if err != nil {
				r.Errors = append(r.Errors, "worktrees: PR state unavailable for "+repo)
				continue
			}
			pruneFinishedWorktrees(ctx, &r, snapshot.StateDir(), repo, expandHomeStr(rc.LocalPath), prs, active[repo])
		}
	}

	return r
}

func pruneFinishedWorktrees(ctx context.Context, r *housekeeping.Result, state, repo, local string, prs []vcs.PR, active map[int]bool) {
	root := filepath.Join(state, "worktrees")
	parent := filepath.Join(root, config.RepoSlugFS(repo))
	if !housekeeping.SafeDir(state, parent) {
		return
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		m := issueWorktreeDirRE.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		issue, err := strconv.Atoi(m[1])
		if err != nil || active[issue] {
			continue
		}
		p := filepath.Join(parent, e.Name())
		if !housekeeping.SafeDir(state, p) {
			continue
		}
		// Acquire, rather than probe, the execution lock through the entire check
		// and removal, closing the worker-start race.
		lock, err := filelock.Try(snapshot.LockPath(repo, issue), snapshot.LockInfo{PID: os.Getpid(), Repo: repo, Issue: issue, Operator: "housekeeping"})
		if err != nil {
			continue
		}
		func() {
			defer filelock.Release(lock)
			gitFile, err := os.Lstat(filepath.Join(p, ".git"))
			if err != nil || !gitFile.Mode().IsRegular() {
				return
			}
			branchOut, err := exec.CommandContext(ctx, "git", "-C", p, "symbolic-ref", "--short", "HEAD").Output()
			if err != nil {
				return
			}
			branch := strings.TrimSpace(string(branchOut))
			finished, open := false, false
			for _, pr := range prs {
				if pr.HeadBranch != branch || (pr.HeadRepo != "" && pr.HeadRepo != repo) {
					continue
				}
				if pr.State == "open" || pr.State == "opened" {
					open = true
				}
				if pr.IsMerged() || pr.State == "closed" {
					finished = true
				}
			}
			if !finished || open {
				return
			}
			status, err := exec.CommandContext(ctx, "git", "-C", p, "status", "--porcelain").Output()
			if err != nil || len(status) != 0 {
				return
			}
			n, err := housekeeping.Size(p)
			if err != nil {
				return
			}
			if !r.DryRun {
				// Never force: git also protects locked worktrees and submodules.
				if err := exec.CommandContext(ctx, "git", "-C", local, "worktree", "remove", p).Run(); err != nil {
					r.Errors = append(r.Errors, fmt.Sprintf("worktree removal failed: %s", p))
					return
				}
			}
			r.Paths = append(r.Paths, p)
			r.Bytes += n
		}()
	}
}
