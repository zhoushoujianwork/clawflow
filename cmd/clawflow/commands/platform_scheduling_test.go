package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestClientRunAndLocalEnableRefused(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	queueConfig(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.Platform.URL = "https://platform.example"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	old := requireStore
	requireStore = func(context.Context) (*store.Store, error) {
		t.Fatal("client reached database gate")
		return nil, errors.New("unexpected gate")
	}
	t.Cleanup(func() { requireStore = old })
	cmd := NewRunCmd()
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), cfg.Settings.Platform.URL) {
		t.Fatalf("unexpected run error: %v", err)
	}
	if err := setRepoEnabled(cmd, "o/r", true); err == nil || !strings.Contains(err.Error(), cfg.Settings.Platform.URL) {
		t.Fatalf("unexpected enable error: %v", err)
	}
	// A configured client can still authenticate, register and poll as a worker.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var registered atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker-secret" {
			t.Error("missing worker token")
		}
		switch r.URL.Path {
		case "/worker/v1/register":
			registered.Store(true)
			_, _ = w.Write([]byte(`{}`))
		case "/worker/v1/claim":
			_, _ = w.Write([]byte(`{"task":null}`))
			cancel()
		default:
			t.Errorf("unexpected worker path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "worker.token")
	if err := os.WriteFile(token, []byte("worker-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(server.Certificate().Raw)
	worker := NewWorkerCmd()
	worker.SetContext(ctx)
	worker.SetArgs([]string{"--coordinator", server.URL, "--token-file", token, "--fingerprint", hex.EncodeToString(pin[:])})
	if err := worker.Execute(); err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !registered.Load() {
		t.Fatal("client worker did not register")
	}
}

func TestClientLocalRunWarnsAndExecutes(t *testing.T) {
	db, dsn := storetest.NewWithPath(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	withRunLog(t)
	queueConfig(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.Platform.URL = "https://platform.example"
	cfg.Settings.Coordinator.Enabled = true
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	queueVCS(t, []map[string]any{targetedIssue(7)}, nil, nil, nil)
	dispatched := recordDispatch(t)
	old := requireStore
	requireStore = func(ctx context.Context) (*store.Store, error) { return store.Open(ctx, dsn) }
	t.Cleanup(func() { requireStore = old })
	cmd := NewRunCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--local", "--repo", "o/r", "--timeout", time.Minute.String()})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "[warn]") || len(dispatched()) != 1 {
		t.Fatalf("warning=%s dispatched=%v", stderr.String(), dispatched())
	}
}
