package commands

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// writeTestConfig seeds an isolated ~/.clawflow/config/config.yaml under a
// temp HOME so config.Load() in newVCSClientForRepo reads our fixture instead
// of the developer's real config.
func writeTestConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNewVCSClientForRepo_ConfiguredRepo(t *testing.T) {
	writeTestConfig(t, &config.Config{
		Repos: map[string]config.Repo{
			"acme/widget": {Enabled: true, Platform: "github"},
		},
	})

	client, canon, repo, err := newVCSClientForRepo("acme/widget")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	if canon != "acme/widget" {
		t.Errorf("canonical name = %q, want acme/widget", canon)
	}
	if repo.Platform != "github" {
		t.Errorf("platform = %q, want github", repo.Platform)
	}
}

// The headline feature: an unconfigured "owner/repo" no longer errors out —
// it resolves to a transient github config and a usable client.
func TestNewVCSClientForRepo_UnconfiguredOwnerRepo(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{}})

	client, canon, repo, err := newVCSClientForRepo("someone/unadded")
	if err != nil {
		t.Fatalf("unconfigured owner/repo should resolve, got error: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	if canon != "someone/unadded" {
		t.Errorf("canonical name = %q, want someone/unadded", canon)
	}
	if repo.Platform != "github" {
		t.Errorf("platform = %q, want github", repo.Platform)
	}
	if repo.BaseURL != "" {
		t.Errorf("base_url = %q, want empty for github.com", repo.BaseURL)
	}
}

// A github.com URL must canonicalize down to owner/repo, because the API
// layer's splitRepo cannot split a full URL.
func TestNewVCSClientForRepo_GitHubURL(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{}})

	_, canon, repo, err := newVCSClientForRepo("https://github.com/acme/widget")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if canon != "acme/widget" {
		t.Errorf("canonical name = %q, want acme/widget", canon)
	}
	if repo.Platform != "github" {
		t.Errorf("platform = %q, want github", repo.Platform)
	}
}

// A self-hosted GitLab URL is the only way to learn the instance base_url
// without `repo add`. Verify platform + base_url are inferred from the URL.
func TestNewVCSClientForRepo_SelfHostedGitLabURL(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{}})

	client, canon, repo, err := newVCSClientForRepo("https://git.example.com/group/sub/proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	if canon != "group/sub/proj" {
		t.Errorf("canonical name = %q, want group/sub/proj", canon)
	}
	if repo.Platform != "gitlab" {
		t.Errorf("platform = %q, want gitlab", repo.Platform)
	}
	if repo.BaseURL != "https://git.example.com" {
		t.Errorf("base_url = %q, want https://git.example.com", repo.BaseURL)
	}
}

// A URL pointing at an already-configured repo should prefer the stored
// config (and its canonical owner/repo key) over a fresh transient one.
func TestNewVCSClientForRepo_URLMatchesConfiguredRepo(t *testing.T) {
	writeTestConfig(t, &config.Config{
		Repos: map[string]config.Repo{
			"acme/widget": {Enabled: true, Platform: "github", BaseBranch: "trunk"},
		},
	})

	_, canon, repo, err := newVCSClientForRepo("https://github.com/acme/widget")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if canon != "acme/widget" {
		t.Errorf("canonical name = %q, want acme/widget", canon)
	}
	if repo.BaseBranch != "trunk" {
		t.Errorf("base_branch = %q, want trunk (stored config should win)", repo.BaseBranch)
	}
}
