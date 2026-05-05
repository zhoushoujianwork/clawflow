package projectpm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// ApplyOutcome reports what happened for one ProposedChange. Status is
// the durable signal the dashboard renders; Detail/Error carry context
// for failure cases.
//
// Status values:
//
//	"written"        — file written, no git involved (project-level files)
//	"unchanged"      — proposed content was byte-identical to disk; no
//	                    file write, no git commit. Healthy normal path:
//	                    a previous apply already landed this exact change,
//	                    or the operator re-proposed the same content.
//	"committed"      — file written and committed; push succeeded
//	"committed-only" — file written and committed locally; push failed
//	                    (likely branch protection); surface for manual push
//	"failed"         — write or git operation errored before commit
//
// CommitHash is set when a commit was created (committed | committed-only).
type ApplyOutcome struct {
	Target     string `json:"target"`
	RepoID     string `json:"repo_id,omitempty"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	CommitHash string `json:"commit_hash,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ApplyResult is the dashboard-facing view of a full Apply call.
type ApplyResult struct {
	Outcomes []ApplyOutcome `json:"outcomes"`
}

// ApplyHealthCheckChanges writes each accepted ProposedChange and, for
// repo-targeted changes, commits + pushes them. Project-level files
// (context.md / testing.md) live under ~/.clawflow/projects/<name>/
// which is not a git repo, so they're written without a commit.
//
// Per-change failures do not abort the rest — each ProposedChange gets
// its own ApplyOutcome and the dashboard renders a per-file status.
// Repo-level pushes that fail (e.g. main is branch-protected) keep
// the local commit and report `committed-only`, mirroring the user
// design choice ("if push fails, keep local + tell the user").
func ApplyHealthCheckChanges(p *project.Project, cfg *config.Config, changes []ProposedChange) *ApplyResult {
	res := &ApplyResult{Outcomes: make([]ApplyOutcome, 0, len(changes))}

	// Group repo-targeted changes by repo so each repo gets a single
	// commit even if multiple files were proposed (forward-looking —
	// today only CLAUDE.md is in scope, but the design shouldn't trip
	// over a future "also propose AGENTS.md" pass).
	byRepo := map[string][]ProposedChange{}
	var repoOrder []string
	var projectChanges []ProposedChange
	for _, c := range changes {
		switch c.Target {
		case "repo":
			if _, seen := byRepo[c.RepoID]; !seen {
				repoOrder = append(repoOrder, c.RepoID)
			}
			byRepo[c.RepoID] = append(byRepo[c.RepoID], c)
		case "project":
			projectChanges = append(projectChanges, c)
		default:
			res.Outcomes = append(res.Outcomes, ApplyOutcome{
				Target: c.Target, Path: c.Path,
				Status: "failed",
				Error:  fmt.Sprintf("unknown target %q", c.Target),
			})
		}
	}
	sort.Strings(repoOrder)

	for _, c := range projectChanges {
		res.Outcomes = append(res.Outcomes, applyProjectChange(p, c))
	}
	for _, repoID := range repoOrder {
		outs := applyRepoChanges(cfg, repoID, byRepo[repoID])
		res.Outcomes = append(res.Outcomes, outs...)
	}
	return res
}

// applyProjectChange writes a project-level file (context.md / testing.md)
// to ~/.clawflow/projects/<name>/. No git involved — these files are
// not under version control.
func applyProjectChange(p *project.Project, c ProposedChange) ApplyOutcome {
	out := ApplyOutcome{Target: "project", Path: c.Path}
	abs, err := resolveProjectPath(p.Name, c.Path)
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		return out
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		out.Status = "failed"
		out.Error = "mkdir: " + err.Error()
		return out
	}
	// Skip the write entirely if the file already matches the proposal.
	// Otherwise we'd `WriteFile` the same bytes back and (for repo files)
	// hit a confusing "nothing to commit" error downstream.
	if existing, err := os.ReadFile(abs); err == nil && string(existing) == c.ProposedContent {
		out.Status = "unchanged"
		return out
	}
	if err := os.WriteFile(abs, []byte(c.ProposedContent), 0o644); err != nil {
		out.Status = "failed"
		out.Error = "write: " + err.Error()
		return out
	}
	out.Status = "written"
	return out
}

// applyRepoChanges writes every accepted file inside one repo, then
// runs a single `git add <files> && git commit && git push` cycle.
//
// Failure modes:
//   - any write fails → emit "failed" outcomes for the failing file
//     and skip git entirely (don't commit a half-applied set)
//   - file content was already identical → emit "unchanged" and skip
//     this file from the commit batch (NOT a failure: previous apply
//     already landed it, or the operator re-proposed the same bytes).
//     This is what used to surface as a confusing "git commit: nothing
//     to commit" error.
//   - commit fails for any other reason → emit "failed" outcomes
//   - push fails (branch protection, network, auth) → keep the local
//     commit and emit "committed-only" with the push error in Detail.
func applyRepoChanges(cfg *config.Config, repoID string, changes []ProposedChange) []ApplyOutcome {
	rc, ok := cfg.Repos[repoID]
	if !ok || rc.LocalPath == "" {
		errMsg := "repo not in clawflow config"
		if ok {
			errMsg = "repo has no local clone"
		}
		outs := make([]ApplyOutcome, 0, len(changes))
		for _, c := range changes {
			outs = append(outs, ApplyOutcome{
				Target: "repo", RepoID: repoID, Path: c.Path,
				Status: "failed", Error: errMsg,
			})
		}
		return outs
	}

	// 1. Write every file. If any write fails, mark the rest of the
	//    changes "failed" with a "skipped: prior write failed" note —
	//    we don't want to commit a partial application.
	outs := make([]ApplyOutcome, len(changes))
	relPaths := make([]string, 0, len(changes))
	writeFailed := false
	for i, c := range changes {
		outs[i] = ApplyOutcome{Target: "repo", RepoID: repoID, Path: c.Path}
		if writeFailed {
			outs[i].Status = "failed"
			outs[i].Error = "skipped: another file in this repo failed to write"
			continue
		}
		rel, err := safeRepoRelPath(rc.LocalPath, c.Path)
		if err != nil {
			outs[i].Status = "failed"
			outs[i].Error = err.Error()
			writeFailed = true
			continue
		}
		abs := filepath.Join(rc.LocalPath, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			outs[i].Status = "failed"
			outs[i].Error = "mkdir: " + err.Error()
			writeFailed = true
			continue
		}
		// Pre-check: if the file already matches the proposal byte-for-
		// byte, mark it "unchanged" and DON'T add it to relPaths. The
		// remaining files in this repo can still be committed; only the
		// no-op file is excluded from the commit batch.
		if existing, err := os.ReadFile(abs); err == nil && string(existing) == c.ProposedContent {
			outs[i].Status = "unchanged"
			continue
		}
		if err := os.WriteFile(abs, []byte(c.ProposedContent), 0o644); err != nil {
			outs[i].Status = "failed"
			outs[i].Error = "write: " + err.Error()
			writeFailed = true
			continue
		}
		relPaths = append(relPaths, rel)
	}
	if writeFailed || len(relPaths) == 0 {
		return outs
	}

	// 2. Stage exactly the files we wrote.
	if stdout, stderr, err := runGit(rc.LocalPath, append([]string{"add", "--"}, relPaths...)...); err != nil {
		errMsg := strings.TrimSpace(stderr + stdout)
		if errMsg == "" {
			errMsg = err.Error()
		}
		for i := range outs {
			if outs[i].Status == "" {
				outs[i].Status = "failed"
				outs[i].Error = "git add: " + errMsg
			}
		}
		return outs
	}

	// 3. Commit. `--only -- <paths>` keeps any other staged work out
	//    of this commit so we don't accidentally pick up unrelated
	//    user changes.
	commitMsg := healthCheckCommitMessage(relPaths)
	commitArgs := append([]string{"commit", "--only", "-m", commitMsg, "--"}, relPaths...)
	if stdout, stderr, err := runGit(rc.LocalPath, commitArgs...); err != nil {
		errMsg := strings.TrimSpace(stderr + stdout)
		if errMsg == "" {
			errMsg = err.Error()
		}
		for i := range outs {
			if outs[i].Status == "" {
				outs[i].Status = "failed"
				outs[i].Error = "git commit: " + errMsg
			}
		}
		return outs
	}

	// 4. Capture the commit hash now — it's useful even if push fails
	//    (user can refer to it when pushing manually).
	commitHash := ""
	if stdout, _, err := runGit(rc.LocalPath, "rev-parse", "HEAD"); err == nil {
		commitHash = strings.TrimSpace(stdout)
	}

	// 5. Push. Failure is the expected outcome under branch protection
	//    so we degrade gracefully rather than treating it as fatal.
	if _, stderr, err := runGit(rc.LocalPath, "push"); err != nil {
		pushErr := strings.TrimSpace(stderr)
		if pushErr == "" {
			pushErr = err.Error()
		}
		for i := range outs {
			if outs[i].Status == "" {
				outs[i].Status = "committed-only"
				outs[i].CommitHash = commitHash
				outs[i].Detail = "push failed (likely branch protection): " + truncate(pushErr, 400)
			}
		}
		return outs
	}

	for i := range outs {
		if outs[i].Status == "" {
			outs[i].Status = "committed"
			outs[i].CommitHash = commitHash
		}
	}
	return outs
}

// healthCheckCommitMessage produces a Conventional Commit message that
// names every touched path so a casual git log reader knows what the
// PM bot did without opening the diff.
func healthCheckCommitMessage(paths []string) string {
	if len(paths) == 1 {
		return "docs(claude): refresh " + paths[0] + " via clawflow pm-health-check"
	}
	return "docs(claude): refresh " + fmt.Sprintf("%d files", len(paths)) + " via clawflow pm-health-check\n\n- " + strings.Join(paths, "\n- ")
}

// safeRepoRelPath validates that `path` stays inside the repo working
// tree. The operator output is trusted-ish but we still reject
// absolute paths and `..` traversal — at this layer the user has
// approved the change but not the path syntax.
func safeRepoRelPath(repoRoot, path string) (string, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("invalid path %q (must be relative, no parent traversal)", path)
	}
	abs := filepath.Join(repoRoot, clean)
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes repo root", path)
	}
	return rel, nil
}

// resolveProjectPath maps a project-target ProposedChange path to an
// absolute path under ~/.clawflow/projects/<name>/. Same path-safety
// rules as safeRepoRelPath but rooted at the project dir.
func resolveProjectPath(projectName, path string) (string, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.Contains(clean, string(filepath.Separator)+"..") {
		return "", fmt.Errorf("invalid path %q (must be relative, no parent traversal)", path)
	}
	return filepath.Join(project.ProjectDir(projectName), clean), nil
}

// runGit runs `git <args>` inside dir and returns stdout/stderr.
// Wrapped instead of inline so the apply functions read at one
// abstraction level. Errors include the cmd name for log clarity.
func runGit(dir string, args ...string) (string, string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), stderr.String(), fmt.Errorf("git %s: exit %d", args[0], exitErr.ExitCode())
		}
		return stdout.String(), stderr.String(), fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.String(), stderr.String(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
