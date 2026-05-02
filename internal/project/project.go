// Package project manages project definitions stored under
// ~/.clawflow/projects/<name>/. A project groups multiple repos
// and carries a context.md that is auto-injected into clawflow chat
// sessions for any member repo.
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

// projectsRoot returns ~/.clawflow/projects.
func projectsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "projects")
}

// projectDir returns the directory for a named project.
func projectDir(name string) string {
	return filepath.Join(projectsRoot(), name)
}

// yamlPath returns the project.yaml path for a named project.
func yamlPath(name string) string {
	return filepath.Join(projectDir(name), "project.yaml")
}

// ContextPath returns the context.md path for a named project.
func ContextPath(name string) string {
	return filepath.Join(projectDir(name), "context.md")
}

// Create creates a new project with the given name. Returns an error
// if a project with that name already exists.
func Create(name string) (*Project, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("project name cannot be empty")
	}
	dir := projectDir(name)
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
	root := projectsRoot()
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
			continue // skip malformed entries
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
	dir := projectDir(name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("project %q not found", name)
	}
	return os.RemoveAll(dir)
}

// AddRepo associates a repo with the project. Enforces the one-project
// constraint: if the repo already belongs to another project, an error
// is returned.
func AddRepo(name, repo string) error {
	p, err := Get(name)
	if err != nil {
		return err
	}
	// Check uniqueness across all projects.
	owner, err := FindByRepo(repo)
	if err == nil && owner.Name != name {
		return fmt.Errorf("repo %q already belongs to project %q", repo, owner.Name)
	}
	for _, r := range p.Repos {
		if r == repo {
			return fmt.Errorf("repo %q is already in project %q", repo, name)
		}
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
	idx := -1
	for i, r := range p.Repos {
		if r == repo {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("repo %q is not in project %q", repo, name)
	}
	p.Repos = append(p.Repos[:idx], p.Repos[idx+1:]...)
	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return save(p)
}

// FindByRepo scans all projects and returns the one containing the
// given repo. Returns an error if no project owns the repo.
func FindByRepo(repo string) (*Project, error) {
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
	return nil, fmt.Errorf("no project contains repo %q", repo)
}

// ReadContext reads the project's context.md. Returns "" if the file
// does not exist (not an error — the user may not have generated it yet).
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

// WriteContext overwrites the project's context.md.
func WriteContext(name, content string) error {
	dir := projectDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(ContextPath(name), []byte(content), 0o644)
}

// save marshals the project to its project.yaml.
func save(p *Project) error {
	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}
	return os.WriteFile(yamlPath(p.Name), data, 0o644)
}
