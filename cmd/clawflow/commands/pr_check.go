package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	gh "github.com/zhoushoujianwork/clawflow/internal/github"
)

func NewPRCheckCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:   "pr-check",
		Short: "Check if an open PR already exists for an issue",
		Example: "  clawflow pr-check --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			exists, err := gh.PRExistsForIssue(repo, issue)
			if err != nil {
				return err
			}
			if exists {
				fmt.Printf("PR exists for %s#%d — skip\n", repo, issue)
			} else {
				fmt.Printf("no PR for %s#%d — proceed\n", repo, issue)
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
