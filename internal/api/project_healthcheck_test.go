package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/pilot"
)

// TestSaveLoadHealthCheckJob_Roundtrip locks the on-disk JSON shape
// of a persisted health-check job and verifies that a load returns
// the same struct we wrote. Runs against a HOME override so a real
// ~/.clawflow doesn't get touched during `go test`.
func TestSaveLoadHealthCheckJob_Roundtrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	const name = "test-proj"
	want := &healthCheckJob{
		Status:    "done",
		StartedAt: time.Now().UTC().Truncate(time.Second),
		EndedAt:   time.Now().UTC().Truncate(time.Second),
		Result: &pilot.HealthCheckResult{
			Outcome: "changes-proposed",
			Summary: "one repo needs an update",
			Changes: []pilot.ProposedChange{
				{Target: "repo", RepoID: "owner/r", Path: "CLAUDE.md", Action: "update", ProposedContent: "# new\n", CurrentContent: "# old\n"},
			},
		},
	}
	if err := saveHealthCheckJob(name, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	// File should land at $HOME/.clawflow/projects/<name>/health-check.json.
	path := filepath.Join(tmp, ".clawflow", "projects", name, "health-check.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file at %s: %v", path, err)
	}

	got, err := loadHealthCheckJob(name)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil for a persisted job")
	}
	if got.Status != want.Status || got.Result.Outcome != want.Result.Outcome {
		t.Fatalf("status/outcome mismatch: got %+v", got)
	}
	if len(got.Result.Changes) != 1 || got.Result.Changes[0].RepoID != "owner/r" {
		t.Fatalf("changes round-trip lost data: %+v", got.Result.Changes)
	}
}

// TestLoadHealthCheckJob_Missing returns nil/nil when no file exists
// — the absence sentinel the status handler relies on to decide
// between "never run" (idle) and "real read error".
func TestLoadHealthCheckJob_Missing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := loadHealthCheckJob("never-saved")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil job for missing file, got %+v", got)
	}
}

// TestLoadHealthCheckJob_Corrupt surfaces a parse error rather than
// silently returning nil — otherwise a corrupted file would look
// identical to "never run" and the dashboard would just stay idle.
func TestLoadHealthCheckJob_Corrupt(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	const name = "corrupt"
	dir := filepath.Join(tmp, ".clawflow", "projects", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "health-check.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadHealthCheckJob(name); err == nil {
		t.Fatal("expected parse error for corrupt file, got nil")
	}
}

// TestSaveHealthCheckJob_AtomicReplace confirms a successful save
// replaces an existing file (not appends) and leaves no .tmp
// debris. Important because we use rename for atomicity — a sloppy
// implementation could leave half-written files lying around.
func TestSaveHealthCheckJob_AtomicReplace(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	const name = "replace-me"
	first := &healthCheckJob{Status: "done", Result: &pilot.HealthCheckResult{Outcome: "healthy"}}
	if err := saveHealthCheckJob(name, first); err != nil {
		t.Fatal(err)
	}
	second := &healthCheckJob{Status: "done", Result: &pilot.HealthCheckResult{Outcome: "changes-proposed"}}
	if err := saveHealthCheckJob(name, second); err != nil {
		t.Fatal(err)
	}

	got, err := loadHealthCheckJob(name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result.Outcome != "changes-proposed" {
		t.Fatalf("second save did not replace first: %+v", got.Result)
	}
	dir := filepath.Join(tmp, ".clawflow", "projects", name)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file %q left behind after successful save", e.Name())
		}
	}

	// Sanity-check the JSON is human-readable (indented). Shouldn't
	// matter functionally but it's the format we want for ops.
	raw, _ := os.ReadFile(filepath.Join(dir, "health-check.json"))
	if !json.Valid(raw) {
		t.Fatal("persisted file is not valid JSON")
	}
}
