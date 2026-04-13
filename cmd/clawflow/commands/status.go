package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	gh "github.com/zhoushoujianwork/clawflow/internal/github"
)

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
				fmt.Println("No repos configured. Edit ~/.clawflow/config/repos.yaml")
				return nil
			}

			fmt.Printf("ClawFlow status — %d repo(s) monitored\n\n", len(repos))

			for repoName := range repos {
				fmt.Printf("  %s\n", repoName)

				issues, err := gh.ListOpenIssues(repoName)
				if err != nil {
					fmt.Printf("    error: %v\n\n", err)
					continue
				}

				var toEvaluate, toExecute, inProgress, skipped, failed int
				for _, issue := range issues {
					switch {
					case issue.HasLabel("in-progress"):
						inProgress++
					case issue.HasLabel("agent-failed"):
						failed++
					case issue.HasLabel("agent-skipped"):
						skipped++
					case issue.HasLabel("ready-for-agent") && issue.HasLabel("agent-evaluated"):
						toExecute++
					case !issue.HasLabel("agent-evaluated"):
						toEvaluate++
					}
				}

				fmt.Printf("    待评估:  %d\n", toEvaluate)
				fmt.Printf("    待执行:  %d\n", toExecute)
				fmt.Printf("    处理中:  %d\n", inProgress)
				fmt.Printf("    已跳过:  %d\n", skipped)
				fmt.Printf("    已失败:  %d\n", failed)
				fmt.Println()
			}

			return nil
		},
	}
}
