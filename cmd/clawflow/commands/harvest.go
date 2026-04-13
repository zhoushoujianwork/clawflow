package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	gh "github.com/zhoushoujianwork/clawflow/internal/github"
)

// HarvestIssue is an issue returned in the harvest output.
type HarvestIssue struct {
	Repo        string `json:"repo"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	WorktreePath string `json:"worktree_path,omitempty"`
}

// HarvestResult is the JSON output of clawflow harvest.
type HarvestResult struct {
	ToEvaluate []HarvestIssue `json:"to_evaluate"`
	ToExecute  []HarvestIssue `json:"to_execute"`
}

func NewHarvestCmd() *cobra.Command {
	var repoFlag string

	cmd := &cobra.Command{
		Use:   "harvest",
		Short: "Scan repos and output pending issues as JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			repos := cfg.EnabledRepos()
			if repoFlag != "" {
				r, ok := repos[repoFlag]
				if !ok {
					return fmt.Errorf("repo %q not found or not enabled", repoFlag)
				}
				repos = map[string]config.Repo{repoFlag: r}
			}

			result := HarvestResult{
				ToEvaluate: []HarvestIssue{},
				ToExecute:  []HarvestIssue{},
			}

			for repoName := range repos {
				issues, err := gh.ListOpenIssues(repoName)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list issues for %s: %v\n", repoName, err)
					continue
				}

				for _, issue := range issues {
					// Skip PRs
					if issue.HasLabel("pull_request") {
						continue
					}

					evaluated := issue.HasLabel("agent-evaluated")
					inProgress := issue.HasLabel("in-progress")
					skipped := issue.HasLabel("agent-skipped")
					failed := issue.HasLabel("agent-failed")
					readyForAgent := issue.HasLabel("ready-for-agent")

					switch {
					// Evaluate queue: no evaluation labels yet
					case !evaluated && !inProgress && !skipped && !failed && !readyForAgent:
						result.ToEvaluate = append(result.ToEvaluate, HarvestIssue{
							Repo:   repoName,
							Number: issue.Number,
							Title:  issue.Title,
							Body:   issue.Body,
						})

					// Execute queue: approved by owner, evaluated, not in-progress
					case readyForAgent && evaluated && !inProgress:
						hasPR, err := gh.PRExistsForIssue(repoName, issue.Number)
						if err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warn: PR check failed for %s#%d: %v\n", repoName, issue.Number, err)
						}
						if hasPR {
							continue // skip — PR already exists
						}
						result.ToExecute = append(result.ToExecute, HarvestIssue{
							Repo:        repoName,
							Number:      issue.Number,
							Title:       issue.Title,
							Body:        issue.Body,
							WorktreePath: config.WorktreePath(repoName, issue.Number),
						})
					}
				}
			}

			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&repoFlag, "repo", "", "Only harvest this repo (owner/repo)")
	return cmd
}
