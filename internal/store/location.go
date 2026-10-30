package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// LegacyDSNEnv is the former PostgreSQL override. It no longer selects a store;
// it is read only by the importer as the source connection.
const LegacyDSNEnv = "CLAWFLOW_DATABASE_DSN"

// EngineSQLite is the settings.database.engine value written by a cutover.
const EngineSQLite = "sqlite"

// ErrCutoverRequired is returned when a PostgreSQL DSN is configured and the
// host has not been cut over. Starting would otherwise create an empty SQLite
// store beside live PostgreSQL data.
var ErrCutoverRequired = errors.New("this host still has a PostgreSQL DSN and no completed cutover; run `clawflow db plan`, `db import`, `db verify` and `db cutover` (see docs/sqlite-host-runbook.md). Refusing to start with an empty SQLite store")

// CutoverRecord is persisted next to the database by `db cutover`.
type CutoverRecord struct {
	SchemaVersion    int       `json:"schema_version"`
	CutoverAt        time.Time `json:"cutover_at"`
	SourceSnapshotID string    `json:"source_snapshot_id"`
	SourceDSN        string    `json:"source_dsn_masked"`
	SQLitePath       string    `json:"sqlite_path"`
	ConfigBackup     string    `json:"config_backup"`
	SourceBackup     string    `json:"source_backup,omitempty"`
	// Fingerprint summarizes SQLite content at cutover. A different value later
	// means SQLite accepted new work, so rollback needs reconciliation.
	Fingerprint string `json:"fingerprint"`
	RolledBack  bool   `json:"rolled_back,omitempty"`
}

// CutoverPath returns where the cutover record lives.
func CutoverPath() string { return filepath.Join(config.StateDir(), "data", "cutover.json") }

// LoadCutover reads the cutover record; a missing file returns (nil, nil).
func LoadCutover() (*CutoverRecord, error) {
	data, err := os.ReadFile(CutoverPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r CutoverRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", CutoverPath(), err)
	}
	return &r, nil
}

// SaveCutover writes the record atomically with private permissions.
func SaveCutover(r *CutoverRecord) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(CutoverPath(), data, 0o600)
}

// WriteFileAtomic writes via a same-directory temp file, fsync and rename.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// ResolvePath returns the controller database path for this host and refuses
// contradictory configuration. It never selects an empty store when data may
// exist elsewhere:
//   - fresh host (no DSN, no engine): the default path, created on open;
//   - a PostgreSQL DSN without a completed cutover: ErrCutoverRequired;
//   - engine=sqlite with a leftover DSN or CLAWFLOW_DATABASE_DSN: an error;
//   - engine=sqlite with a missing file: an error (restore, never recreate).
func ResolvePath(cfg *config.Config) (string, error) {
	env := strings.TrimSpace(os.Getenv(LegacyDSNEnv)) != ""
	configured := cfg != nil && strings.TrimSpace(cfg.Settings.Database.DSN) != ""
	path := DefaultPath()
	if cfg != nil && cfg.Settings.Database.Engine == EngineSQLite {
		if env {
			return "", fmt.Errorf("%s is set but this host was cut over to SQLite; unset it so it cannot point at the old database", LegacyDSNEnv)
		}
		if configured {
			return "", errors.New("settings.database.dsn is still set but this host was cut over to SQLite; remove it (the original is in the cutover config backup)")
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("host is configured for SQLite but %s is unavailable: %w; restore it from a backup instead of starting empty", path, err)
		}
		return path, nil
	}
	if env || configured {
		return "", ErrCutoverRequired
	}
	return path, nil
}
