package commands

import (
	"fmt"
	"os"
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
		Short: "Write a processing record for an issue",
		Example: `  clawflow memory write --repo owner/repo --issue 7 --status success --pr-url https://...
  clawflow memory write --repo owner/repo --issue 7 --status failed --reason "timeout"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if status != "success" && status != "failed" && status != "skipped" {
				return fmt.Errorf("--status must be success, failed, or skipped")
			}

			slug := config.RepoSlug(repo)
			dir := config.MemoryDir(slug)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("cannot create memory dir: %w", err)
			}

			path := fmt.Sprintf("%s/issue-%d.md", dir, issue)
			content := buildMemoryRecord(repo, issue, status, prURL, reason)

			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("cannot write memory record: %w", err)
			}

			fmt.Printf("memory record written: %s\n", path)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number (required)")
	cmd.Flags().StringVar(&status, "status", "", "success | failed | skipped (required)")
	cmd.Flags().StringVar(&prURL, "pr-url", "", "PR URL (for success status)")
	cmd.Flags().StringVar(&reason, "reason", "", "reason (for failed/skipped status)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("status")
	return cmd
}

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
