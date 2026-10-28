package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// The required-DB gate must stop `run` and `web` before any work when no DSN
// is configured.
func TestRunAndWebFailFastWithoutDSN(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(store.DSNEnv, "")
	writeMinimalConfig(t, home)
	for _, cmd := range []string{"run", "web"} {
		_, err := execRoot(t, cmd)
		if !errors.Is(err, store.ErrNoDSN) {
			t.Errorf("%s: want ErrNoDSN, got %v", cmd, err)
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

func TestDBStatusMasksDSN(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeMinimalConfig(t, home)
	t.Setenv(store.DSNEnv, "postgres://u:hunter2@127.0.0.1:1/db?sslmode=disable&connect_timeout=2")
	out, err := execRoot(t, "db", "status")
	if err == nil {
		t.Fatal("expected connectivity failure")
	}
	if strings.Contains(out, "hunter2") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("password leaked: %q / %v", out, err)
	}
}
