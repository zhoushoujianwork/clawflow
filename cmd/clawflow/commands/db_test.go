package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String() + errBuf.String(), err
}

// A fresh host must start `run` and `web` without PostgreSQL, a DSN or any
// database credentials: the gate creates the embedded SQLite store.
func TestGateOpensEmbeddedStoreOnFreshHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	t.Setenv(store.LegacyDSNEnv, "")
	writeMinimalConfig(t, home)
	release, err := gateStore(context.Background())
	if err != nil {
		t.Fatalf("fresh host: %v", err)
	}
	defer release()
	if activeStore == nil || !strings.HasSuffix(activeStore.Path(), filepath.Join("data", store.FileName)) {
		t.Fatalf("store not bound to the default path: %+v", activeStore)
	}
}

// A leftover PostgreSQL DSN without a cutover must stop the gate instead of
// silently starting on an empty SQLite store.
func TestGateRefusesLegacyDSNWithoutCutover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	writeMinimalConfig(t, home)
	t.Setenv(store.LegacyDSNEnv, "postgres://u:hunter2@127.0.0.1:1/db")
	for _, cmd := range []string{"run", "web"} {
		out, err := execRoot(t, cmd)
		if !errors.Is(err, store.ErrCutoverRequired) {
			t.Errorf("%s: want ErrCutoverRequired, got %v", cmd, err)
		}
		if strings.Contains(out, "hunter2") || (err != nil && strings.Contains(err.Error(), "hunter2")) {
			t.Errorf("%s leaked the DSN password", cmd)
		}
	}
}

func writeMinimalConfig(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("settings:\n  poll_interval: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGateStoreReleasesStore(t *testing.T) {
	orig := requireStore
	t.Cleanup(func() { requireStore = orig })
	requireStore = func(context.Context) (*store.Store, error) { return nil, errors.New("gate closed") }
	if _, err := gateStore(context.Background()); err == nil || !strings.Contains(err.Error(), "gate closed") {
		t.Fatalf("want gate error, got %v", err)
	}
	if activeStore != nil {
		t.Fatal("activeStore must stay nil when the gate fails")
	}
}

func TestDBCommandRegistered(t *testing.T) {
	out, err := execRoot(t, "db", "--help")
	if err != nil || !strings.Contains(out, "migrate") || !strings.Contains(out, "status") {
		t.Fatalf("db --help: err=%v out=%q", err, out)
	}
}

func TestDBStatusReportsSQLite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	t.Setenv(store.LegacyDSNEnv, "")
	writeMinimalConfig(t, home)
	if _, err := execRoot(t, "db", "migrate"); err != nil {
		t.Fatal(err)
	}
	out, err := execRoot(t, "db", "status")
	if err != nil || !strings.Contains(out, "SQLite") || !strings.Contains(out, "Integrity:    ok") {
		t.Fatalf("status: %q %v", out, err)
	}
}

func TestDBMigrateImportsHistoryOnlyWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	t.Setenv(store.LegacyDSNEnv, "")
	writeMinimalConfig(t, home)
	s, err := store.EnsureReady(context.Background(), &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	dir := filepath.Join(home, ".clawflow", "data", "runs", "legacy")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "meta.json")
	raw := []byte(`{"repo":"o/r","operator":"implement","status":"success","started_at":"2026-10-01T00:00:00Z"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := execRoot(t, "db", "migrate")
	if err != nil || !strings.Contains(out, "imported=1") {
		t.Fatalf("migrate: %q %v", out, err)
	}
	rows, err := s.Runs(context.Background(), "run", store.RunFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("runs: %+v %v", rows, err)
	}
	// A changed file must not be re-imported once the runs table is populated.
	raw = bytes.ReplaceAll(raw, []byte("success"), []byte("failed"))
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, err = execRoot(t, "db", "migrate")
	if err != nil || strings.Contains(out, "imported=") {
		t.Fatalf("repeat: %q %v", out, err)
	}
	rows, err = s.Runs(context.Background(), "run", store.RunFilter{})
	if err != nil || len(rows) != 1 || rows[0].Status != "success" {
		t.Fatalf("reimported: %+v %v", rows, err)
	}
	// Explicit import remains available to update existing records.
	if _, err := execRoot(t, "db", "import-runs"); err != nil {
		t.Fatal(err)
	}
	out, err = execRoot(t, "db", "import-runs")
	if err != nil || !strings.Contains(out, "skipped runs/legacy/meta.json: unchanged metadata already imported") {
		t.Fatalf("skip output: %q %v", out, err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = execRoot(t, "db", "import-runs")
	if err != nil || !strings.Contains(out, "invalid runs/legacy/meta.json: missing run identity") {
		t.Fatalf("invalid output: %q %v", out, err)
	}
}
