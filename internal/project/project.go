// Package project manages project definitions stored under
// ~/.clawflow/projects/<name>/. A project groups multiple repos
// and carries an AI-generated or user-edited context.md that gets
// auto-injected into every `clawflow chat` session for member repos.
package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"gopkg.in/yaml.v3"
)

// Project is the in-memory representation of project.yaml.
type Project struct {
	Name       string     `yaml:"name"`
	Repos      []string   `yaml:"repos"`
	Automation Automation `yaml:"-"`
	CreatedAt  string     `yaml:"created_at"`
	UpdatedAt  string     `yaml:"updated_at"`
}

// Automation is retained as a zero-valued compatibility view for old API clients.
// Project files neither read nor persist these retired settings.
type Automation struct {
	Enabled         bool   `yaml:"enabled"`
	CooldownMinutes int    `yaml:"cooldown_minutes,omitempty"`
	LastWokenAt     string `yaml:"last_woken_at,omitempty"`
	BoundMachine    string `yaml:"bound_machine,omitempty"`
	LastSkipReason  string `yaml:"last_skip_reason,omitempty"`
	LastSkipAt      string `yaml:"last_skip_at,omitempty"`
}

// ProjectsRoot returns ~/.clawflow/projects.
func ProjectsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "projects")
}

// ProjectDir returns the directory for a named project — i.e.
// ~/.clawflow/projects/<name>/. Exported so callers (e.g. the
// project-chat launcher) can use it as a workdir.
func ProjectDir(name string) string {
	return filepath.Join(ProjectsRoot(), name)
}

// yamlPath returns the project.yaml path for a named project.
func yamlPath(name string) string {
	return filepath.Join(ProjectDir(name), "project.yaml")
}

// ContextPath returns the context.md path for a named project.
func ContextPath(name string) string {
	return filepath.Join(ProjectDir(name), "context.md")
}

// TestingPath returns the testing.md path for a named project.
//
// testing.md is a SOP for the project's local test environment —
// startup order, services that need to be running, hardware/serial
// hookups, etc. It is NOT a list of test cases. The implement
// operator reads it (auto-injected via project header) to decide
// whether to spin up local env for verification before opening a PR.
func TestingPath(name string) string {
	return filepath.Join(ProjectDir(name), "testing.md")
}

// DeploymentPath returns the deployment.md path for a named project.
//
// deployment.md describes how to inspect the project's runtime health:
// log retrieval commands (SSH, kubectl, docker logs, etc.), key metrics
// endpoints, and any other commands the Pilot should run to assess the
// live system before triaging the backlog.
func DeploymentPath(name string) string {
	return filepath.Join(ProjectDir(name), "deployment.md")
}

// Create creates a new project with the given name.
func Create(name string) (*Project, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("project name cannot be empty")
	}
	dir := ProjectDir(name)
	if _, err := os.Stat(yamlPath(name)); err == nil {
		return nil, fmt.Errorf("project %q already exists", name)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create project dir: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	p := &Project{
		Name:      name,
		Repos:     []string{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := save(p); err != nil {
		return nil, err
	}
	// Create empty context.md, testing.md, and deployment.md so all three
	// files always exist. context.md is the Pilot's own evolving memory;
	// testing.md is the local-environment SOP; deployment.md describes
	// the runtime environment and log retrieval methods.
	if err := os.WriteFile(ContextPath(name), []byte(""), 0o644); err != nil {
		return nil, fmt.Errorf("create context.md: %w", err)
	}
	if err := os.WriteFile(TestingPath(name), []byte(""), 0o644); err != nil {
		return nil, fmt.Errorf("create testing.md: %w", err)
	}
	if err := os.WriteFile(DeploymentPath(name), []byte(""), 0o644); err != nil {
		return nil, fmt.Errorf("create deployment.md: %w", err)
	}
	// Generate the Pilot's CLAUDE.md (member repo loader). Auto-loaded
	// by `claude -p` whenever the workdir is the project directory.
	if err := RefreshClaudeMD(name); err != nil {
		fmt.Fprintf(os.Stderr, "[project] CLAUDE.md refresh skipped: %v\n", err)
	}
	// Initialize git repo for version history. Best-effort: if git is
	// not installed the project still works, just without history.
	if err := initGit(name); err != nil {
		fmt.Fprintf(os.Stderr, "[project] git init skipped: %v\n", err)
	}
	return p, nil
}

// Get loads a project by name.
func Get(name string) (*Project, error) {
	data, err := os.ReadFile(yamlPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("project %q not found", name)
		}
		return nil, err
	}
	var p Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse project.yaml: %w", err)
	}
	return &p, nil
}

// List returns all projects sorted by name.
func List() ([]*Project, error) {
	root := ProjectsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var projects []*Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := Get(e.Name())
		if err != nil {
			continue // skip malformed
		}
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Name < projects[j].Name
	})
	return projects, nil
}

// Delete removes a project and its directory.
func Delete(name string) error {
	dir := ProjectDir(name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("project %q not found", name)
	}
	return os.RemoveAll(dir)
}

// AddRepo associates a repo with the project. Returns an error only if the
// repo is already a member of this same project. A repo may belong to
// multiple projects (multi-membership, issue #267) — shared component or
// tooling repos legitimately serve several projects, so cross-project
// membership is no longer rejected. Runtime context injection stays
// single-authority via the repo's primary project (see FindProjectByRepo).
func AddRepo(name, repo string) error {
	p, err := Get(name)
	if err != nil {
		return err
	}
	for _, r := range p.Repos {
		if r == repo {
			return fmt.Errorf("repo %q is already in project %q", repo, name)
		}
	}
	p.Repos = append(p.Repos, repo)
	sort.Strings(p.Repos)
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := save(p); err != nil {
		return err
	}
	if err := RefreshClaudeMD(name); err != nil {
		fmt.Fprintf(os.Stderr, "[project] CLAUDE.md refresh skipped: %v\n", err)
	}
	return nil
}

// RemoveRepo disassociates a repo from the project.
func RemoveRepo(name, repo string) error {
	p, err := Get(name)
	if err != nil {
		return err
	}
	found := false
	filtered := make([]string, 0, len(p.Repos))
	for _, r := range p.Repos {
		if r == repo {
			found = true
			continue
		}
		filtered = append(filtered, r)
	}
	if !found {
		return fmt.Errorf("repo %q is not in project %q", repo, name)
	}
	p.Repos = filtered
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := save(p); err != nil {
		return err
	}
	if err := RefreshClaudeMD(name); err != nil {
		fmt.Fprintf(os.Stderr, "[project] CLAUDE.md refresh skipped: %v\n", err)
	}
	return nil
}

// FindProjectsByRepo returns every project that contains the given repo,
// sorted by name (List already returns name-sorted projects). Returns an
// empty slice (not nil error) when no project claims the repo. This is the
// multi-membership query (issue #267): a shared repo may legitimately belong
// to several projects.
func FindProjectsByRepo(repo string) ([]*Project, error) {
	projects, err := List()
	if err != nil {
		return nil, err
	}
	var matched []*Project
	for _, p := range projects {
		for _, r := range p.Repos {
			if r == repo {
				matched = append(matched, p)
				break
			}
		}
	}
	return matched, nil
}

// FindProjectByRepo returns the single authoritative ("primary") project for
// the given repo. Returns (nil, nil) if no project claims the repo.
//
// Selection rule (issue #267, owner decision (b) — single authority):
//   - if the repo's config sets primary_project AND that project still
//     contains the repo, return it;
//   - otherwise fall back to the lexicographically first project that
//     contains the repo (deterministic; List is name-sorted).
//
// The fallback guarantees that a single-membership repo behaves exactly as
// before, and that a stale/dangling primary_project (project deleted or repo
// removed from it) degrades gracefully instead of injecting the wrong header.
func FindProjectByRepo(repo string) (*Project, error) {
	matched, err := FindProjectsByRepo(repo)
	if err != nil {
		return nil, err
	}
	if len(matched) == 0 {
		return nil, nil
	}
	if primary := primaryProjectName(repo); primary != "" {
		for _, p := range matched {
			if p.Name == primary {
				return p, nil
			}
		}
	}
	return matched[0], nil
}

// primaryProjectName reads the repo's configured primary_project from
// config.yaml. Returns "" when config is absent or the field is unset — both
// are non-fatal and trigger the lexicographic fallback in FindProjectByRepo.
func primaryProjectName(repo string) string {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Repos[repo].PrimaryProject
}

// ReadContext reads the project's context.md. Returns "" if the file
// doesn't exist (not an error — the user may not have generated it yet).
func ReadContext(name string) (string, error) {
	data, err := os.ReadFile(ContextPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// WriteContext writes content to the project's context.md.
func WriteContext(name, content string) error {
	dir := ProjectDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(ContextPath(name), []byte(content), 0o644); err != nil {
		return err
	}
	ensureGit(name)
	if err := CommitChange(name, "update context.md"); err != nil {
		fmt.Fprintf(os.Stderr, "[project] git commit skipped: %v\n", err)
	}
	return nil
}

// ReadTesting reads the project's testing.md. Returns "" if the file
// doesn't exist.
func ReadTesting(name string) (string, error) {
	data, err := os.ReadFile(TestingPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// WriteTesting writes content to the project's testing.md.
func WriteTesting(name, content string) error {
	dir := ProjectDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(TestingPath(name), []byte(content), 0o644); err != nil {
		return err
	}
	ensureGit(name)
	if err := CommitChange(name, "update testing.md"); err != nil {
		fmt.Fprintf(os.Stderr, "[project] git commit skipped: %v\n", err)
	}
	return nil
}

// ReadDeployment reads the project's deployment.md. Returns "" if the
// file doesn't exist (not an error — the user may not have created it yet).
//
// deployment.md contains commands the Pilot uses to inspect runtime health:
// log retrieval, metrics endpoints, SSH/kubectl invocations, etc.
func ReadDeployment(name string) (string, error) {
	data, err := os.ReadFile(DeploymentPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// WriteDeployment writes content to the project's deployment.md.
func WriteDeployment(name, content string) error {
	dir := ProjectDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(DeploymentPath(name), []byte(content), 0o644); err != nil {
		return err
	}
	ensureGit(name)
	if err := CommitChange(name, "update deployment.md"); err != nil {
		fmt.Fprintf(os.Stderr, "[project] git commit skipped: %v\n", err)
	}
	return nil
}

// SetAutomation is a compatibility no-op. Pilot is a global conversation steward.
func SetAutomation(_ string, _ bool, _ int) error { return nil }

// SetAutomationBoundMachine is a compatibility no-op; steward invocations use
// the explicit machine-local Offload profile.
func SetAutomationBoundMachine(_, _ string) error { return nil }

// HeaderForRepo returns a project-context header to prepend to any
// prompt for an operation rooted at `repo`. Empty string if the repo
// isn't a member of any project, or if the project has no
// non-trivial context.md / testing.md / deployment.md to share.
//
// The header carries everything an AI consumer (operator, chat) needs
// to act with project-wide awareness:
//
//   - which project this repo belongs to
//   - sibling repos in the same project
//   - the project's context.md (architecture / conventions / state)
//   - the project's testing.md (local env SOP — start order, services,
//     hardware hookups; consulted before doing local verification)
//   - the project's deployment.md (runtime environment and log
//     retrieval methods; consulted by PM operators for health checks)
//
// All three sections are emitted only when non-empty, so a project
// with no docs won't generate a noisy "_(empty)_" header. The
// returned string ends with `\n---\n\n` so a caller can concatenate
// it directly in front of an existing system prompt.
func HeaderForRepo(repo string, contexts ...context.Context) string {
	p, err := FindProjectByRepo(repo)
	if err != nil || p == nil {
		return ""
	}
	ctx, _ := ReadContext(p.Name)
	testing, _ := ReadTesting(p.Name)
	testing = pinnedTesting(testing, contexts)
	deployment, _ := ReadDeployment(p.Name)
	if strings.TrimSpace(ctx) == "" && strings.TrimSpace(testing) == "" && strings.TrimSpace(deployment) == "" {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Project Context: %s\n\n", p.Name)
	fmt.Fprintf(&b, "This repo is part of the %q project (members: %s).\n\n",
		p.Name, strings.Join(p.Repos, ", "))
	if strings.TrimSpace(ctx) != "" {
		fmt.Fprintf(&b, "## Project overview (context.md)\n\n%s\n\n", ctx)
	}
	b.WriteString(testingSection(testing))
	if strings.TrimSpace(deployment) != "" {
		fmt.Fprintf(&b, "## Deployment environment (deployment.md)\n\n")
		fmt.Fprintf(&b, "_Runtime environment details and log retrieval methods.\n")
		fmt.Fprintf(&b, "Consult this when diagnosing runtime health or fetching logs._\n\n")
		fmt.Fprintf(&b, "%s\n\n", deployment)
	}
	b.WriteString("---\n\n")
	return b.String()
}

// TestingHeaderForRepo shares only the primary project's environment SOP with
// independent reviewers. Context and deployment documents may contain development
// conclusions and must not enter the requirements-only planner or review input.
func TestingHeaderForRepo(repo string, contexts ...context.Context) string {
	p, err := FindProjectByRepo(repo)
	if err != nil || p == nil {
		return ""
	}
	text, _ := ReadTesting(p.Name)
	text = pinnedTesting(text, contexts)
	section := testingSection(text)
	if section == "" {
		return ""
	}
	return fmt.Sprintf("# Project testing environment: %s\n\n%s---\n\n", p.Name, section)
}

func testingSection(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "## Local environment SOP (testing.md)\n\n" +
		"Consult this machine-local SOP before local verification. It describes environment procedures, not development results or additional requirements. " +
		"It grants no permission to control desktop apps, replace daemons, access credentials, change production projects or expand publishing scope. " +
		"Required live checks without the SOP, capabilities, a fresh unique target or the loaded candidate must remain blocked; report readiness steps and next_owner. " +
		"Keep offline, environment-ready, candidate-loaded, live-write and save/reload evidence separate; never infer live verification from a build or CI. " +
		"Use fresh independent evidence for the pinned head; preserve failed and unrun checks.\n\n" + text + "\n\n"
}

// save persists a Project to its project.yaml.
func save(p *Project) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}
	if err := os.WriteFile(yamlPath(p.Name), data, 0o644); err != nil {
		return err
	}
	ensureGit(p.Name)
	if err := CommitChange(p.Name, "update project.yaml"); err != nil {
		fmt.Fprintf(os.Stderr, "[project] git commit skipped: %v\n", err)
	}
	return nil
}

// testingSnapshotKey is private to prevent SOP overrides from untrusted payloads.
type testingSnapshotKey struct{}

// WithTestingSnapshot pins locally verified SOP bytes for this execution only.
func WithTestingSnapshot(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, testingSnapshotKey{}, text)
}

func pinnedTesting(current string, contexts []context.Context) string {
	if len(contexts) > 0 && contexts[0] != nil {
		if text, ok := contexts[0].Value(testingSnapshotKey{}).(string); ok {
			return text
		}
	}
	return current
}
