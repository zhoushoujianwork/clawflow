// Package store is ClawFlow's PostgreSQL persistence foundation (GORM).
// PostgreSQL is required; there is no embedded fallback. See
// docs/storage-design.md.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// Connection pool defaults.
const (
	maxOpenConns    = 10
	maxIdleConns    = 5
	connMaxLifetime = 30 * time.Minute
)

// Store wraps the GORM handle and its connection pool.
type Store struct {
	db  *gorm.DB
	sql *sql.DB
}

// Open connects to PostgreSQL and verifies connectivity. Errors never contain
// the raw password; only the masked DSN is reported.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// The GORM logger is silent: it would otherwise print SQL parameters.
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		return nil, openError(dsn, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, openError(dsn, err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetMaxIdleConns(maxIdleConns)
	sqlDB.SetConnMaxLifetime(connMaxLifetime)

	s := &Store{db: db, sql: sqlDB}
	if err := s.Ping(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, openError(dsn, err)
	}
	return s, nil
}

func openError(dsn string, err error) error {
	return &redactedError{
		msg: fmt.Sprintf("connect to database %s: %s", MaskDSN(dsn), redact(err.Error(), dsn)),
		err: err,
	}
}

// DB returns the GORM handle.
func (s *Store) DB() *gorm.DB { return s.db }

// Ping verifies that the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.sql == nil {
		return errors.New("store is not open")
	}
	return s.sql.PingContext(ctx)
}

// Close releases the connection pool. It is safe to call on a nil Store.
func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}
	return s.sql.Close()
}

// EnsureReady resolves the DSN, opens the store and verifies that no
// migration is pending. The caller owns (and must Close) the returned Store.
func EnsureReady(ctx context.Context, cfg *config.Config) (*Store, error) {
	dsn, err := ResolveDSN(cfg)
	if err != nil {
		return nil, err
	}
	s, err := Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	statuses, err := s.Status(ctx, Migrations)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	pending := 0
	for _, st := range statuses {
		if !st.Applied {
			pending++
		}
	}
	if pending > 0 {
		_ = s.Close()
		return nil, fmt.Errorf("database has %d pending migration(s): run `clawflow db migrate`", pending)
	}
	return s, nil
}
