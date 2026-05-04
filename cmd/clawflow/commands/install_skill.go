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

	skills, err := discoverAgentSkills()
	if err != nil {
		return err
	}

	fmt.Println("AI Tool          Status      Skills Directory")
	fmt.Println("───────────────  ──────────  ────────────────────────────────────")
	for _, t := range knownTargets {
		dir := filepath.Join(home, t.SkillsDir)
		status := "not found"
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			installed := 0
			for _, s := range skills {
				if _, err := os.Stat(filepath.Join(dir, s, "SKILL.md")); err == nil {
					installed++
				}
			}
			if installed == len(skills) {
				status = "installed"
			} else if installed > 0 {
				status = fmt.Sprintf("%d/%d skills", installed, len(skills))
			} else {
				status = "detected"
			}
		}
		fmt.Printf("%-16s %-11s ~/%s\n", t.DisplayName, status, t.SkillsDir)
	}
	fmt.Printf("\nAgent skills (%d): %s\n", len(skills), strings.Join(skills, ", "))
	return nil
}

func installSkill(targets []skillTarget) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	skills, err := discoverAgentSkills()
	if err != nil {
		return err
	}

	for _, skillName := range skills {
		data, err := rootmod.EmbeddedAgentSkills.ReadFile(fmt.Sprintf("agent-skills/%s/SKILL.md", skillName))
		if err != nil {
			fmt.Printf("  [err] read embedded %s: %v\n", skillName, err)
			continue
		}
		for _, t := range targets {
			dest := filepath.Join(home, t.SkillsDir, skillName, "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				fmt.Printf("  [err] %s/%s: %v\n", t.DisplayName, skillName, err)
				continue
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				fmt.Printf("  [err] %s/%s: %v\n", t.DisplayName, skillName, err)
				continue
			}
			fmt.Printf("  [ok] %s → ~/%s/%s/SKILL.md\n", t.DisplayName, t.SkillsDir, skillName)
		}
	}
	return nil
}

func removeSkill(targets []skillTarget) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	skills, err := discoverAgentSkills()
	if err != nil {
		return err
	}

	for _, skillName := range skills {
		for _, t := range targets {
			dir := filepath.Join(home, t.SkillsDir, skillName)
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				fmt.Printf("  [skip] %s/%s — not installed\n", t.DisplayName, skillName)
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				fmt.Printf("  [err] %s/%s: %v\n", t.DisplayName, skillName, err)
				continue
			}
			fmt.Printf("  [ok] %s/%s — removed\n", t.DisplayName, skillName)
		}
	}
	return nil
}

func discoverAgentSkills() ([]string, error) {
	entries, err := rootmod.EmbeddedAgentSkills.ReadDir("agent-skills")
	if err != nil {
		return nil, fmt.Errorf("read embedded agent-skills: %w", err)
	}
	var skills []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := rootmod.EmbeddedAgentSkills.ReadFile(fmt.Sprintf("agent-skills/%s/SKILL.md", e.Name())); err == nil {
			skills = append(skills, e.Name())
		}
	}
	return skills, nil
}
