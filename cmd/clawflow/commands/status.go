package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
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

			for repoName, repoCfg := range repos {
				fmt.Printf("  %s\n", repoName)

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

				var toEvaluate, toExecute, inProgress, skipped, failed, retryEligible int
				var retryIssues []vcs.Issue

				for _, issue := range issues {
					switch {
					case issue.HasLabel("in-progress"):
						inProgress++
					case issue.HasLabel("agent-failed"):
						failed++
					case issue.HasLabel("agent-skipped"):
						skipped++
					case issue.HasLabel("ready-for-agent") && issue.HasLabel("agent-evaluated") && !issue.HasLabel("in-progress"):
						toExecute++
					case !issue.HasLabel("agent-evaluated"):
						toEvaluate++
					case issue.HasLabel("agent-evaluated") && !issue.HasLabel("in-progress") && !issue.HasLabel("ready-for-agent"):
						hasPR, _ := client.PRExistsForIssue(repoName, issue.Number)
						if !hasPR && HasMergedPRInMemory(repoName, issue.Number) {
							retryEligible++
							retryIssues = append(retryIssues, issue)
						}
					}
				}

				fmt.Printf("    待评估:  %d\n", toEvaluate)
				fmt.Printf("    待执行:  %d\n", toExecute)
				fmt.Printf("    处理中:  %d\n", inProgress)
				fmt.Printf("    已跳过:  %d\n", skipped)
				fmt.Printf("    已失败:  %d\n", failed)
				if retryEligible > 0 {
					fmt.Printf("    可重试:  %d\n", retryEligible)
					for _, issue := range retryIssues {
						fmt.Printf("      #%d %s  (run: clawflow retry --repo %s --issue %d)\n",
							issue.Number, issue.Title, repoName, issue.Number)
					}
				}
				fmt.Println()
			}

			return nil
		},
	}
}
