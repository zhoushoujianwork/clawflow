package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Manage issues",
	}
	cmd.AddCommand(newIssueCreateCmd())
	cmd.AddCommand(newIssueListCmd())
	cmd.AddCommand(newIssueCommentCmd())
	cmd.AddCommand(newIssueCloseCmd())
	return cmd
}

func newIssueCreateCmd() *cobra.Command {
	var repo, title, body string

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create an issue in a repository",
		Example: "  clawflow issue create --repo owner/repo --title \"bug: something broken\" --body \"details...\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			issue, err := client.CreateIssue(repo, title, body)
			if err != nil {
				return err
			}
			fmt.Printf("created issue #%d: %s\n", issue.Number, issue.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().StringVar(&title, "title", "", "issue title (required)")
	cmd.Flags().StringVar(&body, "body", "", "issue body")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newIssueListCmd() *cobra.Command {
	var repo, state string
	var labels []string

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List issues in a repository",
		Aliases: []string{"ls"},
		Example: "  clawflow issue list --repo owner/repo\n  clawflow issue list --repo owner/repo --state closed --label agent-evaluated",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			var issues []vcs.Issue
			if state == "open" && len(labels) == 0 {
				issues, err = client.ListOpenIssues(repo)
			} else {
				issues, err = client.ListIssues(repo, state, labels)
			}
			if err != nil {
				return err
			}
			if len(issues) == 0 {
				fmt.Printf("no issues in %s\n", repo)
				return nil
			}
			fmt.Printf("%-6s  %-8s  %s\n", "NUMBER", "STATE", "TITLE")
			for _, i := range issues {
				fmt.Printf("#%-5d  %-8s  %s\n", i.Number, i.State, i.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().StringVar(&state, "state", "open", "issue state: open, closed, all")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "filter by label (repeatable)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func newIssueCommentCmd() *cobra.Command {
	var repo, body string
	var issue int

	cmd := &cobra.Command{
		Use:     "comment",
		Short:   "Post a comment on an issue",
		Example: "  clawflow issue comment --repo owner/repo --issue 7 --body \"looks good\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.PostIssueComment(repo, issue, body); err != nil {
				return err
			}
			fmt.Printf("commented on %s#%d\n", repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().StringVar(&body, "body", "", "comment body (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

func newIssueCloseCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:     "close",
		Short:   "Close an issue",
		Example: "  clawflow issue close --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.CloseIssue(repo, issue); err != nil {
				return err
			}
			fmt.Printf("closed %s#%d\n", repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}
