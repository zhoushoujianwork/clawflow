package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Read and write issue processing records",
	}
	cmd.AddCommand(newMemoryWriteCmd())
	cmd.AddCommand(newMemoryReadCmd())
	return cmd
}

func newMemoryWriteCmd() *cobra.Command {
	var repo string
	var issue int
	var status string
	var prURL string
	var reason string

	cmd := &cobra.Command{
		Use:   "write",
		Short: "Append a processing record for an issue",
		Example: `  clawflow memory write --repo owner/repo --issue 7 --status success --pr-url https://...
  clawflow memory write --repo owner/repo --issue 7 --status failed --reason "timeout"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if status != "success" && status != "failed" && status != "skipped" && status != "ci-failed" {
				return fmt.Errorf("--status must be success, failed, skipped, or ci-failed")
			}

			slug := config.RepoSlug(repo)
			dir := config.MemoryDir(slug)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("cannot create memory dir: %w", err)
			}

			path := fmt.Sprintf("%s/issue-%d.md", dir, issue)

			// Determine attempt number by reading existing file
			attemptNum := 1
			if existing, err := os.ReadFile(path); err == nil {
				attemptNum = countAttempts(string(existing)) + 1
			}

			section := buildAttemptSection(attemptNum, status, prURL, reason)

			var content string
			if attemptNum == 1 {
				// First attempt: write header + section
				content = fmt.Sprintf("# %s#%d\n\n%s", repo, issue, section)
			} else {
				// Subsequent attempts: append section
				existing, _ := os.ReadFile(path)
				content = strings.TrimRight(string(existing), "\n") + "\n\n" + section
			}

			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("cannot write memory record: %w", err)
			}

			fmt.Printf("memory record written (attempt %d): %s\n", attemptNum, path)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().StringVar(&status, "status", "", "success | failed | skipped | ci-failed (required)")
	cmd.Flags().StringVar(&prURL, "pr-url", "", "PR URL (for success status)")
	cmd.Flags().StringVar(&reason, "reason", "", "reason (for failed/skipped status)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

func newMemoryReadCmd() *cobra.Command {
	var repo string
	var issue int

	cmd := &cobra.Command{
		Use:   "read",
		Short: "Read the memory record for an issue",
		Example: `  clawflow memory read --repo owner/repo --issue 7`,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := config.RepoSlug(repo)
			path := fmt.Sprintf("%s/issue-%d.md", config.MemoryDir(slug), issue)
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				fmt.Println("(no memory record)")
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Print(string(data))
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

// countAttempts counts how many "## Attempt N" sections exist in content.
func countAttempts(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## Attempt ") {
			count++
		}
	}
	return count
}

// ReadMemoryFile reads the raw memory file for an issue, returning empty string if not found.
func ReadMemoryFile(repo string, issue int) string {
	slug := config.RepoSlug(repo)
	path := fmt.Sprintf("%s/issue-%d.md", config.MemoryDir(slug), issue)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// HasMergedPRInMemory returns true if the memory record contains a success entry
// (indicating a previously merged PR).
func HasMergedPRInMemory(repo string, issue int) bool {
	content := ReadMemoryFile(repo, issue)
	return strings.Contains(content, "**Status:** success")
}

func buildAttemptSection(attempt int, status, prURL, reason string) string {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	var details string
	switch status {
	case "success":
		details = fmt.Sprintf("**PR:** %s", prURL)
	default:
		details = fmt.Sprintf("**Reason:** %s", reason)
	}

	return fmt.Sprintf("## Attempt %d\n**Status:** %s  \n**Time:** %s  \n%s\n",
		attempt, status, ts, details)
}

// buildMemoryRecord is kept for backward compatibility but no longer used internally.
func buildMemoryRecord(repo string, issue int, status, prURL, reason string) string {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	switch status {
	case "success":
		return fmt.Sprintf("# %s#%d\n\n**Status:** success  \n**Time:** %s  \n**PR:** %s\n", repo, issue, ts, prURL)
	case "failed":
		return fmt.Sprintf("# %s#%d\n\n**Status:** failed  \n**Time:** %s  \n**Reason:** %s\n", repo, issue, ts, reason)
	default:
		return fmt.Sprintf("# %s#%d\n\n**Status:** %s  \n**Time:** %s  \n**Reason:** %s\n", repo, issue, status, ts, reason)
	}
}
