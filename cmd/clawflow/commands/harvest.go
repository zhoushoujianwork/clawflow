package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// HarvestIssue is an issue returned in the harvest output.
type HarvestIssue struct {
	Repo         string   `json:"repo"`
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Labels       []string `json:"labels,omitempty"`
	Comments     []string `json:"comments,omitempty"`
	WorktreePath string   `json:"worktree_path,omitempty"`
}

// HarvestResult is the JSON output of clawflow harvest.
type HarvestResult struct {
	ToEvaluate    []HarvestIssue `json:"to_evaluate"`
	ToExecute     []HarvestIssue `json:"to_execute"`
	ToQueue       []HarvestIssue `json:"to_queue"`
	RetryEligible []HarvestIssue `json:"retry_eligible"`
	SplitDone     []HarvestIssue `json:"split_done,omitempty"`
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
				ToEvaluate:    []HarvestIssue{},
				ToExecute:     []HarvestIssue{},
				ToQueue:       []HarvestIssue{},
				RetryEligible: []HarvestIssue{},
				SplitDone:     []HarvestIssue{},
			}

			maxConcurrent := cfg.Settings.MaxConcurrentAgents
			if maxConcurrent <= 0 {
				maxConcurrent = 3
			}

			inProgressCount := 0
			allIssuesByRepo := make(map[string][]vcs.Issue)
			for repoName, repoCfg := range repos {
				client, err := newVCSClient(repoCfg)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot create client for %s: %v\n", repoName, err)
					continue
				}
				issues, err := client.ListOpenIssues(repoName)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list issues for %s: %v\n", repoName, err)
					continue
				}
				allIssuesByRepo[repoName] = issues
				for _, issue := range issues {
					if issue.HasLabel("in-progress") {
						inProgressCount++
					}
				}
			}

			// Unlock blocked issues before scanning so newly unblocked issues
			// are picked up in this same harvest run.
			sw := &stderrWriter{cmd}
			for repoName, repoCfg := range repos {
				client, err := newVCSClient(repoCfg)
				if err != nil {
					continue
				}
				TryUnlockDownstream(client, repoName, 0, vcs.DependsOnMerge, sw)
				TryUnlockDownstream(client, repoName, 0, vcs.DependsOnPR, sw)
			}

			// Re-fetch issues after unlock so newly unblocked issues are included.
			for repoName, repoCfg := range repos {
				client, err := newVCSClient(repoCfg)
				if err != nil {
					continue
				}
				issues, err := client.ListOpenIssues(repoName)
				if err != nil {
					continue
				}
				allIssuesByRepo[repoName] = issues
			}

			for repoName, issues := range allIssuesByRepo {
				repoCfg := repos[repoName]
				client, err := newVCSClient(repoCfg)
				if err != nil {
					continue
				}
				for _, issue := range issues {
					// Skip issues that are waiting on dependencies.
					if issue.HasLabel("blocked") {
						continue
					}

					evaluated := issue.HasLabel("agent-evaluated")
					inProgress := issue.HasLabel("in-progress")
					skipped := issue.HasLabel("agent-skipped")
					failed := issue.HasLabel("agent-failed")
					readyForAgent := issue.HasLabel("ready-for-agent")
					queued := issue.HasLabel("agent-queued")

					switch {
					case !evaluated && !inProgress && !skipped && !failed && !readyForAgent && !queued:
						comments, err := client.ListIssueComments(repoName, issue.Number)
						if err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list comments for %s#%d: %v\n", repoName, issue.Number, err)
						}
						result.ToEvaluate = append(result.ToEvaluate, HarvestIssue{
							Repo:     repoName,
							Number:   issue.Number,
							Title:    issue.Title,
							Body:     issue.Body,
							Labels:   issue.Labels,
							Comments: comments,
						})

					case readyForAgent && evaluated && !inProgress:
						hasPR, err := client.PRExistsForIssue(repoName, issue.Number)
						if err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warn: PR check failed for %s#%d: %v\n", repoName, issue.Number, err)
						}
						if hasPR {
							continue
						}
						item := HarvestIssue{
							Repo:         repoName,
							Number:       issue.Number,
							Title:        issue.Title,
							Body:         issue.Body,
							Labels:       issue.Labels,
							WorktreePath: config.WorktreePath(repoName, issue.Number),
						}
						if inProgressCount < maxConcurrent {
							result.ToExecute = append(result.ToExecute, item)
							inProgressCount++
						} else {
							result.ToQueue = append(result.ToQueue, item)
						}

					case evaluated && !inProgress && !readyForAgent:
						hasPR, err := client.PRExistsForIssue(repoName, issue.Number)
						if err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warn: PR check failed for %s#%d: %v\n", repoName, issue.Number, err)
						}
						if !hasPR && HasMergedPRInMemory(repoName, issue.Number) {
							result.RetryEligible = append(result.RetryEligible, HarvestIssue{
								Repo:   repoName,
								Number: issue.Number,
								Title:  issue.Title,
								Body:   issue.Body,
								Labels: issue.Labels,
							})
						}
					}
				}
			}

			// Check agent-split main issues: if all sub-issues are closed, add to split_done.
			for repoName, repoCfg := range repos {
				client, err := newVCSClient(repoCfg)
				if err != nil {
					continue
				}
				splitIssues, err := client.ListIssues(repoName, "open", []string{"agent-split"})
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list agent-split issues for %s: %v\n", repoName, err)
					continue
				}
				for _, mainIssue := range splitIssues {
					keyword := fmt.Sprintf("Parent Issue: #%d", mainIssue.Number)
					subIssues, err := client.ListIssuesByBodyKeyword(repoName, keyword)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot search sub-issues for %s#%d: %v\n", repoName, mainIssue.Number, err)
						continue
					}
					// Also check closed sub-issues to count total
					closedSubs, err := client.ListIssues(repoName, "closed", nil)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list closed issues for %s: %v\n", repoName, err)
						continue
					}
					totalSubs := len(subIssues)
					for _, ci := range closedSubs {
						if strings.Contains(ci.Body, keyword) {
							totalSubs++
						}
					}
					// If there are sub-issues and all are closed (none open), mark for closing
					if totalSubs > 0 && len(subIssues) == 0 {
						result.SplitDone = append(result.SplitDone, HarvestIssue{
							Repo:   repoName,
							Number: mainIssue.Number,
							Title:  mainIssue.Title,
							Body:   mainIssue.Body,
							Labels: mainIssue.Labels,
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
