// Package project manages project definitions stored under
// ~/.clawflow/projects/<name>/. A project groups multiple repos
// and carries an AI-generated or user-edited context.md that gets
// auto-injected into every `clawflow chat` session for member repos.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Project is the in-memory representation of project.yaml.
type Project struct {
	Name       string     `yaml:"name"`
	Repos      []string   `yaml:"repos"`
	Automation Automation `yaml:"automation,omitempty"`
	CreatedAt  string     `yaml:"created_at"`
	UpdatedAt  string     `yaml:"updated_at"`
}

// Automation captures the per-project "project manager" toggle.
//
// When Enabled is true, every `clawflow run` pass will, after the
// fixed-layer operators finish, wake the project manager (a
// non-interactive `claude -p` invocation) for this project. The PM
// is restricted to creating new issues — it does not touch existing
// issue state. Created issues flow back into the operator pipeline
// on the next pass, forming the closed loop.
//
// CooldownMinutes throttles wakeups: even if `clawflow run` is
// invoked every few minutes, the PM only fires when at least
// CooldownMinutes has elapsed since LastWokenAt. Zero = no cooldown
// (fires every pass — usually too aggressive).
//
// LastWokenAt is the RFC3339 UTC timestamp of the last PM invocation;
// the runner stamps it after each wake (success or failure) to anchor
// the next cooldown window.
type Automation struct {
	Enabled         bool   `yaml:"enabled"`
	CooldownMinutes int    `yaml:"cooldown_minutes,omitempty"`
	LastWokenAt     string `yaml:"last_woken_at,omitempty"`
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
// endpoints, and any other commands the PM should run to assess the
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
	// Create empty context.md, testing.md, and deployment.md so all
	// three files always exist. context.md is the project overview;
	// testing.md is the local-environment SOP; deployment.md describes
	// the runtime environment and log retrieval methods. All three get
	// auto-injected into operator prompts via the project header.
	if err := os.WriteFile(ContextPath(name), []byte(""), 0o644); err != nil {
		return nil, fmt.Errorf("create context.md: %w", err)
	}
	if err := os.WriteFile(TestingPath(name), []byte(""), 0o644); err != nil {
		return nil, fmt.Errorf("create testing.md: %w", err)
	}
	if err := os.WriteFile(DeploymentPath(name), []byte(""), 0o644); err != nil {
		return nil, fmt.Errorf("create deployment.md: %w", err)
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

// AddRepo associates a repo with the project. Returns an error if the
// repo is already a member of this project or belongs to another project.
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
	// Enforce one-project constraint: scan all projects.
	existing, err := FindProjectByRepo(repo)
	if err == nil && existing != nil {
		return fmt.Errorf("repo %q already belongs to project %q", repo, existing.Name)
	}
	p.Repos = append(p.Repos, repo)
	sort.Strings(p.Repos)
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return save(p)
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
	return save(p)
}

// FindProjectByRepo scans all projects and returns the one containing
// the given repo. Returns (nil, nil) if no project claims the repo.
func FindProjectByRepo(repo string) (*Project, error) {
	projects, err := List()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		for _, r := range p.Repos {
			if r == repo {
				return p, nil
			}
		}
	}
	return nil, nil
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
	return os.WriteFile(ContextPath(name), []byte(content), 0o644)
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
	return os.WriteFile(TestingPath(name), []byte(content), 0o644)
}

// ReadDeployment reads the project's deployment.md. Returns "" if the
// file doesn't exist (not an error — the user may not have created it yet).
//
// deployment.md contains commands the PM uses to inspect runtime health:
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
	return os.WriteFile(DeploymentPath(name), []byte(content), 0o644)
}

// SetAutomation flips the automation toggle and/or cooldown for a
// project. Pass cooldownMinutes < 0 to leave the existing value in
// place (use case: enable/disable without retyping the cooldown).
func SetAutomation(name string, enabled bool, cooldownMinutes int) error {
	p, err := Get(name)
	if err != nil {
		return err
	}
	p.Automation.Enabled = enabled
	if cooldownMinutes >= 0 {
		p.Automation.CooldownMinutes = cooldownMinutes
	}
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return save(p)
}

// MarkWoken stamps LastWokenAt to now. Called by the runner right
// before invoking the PM so cooldown accounting starts immediately —
// a PM that takes 20 minutes to think doesn't get re-fired the
// instant it returns.
func MarkWoken(name string) error {
	p, err := Get(name)
	if err != nil {
		return err
	}
	p.Automation.LastWokenAt = time.Now().UTC().Format(time.RFC3339)
	p.UpdatedAt = p.Automation.LastWokenAt
	return save(p)
}

// CooldownRemaining returns how long until this project's PM can
// next be woken. Returns 0 if the project is ready (cooldown elapsed
// or never woken). Returns 0 also when automation is disabled — the
// caller is expected to filter on Enabled separately.
func (p *Project) CooldownRemaining(now time.Time) time.Duration {
	if p.Automation.CooldownMinutes <= 0 || p.Automation.LastWokenAt == "" {
		return 0
	}
	last, err := time.Parse(time.RFC3339, p.Automation.LastWokenAt)
	if err != nil {
		return 0
	}
	deadline := last.Add(time.Duration(p.Automation.CooldownMinutes) * time.Minute)
	if now.After(deadline) {
		return 0
	}
	return deadline.Sub(now)
}

// ListAutomationEnabled returns all projects with Automation.Enabled
// set, in name order. Used by the runner to decide which PMs to fan
// out to at the end of each pass.
func ListAutomationEnabled() ([]*Project, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	enabled := make([]*Project, 0, len(all))
	for _, p := range all {
		if p.Automation.Enabled {
			enabled = append(enabled, p)
		}
	}
	return enabled, nil
}

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
func HeaderForRepo(repo string) string {
	p, err := FindProjectByRepo(repo)
	if err != nil || p == nil {
		return ""
	}
	ctx, _ := ReadContext(p.Name)
	testing, _ := ReadTesting(p.Name)
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
	if strings.TrimSpace(testing) != "" {
		fmt.Fprintf(&b, "## Local environment SOP (testing.md)\n\n")
		fmt.Fprintf(&b, "_How to bring up the local runtime for this project — startup\n")
		fmt.Fprintf(&b, "order, services, hardware/serial hookups. Consult this before\n")
		fmt.Fprintf(&b, "running local verification of code changes._\n\n")
		fmt.Fprintf(&b, "%s\n\n", testing)
	}
	if strings.TrimSpace(deployment) != "" {
		fmt.Fprintf(&b, "## Deployment environment (deployment.md)\n\n")
		fmt.Fprintf(&b, "_Runtime environment details and log retrieval methods.\n")
		fmt.Fprintf(&b, "Consult this when diagnosing runtime health or fetching logs._\n\n")
		fmt.Fprintf(&b, "%s\n\n", deployment)
	}
	b.WriteString("---\n\n")
	return b.String()
}

// save persists a Project to its project.yaml.
func save(p *Project) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}
	return os.WriteFile(yamlPath(p.Name), data, 0o644)
}
