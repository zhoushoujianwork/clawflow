package commands

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewChatCmd() *cobra.Command {
	var (
		repo  string
		issue int
		model string
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Interactive AI chat with repo/issue context",
		Long: `Start an interactive Claude session with repository or issue context
automatically injected. Uses Claude's native session persistence — closing
and reopening the same repo/issue chat resumes the conversation.

Examples:
  clawflow chat --repo owner/repo
  clawflow chat --repo owner/repo --issue 42
  clawflow chat --repo owner/repo --issue 42 --model sonnet`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			return runChat(cmd.Context(), repo, issue, model)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository (owner/repo)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number for issue-level chat")
	cmd.Flags().StringVar(&model, "model", "haiku", "claude model to use")
	return cmd
}

func runChat(_ context.Context, repo string, issueNum int, model string) error {
	client, repoCfg, err := newVCSClientForRepo(repo)
	if err != nil {
		return err
	}

	// Deterministic session ID so same repo+issue always resumes
	sessionID := chatSessionID(repo, issueNum)

	// Resolve working directory to repo's local_path
	workdir := repoCfg.LocalPath
	if workdir == "" {
		workdir, _ = os.Getwd()
	}

	// Claude rejects --session-id <X> when the session already exists on
	// disk ("Session ID … is already in use"). Sessions live at
	// ~/.claude/projects/<encoded-cwd>/<id>.jsonl where encoded-cwd is
	// the workdir with "/" → "-". If the file is there from a prior
	// chat, --resume the existing session instead of trying to recreate.
	resuming := chatSessionExists(workdir, sessionID)

	// Session display name
	name := fmt.Sprintf("clawflow: %s", repo)
	if issueNum > 0 {
		name = fmt.Sprintf("clawflow: %s #%d", repo, issueNum)
	}

	args := []string{"--model", model, "--name", name}

	if resuming {
		// Resume the existing transcript. Don't re-inject the system
		// prompt — claude already has it from the original session, and
		// re-appending would duplicate the issue body on every reopen.
		args = append(args, "--resume", sessionID)
	} else {
		// First time for this repo+issue: create the session and seed it
		// with the repo/issue context as an appended system prompt.
		var systemCtx string
		if issueNum > 0 {
			systemCtx, err = buildIssueChatContext(client, repo, issueNum)
		} else {
			systemCtx, err = buildRepoChatContext(client, repo, repoCfg.Platform, repoCfg.BaseBranch)
		}
		if err != nil {
			return fmt.Errorf("build context: %w", err)
		}
		tmpFile, err := os.CreateTemp("", "clawflow-chat-ctx-*.md")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		defer os.Remove(tmpFile.Name())
		if _, err := tmpFile.WriteString(systemCtx); err != nil {
			tmpFile.Close()
			return err
		}
		tmpFile.Close()
		args = append(args,
			"--session-id", sessionID,
			"--append-system-prompt-file", tmpFile.Name(),
		)
	}

	bin := claude.Resolve()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	cmd.Env = claude.CleanedEnv(os.Environ())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// chatSessionID generates a deterministic UUID for a repo+issue pair so
// the same chat always resumes the same Claude session.
func chatSessionID(repo string, issue int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("clawflow-chat:%s:%d", repo, issue)))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// chatSessionExists reports whether claude already has a stored session
// transcript for `sessionID` under `cwd`. Claude lays out sessions as
// ~/.claude/projects/<cwd-with-slashes-as-dashes>/<id>.jsonl — when this
// file exists, --session-id refuses with "already in use" and the caller
// must --resume instead.
func chatSessionExists(cwd, sessionID string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	encoded := strings.ReplaceAll(cwd, "/", "-")
	path := filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
	_, err = os.Stat(path)
	return err == nil
}

func buildIssueChatContext(client vcs.Client, repo string, issueNum int) (string, error) {
	issues, err := client.ListOpenIssues(repo)
	if err != nil {
		return "", err
	}
	var issue vcs.Issue
	found := false
	for _, iss := range issues {
		if iss.Number == issueNum {
			issue = iss
			found = true
			break
		}
	}
	if !found {
		allIssues, err := client.ListIssues(repo, "all", nil)
		if err != nil {
			return "", fmt.Errorf("issue #%d not found", issueNum)
		}
		for _, iss := range allIssues {
			if iss.Number == issueNum {
				issue = iss
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("issue #%d not found", issueNum)
		}
	}

	comments, _ := client.ListIssueCommentsDetail(repo, issueNum)
	return chat.BuildIssueContext(repo, issue, comments), nil
}

func buildRepoChatContext(client vcs.Client, repo, platform, baseBranch string) (string, error) {
	if platform == "" {
		platform = "github"
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	issues, err := client.ListOpenIssues(repo)
	if err != nil {
		return "", err
	}
	return chat.BuildRepoContext(repo, platform, baseBranch, issues), nil
}
