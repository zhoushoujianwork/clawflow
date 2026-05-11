package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	githubpkg "github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// feedbackTargetRepo is the upstream ClawFlow repo that `clawflow feedback`
// posts issues to. Hardcoded on purpose — the command is specifically for
// reporting bugs/features about ClawFlow itself, not a user-chosen target.
const feedbackTargetRepo = "zhoushoujianwork/clawflow"

// NewFeedbackCmd wires up the two subcommands:
//   - `clawflow feedback` (default) — interactive Claude session that helps
//     the user describe their bug/feature and then files it.
//   - `clawflow feedback submit` — hidden, invoked by Claude during the
//     session. Posts directly to the upstream repo via the user's GH_TOKEN,
//     bypassing the local repo config so we don't need zhoushoujianwork/clawflow
//     to be in the user's monitored repo list.
func NewFeedbackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feedback",
		Short: "File a bug or feature request about ClawFlow itself",
		Long: `Launch an interactive Claude session that helps you file one or more issues on
https://github.com/zhoushoujianwork/clawflow.

Claude asks a few questions to turn your description into a clear title +
body, then files the issue using your locally-stored GitHub token. After each
submission Claude asks if you have more feedback — you can file multiple issues
in a single session without restarting. If your terminal supports pasting
images (VS Code integrated terminal, iTerm2, Claude Code's native terminal),
Claude will see the screenshot and describe it in the issue body.`,
		Example: "  clawflow feedback",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFeedback(cmd.Context())
		},
	}
	cmd.AddCommand(newFeedbackSubmitCmd())
	return cmd
}

func runFeedback(_ context.Context) error {
	creds, _ := config.LoadCredentials()
	if creds == nil || creds.GHToken == "" {
		return fmt.Errorf("no GitHub token configured — run `clawflow config set-token <ghp_...>` first")
	}

	model := creds.EffectiveChatModel()
	systemPrompt := buildFeedbackSystemPrompt()

	args := []string{
		"--model", model,
		"--name", "clawflow: feedback",
		"--dangerously-skip-permissions",
		// Tight allowlist: Bash for `clawflow feedback submit` and
		// `clawflow --version`; Read so the user can point Claude at a
		// local log file. No Edit/Write/NotebookEdit — nothing about
		// filing feedback should touch the user's files.
		"--disallowedTools", "Edit,Write,NotebookEdit",
	}

	useBare := creds.ClaudeAPIKey != ""
	if useBare {
		// Same rationale as chat.go: when the user has configured an
		// API key (often against a corporate proxy), lock claude to
		// ANTHROPIC_API_KEY-only mode so the proxy sees the real key.
		// --bare disables auto-memory / plugins / hooks, which is fine
		// for this one-shot feedback chat.
		args = append(args, "--bare")
	}

	args = append(args, "--append-system-prompt", systemPrompt)

	bin := claude.Resolve()
	cmd := exec.Command(bin, args...)
	// No workdir matters for a feedback chat — claude doesn't read
	// project files. Use tempdir so claude doesn't accidentally pick up
	// CLAUDE.md from whatever cwd clawflow was launched from.
	cmd.Dir = os.TempDir()

	apiKey, baseURL := creds.ClaudeAPIKey, creds.ClaudeBaseURL
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)

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
	fmt.Fprintf(os.Stderr, "[clawflow] feedback → model=%s key=%s base_url=%s target=%s\n",
		model, keyHint, urlHint, feedbackTargetRepo)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildFeedbackSystemPrompt() string {
	return `You are helping the user file GitHub issues on the ClawFlow repository:
https://github.com/` + feedbackTargetRepo + `

## Your job

1. Greet the user briefly and ask what they'd like to report — a bug, a
   feature request, a question, or general feedback.
2. Collect enough information to make the issue actionable:
   - **Bug:** what they were doing, what they expected, what happened,
     which ClawFlow version (run ` + "`clawflow --version`" + ` via Bash if they don't know).
   - **Feature:** what problem it solves, rough shape of the solution,
     whether a workaround exists today.
   - **Question / feedback:** just their description.
3. If the user pastes a screenshot, **describe what the screenshot shows
   in plain text** — GitHub's API does not support image uploads, so we
   have to translate visuals into words. Place the description under a
   ` + "`### Screenshot description`" + ` heading in the body.
4. Once you have a clear title and body, summarise them back to the user
   in one message and ask for confirmation before filing.
5. On confirmation, call ` + "`clawflow feedback submit --title '...' --body '...'`" + `
   via Bash. Print the resulting issue URL to the user.
6. After a successful submit, ask the user: "Is there anything else you'd
   like to file?" If yes, go back to step 2 and treat it as a fresh issue —
   do not carry over the previous issue's title or body unless the user
   explicitly references them. If no, thank the user and end the session.

## Rules

- Keep the conversation tight — 2 or 3 back-and-forths should be enough
  for most reports. Don't interrogate the user.
- If the user has already described the problem fully in their first
  message, you can skip straight to summarising and confirming.
- The title should be concise (ideally under 70 chars) and lead with a
  tag: ` + "`bug:`" + `, ` + "`feature:`" + `, or ` + "`question:`" + `.
- The body should use GitHub-flavoured markdown. For bug reports, include
  sections for **Steps to reproduce**, **Expected**, **Actual**, and
  **Environment** when applicable.
- Do not file any issue without explicit user confirmation.
- Treat each issue independently — screenshots or details from a previous
  issue in this session do not apply to the next unless the user says so.
- If the submit command fails, show the error to the user and offer to
  retry or adjust the title/body.`
}

func newFeedbackSubmitCmd() *cobra.Command {
	var title, body string

	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Post a feedback issue to zhoushoujianwork/clawflow (invoked by `clawflow feedback`)",
		Long: `Post an issue directly to https://github.com/zhoushoujianwork/clawflow
using the GitHub token in ~/.clawflow/config/credentials.yaml. Bypasses the
local repo config — the upstream repo does not need to be in your monitored
repo list.

This command is normally invoked by Claude during a ` + "`clawflow feedback`" + `
session; run it manually only if you have a specific reason to bypass the
interactive flow.`,
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			creds, err := config.LoadCredentials()
			if err != nil {
				return fmt.Errorf("load credentials: %w", err)
			}
			if creds == nil || creds.GHToken == "" {
				return fmt.Errorf("no GitHub token configured — run `clawflow config set-token <ghp_...>` first")
			}

			// Build the GitHub client directly (no config lookup) so
			// the upstream repo doesn't need to be in cfg.Repos.
			gh := githubpkg.New(creds.GHToken, "")
			issue, err := gh.CreateIssue(feedbackTargetRepo, title, body)
			if err != nil {
				return fmt.Errorf("create issue: %w", err)
			}
			fmt.Printf("filed: https://github.com/%s/issues/%d\n", feedbackTargetRepo, issue.Number)
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "issue title (required)")
	cmd.Flags().StringVar(&body, "body", "", "issue body (markdown)")
	return cmd
}
