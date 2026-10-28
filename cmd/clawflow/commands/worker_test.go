package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestWorkerTokenCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db, dsn := storetest.NewWithDSN(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	previous := requireStore
	requireStore = func(ctx context.Context) (*store.Store, error) { return store.Open(ctx, dsn) }
	t.Cleanup(func() { requireStore = previous })
	run := func(args ...string) string {
		t.Helper()
		cmd := NewWorkerCmd()
		var out, stderr bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if args[1] == "create" {
			if !strings.Contains(stderr.String(), "SHA-256 fingerprint: ") {
				t.Fatal("missing fingerprint")
			}
		}
		return strings.TrimSpace(out.String())
	}
	secret := run("token", "create", "--name", "cli")
	if len(secret) != 64 {
		t.Fatal("missing secret")
	}
	listing := run("token", "list", "--name", "cli")
	var tokens []coordinator.Token
	if err := json.Unmarshal([]byte(listing), &tokens); err != nil || len(tokens) != 1 || tokens[0].Name != "cli" {
		t.Fatalf("listing %s %v", listing, err)
	}
	if strings.Contains(listing, secret) {
		t.Fatal("secret leaked")
	}
	run("token", "revoke", "--name", "cli")
	if _, err := coordinator.New(db.DB()).VerifyToken(context.Background(), secret); err == nil {
		t.Fatal("revoked token valid")
	}
}
func TestWorkerTokenRequiredName(t *testing.T) {
	for _, action := range []string{"create", "revoke"} {
		cmd := NewWorkerCmd()
		cmd.SetArgs([]string{"token", action})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("%s missing name accepted", action)
		}
	}
}

func TestCoordinatorDisabled(t *testing.T) {
	previous := requireStore
	requireStore = func(context.Context) (*store.Store, error) {
		t.Fatal("disabled coordinator opened database")
		return nil, errors.New("unexpected database")
	}
	t.Cleanup(func() { requireStore = previous })
	stop, err := startCoordinator(context.Background(), config.CoordinatorSettings{}, io.Discard, func(error) { t.Error("unexpected server") })
	if err != nil {
		t.Fatal(err)
	}
	stop()
}
