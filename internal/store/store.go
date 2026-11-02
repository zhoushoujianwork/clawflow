// Package store is ClawFlow's embedded SQLite persistence layer (GORM).
//
// The controller owns one database file on local storage:
// <state dir>/data/clawflow.sqlite3. Workers never open it; LAN workers use
// the authenticated HTTPS API. PostgreSQL survives only as the import source
// of the offline migration in importer.go. See docs/sqlite-host-design.md.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

// FileName is the database file inside <state dir>/data.
const FileName = "clawflow.sqlite3"

// Connection policy. Every connection enables foreign keys; WAL plus
// synchronous=FULL makes an acknowledged transition durable across power loss;
// BEGIN IMMEDIATE serializes writers across processes; a bounded busy timeout
// turns contention into an error instead of an unbounded wait.
const (
	busyTimeoutMillis = 10000
	maxOpenConns      = 16
	maxIdleConns      = 4
)

// ErrNetworkFilesystem is returned when the database would live on a network
// share, where SQLite locking is unreliable.
var ErrNetworkFilesystem = errors.New("SQLite database must be on local storage, not a network share")

// Store wraps the GORM handle and its connection pool.
type Store struct {
	db   *gorm.DB
	sql  *sql.DB
	path string
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// DefaultPath returns the controller database location for the current host.
func DefaultPath() string {
	return filepath.Join(config.StateDir(), "data", FileName)
}

func dsnFor(path string) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	q.Set("_time_format", "sqlite")
	q.Set("_timezone", "UTC")
	q.Set("_texttotime", "1")
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}

// Open opens (creating when absent) the database at path with private
// permissions and verifies it. The parent directory is created 0700 and the
// file 0600. It refuses network filesystems.
func Open(ctx context.Context, path string) (*Store, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if path == "" {
		return nil, errors.New("database path is required")
	}
	// SQLite candidates must stay inside their private runtime root, including symlink targets.
	if devmode.Active() {
		if err := devmode.CheckWritePath(path); err != nil {
			return nil, err
		}

	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	if err := requireLocalFilesystem(dir); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	// Create the file before SQLite does so it is never world-readable. SQLite
	// gives the -wal and -shm files the main file's permissions.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database file: %w", err)
	}
	_ = f.Close()
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("restrict database permissions: %w", err)
		}
	}

	conn, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	conn.SetMaxOpenConns(maxOpenConns)
	conn.SetMaxIdleConns(maxIdleConns)
	conn.SetConnMaxLifetime(0)
	db, err := gorm.Open(dialector{conn: conn}, &gorm.Config{Logger: logger.Discard, SkipDefaultTransaction: true})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	s := &Store{db: db, sql: conn, path: path}
	if err := s.Ping(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return s, nil
}

// DB returns the GORM handle.
func (s *Store) DB() *gorm.DB { return s.db }

// Ping verifies that the database is reachable and in WAL mode.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.sql == nil {
		return errors.New("store is not open")
	}
	if err := s.sql.PingContext(ctx); err != nil {
		return err
	}
	var mode string
	if err := s.sql.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("database journal mode is %q, want wal", mode)
	}
	return nil
}

// Close checkpoints the WAL and releases the connection pool. It is safe to
// call on a nil Store.
func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Checkpoint(ctx)
	return s.sql.Close()
}

// Checkpoint folds the WAL into the main database file and truncates it.
func (s *Store) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := s.sql.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("WAL checkpoint was blocked by an active reader or writer")
	}
	return nil
}

// Backup writes a consistent copy of the database, WAL included, to dest with
// VACUUM INTO. A live database file must never be copied directly. dest must
// not exist.
func (s *Store) Backup(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("backup destination %s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Exec("VACUUM INTO ?", dest).Error; err != nil {
		return fmt.Errorf("backup database: %w", err)
	}
	return os.Chmod(dest, 0o600)
}

// IntegrityCheck runs PRAGMA integrity_check and foreign_key_check.
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var rows []string
	if err := s.db.WithContext(ctx).Raw("PRAGMA integrity_check").Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0] != "ok" {
		return fmt.Errorf("integrity_check: %s", strings.Join(rows, "; "))
	}
	r, err := s.sql.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer r.Close()
	if r.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fk int
		_ = r.Scan(&table, &rowid, &parent, &fk)
		return fmt.Errorf("foreign_key_check: row in %s references missing %s", table, parent)
	}
	return r.Err()
}

// EnsureReady opens the controller database for this host, creating it on a
// fresh host, and applies forward-only migrations (after a consistent backup
// when an existing database has pending ones). It refuses to start when a
// legacy PostgreSQL DSN contradicts the SQLite configuration. The caller owns
// (and must Close) the returned Store.
func EnsureReady(ctx context.Context, cfg *config.Config) (*Store, error) {
	path, err := ResolvePath(cfg)
	if err != nil {
		return nil, err
	}
	s, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := s.migrateWithBackup(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrateWithBackup(ctx context.Context) error {
	statuses, err := s.Status(ctx, Migrations)
	if err != nil {
		return err
	}
	applied, pending := 0, 0
	for _, st := range statuses {
		if st.Applied {
			applied++
		} else {
			pending++
		}
	}
	if pending == 0 {
		return nil
	}
	if applied > 0 {
		dest := fmt.Sprintf("%s.pre-migrate-%s.bak", s.path, time.Now().UTC().Format("20060102T150405Z"))
		if err := s.Backup(ctx, dest); err != nil {
			return err
		}
	}
	_, err = s.Migrate(ctx, Migrations)
	return err
}
