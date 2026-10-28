package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func resetTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	old := processInventory
	processInventory = func() ([]byte, error) { return []byte(""), nil }
	t.Cleanup(func() { processInventory = old })
	return filepath.Join(home, ".clawflow")
}

func resetTestWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResetPreviewAndArchive(t *testing.T) {
	root := resetTestHome(t)
	original := "settings:\n  poll_interval: 17\nrepos:\n  ns/repo:\n    enabled: true\n"
	resetTestWrite(t, filepath.Join(root, "config", "repos.yaml"), original)
	resetTestWrite(t, filepath.Join(root, "config", "credentials.yaml"), "credential fixture")
	for _, dir := range []string{"projects", "data", "chats", "state", "memory", "logs", "worktrees", "skills", "bin", "clones"} {
		resetTestWrite(t, filepath.Join(root, dir, "sentinel"), dir)
	}
	r, cfg, err := planReset()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "planned" || len(r.Entries) != 7 {
		t.Fatalf("%+v", r)
	}
	for _, path := range []string{"config/config.yaml", "config/repos.yaml.bak", "locks", "backups", "sync-paused"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("preview created %s", path)
		}
	}
	if err := applyReset(&r, cfg); err != nil {
		t.Fatal(err)
	}
	if r.Status != "applied" || !r.SyncPaused || len(r.Moved) != 7 {
		t.Fatalf("%+v", r)
	}
	for _, dir := range []string{"projects", "data", "chats", "state", "memory", "logs"} {
		if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
			t.Fatalf("not archived: %s", dir)
		}
		if _, err := os.Stat(filepath.Join(r.Backup, dir, "sentinel")); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"config/credentials.yaml", "worktrees/sentinel", "skills/sentinel", "bin/sentinel", "clones/sentinel"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(r.Backup, "config", "repos.yaml"))
	if string(data) != original {
		t.Fatal("original not preserved")
	}
	data, _ = os.ReadFile(filepath.Join(root, "config", "credentials.yaml"))
	if string(data) != "credential fixture" {
		t.Fatal("credentials changed")
	}
	loaded, err := config.Load()
	if err != nil || len(loaded.Repos) != 0 || loaded.Settings.PollInterval != 17 {
		t.Fatalf("%+v %v", loaded, err)
	}
	st, err := os.Stat(r.Backup)
	if err != nil || st.Mode().Perm() != 0700 {
		t.Fatalf("backup permissions %v %v", st, err)
	}
	var manifest resetResult
	data, _ = os.ReadFile(filepath.Join(r.Backup, "manifest.json"))
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Status != "applied" {
		t.Fatalf("%s %v", data, err)
	}
	next, _, err := planReset()
	if err != nil || next.Status != "noop" {
		t.Fatalf("%+v %v", next, err)
	}
}

func TestResetRefusesUnsafeState(t *testing.T) {
	for _, kind := range []string{"malformed", "symlink-root", "symlink-config", "symlink-target", "run-lock", "issue-lock", "process", "inventory-error"} {
		t.Run(kind, func(t *testing.T) {
			root := resetTestHome(t)
			resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos: {}\n")
			switch kind {
			case "malformed":
				resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos: [")
			case "symlink-root":
				os.Rename(root, root+"-real")
				if err := os.Symlink(root+"-real", root); err != nil {
					t.Fatal(err)
				}
			case "symlink-config":
				os.Rename(filepath.Join(root, "config"), filepath.Join(root, "config-real"))
				if err := os.Symlink(filepath.Join(root, "config-real"), filepath.Join(root, "config")); err != nil {
					t.Fatal(err)
				}
			case "symlink-target":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "data")); err != nil {
					t.Fatal(err)
				}
			case "run-lock":
				resetTestWrite(t, snapshot.RunLockPath(), fmt.Sprintf(`{"pid":%d}`, os.Getpid()))
			case "issue-lock":
				resetTestWrite(t, snapshot.LockPath("ns/repo", 1), fmt.Sprintf(`{"pid":%d}`, os.Getpid()))
			case "process":
				processInventory = func() ([]byte, error) { return []byte("99999 clawflow clawflow --debug web\n"), nil }
			case "inventory-error":
				processInventory = func() ([]byte, error) { return nil, fmt.Errorf("denied") }
			}
			if _, _, err := planReset(); err == nil {
				t.Fatal("expected refusal")
			}
			if _, err := os.Lstat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
				t.Fatal("preview mutated backup state")
			}
		})
	}
}

func TestResetPartialFailureAndRecheck(t *testing.T) {
	root := resetTestHome(t)
	resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos:\n  ns/r: {}\n")
	resetTestWrite(t, filepath.Join(root, "data", "sentinel"), "history")
	r, cfg, err := planReset()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a target disappearing between preview and apply.
	if err := os.Rename(filepath.Join(root, "data"), filepath.Join(root, "preserved-data")); err != nil {
		t.Fatal(err)
	}
	if err := applyReset(&r, cfg); err == nil {
		t.Fatal("expected partial failure")
	}
	if !r.SyncPaused || len(r.Moved) != 1 || r.Status != "failed" || r.Backup == "" {
		t.Fatalf("%+v", r)
	}
	data, _ := os.ReadFile(filepath.Join(r.Backup, "manifest.json"))
	if !bytes.Contains(data, []byte(`"status": "failed"`)) {
		t.Fatalf("%s", data)
	}
	if snapshot.LockHeld(snapshot.RunLockPath()) {
		t.Fatal("run lock not released")
	}
	// A new live process between preview and apply is refused before pausing.
	root = resetTestHome(t)
	resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos:\n  ns/r: {}\n")
	r, cfg, err = planReset()
	if err != nil {
		t.Fatal(err)
	}
	processInventory = func() ([]byte, error) { return []byte("99999 clawflow clawflow run\n"), nil }
	if err := applyReset(&r, cfg); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sync-paused")); !os.IsNotExist(err) {
		t.Fatal("mutated before refusal")
	}
}

func TestResetJSONMissingHomeNoWrites(t *testing.T) {
	root := resetTestHome(t)
	var out bytes.Buffer
	cmd := NewResetCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"noop"`) {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("created empty home state")
	}
}

func TestResetCanonicalConfigArchivesOldLegacySchema(t *testing.T) {
	root := resetTestHome(t)
	resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "settings:\n  poll_interval: 17\nrepos:\n  ns/current: {}\n")
	legacy := "repos: []\n"
	resetTestWrite(t, filepath.Join(root, "config", "repos.yaml"), legacy)
	resetTestWrite(t, filepath.Join(root, "worker.log"), "legacy worker log")
	r, cfg, err := planReset()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyReset(&r, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.Backup, "config", "repos.yaml"))
	if err != nil || string(data) != legacy {
		t.Fatalf("legacy archive changed: %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(r.Backup, "worker.log"))
	if err != nil || string(data) != "legacy worker log" {
		t.Fatalf("worker log archive: %q, %v", data, err)
	}
	loaded, err := config.Load()
	if err != nil || loaded.Settings.PollInterval != 17 || len(loaded.Repos) != 0 {
		t.Fatalf("reset config: %+v, %v", loaded, err)
	}
}

func TestResetEmptyConfigStillPausesSync(t *testing.T) {
	root := resetTestHome(t)
	resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos: {}\n")
	credentials := "gist_id: old-project-list\n"
	resetTestWrite(t, filepath.Join(root, "config", "credentials.yaml"), credentials)
	var out bytes.Buffer
	cmd := NewResetCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--yes", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result resetResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.SyncPaused || result.Backup != "" {
		t.Fatalf("result: %s, %v", out.String(), err)
	}
	if _, err := os.Stat(filepath.Join(root, "sync-paused")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "config", "credentials.yaml"))
	if err != nil || string(data) != credentials {
		t.Fatal("credentials changed")
	}
}

func TestResetPreviewKeepsStaleLocksAndEmptyHome(t *testing.T) {
	root := resetTestHome(t)
	resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos:\n  ns/r: {}\n")
	// An unallocated high PID is not a live owner; preview must not reclaim it.
	resetTestWrite(t, snapshot.RunLockPath(), `{"pid":2147483647}`)
	if _, _, err := planReset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshot.RunLockPath()); err != nil {
		t.Fatal("preview removed stale lock")
	}
	t.Setenv("HOME", "")
	r, _, err := planReset()
	if err != nil || r.Status != "noop" || r.Root != "" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestResetRefusesWhilePerRepoRunLockHeld(t *testing.T) {
	root := resetTestHome(t)
	resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos:\n  ns/r: {}\n")
	// The parent process is a live PID other than ours.
	resetTestWrite(t, snapshot.RepoRunLockPath("ns/r"), fmt.Sprintf(`{"pid":%d,"repo":"ns/r"}`, os.Getppid()))
	_, _, err := planReset()
	if err == nil || !strings.Contains(err.Error(), "stop active clawflow run") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "backups")); !os.IsNotExist(err) {
		t.Fatal("refused reset mutated backup state")
	}
	// A dead owner does not block, and the preview leaves its lock alone.
	resetTestWrite(t, snapshot.RepoRunLockPath("ns/r"), `{"pid":2147483647,"repo":"ns/r"}`)
	if _, _, err := planReset(); err != nil {
		t.Fatalf("stale repo lock blocked reset: %v", err)
	}
}

func TestApplyResetRefusesWhilePerRepoRunLockHeld(t *testing.T) {
	root := resetTestHome(t)
	resetTestWrite(t, filepath.Join(root, "config", "config.yaml"), "repos:\n  ns/r: {}\n")
	resetTestWrite(t, filepath.Join(root, "data", "sentinel"), "history")
	r, cfg, err := planReset()
	if err != nil {
		t.Fatal(err)
	}
	resetTestWrite(t, snapshot.RepoRunLockPath("ns/r"), fmt.Sprintf(`{"pid":%d,"repo":"ns/r"}`, os.Getppid()))
	if err := applyReset(&r, cfg); err == nil || !strings.Contains(err.Error(), "stop active clawflow run") {
		t.Fatalf("err = %v", err)
	}
	if snapshot.LockHeld(snapshot.RunLockPath()) {
		t.Fatal("legacy lock not released after refusal")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "sentinel")); err != nil {
		t.Fatal("data moved despite refusal")
	}
}

func TestResetPersistentLockOwner(t *testing.T) {
	resetTestHome(t)
	if err := snapshot.AcquireLock("o/r", 1, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := resetIdle(false); err == nil {
		t.Fatal("reset accepted active issue lock")
	}
	snapshot.ReleaseLock("o/r", 1)
	if err := resetIdle(false); err != nil {
		t.Fatalf("released owner blocked reset: %v", err)
	}
}
