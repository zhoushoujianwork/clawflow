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
	"github.com/zhoushoujianwork/clawflow/internal/project"
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
automatically injected. Each invocation creates a FRESH session and
fetches the latest issue body / labels / comments — close the terminal
window when you're done. Persistence is intentionally off: chat now
lives in the user's native terminal (no clawflow-side destroy hook),
and resuming would either strand stale issue data or have two claude
processes fighting over the same transcript file.

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
	// When the user has explicitly configured an API key in clawflow's
	// settings (typically pointing at a corporate proxy via
	// ANTHROPIC_BASE_URL), claude's default auth path picks the
	// keychain/OAuth login over the env var and silently ignores both
	// ANTHROPIC_API_KEY and ANTHROPIC_BASE_URL — sending the OAuth
	// token to the proxy which then 401s. --bare locks claude to
	// "ANTHROPIC_API_KEY only" mode so the proxy actually sees the key
	// the user configured. Trade-off: --bare also disables hooks, LSP,
	// plugins, auto-memory, and CLAUDE.md auto-discovery — we add
	// --add-dir for the workdir below to restore CLAUDE.md.
	preCreds, _ := config.LoadCredentials()
	useBare := preCreds != nil && preCreds.ClaudeAPIKey != ""
	if useBare {
		args = append(args, "--bare", "--add-dir", workdir)
	}

	// Always create a brand-new session, seeded with freshly fetched
	// repo/issue context. The resume branch was removed when chat moved
	// to the user's native terminal — see the comment on sessionID
	// above for the full rationale.
	var systemCtx string
	if issueNum > 0 {
		systemCtx, err = buildIssueChatContext(client, repo, issueNum)
	} else {
		systemCtx, err = buildRepoChatContext(client, repo, repoCfg.Platform, repoCfg.BaseBranch)
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
	// claude CLI 2.x dropped --append-system-prompt-file; the prompt
	// must be passed inline via --append-system-prompt. ARG_MAX is
	// large enough for the 5–20KB context we build.
	args = append(args,
		"--session-id", sessionID,
		"--append-system-prompt", systemCtx,
	)

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
	// Print a one-line provenance banner before claude takes over the
	// terminal so the user can confirm which credentials this session
	// will use. Same hint format the Settings page uses (last 4 chars).
	// "(none — falling back to OAuth/keychain)" makes the inherit case
	// obvious instead of silent.
	keyHint := "(none — falling back to OAuth/keychain)"
	if apiKey != "" {
		if n := len(apiKey); n >= 4 {
			keyHint = "…" + apiKey[n-4:]
		} else {
			keyHint = "(set, <4 chars)"
		}
	}
	urlHint := baseURL
	if urlHint == "" {
		urlHint = "(default — api.anthropic.com)"
	}
	bareNote := ""
	if useBare {
		bareNote = " --bare (forced, API key takes priority over claude.ai login)"
	}
	fmt.Fprintf(os.Stderr, "[clawflow] chat → model=%s key=%s base_url=%s%s\n", model, keyHint, urlHint, bareNote)
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
