package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewLabelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Manage issue labels",
	}
	cmd.AddCommand(newLabelAddCmd())
	cmd.AddCommand(newLabelRemoveCmd())
	cmd.AddCommand(newLabelInitCmd())
	return cmd
}

func newLabelAddCmd() *cobra.Command {
	var repo string
	var issue int
	var label string

	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Add a label to an issue",
		Example: "  clawflow label add --repo owner/repo --issue 7 --label in-progress",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.AddLabel(repo, issue, label); err != nil {
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
		Use:     "remove",
		Short:   "Remove a label from an issue",
		Example: "  clawflow label remove --repo owner/repo --issue 7 --label in-progress",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.RemoveLabel(repo, issue, label); err != nil {
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

func newLabelInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "init <owner/repo|URL>",
		Short:   "Create standard ClawFlow labels in a repository",
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow label init zhoushoujianwork/clawflow\n  clawflow label init http://gitlab.company.com/ns/group/repo.git",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			info, err := config.ParseRepoInput(args[0], cfg.Settings.GitLabHosts)
			if err != nil {
				return err
			}
			repoCfg, ok := cfg.Repos[info.OwnerRepo]
			if !ok {
				return fmt.Errorf("repo %q not found in config — run: clawflow repo add %s", info.OwnerRepo, args[0])
			}
			client, err := newVCSClient(repoCfg)
			if err != nil {
				return err
			}
			fmt.Printf("Initializing ClawFlow labels in %s ...\n", info.OwnerRepo)
			return client.InitLabels(info.OwnerRepo, vcs.ClawFlowLabels)
		},
	}
}
