package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func saveFixture(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// The 2026-11-04 outage: a binary older than the config loaded it, could not
// represent newer fields, and rewrote the file without them.
func TestSaveRefusesToDropSettingsItDoesNotKnow(t *testing.T) {
	for name, content := range map[string]string{
		"settings": "settings:\n  future_controller:\n    secret_value: do-not-print\nrepos: {}\n",
		"nested":   "settings:\n  coordinator:\n    enabled: true\n    future_field: do-not-print\nrepos: {}\n",
		"repo":     "repos:\n  o/r:\n    enabled: true\n    future_routing: do-not-print\n",
		"top":      "future_section:\n  value: do-not-print\nrepos: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := saveFixture(t, content)
			err := (&Config{}).Save()
			if !errors.Is(err, ErrConfigNewer) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "do-not-print") {
				t.Fatalf("error exposes a value: %v", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != content {
				t.Fatalf("file changed: %q %v", got, readErr)
			}
		})
	}
}

func TestSaveKeepsKnownSettingsAndAllowsExplicitClearing(t *testing.T) {
	path := saveFixture(t, `settings:
  database:
    dsn: postgres://fake@127.0.0.1:1/fake
  coordinator:
    enabled: true
    listen: "8093"
    local_worker:
      enabled: true
      max_parallel: 4
      tags: [a]
repos:
  o/keep:
    enabled: true
    worker_tags: [host-x]
    allowed_issue_authors: [release-bot]
    acceptance_worker: different
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.Database.DSN == "" || !cfg.Settings.Coordinator.LocalWorker.Enabled || len(cfg.Repos["o/keep"].WorkerTags) != 1 || strings.Join(cfg.Repos["o/keep"].AllowedIssueAuthors, ",") != "release-bot" || cfg.Repos["o/keep"].AcceptanceWorker != "different" {
		t.Fatalf("round trip lost settings: %+v", cfg)
	}
	// A known field can still be cleared on purpose.
	cfg.Settings.Database.DSN = ""
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dsn") {
		t.Fatalf("explicit clear ignored:\n%s", raw)
	}
}

// Everything this binary writes must pass its own guard, for every field.
func TestSaveAcceptsItsOwnOutput(t *testing.T) {
	data, err := yaml.Marshal(&Config{Repos: map[string]Repo{"o/r": {Enabled: true, WorkerTags: []string{"t"}, AllowedIssueAuthors: []string{"bot"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if unknown := unknownConfigKeys(data); len(unknown) != 0 {
		t.Fatalf("unknown: %v", unknown)
	}
	if unknown := unknownConfigKeys([]byte("not: [valid")); unknown != nil {
		t.Fatalf("malformed input: %v", unknown)
	}
}

func TestSaveFilePermissionsAndNoLeftovers(t *testing.T) {
	path := saveFixture(t, "")
	if err := (&Config{}).Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode: %v %v", info, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{}).Save(); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("existing mode not kept: %v %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestSaveKeepsSymlinkedConfig(t *testing.T) {
	path := saveFixture(t, "")
	target := filepath.Join(t.TempDir(), "real.yaml")
	if err := os.WriteFile(target, []byte("repos: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{Repos: map[string]Repo{"o/r": {Enabled: true}}}).Save(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v %v", info, err)
	}
	raw, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(raw), "o/r") {
		t.Fatalf("target not updated: %q %v", raw, err)
	}
}

// `sync pull` replaces Settings with the remote copy, which must not remove or
// import this host's controller role.
func TestSyncKeepsCoordinatorLocal(t *testing.T) {
	local := &Config{Repos: map[string]Repo{}}
	local.Settings.Coordinator = CoordinatorSettings{Enabled: true, Listen: "8093"}
	payload, err := MarshalForGist(local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "8093") || strings.Contains(string(payload), "coordinator") {
		t.Fatalf("coordinator exported:\n%s", payload)
	}
	merged, err := MergeConfigs(local, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Settings.Coordinator.Enabled || merged.Settings.Coordinator.Listen != "8093" {
		t.Fatalf("pull removed coordinator: %+v", merged.Settings.Coordinator)
	}
	remote := []byte("settings:\n  coordinator:\n    enabled: true\n    listen: \"9999\"\n")
	other := &Config{Repos: map[string]Repo{}}
	merged, err = MergeConfigs(other, remote)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Settings.Coordinator.Enabled || merged.Settings.Coordinator.Listen != "" {
		t.Fatalf("pull imported a remote role: %+v", merged.Settings.Coordinator)
	}
}
