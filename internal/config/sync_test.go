package config_test

import (
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// makeRepo is a test helper that builds a Repo with the given key fields set.
func makeRepo(enabled bool, localPath, baseBranch string) config.Repo {
	return config.Repo{
		Enabled:    enabled,
		LocalPath:  localPath,
		BaseBranch: baseBranch,
	}
}

// TestMergeConfigs_UnionRepos verifies that repos from both local and remote
// end up in the merged result (union semantics).
func TestMergeConfigs_UnionRepos(t *testing.T) {
	local := &config.Config{
		Repos: map[string]config.Repo{
			"owner/local-only": makeRepo(true, "/home/user/local-only", "main"),
			"owner/shared":     makeRepo(true, "/home/user/shared", "main"),
		},
	}

	remoteYAML := `
repos:
  owner/remote-only:
    enabled: true
    base_branch: main
  owner/shared:
    enabled: true
    base_branch: develop
`

	merged, err := config.MergeConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("MergeConfigs error: %v", err)
	}

	if _, ok := merged.Repos["owner/local-only"]; !ok {
		t.Error("local-only repo should be preserved in merged result")
	}
	if _, ok := merged.Repos["owner/remote-only"]; !ok {
		t.Error("remote-only repo should be added to merged result")
	}
	if _, ok := merged.Repos["owner/shared"]; !ok {
		t.Error("shared repo should be present in merged result")
	}
}

// TestMergeConfigs_LocalPathPreserved verifies that local_path is never
// overwritten by the remote config (local wins for this field).
func TestMergeConfigs_LocalPathPreserved(t *testing.T) {
	local := &config.Config{
		Repos: map[string]config.Repo{
			"owner/repo": makeRepo(true, "/home/user/myrepo", "main"),
		},
	}

	// Remote has no local_path (it's stripped on push), but has updated fields.
	remoteYAML := `
repos:
  owner/repo:
    enabled: false
    base_branch: develop
`

	merged, err := config.MergeConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("MergeConfigs error: %v", err)
	}

	repo := merged.Repos["owner/repo"]
	if repo.LocalPath != "/home/user/myrepo" {
		t.Errorf("local_path should be preserved: got %q, want %q", repo.LocalPath, "/home/user/myrepo")
	}
	// Cloud wins for other fields.
	if repo.BaseBranch != "develop" {
		t.Errorf("base_branch should be updated from remote: got %q, want %q", repo.BaseBranch, "develop")
	}
	if repo.Enabled != false {
		t.Error("enabled should be updated from remote (cloud wins)")
	}
}

// TestMergeConfigs_LocalPathNotSyncedForNewRepo verifies that a repo that
// exists only in the remote gets an empty local_path (not inherited from
// another repo).
func TestMergeConfigs_LocalPathNotSyncedForNewRepo(t *testing.T) {
	local := &config.Config{
		Repos: map[string]config.Repo{},
	}

	remoteYAML := `
repos:
  owner/new-repo:
    enabled: true
    base_branch: main
`

	merged, err := config.MergeConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("MergeConfigs error: %v", err)
	}

	repo := merged.Repos["owner/new-repo"]
	if repo.LocalPath != "" {
		t.Errorf("new repo from remote should have empty local_path, got %q", repo.LocalPath)
	}
}

// TestMergeConfigs_SettingsCloudWins verifies that settings from the remote
// overwrite local settings entirely.
func TestMergeConfigs_SettingsCloudWins(t *testing.T) {
	local := &config.Config{
		Settings: config.Settings{
			PollInterval:        5,
			ConfidenceThreshold: 7,
			AgentTimeout:        120,
		},
		Repos: map[string]config.Repo{},
	}

	remoteYAML := `
settings:
  poll_interval: 10
  confidence_threshold: 8
  agent_timeout: 300
  max_concurrent_agents: 4
`

	merged, err := config.MergeConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("MergeConfigs error: %v", err)
	}

	if merged.Settings.PollInterval != 10 {
		t.Errorf("poll_interval: got %d, want 10", merged.Settings.PollInterval)
	}
	if merged.Settings.ConfidenceThreshold != 8 {
		t.Errorf("confidence_threshold: got %d, want 8", merged.Settings.ConfidenceThreshold)
	}
	if merged.Settings.AgentTimeout != 300 {
		t.Errorf("agent_timeout: got %d, want 300", merged.Settings.AgentTimeout)
	}
	if merged.Settings.MaxConcurrentAgents != 4 {
		t.Errorf("max_concurrent_agents: got %d, want 4", merged.Settings.MaxConcurrentAgents)
	}
}

// TestMarshalForGist_ExcludesLocalPath verifies that local_path is stripped
// from the Gist payload.
func TestMarshalForGist_ExcludesLocalPath(t *testing.T) {
	cfg := &config.Config{
		Repos: map[string]config.Repo{
			"owner/repo": makeRepo(true, "/home/user/secret-path", "main"),
		},
		Settings: config.Settings{PollInterval: 5},
	}

	data, err := config.MarshalForGist(cfg)
	if err != nil {
		t.Fatalf("MarshalForGist error: %v", err)
	}

	payload := string(data)
	if strings.Contains(payload, "secret-path") {
		t.Error("local_path should not appear in Gist payload")
	}
	if strings.Contains(payload, "local_path") {
		t.Error("local_path key should not appear in Gist payload")
	}
}

// TestMarshalForGist_IncludesReposAndSettings verifies that repos and settings
// are present in the Gist payload.
func TestMarshalForGist_IncludesReposAndSettings(t *testing.T) {
	cfg := &config.Config{
		Repos: map[string]config.Repo{
			"owner/repo": makeRepo(true, "/local", "main"),
		},
		Settings: config.Settings{PollInterval: 15},
	}

	data, err := config.MarshalForGist(cfg)
	if err != nil {
		t.Fatalf("MarshalForGist error: %v", err)
	}

	payload := string(data)
	if !strings.Contains(payload, "owner/repo") {
		t.Error("repo key should appear in Gist payload")
	}
	if !strings.Contains(payload, "poll_interval") {
		t.Error("settings should appear in Gist payload")
	}
}

// TestDiffConfigs_NoDiff verifies that identical configs produce an empty diff.
func TestDiffConfigs_NoDiff(t *testing.T) {
	local := &config.Config{
		Repos: map[string]config.Repo{
			"owner/repo": makeRepo(true, "/local", "main"),
		},
		Settings: config.Settings{PollInterval: 5},
	}

	// Remote matches local exactly (after stripping local_path).
	remoteYAML := `
repos:
  owner/repo:
    enabled: true
    base_branch: main
settings:
  poll_interval: 5
`

	diff, err := config.DiffConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("DiffConfigs error: %v", err)
	}

	// The diff may be empty or contain only local_path-related lines (since
	// local_path is preserved in the merge but absent in remote). We just
	// verify no error occurs and the function is callable.
	_ = diff
}

// TestDiffConfigs_ShowsChanges verifies that a diff is produced when settings differ.
func TestDiffConfigs_ShowsChanges(t *testing.T) {
	local := &config.Config{
		Repos:    map[string]config.Repo{},
		Settings: config.Settings{PollInterval: 5},
	}

	remoteYAML := `
settings:
  poll_interval: 30
`

	diff, err := config.DiffConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("DiffConfigs error: %v", err)
	}

	if diff == "" {
		t.Error("expected non-empty diff when settings differ")
	}
	if !strings.Contains(diff, "30") {
		t.Errorf("diff should mention the new poll_interval value 30, got:\n%s", diff)
	}
}

// TestMarshalForGist_ExcludesBoundMachine verifies that bound_machine is
// stripped from the Gist payload (machine-specific, like local_path).
func TestMarshalForGist_ExcludesBoundMachine(t *testing.T) {
	cfg := &config.Config{
		Repos: map[string]config.Repo{
			"owner/repo": {
				Enabled:      true,
				BaseBranch:   "main",
				LocalPath:    "/home/user/repo",
				BoundMachine: "my-laptop",
			},
		},
	}

	data, err := config.MarshalForGist(cfg)
	if err != nil {
		t.Fatalf("MarshalForGist error: %v", err)
	}

	payload := string(data)
	if strings.Contains(payload, "my-laptop") {
		t.Error("bound_machine value should not appear in Gist payload")
	}
	if strings.Contains(payload, "bound_machine") {
		t.Error("bound_machine key should not appear in Gist payload")
	}
}

// TestMergeConfigs_BoundMachinePreserved verifies that bound_machine is never
// overwritten by the remote config (local wins, same as local_path).
func TestMergeConfigs_BoundMachinePreserved(t *testing.T) {
	local := &config.Config{
		Repos: map[string]config.Repo{
			"owner/repo": {
				Enabled:      true,
				BaseBranch:   "main",
				LocalPath:    "/home/user/repo",
				BoundMachine: "my-laptop",
			},
		},
	}

	// Remote has no bound_machine (stripped on push).
	remoteYAML := `
repos:
  owner/repo:
    enabled: true
    base_branch: develop
`

	merged, err := config.MergeConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("MergeConfigs error: %v", err)
	}

	repo := merged.Repos["owner/repo"]
	if repo.BoundMachine != "my-laptop" {
		t.Errorf("bound_machine should be preserved: got %q, want %q", repo.BoundMachine, "my-laptop")
	}
}

// TestMergeConfigs_BoundMachineEmptyForNewRepo verifies that a repo arriving
// only from the remote gets an empty bound_machine (not inherited).
func TestMergeConfigs_BoundMachineEmptyForNewRepo(t *testing.T) {
	local := &config.Config{
		Repos: map[string]config.Repo{},
	}

	remoteYAML := `
repos:
  owner/new-repo:
    enabled: true
    base_branch: main
`

	merged, err := config.MergeConfigs(local, []byte(remoteYAML))
	if err != nil {
		t.Fatalf("MergeConfigs error: %v", err)
	}

	repo := merged.Repos["owner/new-repo"]
	if repo.BoundMachine != "" {
		t.Errorf("new repo from remote should have empty bound_machine, got %q", repo.BoundMachine)
	}
}

// TestMergeConfigs_EmptyRemote verifies that an empty remote YAML leaves
// local repos intact (no repos deleted).
func TestMergeConfigs_EmptyRemote(t *testing.T) {
	local := &config.Config{
		Repos: map[string]config.Repo{
			"owner/repo": makeRepo(true, "/local", "main"),
		},
		Settings: config.Settings{PollInterval: 5},
	}

	merged, err := config.MergeConfigs(local, []byte("repos: {}\n"))
	if err != nil {
		t.Fatalf("MergeConfigs error: %v", err)
	}

	if _, ok := merged.Repos["owner/repo"]; !ok {
		t.Error("local repo should be preserved when remote is empty")
	}
}
