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
	cmd.AddCommand(newIssueCommentListCmd())
	cmd.AddCommand(newIssueCommentDeleteCmd())
	cmd.AddCommand(newIssueCloseCmd())
	cmd.AddCommand(newIssueUnblockCmd())
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

func newIssueCommentListCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:     "comment-list",
		Short:   "List comments on an issue with IDs and authors",
		Example: "  clawflow issue comment-list --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			comments, err := client.ListIssueCommentsDetail(repo, issue)
			if err != nil {
				return err
			}
			if len(comments) == 0 {
				fmt.Printf("no comments on %s#%d\n", repo, issue)
				return nil
			}
			fmt.Printf("%-12s  %-20s  %s\n", "ID", "AUTHOR", "BODY")
			for _, c := range comments {
				body := c.Body
				if len(body) > 60 {
					body = body[:57] + "..."
				}
				fmt.Printf("%-12d  %-20s  %s\n", c.ID, c.Author, body)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func newIssueCommentDeleteCmd() *cobra.Command {
	var repo, author string
	var issue int
	var commentID int64

	cmd := &cobra.Command{
		Use:   "comment-delete",
		Short: "Delete a comment (by ID) or all comments by an author on an issue",
		Example: "  clawflow issue comment-delete --repo owner/repo --issue 7 --comment-id 123456\n" +
			"  clawflow issue comment-delete --repo owner/repo --issue 7 --author bot-user",
		RunE: func(cmd *cobra.Command, args []string) error {
			if commentID == 0 && author == "" {
				return fmt.Errorf("provide --comment-id or --author")
			}
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			// Single comment delete by ID
			if commentID != 0 {
				if err := client.DeleteIssueComment(repo, issue, commentID); err != nil {
					return err
				}
				fmt.Printf("deleted comment %d on %s#%d\n", commentID, repo, issue)
				return nil
			}
			// Batch delete by author
			comments, err := client.ListIssueCommentsDetail(repo, issue)
			if err != nil {
				return err
			}
			deleted := 0
			for _, c := range comments {
				if c.Author != author {
					continue
				}
				if err := client.DeleteIssueComment(repo, issue, c.ID); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot delete comment %d: %v\n", c.ID, err)
					continue
				}
				deleted++
			}
			fmt.Printf("deleted %d comment(s) by %q on %s#%d\n", deleted, author, repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().Int64Var(&commentID, "comment-id", 0, "delete a specific comment by ID")
	cmd.Flags().StringVar(&author, "author", "", "delete all comments by this author")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}
