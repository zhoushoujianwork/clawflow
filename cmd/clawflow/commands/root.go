package commands

import "github.com/spf13/cobra"

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "clawflow",
		Short: "ClawFlow — automated issue → fix → PR pipeline",
		Long: `ClawFlow CLI handles the deterministic parts of the pipeline:
harvesting issues, managing labels, creating worktrees, and writing memory records.
The AI skill (SKILL.md) handles evaluation and sub-agent orchestration.`,
	}

	root.AddCommand(NewHarvestCmd())
	root.AddCommand(NewLabelCmd())
	root.AddCommand(NewWorktreeCmd())
	root.AddCommand(NewMemoryCmd())
	root.AddCommand(NewStatusCmd())
	root.AddCommand(NewPRCheckCmd())

	return root
}
