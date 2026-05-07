package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Manage issues",
	}
	cmd.AddCommand(newIssueCreateCmd())
	cmd.AddCommand(newIssueListCmd())
	cmd.AddCommand(newIssueSearchCmd())
	cmd.AddCommand(newIssueCommentCmd())
	cmd.AddCommand(newIssueCommentListCmd())
	cmd.AddCommand(newIssueCommentDeleteCmd())
	cmd.AddCommand(newIssueCloseCmd())
	cmd.AddCommand(newIssueAddSubCmd())
	cmd.AddCommand(newIssueListSubCmd())
	return cmd
}

// searchHit pairs an Issue with the repo it came from, so cross-repo
// project searches can render unambiguously and JSON consumers know
// which repo each result lives in.
type searchHit struct {
	Repo  string    `json:"repo"`
	Issue vcs.Issue `json:"issue"`
}

func newIssueSearchCmd() *cobra.Command {
	var repo, projectName, state string
	var limit int
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Keyword-search issues across title + body (one repo or all member repos of a project)",
		Long: `Run a keyword search across issues — title and body matched server-side.

Use --repo for a single repo, or --project to fan out across every member repo
of a clawflow project in parallel. Default state is "all" so closed issues
(the historical-decision archive) are included; pass --state open to narrow.

Designed for AI agent use: the evaluate / implement / PM prompts call this
command to pull historical related issues into their evaluation context.
Pass --json for AI consumption; the default table output is for humans.`,
		Example: "  clawflow issue search \"auth token\" --repo owner/name\n  clawflow issue search \"auth token\" --project bbclaw --state all\n  clawflow issue search \"flaky test\" --repo owner/name --json --limit 50",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			if (repo == "" && projectName == "") || (repo != "" && projectName != "") {
				return fmt.Errorf("exactly one of --repo or --project is required")
			}
			repos, err := resolveSearchScope(repo, projectName)
			if err != nil {
				return err
			}
			hits, err := runSearch(repos, query, state, limit)
			if err != nil {
				return err
			}
			return printSearchHits(hits, jsonOutput, query)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo to search (mutually exclusive with --project)")
	cmd.Flags().StringVar(&projectName, "project", "", "search every member repo of this project in parallel")
	cmd.Flags().StringVar(&state, "state", "all", "issue state: open, closed, all")
	cmd.Flags().IntVar(&limit, "limit", 20, "max results per repo (clamped to 100)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output search hits as JSON")
	return cmd
}

func resolveSearchScope(repo, projectName string) ([]string, error) {
	if repo != "" {
		return []string{repo}, nil
	}
	p, err := project.Get(projectName)
	if err != nil {
		return nil, err
	}
	if len(p.Repos) == 0 {
		return nil, fmt.Errorf("project %q has no member repos", projectName)
	}
	return p.Repos, nil
}

// runSearch fans out one search per repo in parallel. Per-repo errors
// are logged to stderr but don't fail the whole command — partial
// results are more useful than nothing for AI consumers.
func runSearch(repos []string, query, state string, limit int) ([]searchHit, error) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		hits []searchHit
	)
	for _, r := range repos {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			client, _, err := newVCSClientForRepo(r)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] client: %v\n", r, err)
				return
			}
			results, err := client.SearchIssues(r, query, state, limit)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] search: %v\n", r, err)
				return
			}
			mu.Lock()
			for _, iss := range results {
				hits = append(hits, searchHit{Repo: r, Issue: iss})
			}
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	return hits, nil
}

func printSearchHits(hits []searchHit, jsonOutput bool, query string) error {
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if hits == nil {
			hits = []searchHit{}
		}
		return enc.Encode(hits)
	}
	if len(hits) == 0 {
		fmt.Printf("no issues match %q\n", query)
		return nil
	}
	fmt.Printf("%d issue(s) matching %q:\n\n", len(hits), query)
	for _, h := range hits {
		labels := ""
		if len(h.Issue.Labels) > 0 {
			labels = "  [" + strings.Join(h.Issue.Labels, ", ") + "]"
		}
		fmt.Printf("  %s  #%d  %-7s%s  %s\n", h.Repo, h.Issue.Number, h.Issue.State, labels, h.Issue.Title)
		if h.Issue.UpdatedAt != "" {
			fmt.Printf("      updated %s\n", h.Issue.UpdatedAt)
		}
	}
	return nil
}

func newIssueCreateCmd() *cobra.Command {
	var repo, title, body string

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create an issue in a repository",
		Example: "  clawflow issue create --repo owner/repo --title \"bug: something broken\" --body \"details...\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			issue, err := client.CreateIssue(repo, title, body)
			if err != nil {
				return err
			}
			fmt.Printf("created issue #%d: %s\n", issue.Number, issue.Title)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().StringVar(&title, "title", "", "issue title (required)")
	cmd.Flags().StringVar(&body, "body", "", "issue body")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func newIssueListCmd() *cobra.Command {
	var repo, state string
	var labels []string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List issues in a repository",
		Aliases: []string{"ls"},
		Example: "  clawflow issue list --repo owner/repo\n  clawflow issue list --repo owner/repo --state closed --label agent-evaluated\n  clawflow issue list --repo owner/repo --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			var issues []vcs.Issue
			if state == "open" && len(labels) == 0 {
				issues, err = client.ListOpenIssues(repo)
			} else {
				issues, err = client.ListIssues(repo, state, labels)
			}
			if err != nil {
				return err
			}
			if len(issues) == 0 {
				if jsonOutput {
					fmt.Println("[]")
				} else {
					fmt.Printf("no issues in %s\n", repo)
				}
				return nil
			}
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(issues)
			}
			fmt.Printf("%-6s  %-8s  %s\n", "NUMBER", "STATE", "TITLE")
			for _, i := range issues {
				fmt.Printf("#%-5d  %-8s  %s\n", i.Number, i.State, i.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().StringVar(&state, "state", "open", "issue state: open, closed, all")
	cmd.Flags().StringArrayVar(&labels, "label", nil, "filter by label (repeatable)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output full issues as JSON")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func newIssueCommentCmd() *cobra.Command {
	var repo, body string
	var issue int

	cmd := &cobra.Command{
		Use:     "comment",
		Short:   "Post a comment on an issue",
		Example: "  clawflow issue comment --repo owner/repo --issue 7 --body \"looks good\"",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.PostIssueComment(repo, issue, body); err != nil {
				return err
			}
			fmt.Printf("commented on %s#%d\n", repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().StringVar(&body, "body", "", "comment body (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("body")
	return cmd
}

func newIssueCloseCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:     "close",
		Short:   "Close an issue",
		Example: "  clawflow issue close --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.CloseIssue(repo, issue); err != nil {
				return err
			}
			fmt.Printf("closed %s#%d\n", repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func newIssueCommentListCmd() *cobra.Command {
	var repo string
	var issue int
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:     "comment-list",
		Short:   "List comments on an issue with IDs and authors",
		Example: "  clawflow issue comment-list --repo owner/repo --issue 7\n  clawflow issue comment-list --repo owner/repo --issue 7 --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			comments, err := client.ListIssueCommentsDetail(repo, issue)
			if err != nil {
				return err
			}
			if len(comments) == 0 {
				if jsonOutput {
					fmt.Println("[]")
				} else {
					fmt.Printf("no comments on %s#%d\n", repo, issue)
				}
				return nil
			}
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(comments)
			}
			fmt.Printf("%-12s  %-20s  %s\n", "ID", "AUTHOR", "BODY")
			for _, c := range comments {
				body := c.Body
				if len(body) > 60 {
					body = body[:57] + "..."
				}
				fmt.Printf("%-12d  %-20s  %s\n", c.ID, c.Author, body)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output full comments as JSON")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func newIssueCommentDeleteCmd() *cobra.Command {
	var repo, author string
	var issue int
	var commentID int64

	cmd := &cobra.Command{
		Use:   "comment-delete",
		Short: "Delete a comment (by ID) or all comments by an author on an issue",
		Example: "  clawflow issue comment-delete --repo owner/repo --issue 7 --comment-id 123456\n" +
			"  clawflow issue comment-delete --repo owner/repo --issue 7 --author bot-user",
		RunE: func(cmd *cobra.Command, args []string) error {
			if commentID == 0 && author == "" {
				return fmt.Errorf("provide --comment-id or --author")
			}
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			// Single comment delete by ID
			if commentID != 0 {
				if err := client.DeleteIssueComment(repo, issue, commentID); err != nil {
					return err
				}
				fmt.Printf("deleted comment %d on %s#%d\n", commentID, repo, issue)
				return nil
			}
			// Batch delete by author
			comments, err := client.ListIssueCommentsDetail(repo, issue)
			if err != nil {
				return err
			}
			deleted := 0
			for _, c := range comments {
				if c.Author != author {
					continue
				}
				if err := client.DeleteIssueComment(repo, issue, c.ID); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot delete comment %d: %v\n", c.ID, err)
					continue
				}
				deleted++
			}
			fmt.Printf("deleted %d comment(s) by %q on %s#%d\n", deleted, author, repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().Int64Var(&commentID, "comment-id", 0, "delete a specific comment by ID")
	cmd.Flags().StringVar(&author, "author", "", "delete all comments by this author")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func newIssueAddSubCmd() *cobra.Command {
	var repo string
	var parent, sub int

	cmd := &cobra.Command{
		Use:     "add-sub",
		Short:   "Add a sub-issue to a parent issue (GitHub only)",
		Example: "  clawflow issue add-sub --repo owner/repo --parent 10 --sub 11",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			// Resolve the sub-issue's internal ID from its number.
			issues, err := client.ListIssues(repo, "all", nil)
			if err != nil {
				return fmt.Errorf("list issues: %w", err)
			}
			var subID int64
			for _, iss := range issues {
				if iss.Number == sub {
					subID = iss.ID
					break
				}
			}
			if subID == 0 {
				return fmt.Errorf("issue #%d not found in %s", sub, repo)
			}
			if err := client.AddSubIssue(repo, parent, subID); err != nil {
				return err
			}
			fmt.Printf("linked %s#%d as sub-issue of #%d\n", repo, sub, parent)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&parent, "parent", 0, "parent issue number (required)")
	cmd.Flags().IntVar(&sub, "sub", 0, "sub-issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("parent")
	_ = cmd.MarkFlagRequired("sub")
	return cmd
}

func newIssueListSubCmd() *cobra.Command {
	var repo string
	var issue int
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:     "list-sub",
		Short:   "List sub-issues of an issue (GitHub only)",
		Example: "  clawflow issue list-sub --repo owner/repo --issue 10\n  clawflow issue list-sub --repo owner/repo --issue 10 --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			subs, err := client.ListSubIssues(repo, issue)
			if err != nil {
				return err
			}
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(subs)
			}
			if len(subs) == 0 {
				fmt.Printf("no sub-issues for %s#%d\n", repo, issue)
				return nil
			}
			for _, s := range subs {
				fmt.Printf("#%d [%s] %s\n", s.Number, s.State, s.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}
