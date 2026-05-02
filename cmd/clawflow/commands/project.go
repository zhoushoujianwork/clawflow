package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// NewProjectCmd returns the `clawflow project` parent command with all
// subcommands registered.
func NewProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage multi-repo projects",
		Long: `Group multiple repositories under a named project. Each project
carries a context.md that is automatically injected into clawflow chat
sessions for any member repo, giving the AI cross-repo awareness.`,
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
			fmt.Printf("  add repos with: clawflow project add-repo %s <owner/repo>\n", p.Name)
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
			fmt.Printf("%-30s %-10s %s\n", "PROJECT", "REPOS", "UPDATED")
			fmt.Println(strings.Repeat("─", 70))
			for _, p := range projects {
				fmt.Printf("%-30s %-10d %s\n", p.Name, len(p.Repos), p.UpdatedAt)
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
			fmt.Printf("Name:       %s\n", p.Name)
			fmt.Printf("Created:    %s\n", p.CreatedAt)
			fmt.Printf("Updated:    %s\n", p.UpdatedAt)
			fmt.Printf("Repos (%d):\n", len(p.Repos))
			for _, r := range p.Repos {
				fmt.Printf("  - %s\n", r)
			}
			ctx, _ := project.ReadContext(p.Name)
			if strings.TrimSpace(ctx) != "" {
				fmt.Println()
				fmt.Println("--- context.md ---")
				fmt.Println(ctx)
			} else {
				fmt.Println()
				fmt.Println("context.md is empty. Generate with: clawflow project generate " + p.Name)
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

func newProjectGenerateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "generate <name>",
		Short: "AI-generate context.md by scanning member repos",
		Long: `For each repo in the project that has local_path configured in
clawflow config, collects README, main config files, and top-level
directory structure. Sends the collected info to Claude to produce
an architecture overview, which is written to context.md.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectGenerate(cmd.Context(), args[0])
		},
	}
}

func newProjectChatCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "chat <name>",
		Short: "Interactive chat to review/modify context.md",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectChat(cmd.Context(), args[0], model)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "claude model to use")
	return cmd
}

// runProjectGenerate collects repo info and asks Claude to produce context.md.
func runProjectGenerate(_ context.Context, name string) error {
	p, err := project.Get(name)
	if err != nil {
		return err
	}
	if len(p.Repos) == 0 {
		return fmt.Errorf("project %q has no repos — add some first", name)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Collect info from each repo that has a local_path.
	var repoSections []string
	var skipped []string
	for _, repoName := range p.Repos {
		repoCfg, ok := cfg.Repos[repoName]
		if !ok || repoCfg.LocalPath == "" {
			skipped = append(skipped, repoName)
			continue
		}
		section := collectRepoInfo(repoName, repoCfg.LocalPath)
		repoSections = append(repoSections, section)
	}

	if len(skipped) > 0 {
		fmt.Fprintf(os.Stderr, "[warn] skipped repos without local_path: %s\n", strings.Join(skipped, ", "))
	}
	if len(repoSections) == 0 {
		return fmt.Errorf("no repos with local_path found — configure local_path for at least one repo")
	}

	prompt := fmt.Sprintf(`You are analyzing a software project called %q that consists of multiple repositories.
Below is information collected from each repo (README, config files, directory structure).

%s

Based on this information, produce a concise project overview in Markdown covering:
1. **Project Summary** — what the overall project does
2. **Repository Roles** — each repo's responsibility (one paragraph each)
3. **Inter-repo Dependencies** — how repos depend on or collaborate with each other
4. **Architecture Overview** — high-level architecture diagram description

Write the output as a single Markdown document. Be factual and concise.`,
		name, strings.Join(repoSections, "\n\n---\n\n"))

	// Invoke claude in --print mode to get the output.
	creds, _ := config.LoadCredentials()
	model := creds.EffectiveOperatorModel()

	bin := claude.Resolve()
	args := []string{"--print", "--model", model, "-p", prompt}

	cmd := exec.Command(bin, args...)
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)

	fmt.Fprintf(os.Stderr, "[clawflow] generating context.md for project %q (model=%s)...\n", name, model)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("claude failed: %s", string(exitErr.Stderr))
		}
		return fmt.Errorf("claude failed: %w", err)
	}

	if err := project.WriteContext(name, string(out)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ context.md written (%d bytes)\n", len(out))
	return nil
}

// collectRepoInfo gathers README, config files, and directory listing
// from a local repo path.
func collectRepoInfo(repoName, localPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Repository: %s\n\n", repoName)
	fmt.Fprintf(&b, "Path: %s\n\n", localPath)

	// README
	for _, name := range []string{"README.md", "readme.md", "README", "README.rst"} {
		data, err := os.ReadFile(filepath.Join(localPath, name))
		if err == nil {
			content := string(data)
			// Truncate very long READMEs
			if len(content) > 4000 {
				content = content[:4000] + "\n\n... (truncated)"
			}
			fmt.Fprintf(&b, "### README\n\n```\n%s\n```\n\n", content)
			break
		}
	}

	// Config files
	configFiles := []string{
		"go.mod", "package.json", "Cargo.toml", "pyproject.toml",
		"pom.xml", "build.gradle", "Makefile", "docker-compose.yml",
		"Dockerfile",
	}
	for _, cf := range configFiles {
		data, err := os.ReadFile(filepath.Join(localPath, cf))
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > 2000 {
			content = content[:2000] + "\n... (truncated)"
		}
		fmt.Fprintf(&b, "### %s\n\n```\n%s\n```\n\n", cf, content)
	}

	// Top-level directory listing
	entries, err := os.ReadDir(localPath)
	if err == nil {
		fmt.Fprintf(&b, "### Directory structure\n\n```\n")
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			indicator := ""
			if e.IsDir() {
				indicator = "/"
			}
			fmt.Fprintf(&b, "%s%s\n", e.Name(), indicator)
		}
		fmt.Fprintf(&b, "```\n")
	}

	return b.String()
}

// runProjectChat launches an interactive Claude session with the current
// context.md loaded, allowing the user to review and modify it.
func runProjectChat(_ context.Context, name, model string) error {
	p, err := project.Get(name)
	if err != nil {
		return err
	}

	currentCtx, err := project.ReadContext(name)
	if err != nil {
		return err
	}

	// Build system prompt
	var sysPrompt strings.Builder
	fmt.Fprintf(&sysPrompt, "# Project: %s\n\n", p.Name)
	fmt.Fprintf(&sysPrompt, "Repos: %s\n\n", strings.Join(p.Repos, ", "))
	if strings.TrimSpace(currentCtx) != "" {
		fmt.Fprintf(&sysPrompt, "## Current context.md\n\n%s\n\n", currentCtx)
	} else {
		fmt.Fprint(&sysPrompt, "## Current context.md\n\n_(empty — no overview generated yet)_\n\n")
	}
	fmt.Fprintln(&sysPrompt, `---

## Your role

You are helping the user review and edit the project overview (context.md)
for this multi-repo project. The user may ask you to:
- Rewrite sections
- Add missing information
- Restructure the document
- Generate a fresh overview

When the user is satisfied with the changes, output the final version of
context.md wrapped in a markdown code block tagged with `+"`context.md`"+`:

`+"```context.md"+`
<full content here>
`+"```"+`

The user can then copy this into their context.md, or you can write it
directly if they confirm.`)

	// Resolve model
	if model == "" {
		creds, _ := config.LoadCredentials()
		model = creds.EffectiveChatModel()
	}

	sessionID := chat.NewSessionID("project:"+name, 0)
	sessionName := fmt.Sprintf("clawflow: project %s", name)

	// Write system prompt to temp file
	tmpFile, err := os.CreateTemp("", "clawflow-project-chat-*.md")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	if _, err := tmpFile.WriteString(sysPrompt.String()); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	args := []string{
		"--model", model,
		"--name", sessionName,
		"--session-id", sessionID,
		"--append-system-prompt-file", tmpFile.Name(),
	}

	creds, _ := config.LoadCredentials()
	useBare := creds != nil && creds.ClaudeAPIKey != ""
	if useBare {
		workdir := os.TempDir()
		args = append(args, "--bare", "--add-dir", workdir)
	}

	bin := claude.Resolve()
	cmd := exec.Command(bin, args...)
	cmd.Dir = os.TempDir()
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Fprintf(os.Stderr, "[clawflow] project chat → %s (model=%s)\n", name, model)
	return cmd.Run()
}
