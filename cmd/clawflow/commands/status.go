package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// NewStatusCmd shows a per-repo health summary using the operator-era labels.
// For deeper inspection, users can run `clawflow issue list --repo X --label Y`.
func NewStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current state of all monitored repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			repos := cfg.EnabledRepos()
			if len(repos) == 0 {
				fmt.Println("No repos configured. Edit ~/.clawflow/config/config.yaml")
				return nil
			}

			fmt.Printf("ClawFlow status — %d repo(s) monitored\n\n", len(repos))

			for repoName, repoCfg := range repos {
				fmt.Printf("  %s\n", repoName)
				flags := []string{}
				if repoCfg.AutoFix {
					flags = append(flags, "auto_fix:on")
				}
				if repoCfg.AutoMerge {
					flags = append(flags, "auto_merge:on")
				}
				if len(flags) > 0 {
					fmt.Printf("    [%s]\n", strings.Join(flags, "  "))
				}

				client, err := newVCSClient(repoCfg)
				if err != nil {
					fmt.Printf("    error: %v\n\n", err)
					continue
				}

				issues, err := client.ListOpenIssues(repoName)
				if err != nil {
					fmt.Printf("    error: %v\n\n", err)
					continue
				}

				// Tallies use the current operator label scheme. An issue can
				// occupy multiple buckets (e.g. `bug` + `agent-evaluated`);
				// the switch picks the most "advanced" state first.
				var awaitingEval, readyToImplement, running, implemented, failed, skipped int
				for _, issue := range issues {
					switch {
					case issue.HasLabel("agent-running"):
						running++
					case issue.HasLabel("agent-failed"):
						failed++
					case issue.HasLabel("agent-skipped"):
						skipped++
					case issue.HasLabel("agent-implemented"):
						implemented++
					case issue.HasLabel("ready-for-agent"):
						readyToImplement++
					case (issue.HasLabel("bug") || issue.HasLabel("feat")) && !issue.HasLabel("agent-evaluated"):
						awaitingEval++
					}
				}

				fmt.Printf("    awaiting eval:     %d\n", awaitingEval)
				fmt.Printf("    ready to implement: %d\n", readyToImplement)
				fmt.Printf("    running:           %d\n", running)
				fmt.Printf("    implemented:       %d\n", implemented)
				fmt.Printf("    skipped:           %d\n", skipped)
				fmt.Printf("    failed:            %d\n", failed)
				fmt.Println()
			}
			return nil
		},
	}
}
