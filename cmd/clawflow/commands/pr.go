package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewPRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Manage pull requests / merge requests",
	}
	cmd.AddCommand(newPRCreateCmd())
	cmd.AddCommand(newPRViewCmd())
	cmd.AddCommand(newPRListCmd())
	cmd.AddCommand(newPRCommentCmd())
	cmd.AddCommand(newPRCIWaitCmd())
	cmd.AddCommand(newPRMergeCmd())
	cmd.AddCommand(newPRCloseCmd())
	cmd.AddCommand(newPRRebaseCmd())
	return cmd
}

func newPRCreateCmd() *cobra.Command {
	var repo, title, body, head, base string

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a pull request / merge request",
		Example: "  clawflow pr create --repo owner/repo --title \"fix: bug\" --head fix/issue-7 --base main --body \"Fixes #7\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			pr, err := client.CreatePR(repo, vcs.PRCreateOpts{
				Title: title,
				Body:  body,
				Head:  head,
				Base:  base,
			})
			if err != nil {
				return err
			}
			fmt.Printf("created PR #%d: %s\n%s\n", pr.Number, pr.Title, pr.URL)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().StringVar(&title, "title", "", "PR title (required)")
	cmd.Flags().StringVar(&body, "body", "", "PR body")
	cmd.Flags().StringVar(&head, "head", "", "source branch (required)")
	cmd.Flags().StringVar(&base, "base", "", "target branch (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("head")
	_ = cmd.MarkFlagRequired("base")
	return cmd
}

func newPRViewCmd() *cobra.Command {
	var repo string
	var number int

	cmd := &cobra.Command{
		Use:     "view",
		Short:   "View a pull request / merge request",
		Example: "  clawflow pr view --repo owner/repo --pr 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			pr, err := client.GetPR(repo, number)
			if err != nil {
				return err
			}
			fmt.Printf("#%d  [%s]  %s\n", pr.Number, pr.State, pr.Title)
			fmt.Printf("branch: %s\n", pr.HeadBranch)
			fmt.Printf("url:    %s\n", pr.URL)
			if pr.MergedAt != "" {
				fmt.Printf("merged: %s\n", pr.MergedAt)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&number, "pr", 0, "PR number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}

func newPRListCmd() *cobra.Command {
	var repo, state string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List pull requests / merge requests",
		Aliases: []string{"ls"},
		Example: "  clawflow pr list --repo owner/repo\n  clawflow pr list --repo owner/repo --state merged\n  clawflow pr list --repo owner/repo --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			var prs []vcs.PR
			if state == "open" {
				prs, err = client.ListOpenPRs(repo)
			} else {
				prs, err = client.ListPRs(repo, state)
			}
			if err != nil {
				return err
			}
			if len(prs) == 0 {
				if jsonOutput {
					fmt.Println("[]")
				} else {
					fmt.Printf("no PRs in %s\n", repo)
				}
				return nil
			}
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(prs)
			}
			fmt.Printf("%-6s  %-8s  %-30s  %s\n", "NUMBER", "STATE", "BRANCH", "TITLE")
			for _, p := range prs {
				fmt.Printf("#%-5d  %-8s  %-30s  %s\n", p.Number, p.State, p.HeadBranch, p.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().StringVar(&state, "state", "open", "PR state: open, closed, merged, all")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output full PRs as JSON")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func newPRCommentCmd() *cobra.Command {
	var repo, body string
	var number int

	cmd := &cobra.Command{
		Use:     "comment",
		Short:   "Post a comment on a pull request",
		Example: "  clawflow pr comment --repo owner/repo --pr 7 --body \"CI failed: ...\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.PostPRComment(repo, number, body); err != nil {
				return err
			}
			fmt.Printf("commented on %s PR #%d\n", repo, number)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&number, "pr", 0, "PR number (required)")
	cmd.Flags().StringVar(&body, "body", "", "comment body (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

func newPRCIWaitCmd() *cobra.Command {
	var repo string
	var number int
	var timeout int

	cmd := &cobra.Command{
		Use:     "ci-wait",
		Short:   "Wait for CI checks on a pull request to complete",
		Example: "  clawflow pr ci-wait --repo owner/repo --pr 7 --timeout 600",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			deadline := time.Now().Add(time.Duration(timeout) * time.Second)
			fmt.Printf("waiting for CI on %s PR #%d (timeout %ds)...\n", repo, number, timeout)
			for {
				status, err := client.GetCIStatus(repo, number)
				if err != nil {
					return err
				}
				switch status {
				case vcs.CIStatusSuccess:
					fmt.Println("CI passed")
					return nil
				case vcs.CIStatusFailure:
					fmt.Println("CI failed")
					return fmt.Errorf("CI checks failed")
				case vcs.CIStatusNone:
					fmt.Println("no CI checks configured")
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("timed out waiting for CI after %ds", timeout)
				}
				time.Sleep(15 * time.Second)
				fmt.Print(".")
			}
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&number, "pr", 0, "PR number (required)")
	cmd.Flags().IntVar(&timeout, "timeout", 600, "max wait time in seconds")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}

func newPRMergeCmd() *cobra.Command {
	var repo string
	var number int
	var noDeleteBranch bool

	cmd := &cobra.Command{
		Use:     "merge",
		Short:   "Merge a pull request via the VCS API",
		Example: "  clawflow pr merge --repo owner/repo --pr 7\n  clawflow pr merge --repo owner/repo --pr 7 --no-delete-branch",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.MergePR(repo, number); err != nil {
				return err
			}
			fmt.Printf("merged %s PR #%d\n", repo, number)

			// Default behavior: delete the remote source branch after a
			// successful merge, matching GitHub/GitLab's "Delete source
			// branch" UI default. Pass --no-delete-branch to keep it.
			// Mirrors the cleanup path in `clawflow run` auto-merge so
			// manual and automatic merges behave consistently. Failures
			// are non-fatal: the merge already landed, branch deletion
			// is just housekeeping.
			if !noDeleteBranch {
				baseBranch := "main"
				if cfg, err := config.Load(); err == nil {
					if repoCfg, ok := cfg.Repos[repo]; ok && repoCfg.BaseBranch != "" {
						baseBranch = repoCfg.BaseBranch
					}
				}
				if pr, err := client.GetPR(repo, number); err != nil {
					fmt.Fprintf(os.Stderr, "⚠ branch cleanup: lookup PR failed: %v\n", err)
				} else if head := pr.HeadBranch; head != "" && head != baseBranch {
					if err := client.DeleteBranch(repo, head); err != nil {
						fmt.Fprintf(os.Stderr, "⚠ branch cleanup: delete %s failed: %v\n", head, err)
					} else {
						fmt.Printf("deleted branch %s\n", head)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&number, "pr", 0, "PR number (required)")
	cmd.Flags().BoolVar(&noDeleteBranch, "no-delete-branch", false, "keep the source branch after merging (default: delete)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}

func newPRCloseCmd() *cobra.Command {
	var repo string
	var number int

	cmd := &cobra.Command{
		Use:     "close",
		Short:   "Close a pull request / merge request without merging",
		Example: "  clawflow pr close --repo owner/repo --pr 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, repo, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.ClosePR(repo, number); err != nil {
				return err
			}
			fmt.Printf("closed %s PR #%d\n", repo, number)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&number, "pr", 0, "PR number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}

func newPRRebaseCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:     "rebase",
		Short:   "Rebase the issue branch onto base branch and force-push",
		Example: "  clawflow pr rebase --repo owner/repo --issue 7",
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
			base := repoCfg.BaseBranch
			if base == "" {
				base = "main"
			}
			branch := config.BranchName(issue)

			// Fetch latest base
			if err := runGit(localPath, "fetch", "origin", base); err != nil {
				return fmt.Errorf("fetch failed: %w", err)
			}

			// Rebase branch onto origin/base
			worktreePath := config.WorktreePath(repo, issue)
			if err := runGit(worktreePath, "rebase", "origin/"+base); err != nil {
				// Abort rebase on failure so worktree is clean
				_ = runGit(worktreePath, "rebase", "--abort")
				return fmt.Errorf("rebase failed (conflicts): %w", err)
			}

			// Force-push
			if err := runGit(worktreePath, "push", "origin", branch, "--force-with-lease"); err != nil {
				return fmt.Errorf("force-push failed: %w", err)
			}
			fmt.Printf("rebased and pushed %s onto %s\n", branch, base)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}
