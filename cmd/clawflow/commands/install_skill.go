package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	rootmod "github.com/zhoushoujianwork/clawflow"
)

type skillTarget struct {
	Key         string
	DisplayName string
	SkillsDir   string // relative to $HOME
}

var knownTargets = []skillTarget{
	{"claude", "Claude Code", ".claude/skills"},
	{"cursor", "Cursor", ".cursor/skills-cursor"},
	{"codex", "Codex", ".codex/skills"},
	{"windsurf", "Windsurf", ".windsurf/skills"},
}

func NewInstallSkillCmd() *cobra.Command {
	var target string
	var list bool
	var remove bool

	cmd := &cobra.Command{
		Use:   "install-skill",
		Short: "Install the clawflow agent skill to AI coding tools",
		Long: `Detects installed AI coding CLI tools (Claude Code, Cursor, Codex, Windsurf)
and installs the clawflow SKILL.md so the AI assistant knows how to use clawflow.

By default, installs to all detected tools. Use --target to limit to specific ones.`,
		Example: `  clawflow install-skill                        # auto-detect & install to all
  clawflow install-skill --target claude,codex  # install to specific tools
  clawflow install-skill --list                 # show detected tools
  clawflow install-skill --remove               # uninstall from all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if list {
				return listTargets()
			}
			targets, err := resolveTargets(target)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				fmt.Println("  [skip] no supported AI tools detected")
				return nil
			}
			if remove {
				return removeSkill(targets)
			}
			return installSkill(targets)
		},
	}

	cmd.Flags().StringVar(&target, "target", "", "Comma-separated list of tools (claude,cursor,codex,windsurf)")
	cmd.Flags().BoolVar(&list, "list", false, "List detected tools and install status")
	cmd.Flags().BoolVar(&remove, "remove", false, "Remove the clawflow skill from detected tools")
	return cmd
}

func resolveTargets(filter string) ([]skillTarget, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	var wanted map[string]bool
	if filter != "" {
		wanted = make(map[string]bool)
		for _, k := range strings.Split(filter, ",") {
			k = strings.TrimSpace(k)
			if !isKnownTarget(k) {
				return nil, fmt.Errorf("unknown target %q (known: claude, cursor, codex, windsurf)", k)
			}
			wanted[k] = true
		}
	}

	var result []skillTarget
	for _, t := range knownTargets {
		if wanted != nil && !wanted[t.Key] {
			continue
		}
		dir := filepath.Join(home, t.SkillsDir)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			result = append(result, t)
		}
	}
	return result, nil
}

func isKnownTarget(key string) bool {
	for _, t := range knownTargets {
		if t.Key == key {
			return true
		}
	}
	return false
}

func listTargets() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	fmt.Println("AI Tool          Status      Skills Directory")
	fmt.Println("───────────────  ──────────  ────────────────────────────────────")
	for _, t := range knownTargets {
		dir := filepath.Join(home, t.SkillsDir)
		skillPath := filepath.Join(dir, "clawflow", "SKILL.md")

		status := "not found"
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			status = "detected"
			if _, err := os.Stat(skillPath); err == nil {
				status = "installed"
			}
		}

		fmt.Printf("%-16s %-11s ~/%s\n", t.DisplayName, status, t.SkillsDir)
	}
	return nil
}

func installSkill(targets []skillTarget) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	data, err := rootmod.EmbeddedAgentSkills.ReadFile("agent-skills/clawflow/SKILL.md")
	if err != nil {
		return fmt.Errorf("read embedded skill: %w", err)
	}

	for _, t := range targets {
		dest := filepath.Join(home, t.SkillsDir, "clawflow", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			fmt.Printf("  [err] %s: %v\n", t.DisplayName, err)
			continue
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			fmt.Printf("  [err] %s: %v\n", t.DisplayName, err)
			continue
		}
		fmt.Printf("  [ok] %s → ~/%s/clawflow/SKILL.md\n", t.DisplayName, t.SkillsDir)
	}
	return nil
}

func removeSkill(targets []skillTarget) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	for _, t := range targets {
		dir := filepath.Join(home, t.SkillsDir, "clawflow")
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			fmt.Printf("  [skip] %s — not installed\n", t.DisplayName)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			fmt.Printf("  [err] %s: %v\n", t.DisplayName, err)
			continue
		}
		fmt.Printf("  [ok] %s — removed\n", t.DisplayName)
	}
	return nil
}
