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
	Name      string   `yaml:"name"`
	Repos     []string `yaml:"repos"`
	CreatedAt string   `yaml:"created_at"`
	UpdatedAt string   `yaml:"updated_at"`
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
	// Create an empty context.md so the file always exists.
	if err := os.WriteFile(ContextPath(name), []byte(""), 0o644); err != nil {
		return nil, fmt.Errorf("create context.md: %w", err)
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

// save persists a Project to its project.yaml.
func save(p *Project) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}
	return os.WriteFile(yamlPath(p.Name), data, 0o644)
}
