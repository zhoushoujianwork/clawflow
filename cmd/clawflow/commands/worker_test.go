package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestWorkerTokenCLI(t *testing.T) {
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
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
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
