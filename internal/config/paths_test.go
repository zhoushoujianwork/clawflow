package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootDirFollowsMigratedAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	legacy := filepath.Join(home, ".clawflow")
	if RootDir() != legacy {
		t.Fatal("legacy resolution changed before migration")
	}
	standard := filepath.Join(home, "Library", "Application Support", "ClawFlow")
	if err := os.MkdirAll(standard, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(standard, legacy); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(standard)
	if err != nil {
		t.Fatal(err)
	}
	if RootDir() != canonical || StateDir() != canonical {
		t.Fatal("runtime and config roots disagree")
	}
	if ConfigPath() != filepath.Join(canonical, "config", "config.yaml") || CredentialsPath() != filepath.Join(canonical, "config", "credentials.yaml") {
		t.Fatal("configuration and credentials did not follow migration")
	}
	t.Setenv("CLAWFLOW_STATE_DIR", filepath.Join(home, "independent-worker"))
	if StateDir() == RootDir() {
		t.Fatal("independent worker state override lost")
	}
}
