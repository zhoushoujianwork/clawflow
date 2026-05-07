package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// ClaudeMDPath returns the CLAUDE.md path for a named project.
//
// CLAUDE.md is auto-loaded by `claude -p` whenever the workdir is
// the project directory. It serves as the Pilot's "persistent
// identity" — telling the Pilot which member repos belong to this
// project, where they live on local disk, and where its working
// files (context.md / goals.md / deployment.md) are.
//
// CLAUDE.md is owned by clawflow: regenerated from project.yaml
// every time member repos change (Create/AddRepo/RemoveRepo) and
// at the start of every Pilot wake. User edits will be overwritten.
func ClaudeMDPath(name string) string {
	return filepath.Join(ProjectDir(name), "CLAUDE.md")
}

// RefreshClaudeMD writes the project's CLAUDE.md based on current
// project.yaml + cfg.Repos. Idempotent — if the rendered content
// matches what's already on disk, no write and no commit happen
// (CommitChange is also a no-op on a clean tree, but this avoids
// even the file-write churn on the common path).
//
// Best-effort: failures here should never block the parent operation.
// Returns the error so callers can log it.
func RefreshClaudeMD(name string) error {
	p, err := Get(name)
	if err != nil {
		return err
	}
	cfg, _ := config.Load() // missing config is fine — we just lose local_paths

	content := renderClaudeMD(p, cfg)

	path := ClaudeMDPath(name)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return nil // unchanged
	}

	if err := os.MkdirAll(ProjectDir(name), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	ensureGit(name)
	if err := CommitChange(name, "update CLAUDE.md"); err != nil {
		fmt.Fprintf(os.Stderr, "[project] git commit skipped: %v\n", err)
	}
	return nil
}

// renderClaudeMD builds the CLAUDE.md body. Pure function — split out
// so tests can assert on the output without touching disk.
func renderClaudeMD(p *Project, cfg *config.Config) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Project: %s\n\n", p.Name)
	fmt.Fprintf(&b, "You're the Pilot for project `%s`.\n\n", p.Name)

	fmt.Fprintln(&b, "## Member repos")
	fmt.Fprintln(&b)
	if len(p.Repos) == 0 {
		fmt.Fprintln(&b, "_(no repos in this project yet — `clawflow project add-repo <name> <repo>` to attach one.)_")
	} else {
		for _, repo := range p.Repos {
			localPath := ""
			if cfg != nil {
				if rc, ok := cfg.Repos[repo]; ok {
					localPath = rc.LocalPath
				}
			}
			if localPath == "" {
				fmt.Fprintf(&b, "- `%s` — no local clone (VCS metadata only via `clawflow` CLI)\n", repo)
				continue
			}
			fmt.Fprintf(&b, "- `%s` — local: `%s`\n", repo, localPath)
			repoClaude := filepath.Join(localPath, "CLAUDE.md")
			if _, err := os.Stat(repoClaude); err == nil {
				fmt.Fprintf(&b, "  - read `%s` for repo-specific conventions\n", repoClaude)
			}
		}
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Your working files (in this directory)")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "- `context.md` — your own evolving understanding of this project. Read at wake start. Update at wake end via a fenced ```context.md``` block when something material was learned.")
	fmt.Fprintln(&b, "- `goals.md` — user-maintained objectives and priorities. Read-only for you.")
	fmt.Fprintln(&b, "- `deployment.md` — runtime health / log-retrieval commands. Read on demand.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "_Per-wake state — backlog snapshot, recent wake history, this wake's job — arrives in the system prompt at each wake._")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "<!-- managed by clawflow — auto-regenerated from project.yaml -->")

	return b.String()
}
