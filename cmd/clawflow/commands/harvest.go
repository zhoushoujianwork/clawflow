package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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

			var mu sync.Mutex
			var wg sync.WaitGroup
			inProgressCount := 0
			clients := make(map[string]vcs.Client)
			allIssuesByRepo := make(map[string][]vcs.Issue)

			// Phase 1: create clients and fetch open issues concurrently.
			for repoName, repoCfg := range repos {
				wg.Add(1)
				go func(name string, cfg config.Repo) {
					defer wg.Done()
					client, err := newVCSClient(cfg)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot create client for %s: %v\n", name, err)
						return
					}
					issues, err := client.ListOpenIssues(name)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list issues for %s: %v\n", name, err)
						return
					}
					count := 0
					for _, issue := range issues {
						if issue.HasLabel("in-progress") {
							count++
						}
					}
					mu.Lock()
					clients[name] = client
					allIssuesByRepo[name] = issues
					inProgressCount += count
					mu.Unlock()
				}(repoName, repoCfg)
			}
			wg.Wait()

			// Phase 2: unlock blocked issues concurrently.
			sw := &stderrWriter{cmd}
			for repoName, client := range clients {
				wg.Add(1)
				go func(name string, c vcs.Client) {
					defer wg.Done()
					TryUnlockDownstream(c, name, 0, vcs.DependsOnMerge, sw)
					TryUnlockDownstream(c, name, 0, vcs.DependsOnPR, sw)
				}(repoName, client)
			}
			wg.Wait()

			// Phase 3: re-fetch issues after unlock concurrently.
			for repoName, client := range clients {
				wg.Add(1)
				go func(name string, c vcs.Client) {
					defer wg.Done()
					issues, err := c.ListOpenIssues(name)
					if err != nil {
						return
					}
					mu.Lock()
					allIssuesByRepo[name] = issues
					mu.Unlock()
				}(repoName, client)
			}
			wg.Wait()

			// Phase 4: classify issues concurrently; protect result and inProgressCount with mu.
			for repoName, issues := range allIssuesByRepo {
				wg.Add(1)
				go func(name string, issueList []vcs.Issue) {
					defer wg.Done()
					client := clients[name]
					for _, issue := range issueList {
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
							comments, err := client.ListIssueComments(name, issue.Number)
							if err != nil {
								fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list comments for %s#%d: %v\n", name, issue.Number, err)
							}
							mu.Lock()
							result.ToEvaluate = append(result.ToEvaluate, HarvestIssue{
								Repo:     name,
								Number:   issue.Number,
								Title:    issue.Title,
								Body:     issue.Body,
								Labels:   issue.Labels,
								Comments: comments,
							})
							mu.Unlock()

						case readyForAgent && evaluated && !inProgress:
							hasPR, err := client.PRExistsForIssue(name, issue.Number)
							if err != nil {
								fmt.Fprintf(cmd.ErrOrStderr(), "warn: PR check failed for %s#%d: %v\n", name, issue.Number, err)
							}
							if hasPR {
								continue
							}
							item := HarvestIssue{
								Repo:         name,
								Number:       issue.Number,
								Title:        issue.Title,
								Body:         issue.Body,
								Labels:       issue.Labels,
								WorktreePath: config.WorktreePath(name, issue.Number),
							}
							mu.Lock()
							if inProgressCount < maxConcurrent {
								result.ToExecute = append(result.ToExecute, item)
								inProgressCount++
							} else {
								result.ToQueue = append(result.ToQueue, item)
							}
							mu.Unlock()

						case evaluated && !inProgress && !readyForAgent:
							hasPR, err := client.PRExistsForIssue(name, issue.Number)
							if err != nil {
								fmt.Fprintf(cmd.ErrOrStderr(), "warn: PR check failed for %s#%d: %v\n", name, issue.Number, err)
							}
							if !hasPR && HasMergedPRInMemory(name, issue.Number) {
								mu.Lock()
								result.RetryEligible = append(result.RetryEligible, HarvestIssue{
									Repo:   name,
									Number: issue.Number,
									Title:  issue.Title,
									Body:   issue.Body,
									Labels: issue.Labels,
								})
								mu.Unlock()
							}
						}
					}
				}(repoName, issues)
			}
			wg.Wait()

			// Phase 5: check agent-split main issues concurrently.
			for repoName, client := range clients {
				wg.Add(1)
				go func(name string, c vcs.Client) {
					defer wg.Done()
					splitIssues, err := c.ListIssues(name, "open", []string{"agent-split"})
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list agent-split issues for %s: %v\n", name, err)
						return
					}
					if len(splitIssues) == 0 {
						return
					}
					closedIssues, err := c.ListIssues(name, "closed", nil)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot list closed issues for %s: %v\n", name, err)
						return
					}
					var done []HarvestIssue
					for _, mainIssue := range splitIssues {
						keyword := fmt.Sprintf("Parent Issue: #%d", mainIssue.Number)
						subIssues, err := c.ListIssuesByBodyKeyword(name, keyword)
						if err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot search sub-issues for %s#%d: %v\n", name, mainIssue.Number, err)
							continue
						}
						totalSubs := len(subIssues)
						for _, ci := range closedIssues {
							if strings.Contains(ci.Body, keyword) {
								totalSubs++
							}
						}
						if totalSubs > 0 && len(subIssues) == 0 {
							done = append(done, HarvestIssue{
								Repo:   name,
								Number: mainIssue.Number,
								Title:  mainIssue.Title,
								Body:   mainIssue.Body,
								Labels: mainIssue.Labels,
							})
						}
					}
					if len(done) > 0 {
						mu.Lock()
						result.SplitDone = append(result.SplitDone, done...)
						mu.Unlock()
					}
				}(repoName, client)
			}
			wg.Wait()

			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&repoFlag, "repo", "", "Only harvest this repo (owner/repo)")
	return cmd
}
