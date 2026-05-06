package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// projectGitignore is written to .gitignore on git init. JSON files are
// runtime state (health-check results, generate job status) that change
// frequently and carry no value in version history. Only the markdown
// docs and project.yaml are worth tracking.
const projectGitignore = `# Runtime state — not worth versioning
*.json
`

// initGit initializes a git repo in the project directory, writes
// .gitignore, and creates the initial commit. Called once from Create.
// Errors are non-fatal: if git is not installed or init fails, the
// project still works — just without version history.
func initGit(name string) error {
	dir := ProjectDir(name)

	if err := runGit(dir, "init"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}

	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(projectGitignore), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}

	if err := runGit(dir, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := runGit(dir, "commit", "-m", "init: create project"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// ensureGit checks whether the project directory has a .git/ folder.
// If not, it initializes one and commits the current state. This
// handles existing projects created before git tracking was added.
// Returns silently if git is unavailable.
func ensureGit(name string) {
	dir := ProjectDir(name)
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return // already initialized
	}
	// Best-effort: don't fail the write operation if git setup fails.
	_ = initGit(name)
}

// CommitChange stages all changes in the project directory and commits
// with the given message. Best-effort: errors are returned but callers
// should log and continue rather than failing the parent operation.
//
// If there are no changes to commit (git status is clean), this is a
// no-op and returns nil.
func CommitChange(name, message string) error {
	dir := ProjectDir(name)

	// Skip if no .git directory (git not available or init failed).
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return nil
	}

	// Check if there's anything to commit.
	out, err := runGitOutput(dir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return nil // nothing to commit
	}

	if err := runGit(dir, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := runGit(dir, "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// runGit executes a git command in the given directory. Stderr is
// discarded (git is chatty); only the exit code matters.
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ClawFlow",
		"GIT_AUTHOR_EMAIL=clawflow@local",
		"GIT_COMMITTER_NAME=ClawFlow",
		"GIT_COMMITTER_EMAIL=clawflow@local",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// runGitOutput executes a git command and returns stdout.
func runGitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ClawFlow",
		"GIT_AUTHOR_EMAIL=clawflow@local",
		"GIT_COMMITTER_NAME=ClawFlow",
		"GIT_COMMITTER_EMAIL=clawflow@local",
	)
	out, err := cmd.Output()
	return string(out), err
}
