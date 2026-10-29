package commands

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/projectgen"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
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
	cmd.AddCommand(newProjectAutomationCmd())
	cmd.AddCommand(newProjectCheckCmd())
	return cmd
}

// Legacy automation commands deliberately perform no reads or writes.
func newProjectAutomationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "automation", Short: "已移除：请使用 pilot ask 或 pilot chat", DisableFlagParsing: true,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "项目自动唤醒已移除；请使用 clawflow pilot ask 或 clawflow pilot chat。")
		}}
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
			testing, err := project.ReadTesting(p.Name)
			if err != nil {
				return err
			}
			if strings.TrimSpace(testing) != "" {
				fmt.Println()
				fmt.Println("--- testing.md ---")
				fmt.Println(testing)
			} else {
				fmt.Println()
				fmt.Println("testing.md: (empty)")
				fmt.Println("  describe local-env startup SOP via: clawflow project chat", p.Name)
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
	cmd.Flags().StringVar(&model, "model", "", "explicit model override (default: Offload profile model)")
	return cmd
}

func newProjectChatCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "chat <name>",
		Short: "Interactive chat to review/modify context.md",
		Long: `Loads the current context.md as system context and launches an
plain text Offload conversation. Each line is one turn; /exit or EOF leaves.
After the session ends, if the assistant produced
an updated context.md (in a fenced code block tagged "context.md"),
you'll be shown a preview and asked whether to save it back.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectChat(args[0], model)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "explicit model override (default: Offload profile model)")
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

// runProjectGenerate is a thin CLI wrapper around projectgen.Generate
// — it adds stderr provenance lines and prints the generated content
// to stdout. The actual prompt construction and `offload` invocation
// live in internal/projectgen so the dashboard's HTTP handler can
// reuse them.
func runProjectGenerate(name, model string) error {
	resolved := model
	fmt.Fprintf(os.Stderr, "[clawflow] generating context.md for project %q (model=%s)...\n", name, resolved)

	content, err := projectgen.Generate(name, model, "")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "✓ context.md written (%d bytes)\n", len(content))
	// Match the dashboard API path: refresh snapshot so a running
	// `clawflow web` reflects the new context.md on next page load.
	if err := snapshot.WriteProjects(); err != nil {
		fmt.Fprintf(os.Stderr, "[clawflow] (warning) snapshot refresh failed: %v\n", err)
	}
	fmt.Println(content)
	return nil
}

// runProjectChat launches the project-manager interactive Claude
// session. It is the single surface for two things:
//
//  1. Editing the project's context.md (write-back via post-chat
//     session-resume extract — see the bottom of this function).
//  2. Cross-repo project work — issue triage, PR review, label and
//     milestone management — via the clawflow CLI invoked through
//     Bash. All member repos with local clones are mounted via
//     --add-dir so Claude can grep across the whole project in one
//     session.
//
// Code edits are blocked at the launcher level via --disallowedTools.
// The implement operator (run on a labeled issue) remains the only
// path that mutates code.
func runProjectChat(name, model string) error {
	p, err := project.Get(name)
	if err != nil {
		return err
	}

	originalCtx, err := project.ReadContext(name)
	if err != nil {
		return err
	}
	originalTesting, err := project.ReadTesting(name)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Build the member-repo descriptor table — both for the system
	// prompt (so Claude knows what's where) and for --add-dir below
	// (so it can actually read those paths).
	chatRepos := make([]chat.ProjectChatRepo, 0, len(p.Repos))
	for _, repoName := range p.Repos {
		localPath := ""
		if rc, ok := cfg.Repos[repoName]; ok {
			localPath = rc.LocalPath
		}
		chatRepos = append(chatRepos, chat.ProjectChatRepo{Name: repoName, LocalPath: localPath})
	}

	systemCtx := chat.BuildProjectChatContext(name, chatRepos, originalCtx, originalTesting, cfg.Settings.Language)

	sessionID, profile := "", ""
	workdir := project.ProjectDir(name)
	if err := execution.Chat(context.Background(), os.Stdin, os.Stdout, execution.Options{Workspace: workdir, Model: model, Role: config.RoleChat, Instructions: systemCtx, OnResult: func(result execution.Result) { sessionID = result.NativeSessionID; profile = result.Profile }}); err != nil {
		return err
	}
	if sessionID == "" {
		return nil
	}
	wbErr := resumeAndMaybeWriteBack(name, model, profile, sessionID, workdir, originalCtx, originalTesting)

	// Refresh the dashboard snapshot regardless of whether we wrote
	// back via the extract path — the AI may also have edited
	// context.md / project.yaml directly via Edit/Write while in the
	// project workdir, and the dashboard's /data/projects.json
	// otherwise wouldn't pick that up until the next web restart.
	if err := snapshot.WriteProjects(); err != nil {
		fmt.Fprintf(os.Stderr, "[clawflow] (warning) snapshot refresh failed: %v\n", err)
	}
	return wbErr
}

// resumeAndMaybeWriteBack runs `offload -r <actual-native-session>` to
// extract the final context.md AND testing.md from the just-ended
// chat. For each doc whose extracted content differs from the
// original, the user is shown a preview and asked to confirm save.
// Each doc is offered independently so the user can save one and
// discard the other.
//
// Cost: one short follow-up turn covers both docs (~1–5s, a few
// cents). Skipped silently for any doc the model omits — the prompt
// invites the model to output zero, one, or both blocks.
func resumeAndMaybeWriteBack(name, model, profile, sessionID, workdir, originalCtx, originalTesting string) error {
	fmt.Fprintln(os.Stderr, "\n[clawflow] checking for context.md / testing.md updates from this conversation…")

	extractPrompt := `If the conversation produced or refined either of these documents, output the COMPLETE final version of each, inside its own fenced code block tagged with the doc name. Output zero, one, or both blocks — whatever changed.

` + "```" + `context.md
# ...full document...
` + "```" + `

` + "```" + `testing.md
# ...full document...
` + "```" + `

If neither doc changed, reply with the literal token NO_UPDATES on a single line.

Do not include any other text, preamble, or explanation.`

	result, err := execution.Execute(context.Background(), extractPrompt, execution.Options{Workspace: workdir, Model: model, Role: config.RoleChat, Profile: profile, SessionKey: sessionID, Resume: true, ToolsDisabled: true})
	if err != nil {
		return fmt.Errorf("Offload extract updates: %w", err)
	}
	outStr := result.Text

	if err := offerSave(outStr, originalCtx, "context.md", chat.ExtractLastContextMD,
		project.ContextPath(name), func(c string) error { return project.WriteContext(name, c) }); err != nil {
		return err
	}
	if err := offerSave(outStr, originalTesting, "testing.md", chat.ExtractLastTestingMD,
		project.TestingPath(name), func(c string) error { return project.WriteTesting(name, c) }); err != nil {
		return err
	}
	return nil
}

// offerSave is the per-doc tail of resumeAndMaybeWriteBack. Pulled
// out so context.md and testing.md follow identical extract → diff →
// confirm → write paths without duplicating 25 lines.
func offerSave(output, original, label string, extract func(string) string, path string, write func(string) error) error {
	proposed := extract(output)
	if proposed == "" {
		fmt.Fprintf(os.Stderr, "[clawflow] no %s updates from this chat.\n", label)
		return nil
	}
	if !strings.HasSuffix(proposed, "\n") {
		proposed += "\n"
	}
	if proposed == original {
		fmt.Fprintf(os.Stderr, "[clawflow] proposed %s is identical to current — no save needed.\n", label)
		return nil
	}

	fmt.Fprintf(os.Stderr, "\n[clawflow] ─── Proposed %s ───\n", label)
	fmt.Fprintln(os.Stderr, proposed)
	fmt.Fprintln(os.Stderr, "[clawflow] ─── End ───")
	fmt.Fprintf(os.Stderr, "[clawflow] Save this as the new %s? [y/N] ", label)

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(strings.ToLower(answer))
	if answer != "y" && answer != "yes" {
		fmt.Fprintf(os.Stderr, "[clawflow] discarded — %s unchanged.\n", label)
		return nil
	}

	if err := write(proposed); err != nil {
		return fmt.Errorf("write %s: %w", label, err)
	}
	fmt.Fprintf(os.Stderr, "[clawflow] saved → %s\n", path)
	return nil
}
