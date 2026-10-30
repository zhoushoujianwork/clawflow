// Package storetest provides the test harness: every test gets its own
// SQLite database file in a temporary directory, so tests run in parallel and
// need no database service. PostgreSQL appears only in the importer tests,
// through CLAWFLOW_TEST_DATABASE_DSN (see PostgresDSN).
package storetest

import (
	"context"
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// DSNEnv names the environment variable holding the isolated PostgreSQL test
// database used only by the importer tests.
const DSNEnv = "CLAWFLOW_TEST_DATABASE_DSN"

// PostgresDSN returns the configured PostgreSQL fixture DSN, skipping the test
// when unset. A skip is not a pass: exact-head CI provides the PostgreSQL run.
func PostgresDSN(t testing.TB) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(DSNEnv))
	if dsn == "" {
		t.Skipf("%s not set; skipping PostgreSQL importer test", DSNEnv)
	}
	return dsn
}

// New returns a Store bound to a fresh database file, closed on cleanup.
func New(t testing.TB) *store.Store {
	t.Helper()
	s, _ := NewWithPath(t)
	return s
}

// NewWithPath is like New but also returns the database path, for tests that
// open additional connections to the same file (separate pools stand in for
// separate processes).
func NewWithPath(t testing.TB) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", store.FileName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatalf("storetest: open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

//go:embed postgres_schema_v11.sql
var postgresSchemaV11 string

// PostgresSource creates an isolated schema holding the last PostgreSQL release
// schema (migrations 1-11) and returns a DSN that targets it plus a handle for
// seeding rows. The schema is dropped on cleanup. The importer tests use it;
// nothing else touches PostgreSQL.
func PostgresSource(t testing.TB) (dsn string, db *gorm.DB) {
	t.Helper()
	base := PostgresDSN(t)
	admin, err := gorm.Open(postgres.Open(base), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("storetest: connect PostgreSQL fixture: %v", err)
	}
	schema := fmt.Sprintf("clawflow_import_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" {
		t.Fatalf("storetest: %s must be a postgres:// URL", DSNEnv)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	dsn = u.String()
	db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	conn.SetMaxOpenConns(1) // keep one session so search_path always applies
	t.Cleanup(func() {
		_ = conn.Close()
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if c, err := admin.DB(); err == nil {
			_ = c.Close()
		}
	})
	if err := db.Exec(postgresSchemaV11).Error; err != nil {
		t.Fatalf("storetest: load PostgreSQL schema: %v", err)
	}
	for v := 1; v <= 11; v++ {
		if err := db.Exec("INSERT INTO schema_migrations (version, name) VALUES (?, ?)", v, fmt.Sprintf("m%d", v)).Error; err != nil {
			t.Fatal(err)
		}
	}
	return dsn, db
}
