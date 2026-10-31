package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// CutoverOptions configures Cutover. Dir overrides the state dir for tests.
type CutoverOptions struct {
	SourceDSN string
	Now       func() time.Time
}

func (o CutoverOptions) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// Cutover publishes a verified staging import as the controller database and
// switches the shared configuration. The caller must already hold the
// controller lock (no controller is running). Steps, each safe to repeat:
//
//  1. re-verify the staging file against a fresh read of the source;
//  2. back up config.yaml (the untouched source configuration);
//  3. checkpoint and rename the staging file to the controller path;
//  4. persist the cutover record;
//  5. rewrite config.yaml atomically: engine=sqlite, DSN removed.
//
// The PostgreSQL database is never modified.
func Cutover(ctx context.Context, plan *Plan, src *Source, opts CutoverOptions) (*CutoverRecord, error) {
	if existing, err := LoadCutover(); err != nil {
		return nil, err
	} else if existing != nil && !existing.RolledBack {
		return nil, fmt.Errorf("this host was already cut over at %s; use `clawflow db rollback` before importing again", existing.CutoverAt.Format(time.RFC3339))
	}
	staging := StagingPath(plan.SnapshotID)
	target := DefaultPath()
	if _, err := os.Stat(staging); err != nil {
		if _, terr := os.Stat(target); terr != nil {
			return nil, fmt.Errorf("no staging import for snapshot %s; run `clawflow db import` first", plan.SnapshotID[:12])
		}
	}

	if _, err := os.Stat(staging); err == nil {
		s, err := Open(ctx, staging)
		if err != nil {
			return nil, err
		}
		report, err := s.Verify(ctx, src)
		if err != nil || !report.OK() {
			_ = s.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("verification failed, configuration unchanged: %v", report.Blockers)
		}
		if err := s.Close(); err != nil { // checkpoints and removes the WAL
			return nil, err
		}
	}

	// Config backup first: it is the rollback source.
	cfgPath := config.ConfigPath()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	stamp := opts.now().Format("20060102T150405Z")
	backup := cfgPath + ".pre-sqlite-" + stamp + ".bak"
	if err := WriteFileAtomic(backup, raw, 0o600); err != nil {
		return nil, fmt.Errorf("back up config: %w", err)
	}

	// Publish. A previous interrupted cutover may already have renamed the
	// file; accept it only when it carries this snapshot's provenance.
	if _, err := os.Stat(target); err == nil {
		t, oerr := Open(ctx, target)
		if oerr != nil {
			return nil, oerr
		}
		snap, _, perr := t.Provenance(ctx)
		_ = t.Close()
		if perr != nil || snap != plan.SnapshotID {
			return nil, fmt.Errorf("%s already exists and is not this import; refusing to replace it", target)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		if err := os.Rename(staging, target); err != nil {
			return nil, fmt.Errorf("publish database: %w", err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(staging + suffix)
		}
	}

	final, err := Open(ctx, target)
	if err != nil {
		return nil, err
	}
	fp, err := final.Fingerprint(ctx)
	_ = final.Close()
	if err != nil {
		return nil, err
	}
	rec := &CutoverRecord{
		SchemaVersion: 1, CutoverAt: opts.now(), SourceSnapshotID: plan.SnapshotID,
		SourceDSN: MaskDSN(opts.SourceDSN), SQLitePath: target, ConfigBackup: backup, Fingerprint: fp,
	}
	if err := SaveCutover(rec); err != nil {
		return nil, fmt.Errorf("persist cutover record: %w", err)
	}
	if err := switchConfig(raw); err != nil {
		return nil, fmt.Errorf("update configuration (the database is published; rerun `db cutover` to finish): %w", err)
	}
	return rec, nil
}

// switchConfig sets settings.database.engine=sqlite and removes the DSN,
// keeping every other setting, in one atomic file replacement.
func switchConfig(raw []byte) error {
	var cfg config.Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	cfg.Settings.Database.DSN = ""
	cfg.Settings.Database.Engine = EngineSQLite
	if cfg.Repos == nil {
		cfg.Repos = map[string]config.Repo{}
	}
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return WriteFileAtomic(config.ConfigPath(), out, 0o600)
}

// ErrReconcileRequired is returned by Rollback after SQLite accepted new work.
var ErrReconcileRequired = errors.New("SQLite has accepted work since the cutover; switching back to the older PostgreSQL snapshot would replay completed tasks. Reconcile the state first or perform a verified reverse import (see docs/sqlite-host-runbook.md)")

// Rollback restores the pre-cutover configuration while SQLite has not
// accepted new work. The SQLite file is renamed, never deleted, and the
// record is kept with RolledBack set. The caller holds the controller lock.
func Rollback(ctx context.Context) (*CutoverRecord, error) {
	rec, err := LoadCutover()
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.RolledBack {
		return nil, errors.New("no active cutover to roll back")
	}
	s, err := Open(ctx, rec.SQLitePath)
	if err != nil {
		return nil, err
	}
	fp, ferr := s.Fingerprint(ctx)
	_ = s.Close()
	if ferr != nil {
		return nil, ferr
	}
	if fp != rec.Fingerprint {
		return nil, ErrReconcileRequired
	}
	backup, err := os.ReadFile(rec.ConfigBackup)
	if err != nil {
		return nil, fmt.Errorf("read config backup %s: %w", rec.ConfigBackup, err)
	}
	if err := WriteFileAtomic(config.ConfigPath(), backup, 0o600); err != nil {
		return nil, err
	}
	moved := rec.SQLitePath + ".rolled-back-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(rec.SQLitePath, moved); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(rec.SQLitePath + suffix)
	}
	rec.RolledBack = true
	rec.SQLitePath = moved
	if err := SaveCutover(rec); err != nil {
		return nil, err
	}
	return rec, nil
}
