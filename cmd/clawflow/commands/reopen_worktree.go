package commands

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func setupReopenWorktree(cfg config.Repo, repo string, number int, started time.Time, override string, r *reopenContext) (worktreeResult, func(), error) {
	return setupReopenWorktreeContext(context.Background(), cfg, repo, number, started, override, r)
}
func setupReopenWorktreeContext(ctx context.Context, cfg config.Repo, repo string, number int, started time.Time, override string, r *reopenContext) (worktreeResult, func(), error) {
	base := override
	if base == "" {
		base = cfg.BaseBranch
	}
	if base == "" {
		base = "main"
	}
	// Unlike an ordinary first attempt, a new cycle must not use a stale base.
	if out, err := runGitWithRetryOutputContext(ctx, func() *exec.Cmd { return exec.CommandContext(ctx, "git", "-C", cfg.LocalPath, "fetch", "origin", base) }); err != nil {
		return worktreeResult{}, nil, fmt.Errorf("reopen requires fresh base %s: %w: %s", base, err, out)
	}
	wt, cleanup, err := setupWorktree(ctx, cfg, cycleWorktreeRepo(repo, r), number, started, override)
	if err != nil {
		return wt, cleanup, err
	}
	if wt.Resumed {
		if wt.BranchName != r.branch(number) && wt.BranchName != "HEAD" && wt.BranchName != "" {
			return worktreeResult{}, nil, fmt.Errorf("unexpected branch in reopen cycle: %s", wt.BranchName)
		}
		return wt, cleanup, nil
	}
	// If a clean interrupted cycle left its branch, resume only that branch.
	branch := r.branch(number)
	if err := exec.CommandContext(ctx, "git", "-C", cfg.LocalPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run(); err == nil {
		if out, err := exec.CommandContext(ctx, "git", "-C", wt.Path, "checkout", branch).CombinedOutput(); err != nil {
			return wt, cleanup, fmt.Errorf("resume cycle branch: %w: %s", err, out)
		}
		wt.Resumed = true
		wt.BranchName = branch
	}
	return wt, cleanup, nil
}
