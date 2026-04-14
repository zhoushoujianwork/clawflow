package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	gh "github.com/zhoushoujianwork/clawflow/internal/github"
)

func NewRetryCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:   "retry",
		Short: "Re-trigger the pipeline for a previously processed issue",
		Example: `  clawflow retry --repo owner/repo --issue 7`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 1. Remove blocking labels so issue re-enters to_evaluate
			labelsToRemove := []string{"agent-evaluated", "agent-failed", "agent-skipped"}
			for _, label := range labelsToRemove {
				if err := gh.RemoveLabel(repo, issue, label); err != nil {
					// Ignore "label not found" errors — label may not exist
					if !isLabelNotFoundErr(err) {
						fmt.Fprintf(os.Stderr, "warn: cannot remove label %q: %v\n", label, err)
					}
				} else {
					fmt.Printf("label %q removed from %s#%d\n", label, repo, issue)
				}
			}

			// 2. Read existing memory to find previous PR URL
			prevPRURL := extractLastPRURL(ReadMemoryFile(repo, issue))

			// 3. Append retry entry to memory record
			slug := config.RepoSlug(repo)
			dir := config.MemoryDir(slug)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("cannot create memory dir: %w", err)
			}
			path := fmt.Sprintf("%s/issue-%d.md", dir, issue)

			attemptNum := 1
			if existing, err := os.ReadFile(path); err == nil {
				attemptNum = countAttempts(string(existing)) + 1
			}

			ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
			section := fmt.Sprintf("## Attempt %d\n**Status:** in-progress  \n**Time:** %s  \n**Reason:** retry initiated\n",
				attemptNum, ts)

			var content string
			if attemptNum == 1 {
				content = fmt.Sprintf("# %s#%d\n\n%s", repo, issue, section)
			} else {
				existing, _ := os.ReadFile(path)
				content = strings.TrimRight(string(existing), "\n") + "\n\n" + section
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "warn: cannot write memory record: %v\n", err)
			}

			// 4. Post comment on the issue
			commentBody := buildRetryComment(prevPRURL)
			if err := gh.PostIssueComment(repo, issue, commentBody); err != nil {
				fmt.Fprintf(os.Stderr, "warn: cannot post comment: %v\n", err)
			}

			fmt.Printf("retry initiated for %s#%d — issue will re-enter to_evaluate on next harvest\n", repo, issue)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

func buildRetryComment(prevPRURL string) string {
	prev := "(none)"
	if prevPRURL != "" {
		prev = prevPRURL
	}
	return fmt.Sprintf("## 🔄 ClawFlow Retry Initiated\n\nThis issue has been re-queued for processing.\n\n**Previous attempt:** %s\n\nThe issue will re-enter the evaluation queue on the next harvest cycle.\n\n---\n🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow)", prev)
}

// extractLastPRURL finds the last PR URL in a memory record.
func extractLastPRURL(content string) string {
	last := ""
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "**PR:**") {
			last = strings.TrimSpace(strings.TrimPrefix(line, "**PR:**"))
		}
	}
	return last
}

// isLabelNotFoundErr returns true if the error indicates the label doesn't exist on the issue.
func isLabelNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Label does not exist") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "Could not remove label")
}
