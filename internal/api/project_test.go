package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSaveLoadGenerateJob_Roundtrip locks the on-disk shape of a
// persisted generate-context job and verifies load returns the same
// fields after a fresh process boot.
func TestSaveLoadGenerateJob_Roundtrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	const name = "gen-roundtrip"
	want := &generateJob{
		Status:    "error",
		Error:     "claude exited 1",
		StartedAt: time.Now().UTC().Truncate(time.Second),
		EndedAt:   time.Now().UTC().Truncate(time.Second),
	}
	if err := saveGenerateJob(name, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	path := filepath.Join(tmp, ".clawflow", "projects", name, "generate-context.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}

	got, err := loadGenerateJob(name)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil for a persisted job")
	}
	if got.Status != want.Status || got.Error != want.Error {
		t.Fatalf("status/error mismatch: got %+v want %+v", got, want)
	}
}

func TestLoadGenerateJob_Missing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := loadGenerateJob("never-saved")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil job for missing file, got %+v", got)
	}
}
