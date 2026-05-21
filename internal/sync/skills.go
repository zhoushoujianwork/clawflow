package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// skillsRoot returns ~/.clawflow/skills, the user-defined operator dir.
// Errors from UserHomeDir collapse to an empty path; callers treat that
// as "no skills to sync" via the os.IsNotExist check in DiscoverSkillAssets.
func skillsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".clawflow", "skills")
}

// isSyncableSkillFile reports whether a file inside a skill directory should
// be uploaded. We sync .md (SKILL.md plus any helper docs the skill links to,
// e.g. evaluation.md) and .yaml (rare but harmless). Everything else
// (scripts/*, binaries, dotfiles) is skipped — same conservative rule
// applied to project assets.
func isSyncableSkillFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".md"
}

// DiscoverSkillAssets walks ~/.clawflow/skills/ and returns Gist filename →
// file content for every eligible user skill asset. Mirrors
// DiscoverProjectAssets — same flat layout, different top-level prefix.
//
// Each immediate subdirectory of skills/ is treated as one skill (named
// after the directory). Within that directory only the top level is walked;
// nested directories such as scripts/ are intentionally skipped to keep the
// sync envelope predictable, matching the projects/ behaviour.
func DiscoverSkillAssets() (map[string]string, error) {
	root := skillsRoot()
	result := make(map[string]string)
	if root == "" {
		return result, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // no custom skills — nothing to sync
		}
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillName := entry.Name()
		skillDir := filepath.Join(root, skillName)

		files, err := os.ReadDir(skillDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ sync: cannot read skill dir %s: %v\n", skillDir, err)
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if !isSyncableSkillFile(f.Name()) {
				continue
			}

			absPath := filepath.Join(skillDir, f.Name())
			content, err := os.ReadFile(absPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠ sync: cannot read %s: %v\n", absPath, err)
				continue
			}

			relPath := filepath.Join("skills", skillName, f.Name())
			gistFilename := EncodeGistFilename(relPath)
			result[gistFilename] = string(content)
		}
	}

	return result, nil
}
