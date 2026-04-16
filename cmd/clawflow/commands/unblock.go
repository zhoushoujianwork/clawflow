package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// TryUnlockDownstream scans all blocked issues in repo and removes the
// "blocked" label from any whose dependencies are now satisfied.
//
// closedIssueNumber: the issue that was just closed/merged (0 = check all).
// triggerType: DependsOnMerge (PR merged) or DependsOnPR (PR opened).
func TryUnlockDownstream(
	client vcs.Client,
	repo string,
	closedIssueNumber int,
	triggerType vcs.DependencyType,
	stderr interface{ Printf(string, ...any) },
) {
	blocked, err := client.ListIssues(repo, "open", []string{"blocked"})
	if err != nil {
		if stderr != nil {
			stderr.Printf("warn: cannot list blocked issues for %s: %v\n", repo, err)
		}
		return
	}

	for _, issue := range blocked {
		comments, err := client.ListIssueComments(repo, issue.Number)
		if err != nil {
			if stderr != nil {
				stderr.Printf("warn: cannot list comments for %s#%d: %v\n", repo, issue.Number, err)
			}
			continue
		}

		deps := vcs.ParseDependencies(issue.Body, comments)
		if len(deps) == 0 {
			continue
		}

		// If a specific closed issue was given, skip issues that don't depend on it.
		if closedIssueNumber > 0 && !vcs.ContainsIssue(deps, closedIssueNumber) {
			continue
		}

		// Check that ALL merge-type deps are satisfied (closed issues).
		if !allMergeDepsSatisfied(client, repo, deps) {
			continue
		}

		// Check that all pr-type deps are satisfied (open PR exists).
		if !allPRDepsSatisfied(client, repo, deps) {
			continue
		}

		if err := client.RemoveLabel(repo, issue.Number, "blocked"); err != nil {
			if stderr != nil {
				stderr.Printf("warn: cannot remove blocked label from %s#%d: %v\n", repo, issue.Number, err)
			}
			continue
		}
		if stderr != nil {
			stderr.Printf("unblocked %s#%d (deps: %s)\n", repo, issue.Number, vcs.DepsSummary(deps))
		}
	}
}

// allMergeDepsSatisfied returns true when every depends-on-merge issue is closed.
func allMergeDepsSatisfied(client vcs.Client, repo string, deps []vcs.Dependency) bool {
	mergeDeps := vcs.FilterByType(deps, vcs.DependsOnMerge)
	for _, d := range mergeDeps {
		issues, err := client.ListIssues(repo, "closed", nil)
		if err != nil {
			return false
		}
		found := false
		for _, i := range issues {
			if i.Number == d.IssueNumber {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// allPRDepsSatisfied returns true when every depends-on-pr issue has an open PR.
func allPRDepsSatisfied(client vcs.Client, repo string, deps []vcs.Dependency) bool {
	prDeps := vcs.FilterByType(deps, vcs.DependsOnPR)
	for _, d := range prDeps {
		exists, err := client.PRExistsForIssue(repo, d.IssueNumber)
		if err != nil || !exists {
			return false
		}
	}
	return true
}

// newIssueUnblockCmd is the manual unblock command:
//
//	clawflow issue unblock --repo owner/repo --issue 7
func newIssueUnblockCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:     "unblock",
		Short:   "Manually remove the blocked label from an issue",
		Example: "  clawflow issue unblock --repo owner/repo --issue 7",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repo)
			if err != nil {
				return err
			}
			if err := client.RemoveLabel(repo, issue, "blocked"); err != nil {
				return err
			}
			fmt.Printf("unblocked %s#%d\n", repo, issue)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

// newUnblockScanCmd scans all repos and unlocks any issues whose deps are met:
//
//	clawflow unblock-scan [--repo owner/repo]
func NewUnblockScanCmd() *cobra.Command {
	var repoFlag string

	cmd := &cobra.Command{
		Use:   "unblock-scan",
		Short: "Scan all repos and unlock issues whose dependencies are satisfied",
		Example: "  clawflow unblock-scan\n  clawflow unblock-scan --repo owner/repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := newVCSClientForRepo(repoFlag)
			if err != nil {
				return err
			}
			sw := &stderrWriter{cmd}
			TryUnlockDownstream(client, repoFlag, 0, vcs.DependsOnMerge, sw)
			TryUnlockDownstream(client, repoFlag, 0, vcs.DependsOnPR, sw)
			return nil
		},
	}
	cmd.Flags().StringVar(&repoFlag, "repo", "", "owner/repo (required)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

type stderrWriter struct{ cmd *cobra.Command }

func (s *stderrWriter) Printf(format string, args ...any) {
	fmt.Fprintf(s.cmd.ErrOrStderr(), format, args...)
}
