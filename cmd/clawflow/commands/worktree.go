package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/clone"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewWorktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage git worktrees for issue fixes",
	}
	cmd.AddCommand(newWorktreeCreateCmd())
	cmd.AddCommand(newWorktreeRemoveCmd())
	cmd.AddCommand(newWorktreePruneCmd())
	return cmd
}

func newWorktreeCreateCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an isolated git worktree for an issue",
		Example: "  clawflow worktree create --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			repoCfg, ok := cfg.Repos[repo]
			if !ok {
				return fmt.Errorf("repo %q not found in config", repo)
			}

			localPath, err := resolveLocalPath(cfg, repo, repoCfg)
			if err != nil {
				return err
			}

			worktreePath := config.WorktreePath(repo, issue)
			branch := config.BranchName(issue)
			base := repoCfg.BaseBranch
			if base == "" {
				base = "main"
			}

			// Fetch latest base branch first
			if err := runGit(localPath, "fetch", "origin", base); err != nil {
				fmt.Fprintf(os.Stderr, "warn: fetch failed, using cached origin/%s: %v\n", base, err)
			}

			if err := runGit(localPath, "worktree", "add", worktreePath, "-b", branch, "origin/"+base); err != nil {
				return fmt.Errorf("git worktree add failed: %w", err)
			}

			fmt.Println(worktreePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func newWorktreeRemoveCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove the worktree for an issue (cleanup after success or failure)",
		Example: "  clawflow worktree remove --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			repoCfg, ok := cfg.Repos[repo]
			if !ok {
				return fmt.Errorf("repo %q not found in config", repo)
			}

			localPath, err := resolveLocalPath(cfg, repo, repoCfg)
			if err != nil {
				return err
			}

			worktreePath := config.WorktreePath(repo, issue)

			// Remove worktree registration from git (--force handles dirty state)
			if err := runGit(localPath, "worktree", "remove", worktreePath, "--force"); err != nil {
				// If already removed, not an error
				if !strings.Contains(err.Error(), "is not a working tree") {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: git worktree remove: %v\n", err)
				}
			}

			// Delete the branch that was created for this issue
			branch := config.BranchName(issue)
			if err := runGit(localPath, "branch", "-D", branch); err != nil {
				// Not an error if branch doesn't exist
				if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "error: branch") {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: git branch -D %s: %v\n", branch, err)
				}
			}

			// Remove directory if still present
			if _, statErr := os.Stat(worktreePath); statErr == nil {
				if removeErr := os.RemoveAll(worktreePath); removeErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: could not remove dir %s: %v\n", worktreePath, removeErr)
				}
			}

			fmt.Printf("worktree removed: %s\n", worktreePath)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func newWorktreePruneCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove persistent analysis worktrees left over from past runs",
		Long: `Scans ~/.clawflow/worktrees/*/analysis-* and removes analysis worktrees
that are no longer needed. By default every found analysis worktree is removed;
use --dry-run to preview what would be deleted without making any changes.

Analysis worktrees are persistent by design (they are reused across runs for
performance), but they can accumulate over time and remain registered in the
source clone's .git/worktrees/ indefinitely. Run 'clawflow worktree prune'
to clean them up.`,
		Example: `  # Preview what would be removed
  clawflow worktree prune --dry-run

  # Remove all analysis worktrees
  clawflow worktree prune`,
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			wtRoot := filepath.Join(home, ".clawflow", "worktrees")

			slugDirs, err := os.ReadDir(wtRoot)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("no worktrees directory found — nothing to prune")
					return nil
				}
				return fmt.Errorf("read worktrees dir: %w", err)
			}

			found := 0
			removed := 0
			for _, slugEntry := range slugDirs {
				if !slugEntry.IsDir() {
					continue
				}
				slugDir := filepath.Join(wtRoot, slugEntry.Name())
				entries, err := os.ReadDir(slugDir)
				if err != nil {
					continue
				}
				for _, entry := range entries {
					if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "analysis-") {
						continue
					}
					wtPath := filepath.Join(slugDir, entry.Name())
					found++
					if dryRun {
						fmt.Printf("would remove: %s\n", wtPath)
						continue
					}
					if removeAnalysisWorktree(wtPath) {
						fmt.Printf("removed: %s\n", wtPath)
						removed++
					} else {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: could not remove %s\n", wtPath)
					}
				}
			}

			if found == 0 {
				fmt.Println("no analysis worktrees found")
				return nil
			}
			if dryRun {
				fmt.Printf("\n%d analysis worktree(s) would be removed (re-run without --dry-run to apply)\n", found)
			} else {
				fmt.Printf("\n%d/%d analysis worktree(s) removed\n", removed, found)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview removals without making any changes")
	return cmd
}

// resolveLocalPath is a thin shim around clone.EnsureLocalClone, kept
// for backwards compatibility with the rest of the commands package.
// `git clone` progress is teed to stderr so users running interactive
// `clawflow run` see the clone happen.
func resolveLocalPath(cfg *config.Config, ownerRepo string, repoCfg config.Repo) (string, error) {
	creds, _ := config.LoadCredentials()
	var token *clone.Token
	if creds != nil {
		token = &clone.Token{GHToken: creds.GHToken, GitLabToken: creds.GitLabToken}
	}
	return clone.EnsureLocalClone(cfg, ownerRepo, repoCfg, os.Stderr, token)
}

func expandHomeStr(path string) string {
	return clone.ExpandHome(path)
}

func runGit(dir string, args ...string) error {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}
