package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage project-level context documents",
		Long: `Manage project-level context.md documents stored in ~/.clawflow/projects/<name>/.
These documents capture a project's purpose, architecture, conventions, and
current state. Use 'project chat' to refine them interactively with Claude.`,
	}
	cmd.AddCommand(newProjectInitCmd())
	cmd.AddCommand(newProjectListCmd())
	cmd.AddCommand(newProjectChatCmd())
	cmd.AddCommand(newProjectShowCmd())
	return cmd
}

func newProjectInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <name>",
		Short: "Initialize a new project with a starter context.md",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			existing, err := chat.LoadProjectContext(name)
			if err != nil {
				return err
			}
			if existing != "" {
				return fmt.Errorf("project %q already has a context.md; use 'clawflow project chat %s' to edit it", name, name)
			}
			starter := fmt.Sprintf("# %s\n\n## Overview\n\n_Describe the project here._\n\n## Architecture\n\n## Conventions\n\n## Current State\n", name)
			if err := chat.SaveProjectContext(name, starter); err != nil {
				return err
			}
			path, _ := chat.ProjectContextPath(name)
			fmt.Fprintf(os.Stderr, "Created %s\n", path)
			fmt.Fprintf(os.Stderr, "Edit it directly or run: clawflow project chat %s\n", name)
			return nil
		},
	}
}

func newProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all projects with a context.md",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			names, err := chat.ListProjects()
			if err != nil {
				return err
			}
			if len(names) == 0 {
				fmt.Println("No projects found.")
				fmt.Println("Create one with: clawflow project init <name>")
				return nil
			}
			for _, n := range names {
				fmt.Println(n)
			}
			return nil
		},
	}
}

func newProjectShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print the current context.md for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := chat.LoadProjectContext(args[0])
			if err != nil {
				return err
			}
			if content == "" {
				return fmt.Errorf("project %q has no context.md; run 'clawflow project init %s' first", args[0], args[0])
			}
			fmt.Print(content)
			return nil
		},
	}
}

func newProjectChatCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "chat <name>",
		Short: "Interactive Claude session to refine a project's context.md",
		Long: `Start an interactive Claude session with the project's context.md loaded
as system context. When the session ends, if Claude produced an updated
context.md (in a fenced code block tagged "context.md"), you'll be shown
a preview and asked whether to save it back.

Example:
  clawflow project chat my-project`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectChat(cmd.Context(), args[0], model)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "claude model to use (default: settings chat model)")
	return cmd
}

func runProjectChat(_ context.Context, name, model string) error {
	// Load existing context.md (may be empty for a new project).
	originalCtx, err := chat.LoadProjectContext(name)
	if err != nil {
		return err
	}
	if originalCtx == "" {
		fmt.Fprintf(os.Stderr, "[clawflow] project %q has no context.md yet — starting with an empty document.\n", name)
		fmt.Fprintf(os.Stderr, "[clawflow] Tip: run 'clawflow project init %s' first for a starter template.\n", name)
	}

	// Resolve model
	if model == "" {
		creds, _ := config.LoadCredentials()
		model = creds.EffectiveChatModel()
	}

	// Build system prompt
	systemCtx := chat.BuildProjectChatContext(name, originalCtx)
	tmpFile, err := os.CreateTemp("", "clawflow-project-ctx-*.md")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(systemCtx); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	// Session setup — fresh session each time, same pattern as chat.go
	sessionID := chat.NewSessionID("project/"+name, 0)
	sessionName := fmt.Sprintf("clawflow: project/%s", name)

	// Working directory: use a temp dir since project chat is not repo-bound
	workdir := os.TempDir()

	args := []string{
		"--model", model,
		"--name", sessionName,
		"--output-format", "stream-json",
		"--session-id", sessionID,
		"--append-system-prompt-file", tmpFile.Name(),
	}

	// When the user has an API key configured, use --bare mode (same logic as chat.go)
	preCreds, _ := config.LoadCredentials()
	useBare := preCreds != nil && preCreds.ClaudeAPIKey != ""
	if useBare {
		args = append(args, "--bare", "--add-dir", workdir)
	}

	bin := claude.Resolve()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir

	creds, _ := config.LoadCredentials()
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)

	// Print provenance banner (same pattern as chat.go)
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
	fmt.Fprintf(os.Stderr, "[clawflow] project chat → model=%s key=%s base_url=%s%s\n", model, keyHint, urlHint, bareNote)

	// Connect stdin directly so the user can interact.
	cmd.Stdin = os.Stdin

	// Capture stdout (stream-json) while also displaying it to the user via
	// stderr. stream-json lines are machine-readable, so we parse them and
	// echo a human-friendly rendering to stderr for the interactive experience.
	var capturedOutput bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	// Read stream-json from stdout, display assistant text to stderr, and
	// capture everything for post-session extraction.
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- streamAndCapture(stdoutPipe, os.Stderr, &capturedOutput)
	}()

	// Wait for the stream reader to finish before waiting on the process,
	// so we don't miss any output.
	if err := <-streamDone; err != nil {
		debugf("stream reader error: %v", err)
	}

	runErr := cmd.Wait()

	// Even if claude exited non-zero (e.g. user Ctrl-C), we still try to
	// extract and offer write-back from whatever output was captured.
	if runErr != nil {
		debugf("claude exited with: %v", runErr)
	}

	// Extract the last context.md block from the captured stream-json output.
	captured := capturedOutput.String()
	newCtx := chat.ExtractLastContextMD(captured)

	if newCtx == "" {
		fmt.Fprintln(os.Stderr, "\n[clawflow] No updated context.md found in the session output.")
		fmt.Fprintln(os.Stderr, "[clawflow] Tip: ask Claude to output the final document in a ```context.md code block.")
		return runErr
	}

	// Ensure the extracted content ends with a newline
	if !strings.HasSuffix(newCtx, "\n") {
		newCtx += "\n"
	}

	// Show preview
	fmt.Fprintln(os.Stderr, "\n[clawflow] ─── Updated context.md preview ───")
	fmt.Fprintln(os.Stderr, newCtx)
	fmt.Fprintln(os.Stderr, "[clawflow] ─── End preview ───")

	if originalCtx == newCtx {
		fmt.Fprintln(os.Stderr, "[clawflow] No changes detected — context.md is unchanged.")
		return runErr
	}

	// Prompt for confirmation
	fmt.Fprint(os.Stderr, "[clawflow] Save updated context.md? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))

	if answer == "y" || answer == "yes" {
		if err := chat.SaveProjectContext(name, newCtx); err != nil {
			return fmt.Errorf("write context.md: %w", err)
		}
		path, _ := chat.ProjectContextPath(name)
		fmt.Fprintf(os.Stderr, "[clawflow] Saved → %s\n", path)
	} else {
		fmt.Fprintln(os.Stderr, "[clawflow] Discarded — context.md unchanged.")
	}

	return runErr
}

// streamAndCapture reads stream-json lines from r, writes them to captured,
// and renders a human-friendly version of assistant text to display.
func streamAndCapture(r io.Reader, display io.Writer, captured *bytes.Buffer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		// Always capture the raw line
		captured.Write(line)
		captured.WriteByte('\n')

		// Try to extract displayable text for the user
		displayStreamLine(line, display)
	}
	return scanner.Err()
}

// displayStreamLine parses a single stream-json line and writes any
// assistant text content to w for the user to see.
func displayStreamLine(line []byte, w io.Writer) {
	// Quick pre-check to avoid parsing non-text events
	if !bytes.Contains(line, []byte(`"text"`)) && !bytes.Contains(line, []byte(`"assistant"`)) {
		return
	}

	var evt struct {
		Type         string `json:"type"`
		Role         string `json:"role,omitempty"`
		Content      string `json:"content,omitempty"`
		ContentBlock *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content_block,omitempty"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta,omitempty"`
		Message *struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message,omitempty"`
	}

	if err := json.Unmarshal(line, &evt); err != nil {
		return
	}

	// content_block_delta with text — the most common streaming event
	if evt.Delta != nil && evt.Delta.Type == "text_delta" {
		fmt.Fprint(w, evt.Delta.Text)
		return
	}

	// Full content_block
	if evt.ContentBlock != nil && evt.ContentBlock.Type == "text" {
		fmt.Fprint(w, evt.ContentBlock.Text)
		return
	}

	// Inline assistant content
	if evt.Role == "assistant" && evt.Content != "" {
		fmt.Fprint(w, evt.Content)
		return
	}

	// Message wrapper
	if evt.Message != nil && evt.Message.Role == "assistant" {
		for _, cb := range evt.Message.Content {
			if cb.Type == "text" {
				fmt.Fprint(w, cb.Text)
			}
		}
	}
}
