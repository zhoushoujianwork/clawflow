package commands

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
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
	return cmd
}

func newPRCreateCmd() *cobra.Command {
	var repo, title, body, head, base string

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a pull request / merge request",
		Example: "  clawflow pr create --repo owner/repo --title \"fix: bug\" --head fix/issue-7 --base main --body \"Fixes #7\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
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
			client, _, err := newVCSClientForRepo(repo)
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

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List pull requests / merge requests",
		Aliases: []string{"ls"},
		Example: "  clawflow pr list --repo owner/repo\n  clawflow pr list --repo owner/repo --state merged",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
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
				fmt.Printf("no PRs in %s\n", repo)
				return nil
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
			client, _, err := newVCSClientForRepo(repo)
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
			client, _, err := newVCSClientForRepo(repo)
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
