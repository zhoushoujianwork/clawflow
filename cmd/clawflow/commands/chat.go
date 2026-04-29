package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
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
	// Empty default means "use whatever the settings page configured" —
	// resolved below via Credentials.EffectiveChatModel(). An explicit
	// --model on the CLI still wins.
	cmd.Flags().StringVar(&model, "model", "", "claude model to use (default: settings → chat_model, falls back to haiku)")
	return cmd
}

func runChat(_ context.Context, repo string, issueNum int, model string) error {
	client, repoCfg, err := newVCSClientForRepo(repo)
	if err != nil {
		return err
	}

	// Deterministic session ID so same repo+issue always resumes
	sessionID := chat.SessionID(repo, issueNum)

	// Working directory: prefer the repo's local clone so claude can
	// read code in-place. When no local_path is configured we fall
	// back to a clean temp dir — NOT os.Getwd(), because the chat is
	// usually launched from `clawflow web`'s cwd (the clawflow source
	// tree itself), which would let claude read THAT repo's CLAUDE.md
	// and mistake it for the chat target.
	workdir := repoCfg.LocalPath
	if workdir == "" {
		workdir = os.TempDir()
	}

	// Claude rejects --session-id <X> when the session already exists on
	// disk ("Session ID … is already in use"). Sessions live at
	// ~/.claude/projects/<encoded-cwd>/<id>.jsonl where encoded-cwd is
	// the workdir with "/" → "-". If the file is there from a prior
	// chat, --resume the existing session instead of trying to recreate.
	resuming := chat.SessionExists(workdir, sessionID)

	// Session display name
	name := fmt.Sprintf("clawflow: %s", repo)
	if issueNum > 0 {
		name = fmt.Sprintf("clawflow: %s #%d", repo, issueNum)
	}

	// Resolve the model: explicit --model > settings (chat slot) >
	// built-in default. We resolve here rather than at flag-parse time
	// so a settings-page change takes effect on the next chat without
	// rebuilding the binary.
	if model == "" {
		creds, _ := config.LoadCredentials()
		model = creds.EffectiveChatModel()
	}

	// Hard-block file mutations and notebook edits. The chat is
	// strictly an analysis / planning assistant — code changes go
	// through `clawflow run` (the implement operator) on a labeled
	// issue, not from this REPL. Read/Bash/Grep/Glob/etc. stay
	// allowed so claude can still inspect the repo to inform its
	// analysis.
	args := []string{
		"--model", model,
		"--name", name,
		"--disallowedTools", "Edit,Write,NotebookEdit",
	}

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
	// LoadCredentials missing/unreadable falls through to empty
	// strings, which EnvWithCredentials treats as "don't override"
	// — same behavior as before for users with no custom claude
	// config.
	creds, _ := config.LoadCredentials()
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
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
