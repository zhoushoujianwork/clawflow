package sync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
