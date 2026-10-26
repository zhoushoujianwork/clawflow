package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewChatCmd() *cobra.Command {
	var (
		repo  string
		issue int
		model string
		mode  string
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Interactive AI chat with repo/issue context",
		Long: `Start a plain text AGH conversation with repository or issue context.
Each invocation fetches current context and creates a fresh logical session.
Enter one message per line; later turns resume that same session.
Use /exit, EOF or Ctrl-C to leave. Native TUI and image paste are not supported.

Issue-level chat supports two modes (--mode flag):
  issue  (default) — discussion and issue-tracker operations only.
                     Edit/Write/NotebookEdit are disabled. The AI is
                     prompted to land conclusions as comments, labels,
                     or sub-issues. Safer default: prevents accidental
                     file edits during requirements discussions.
  edit             — full file-editing access (historical behaviour).
                     Use when you want to hot-fix the issue directly
                     without going through clawflow run.

The default mode can be persisted in ~/.clawflow/config/credentials.yaml
as chat_default_mode: issue|edit. The --mode flag overrides it per session.

Examples:
  clawflow chat --repo owner/repo
  clawflow chat --repo owner/repo --issue 42
  clawflow chat --repo owner/repo --issue 42 --mode edit
  clawflow chat --repo owner/repo --issue 42 --model sonnet`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			// Validate --mode when explicitly provided.
			if mode != "" && mode != "issue" && mode != "edit" {
				return fmt.Errorf("--mode must be 'issue' or 'edit', got %q", mode)
			}
			return runChat(cmd.Context(), repo, issue, model, mode)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository (owner/repo)")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number for issue-level chat")
	// Empty default means "use whatever the first enabled provider
	// configured in its chat slot" — resolved below via
	// config.ResolveModelForRole. An explicit --model on the CLI still wins.
	cmd.Flags().StringVar(&model, "model", "", "model to request from AGH (default: configured chat model)")
	// Empty default means "use chat_default_mode from credentials.yaml" —
	// resolved below via Credentials.EffectiveChatDefaultMode(). Only
	// applies to issue-level chat (--issue N); repo-level chat is always
	// read-only regardless of this flag.
	cmd.Flags().StringVar(&mode, "mode", "", "issue chat mode: 'issue' (default, no file edits) or 'edit' (full access)")
	return cmd
}

func runChat(ctx context.Context, repo string, issueNum int, model string, modeFlag string) error {
	client, canonRepo, repoCfg, err := newVCSClientForRepo(repo)
	if err != nil {
		return err
	}
	repo = canonRepo

	// Per-launch session id (timestamp-seeded). Two reasons we don't
	// reuse the deterministic SessionID-by-(repo, issue) anymore:
	//   1. Each spawn now refetches issue data, so resuming an old
	//      transcript would drag stale labels/comments forward.
	//   2. Chat runs in the user's native terminal — clawflow web has
	//      no kill switch — so two clicks while the first window is
	//      still open would pit two claude processes against the same
	//      jsonl ("Session ID already in use" or worse).
	sessionID := chat.NewSessionID(repo, issueNum)

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

	// Resolve the model: explicit --model > first enabled provider's
	// chat slot > built-in default. We resolve here rather than at
	// flag-parse time so a provider edit takes effect on the next chat
	// without rebuilding the binary.
	if model == "" {
		creds, _ := config.LoadCredentials()
		model = config.ResolveModelForRole(creds, config.RoleChat)
	}

	// Resolve the issue chat mode: explicit --mode > credentials.yaml
	// chat_default_mode > built-in default ("issue"). Resolved for both
	// issue- and repo-level chat so the title can show the value — note
	// that for repo-level chat the flag has no effect on behaviour (repo
	// chat is always read-only; see --disallowedTools logic below).
	if modeFlag == "" {
		creds, _ := config.LoadCredentials()
		modeFlag = creds.EffectiveChatDefaultMode()
	}

	// Always create a brand-new session, seeded with freshly fetched
	// repo/issue context. The resume branch was removed when chat moved
	// to the user's native terminal — see the comment on sessionID
	// above for the full rationale.
	// Resolve language preference for AI output.
	var lang string
	if globalCfg, cfgErr := config.Load(); cfgErr == nil {
		lang = globalCfg.Settings.Language
	}

	var systemCtx string
	if issueNum > 0 {
		systemCtx, err = buildIssueChatContext(client, repo, issueNum, modeFlag, lang)
	} else {
		systemCtx, err = buildRepoChatContext(client, repo, repoCfg.Platform, repoCfg.BaseBranch, lang)
	}
	if err != nil {
		return fmt.Errorf("build context: %w", err)
	}

	// Auto-inject project context (context.md + testing.md) when this
	// repo belongs to a project. Uses the same helper as the operator
	// runner so chat and operators see identical project framing.
	if header := project.HeaderForRepo(repo); header != "" {
		systemCtx = header + systemCtx
	}
	disallowed := []string{}
	if issueNum == 0 || modeFlag != "edit" {
		disallowed = []string{"Edit", "Write", "NotebookEdit"}
	}
	return execution.Chat(ctx, os.Stdin, os.Stdout, execution.Options{Workspace: workdir, Model: model, Role: config.RoleChat, SessionKey: sessionID, Instructions: systemCtx, PermissionMode: "bypassPermissions", DisallowedTools: disallowed})
}

func buildIssueChatContext(client vcs.Client, repo string, issueNum int, mode string, lang string) (string, error) {
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
	if mode == "edit" {
		return chat.BuildIssueContext(repo, issue, comments, lang), nil
	}
	return chat.BuildIssueModeContext(repo, issue, comments, lang), nil
}

func buildRepoChatContext(client vcs.Client, repo, platform, baseBranch string, lang string) (string, error) {
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
	return chat.BuildRepoContext(repo, platform, baseBranch, issues, lang), nil
}
