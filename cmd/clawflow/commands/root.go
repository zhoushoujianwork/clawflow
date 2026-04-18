package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var Version = "dev"

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "clawflow",
		Short: "Coding as a Service — automated issue → fix → PR pipeline",
		Long: `ClawFlow CLI handles the deterministic parts of the pipeline:
harvesting issues, managing labels, creating worktrees, and writing memory records.
The AI skill (SKILL.md) handles evaluation and sub-agent orchestration.`,
		Version: Version,
	}

	root.AddCommand(NewConnectCmd())
	root.AddCommand(NewHarvestCmd())
	root.AddCommand(NewLabelCmd())
	root.AddCommand(NewWorktreeCmd())
	root.AddCommand(NewMemoryCmd())
	root.AddCommand(NewStatusCmd())
	root.AddCommand(NewPRCheckCmd())
	root.AddCommand(NewUpdateCmd())
	root.AddCommand(NewRepoCmd())
	root.AddCommand(NewConfigCmd())
	root.AddCommand(NewRetryCmd())
	root.AddCommand(NewIssueCmd())
	root.AddCommand(NewPRCmd())
	root.AddCommand(NewUnblockScanCmd())
	root.AddCommand(NewBillingHookCmd())
	root.AddCommand(NewLangCmd())
	root.AddCommand(NewLoginCmd())
	root.AddCommand(NewWorkerCmd())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(Version)
			latest := FetchLatestTag()
			if latest != "" && latest != Version {
				fmt.Printf("  → new version available: %s  (run: clawflow update)\n", latest)
			}
		},
	})

	return root
}
