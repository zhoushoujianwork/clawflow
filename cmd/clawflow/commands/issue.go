package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Manage issues",
	}
	cmd.AddCommand(newIssueCreateCmd())
	cmd.AddCommand(newIssueListCmd())
	return cmd
}

func newIssueCreateCmd() *cobra.Command {
	var repo string
	var title string
	var body string

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
	var repo string

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List open issues in a repository",
		Aliases: []string{"ls"},
		Example: "  clawflow issue list --repo owner/repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			info, err := config.ParseRepoInput(repo, cfg.Settings.GitLabHosts)
			if err != nil {
				return err
			}
			repoCfg, ok := cfg.Repos[info.OwnerRepo]
			if !ok {
				return fmt.Errorf("repo %q not found in config", info.OwnerRepo)
			}
			client, err := newVCSClient(repoCfg)
			if err != nil {
				return err
			}
			issues, err := client.ListOpenIssues(info.OwnerRepo)
			if err != nil {
				return err
			}
			if len(issues) == 0 {
				fmt.Printf("no open issues in %s\n", info.OwnerRepo)
				return nil
			}
			fmt.Printf("%-6s  %s\n", "NUMBER", "TITLE")
			for _, i := range issues {
				fmt.Printf("#%-5d  %s\n", i.Number, i.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
