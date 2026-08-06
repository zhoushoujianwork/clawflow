package snapshot

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// makeClone builds a bare remote with a `main` branch plus a clone of it,
// returning the clone path.
func makeClone(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	clone := filepath.Join(root, "clone")
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(root, "init", "--bare", "--initial-branch=main", remote)
	git(root, "init", "--initial-branch=main", work)
	git(work, "config", "user.email", "t@example.com")
	git(work, "config", "user.name", "t")
	git(work, "commit", "--allow-empty", "-m", "init")
	git(work, "remote", "add", "origin", remote)
	git(work, "push", "-u", "origin", "main")
	git(root, "clone", remote, clone)
	return clone
}

func readRepoViews(t *testing.T) map[string]RepoView {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(DataDir(), "repos.json"))
	if err != nil {
		t.Fatalf("read repos.json: %v", err)
	}
	var views []RepoView
	if err := json.Unmarshal(raw, &views); err != nil {
		t.Fatalf("unmarshal repos.json: %v", err)
	}
	byName := make(map[string]RepoView, len(views))
	for _, v := range views {
		byName[v.FullName] = v
	}
	return byName
}

// A base_branch of "origin" (the issue #300 misconfiguration) must be flagged
// in repos.json so the dashboard can render it, while a correct one is not.
func TestWriteRepos_FlagsInvalidBaseBranch(t *testing.T) {
	clone := makeClone(t)
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Repos: map[string]config.Repo{
		"o/bad":  {Enabled: true, LocalPath: clone, BaseBranch: "origin"},
		"o/good": {Enabled: true, LocalPath: clone, BaseBranch: "main"},
	}}
	if err := WriteRepos(cfg); err != nil {
		t.Fatalf("WriteRepos: %v", err)
	}

	views := readRepoViews(t)
	if bad := views["o/bad"]; bad.BaseBranchValid || bad.BaseBranchHint == "" {
		t.Errorf("misconfigured base should be flagged with a hint: %+v", bad)
	}
	if good := views["o/good"]; !good.BaseBranchValid || good.BaseBranchHint != "" {
		t.Errorf("correct base should be clean: %+v", good)
	}
}

// Repos without a local clone can't be validated; they must not be flagged.
func TestWriteRepos_NoLocalPathStaysValid(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Repos: map[string]config.Repo{
		"o/remote-only": {Enabled: true, BaseBranch: "whatever"},
		"o/disabled":    {Enabled: false, BaseBranch: "origin"},
	}}
	if err := WriteRepos(cfg); err != nil {
		t.Fatalf("WriteRepos: %v", err)
	}
	for name, v := range readRepoViews(t) {
		if !v.BaseBranchValid {
			t.Errorf("%s should not be flagged (unvalidatable): %+v", name, v)
		}
	}
}
