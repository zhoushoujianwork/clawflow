package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

var Version = "dev"

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "clawflow",
		Short: "Label-driven automation that turns issues into PRs on GitHub and GitLab",
		Long: `ClawFlow matches each open issue/PR against a set of operators (self-contained
SKILL.md files) and runs the matching operator through 'claude -p'. State lives
entirely in VCS labels and comments. Run 'clawflow run' once, or schedule it.`,
		Version: Version,
	}

	// --debug is a verbosity switch: when set, commands emit additional
	// "[debug] ..." trace lines to stderr explaining decisions (operator
	// matcher results, fetch counts, skip reasons). Off by default so normal
	// runs stay quiet enough to schedule via cron.
	root.PersistentFlags().BoolVar(&Debug, "debug", false, "Print debug trace to stderr (matcher decisions, scan counts)")

	root.AddCommand(NewCloudCmd())
	root.AddCommand(NewWorkerCmd())
	root.AddCommand(NewWebCmd())
	root.AddCommand(NewChatCmd())
	root.AddCommand(NewFeedbackCmd())
	root.AddCommand(NewOperatorsCmd())
	root.AddCommand(NewRepoCmd())
	root.AddCommand(NewIssueCmd())
	root.AddCommand(NewPRCmd())
	root.AddCommand(NewLabelCmd())
	root.AddCommand(NewConfigCmd())
	root.AddCommand(NewUpdateCmd())
	root.AddCommand(NewInstallSkillCmd())
	root.AddCommand(NewProjectCmd())
	root.AddCommand(NewPilotCmd())
	root.AddCommand(NewRespawnCmd())
	// Helpers retained for operator use (invoked from SKILL.md bodies):
	root.AddCommand(NewWorktreeCmd())
	root.AddCommand(NewStatusCmd())
	root.AddCommand(NewPRCheckCmd())
	root.AddCommand(NewLangCmd())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(Version)
			latest := FetchLatestTag()
			// Only announce an update when the remote tag is strictly newer.
			// Guards against two otherwise-noisy cases:
			//   1. Local is a just-tagged release whose GitHub Actions
			//      build hasn't published to /releases/latest yet, so the
			//      API still reports an older tag.
			//   2. Local is a dev build ahead of the release tag (git
			//      describe emits "vX.Y.Z-<N>-g<sha>").
			if latest == "" || !IsNewerVersion(Version, latest) {
				return
			}
			fmt.Printf("  → new version available: %s  (run: clawflow update)\n", latest)
		},
	})

	return root
}
