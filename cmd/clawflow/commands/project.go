package commands

import (
	"bufio"
	"bytes"
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
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// NewProjectCmd returns the `clawflow project` parent command with all
// subcommands for managing multi-repo project groupings.
func NewProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage multi-repo project groupings",
		Long: `Projects group multiple repos under a single name and carry a shared
context.md that is auto-injected into every 'clawflow chat' session
for any member repo. This gives the AI cross-repo awareness without
manual intervention.

Storage: ~/.clawflow/projects/<name>/project.yaml + context.md`,
	}
	cmd.AddCommand(newProjectCreateCmd())
	cmd.AddCommand(newProjectListCmd())
	cmd.AddCommand(newProjectShowCmd())
	cmd.AddCommand(newProjectAddRepoCmd())
	cmd.AddCommand(newProjectRemoveRepoCmd())
	cmd.AddCommand(newProjectGenerateCmd())
	cmd.AddCommand(newProjectChatCmd())
	cmd.AddCommand(newProjectDeleteCmd())
	return cmd
}

func newProjectCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Create(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("project %q created\n", p.Name)
			fmt.Printf("  dir: %s\n", project.ContextPath(p.Name))
			fmt.Println("  next: clawflow project add-repo", p.Name, "<owner/repo>")
			return nil
		},
	}
}

func newProjectListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all projects",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			projects, err := project.List()
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				fmt.Println("No projects configured.")
				fmt.Println("Create one with: clawflow project create <name>")
				return nil
			}
			fmt.Printf("%-25s %-6s %s\n", "PROJECT", "REPOS", "MEMBERS")
			fmt.Println(strings.Repeat("─", 70))
			for _, p := range projects {
				members := "(none)"
				if len(p.Repos) > 0 {
					members = strings.Join(p.Repos, ", ")
				}
				fmt.Printf("%-25s %-6d %s\n", p.Name, len(p.Repos), members)
			}
			return nil
		},
	}
}

func newProjectShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show project details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := project.Get(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Project: %s\n", p.Name)
			fmt.Printf("Created: %s\n", p.CreatedAt)
			fmt.Printf("Updated: %s\n", p.UpdatedAt)
			fmt.Printf("Repos (%d):\n", len(p.Repos))
			if len(p.Repos) == 0 {
				fmt.Println("  (none)")
			}
			for _, r := range p.Repos {
				fmt.Printf("  - %s\n", r)
			}
			ctx, err := project.ReadContext(p.Name)
			if err != nil {
				return err
			}
			if strings.TrimSpace(ctx) != "" {
				fmt.Println()
				fmt.Println("--- context.md ---")
				fmt.Println(ctx)
			} else {
				fmt.Println()
				fmt.Println("context.md: (empty)")
				fmt.Println("  generate with: clawflow project generate", p.Name)
			}
			return nil
		},
	}
}

func newProjectAddRepoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add-repo <project> <owner/repo>",
		Short: "Associate a repo with a project",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := project.AddRepo(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("repo %q added to project %q\n", args[1], args[0])
			return nil
		},
	}
}

func newProjectRemoveRepoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove-repo <project> <owner/repo>",
		Short: "Disassociate a repo from a project",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := project.RemoveRepo(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("repo %q removed from project %q\n", args[1], args[0])
			return nil
		},
	}
}

func newProjectGenerateCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "generate <name>",
		Short: "AI-generate context.md by scanning member repos",
		Long: `For each repo in the project that has local_path configured in
clawflow's config, collects README, main config files (go.mod,
package.json, Cargo.toml, etc.), and top-level directory structure.
Builds a prompt asking Claude to produce an architecture overview,
then writes the result to context.md.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectGenerate(args[0], model)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "claude model (default: settings chat model)")
	return cmd
}

func newProjectChatCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "chat <name>",
		Short: "Interactive chat to review/modify context.md",
		Long: `Loads the current context.md as system context and launches an
interactive Claude session. After the session ends, if Claude produced
an updated context.md (in a fenced code block tagged "context.md"),
you'll be shown a preview and asked whether to save it back.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectChat(args[0], model)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "claude model (default: settings chat model)")
	return cmd
}

func newProjectDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := project.Delete(args[0]); err != nil {
				return err
			}
			fmt.Printf("project %q deleted\n", args[0])
			return nil
		},
	}
}

// runProjectGenerate collects repo metadata and asks Claude to produce
// a project overview, writing the result to context.md.
func runProjectGenerate(name, model string) error {
	p, err := project.Get(name)
	if err != nil {
		return err
	}
	if len(p.Repos) == 0 {
		return fmt.Errorf("project %q has no repos — add some first with: clawflow project add-repo %s <owner/repo>", name, name)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Collect repo info for the prompt
	var promptParts []string
	promptParts = append(promptParts, fmt.Sprintf("# Project: %s\n", name))
	promptParts = append(promptParts, fmt.Sprintf("This project contains %d repositories:\n", len(p.Repos)))

	scanned := 0
	for _, repoName := range p.Repos {
		repoCfg, ok := cfg.Repos[repoName]
		localPath := ""
		if ok {
			localPath = repoCfg.LocalPath
		}
		if localPath == "" {
			promptParts = append(promptParts, fmt.Sprintf("\n## %s\n(no local_path configured — skipped)\n", repoName))
			continue
		}

		promptParts = append(promptParts, fmt.Sprintf("\n## %s\nLocal path: %s\n", repoName, localPath))

		// Collect README
		for _, readme := range []string{"README.md", "readme.md", "README"} {
			data, err := os.ReadFile(fmt.Sprintf("%s/%s", localPath, readme))
			if err == nil {
				content := string(data)
				if len(content) > 3000 {
					content = content[:3000] + "\n... (truncated)"
				}
				promptParts = append(promptParts, fmt.Sprintf("\n### README\n```\n%s\n```\n", content))
				break
			}
		}

		// Collect config files
		configFiles := []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "pom.xml", "build.gradle"}
		for _, cf := range configFiles {
			data, err := os.ReadFile(fmt.Sprintf("%s/%s", localPath, cf))
			if err == nil {
				content := string(data)
				if len(content) > 1500 {
					content = content[:1500] + "\n... (truncated)"
				}
				promptParts = append(promptParts, fmt.Sprintf("\n### %s\n```\n%s\n```\n", cf, content))
			}
		}

		// Top-level directory listing
		entries, err := os.ReadDir(localPath)
		if err == nil {
			var dirs, files []string
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".") {
					continue
				}
				if e.IsDir() {
					dirs = append(dirs, e.Name()+"/")
				} else {
					files = append(files, e.Name())
				}
			}
			promptParts = append(promptParts, "\n### Directory structure\n```\n")
			for _, d := range dirs {
				promptParts = append(promptParts, d+"\n")
			}
			for _, f := range files {
				promptParts = append(promptParts, f+"\n")
			}
			promptParts = append(promptParts, "```\n")
		}
		scanned++
	}

	if scanned == 0 {
		return fmt.Errorf("no repos with local_path configured — set local_path in config for at least one member repo")
	}

	promptParts = append(promptParts, `
---

Based on the repository information above, produce a project overview document in Markdown. Include:

1. **Project Overview** — one paragraph describing what this project does
2. **Repository Roles** — for each repo, its role and responsibility (2-3 sentences)
3. **Inter-repo Dependencies** — how the repos depend on and collaborate with each other
4. **Architecture Overview** — high-level architecture description

Write the output as a standalone Markdown document suitable for context.md.
Output ONLY the Markdown document, no preamble or explanation.`)

	prompt := strings.Join(promptParts, "")

	if model == "" {
		creds, _ := config.LoadCredentials()
		model = creds.EffectiveChatModel()
	}

	fmt.Fprintf(os.Stderr, "[clawflow] generating context.md for project %q (model=%s, %d repos scanned)...\n", name, model, scanned)

	bin := claude.Resolve()
	args := []string{
		"--model", model,
		"--print",
		"-p", prompt,
	}

	cmd := exec.Command(bin, args...)
	creds, _ := config.LoadCredentials()
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("claude generate failed: %w", err)
	}

	content := strings.TrimSpace(string(output))
	if content == "" {
		return fmt.Errorf("claude returned empty output")
	}

	if err := project.WriteContext(name, content); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ context.md written (%d bytes)\n", len(content))
	fmt.Println(content)
	return nil
}

// runProjectChat launches an interactive Claude session with the current
// context.md loaded as system context. After the session, it extracts any
// updated context.md from the stream-json output and offers to write it back.
func runProjectChat(name, model string) error {
	p, err := project.Get(name)
	if err != nil {
		return err
	}

	originalCtx, err := project.ReadContext(name)
	if err != nil {
		return err
	}

	if model == "" {
		creds, _ := config.LoadCredentials()
		model = creds.EffectiveChatModel()
	}

	// Build system context using the chat package's prompt builder
	systemCtx := chat.BuildProjectChatContext(name, originalCtx)

	// Append member-repo info so the AI knows the project structure
	var sb strings.Builder
	sb.WriteString(systemCtx)
	fmt.Fprintf(&sb, "\n\nMember repos: %s\n", strings.Join(p.Repos, ", "))
	fullCtx := sb.String()

	tmpFile, err := os.CreateTemp("", "clawflow-project-chat-*.md")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(fullCtx); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	sessionID := chat.NewSessionID("project/"+name, 0)
	sessionName := fmt.Sprintf("clawflow: project/%s", name)
	workdir := os.TempDir()

	args := []string{
		"--model", model,
		"--name", sessionName,
		"--output-format", "stream-json",
		"--session-id", sessionID,
		"--append-system-prompt-file", tmpFile.Name(),
	}

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

	// Print provenance banner
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
	fmt.Fprintf(os.Stderr, "[clawflow] project chat → %s (model=%s key=%s base_url=%s%s)\n", name, model, keyHint, urlHint, bareNote)

	// Connect stdin directly so the user can interact.
	cmd.Stdin = os.Stdin

	// Capture stdout (stream-json) while also displaying assistant text
	// to stderr for the interactive experience.
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

	if err := <-streamDone; err != nil {
		debugf("stream reader error: %v", err)
	}

	runErr := cmd.Wait()

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
		if err := project.WriteContext(name, newCtx); err != nil {
			return fmt.Errorf("write context.md: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[clawflow] Saved → %s\n", project.ContextPath(name))
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
