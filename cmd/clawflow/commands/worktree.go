package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewWorktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Manage git worktrees for issue fixes",
	}
	cmd.AddCommand(newWorktreeCreateCmd())
	cmd.AddCommand(newWorktreeRemoveCmd())
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

			localPath, err := resolveLocalPath(repo, repoCfg.LocalPath)
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

			localPath, err := resolveLocalPath(repo, repoCfg.LocalPath)
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

// resolveLocalPath returns the local repo path from config or auto-discovery.
// If the path doesn't exist, it clones the repo automatically.
func resolveLocalPath(ownerRepo, configured string) (string, error) {
	if configured != "" {
		expanded := expandHomeStr(configured)
		if _, err := os.Stat(expanded); err == nil {
			return expanded, nil
		}
		// Configured path doesn't exist — clone there
		fmt.Fprintf(os.Stderr, "local path %q not found, cloning %s ...\n", expanded, ownerRepo)
		if err := cloneRepo(ownerRepo, expanded); err != nil {
			return "", fmt.Errorf("auto-clone failed: %w", err)
		}
		return expanded, nil
	}

	// Fallback: search ~/github/<repo>
	parts := strings.SplitN(ownerRepo, "/", 2)
	repoName := parts[len(parts)-1]
	home, _ := os.UserHomeDir()
	candidate := filepath.Join(home, "github", repoName)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}

	// Not found anywhere — clone to ~/github/<repo>
	fmt.Fprintf(os.Stderr, "local clone not found, cloning %s to %s ...\n", ownerRepo, candidate)
	if err := cloneRepo(ownerRepo, candidate); err != nil {
		return "", fmt.Errorf("auto-clone failed: %w", err)
	}
	return candidate, nil
}

func cloneRepo(ownerRepo, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	url := "https://github.com/" + ownerRepo + ".git"
	c := exec.Command("git", "clone", url, dest)
	c.Stdout = os.Stderr // progress to stderr
	c.Stderr = os.Stderr
	return c.Run()
}

func expandHomeStr(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
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
