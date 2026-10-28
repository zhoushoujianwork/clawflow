// Package storetest provides a PostgreSQL test harness: each test gets its own
// schema, dropped on cleanup, so tests can run in parallel against one server.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// DSNEnv names the environment variable holding the test database DSN.
const DSNEnv = "CLAWFLOW_TEST_DATABASE_DSN"

// BaseDSN returns the configured test DSN, skipping the test when unset.
func BaseDSN(t testing.TB) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(DSNEnv))
	if dsn == "" {
		t.Skipf("%s not set; skipping PostgreSQL test", DSNEnv)
	}
	return dsn
}

// New returns a Store bound to a fresh, unique schema (via search_path). The
// schema is dropped with CASCADE when the test finishes.
func New(t testing.TB) *store.Store {
	t.Helper()
	s, _ := NewWithDSN(t)
	return s
}

// NewWithDSN is like New but also returns the schema-scoped DSN, for tests
// that need to open additional connections to the same schema.
func NewWithDSN(t testing.TB) (*store.Store, string) {
	t.Helper()
	base := BaseDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	schema, err := uniqueSchema()
	if err != nil {
		t.Fatalf("storetest: schema name: %v", err)
	}

	admin, err := store.Open(ctx, base)
	if err != nil {
		t.Fatalf("storetest: open admin connection: %v", err)
	}
	if err := admin.DB().WithContext(ctx).Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		_ = admin.Close()
		t.Fatalf("storetest: create schema: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		if err := admin.DB().WithContext(cctx).Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error; err != nil {
			t.Errorf("storetest: drop schema %s: %v", schema, err)
		}
		_ = admin.Close()
	})

	dsn, err := withSearchPath(base, schema)
	if err != nil {
		t.Fatalf("storetest: build DSN: %v", err)
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("storetest: open store: %v", err)
	}
	// Registered after the drop so it runs first (LIFO) and releases its
	// connections before the schema disappears.
	t.Cleanup(func() { _ = s.Close() })
	return s, dsn
}

func uniqueSchema() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "t_" + hex.EncodeToString(b), nil
}

// withSearchPath pins every connection to schema using the pgx runtime
// parameter search_path, for both URL-form and key=value DSNs.
func withSearchPath(dsn, schema string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("parse test DSN: %w", err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return dsn + " search_path=" + schema, nil
}
