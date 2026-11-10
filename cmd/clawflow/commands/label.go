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
			if queued, err := queueEffect(cmd.OutOrStdout(), jobEffect{Kind: "add-label", Repo: repo, Number: issue, Labels: []string{label}}); queued {
				return err
			}
			client, repo, _, err := newVCSClientForRepo(repo)
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
			if queued, err := queueEffect(cmd.OutOrStdout(), jobEffect{Kind: "remove-label", Repo: repo, Number: issue, Labels: []string{label}}); queued {
				return err
			}
			client, repo, _, err := newVCSClientForRepo(repo)
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
			t, err := cfg.Resolve(args[0])
			if err != nil {
				return err
			}
			if !t.Configured {
				return fmt.Errorf("repo %q not found in config (%s) — run: clawflow repo add %s", t.Key, t.Describe(), args[0])
			}
			repoCfg := t.Repo
			client, err := newVCSClient(repoCfg)
			if err != nil {
				return err
			}
			fmt.Printf("Initializing ClawFlow labels in %s ...\n", t.Key)
			return client.InitLabels(t.Key, vcs.ClawFlowLabels)
		},
	}
}
