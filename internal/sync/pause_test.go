package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPauseMigratedHostAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "Library", "Application Support", "ClawFlow")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(home, ".clawflow")); err != nil {
		t.Fatal(err)
	}
	if err := Pause(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sync-paused")); err != nil {
		t.Fatal(err)
	}
	if paused, err := IsPaused(); err != nil || !paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	if err := Resume(); err != nil {
		t.Fatal(err)
	}
	if paused, err := IsPaused(); err != nil || paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	// A marker redirected elsewhere must still fail closed.
	if err := os.Symlink(filepath.Join(home, "other"), filepath.Join(root, "sync-paused")); err != nil {
		t.Fatal(err)
	}
	if _, err := IsPaused(); err == nil {
		t.Fatal("linked marker accepted")
	}
}

func TestPauseRejectsUntrustedMigrationAlias(t *testing.T) {
	for _, redirectedParent := range []bool{false, true} {
		t.Run(fmt.Sprint(redirectedParent), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			other := t.TempDir()
			target := other
			if redirectedParent {
				if err := os.Symlink(other, filepath.Join(home, "Library")); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(home, "Library", "Application Support", "ClawFlow")
			}
			if err := os.Symlink(target, filepath.Join(home, ".clawflow")); err != nil {
				t.Fatal(err)
			}
			if err := Pause(); err == nil {
				t.Fatal("untrusted alias accepted")
			}
		})
	}
}

func TestPausedClientBeforeCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".clawflow", "config"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".clawflow", "config", "credentials.yaml")
	if err := os.WriteFile(path, []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Pause(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Client(); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("got %v", err)
	}
	if err := Resume(); err != nil {
		t.Fatal(err)
	}
	if paused, err := IsPaused(); err != nil || paused {
		t.Fatalf("paused=%v err=%v", paused, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "invalid: [" {
		t.Fatal("credentials modified")
	}
}
