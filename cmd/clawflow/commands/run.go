package commands

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// NewRunCmd wraps `claude -p "ClawFlow run"` with a task payload piped on
// stdin, so the SaaS WorkerLoop can shell out to a stable clawflow interface
// instead of re-implementing the Claude invocation itself. Same entrypoint the
// CLI worker loop and the old cron-run.sh script used.
func NewRunCmd() *cobra.Command {
	var (
		repo   string
		issue  int
		token  string
		branch string
		base   string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute the ClawFlow pipeline for a single issue (invokes Claude)",
		Long: `Invokes 'claude -p "ClawFlow run"' with a task payload on stdin.
Used by the SaaS WorkerLoop; not typically run by hand.

On success, prints 'PR_URL=<url>' on its own line so callers can parse it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" || issue == 0 || token == "" {
				return fmt.Errorf("--repo, --issue and --token are required")
			}

			payload := map[string]interface{}{
				"repo":         repo,
				"issue_number": issue,
				"token":        token,
				"branch":       branch,
				"base_branch":  base,
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("marshal payload: %w", err)
			}

			// Load ~/.clawflow/config/env so ANTHROPIC_BASE_URL / AUTH_TOKEN are
			// available to claude — mirrors cron-run.sh's `source $ENV_FILE`
			// so operators have one canonical place to configure the runtime.
			loadEnvFile()

			claudeCmd := exec.Command("claude", "-p", "--dangerously-skip-permissions", "ClawFlow run")
			claudeCmd.Stdin = bytes.NewReader(body)
			claudeCmd.Env = os.Environ()

			// Tee stdout so we can stream to the caller (systemd/SaaS) AND scan
			// for the PR URL at the end. stderr inherits directly.
			var stdoutBuf bytes.Buffer
			claudeCmd.Stdout = io.MultiWriter(os.Stdout, &stdoutBuf)
			claudeCmd.Stderr = os.Stderr

			if err := claudeCmd.Run(); err != nil {
				return fmt.Errorf("claude: %w", err)
			}

			if url := extractPRURL(stdoutBuf.String()); url != "" {
				fmt.Printf("PR_URL=%s\n", url)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Repository full_name (e.g. owner/repo)")
	cmd.Flags().IntVar(&issue, "issue", 0, "Issue number to process")
	cmd.Flags().StringVar(&token, "token", "", "VCS access token (GitHub/GitLab)")
	cmd.Flags().StringVar(&branch, "branch", "", "Feature branch name (e.g. fix/issue-42)")
	cmd.Flags().StringVar(&base, "base", "main", "Base branch for the PR")

	return cmd
}

// loadEnvFile reads ~/.clawflow/config/env and sets each `export KEY=VALUE`
// line into the current process environment. Silently skips if the file is
// missing. Strips surrounding quotes and a leading `export ` so the format
// matches what cron-run.sh sources with bash.
func loadEnvFile() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(home, ".clawflow", "config", "env")
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		if key != "" {
			_ = os.Setenv(key, val)
		}
	}
}
