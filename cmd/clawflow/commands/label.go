package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	gh "github.com/zhoushoujianwork/clawflow/internal/github"
)

func NewLabelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Manage GitHub issue labels",
	}
	cmd.AddCommand(newLabelAddCmd())
	cmd.AddCommand(newLabelRemoveCmd())
	return cmd
}

func newLabelAddCmd() *cobra.Command {
	var repo string
	var issue int
	var label string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a label to an issue",
		Example: "  clawflow label add --repo owner/repo --issue 7 --label in-progress",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gh.AddLabel(repo, issue, label); err != nil {
				return err
			}
			fmt.Printf("label %q added to %s#%d\n", label, repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().StringVar(&label, "label", "", "label name (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("label")
	return cmd
}

func newLabelRemoveCmd() *cobra.Command {
	var repo string
	var issue int
	var label string

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove a label from an issue",
		Example: "  clawflow label remove --repo owner/repo --issue 7 --label in-progress",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gh.RemoveLabel(repo, issue, label); err != nil {
				return err
			}
			fmt.Printf("label %q removed from %s#%d\n", label, repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().StringVar(&label, "label", "", "label name (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("label")
	return cmd
}
