package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// migrationLockKey is the fixed pg_advisory_xact_lock key serializing
// concurrent migrators ("clawflow" packed into an int64).
const migrationLockKey int64 = 0x636c6177666c6f77

// Migration is one versioned, forward-only schema change.
type Migration struct {
	Version int
	Name    string
	Up      func(tx *gorm.DB) error
}

// MigrationStatus reports whether a migration has been applied.
type MigrationStatus struct {
	Version   int
	Name      string
	Applied   bool
	AppliedAt *time.Time
}

// Migrations is the ordered registry of schema migrations. Append only; never
// edit or reorder a released entry.
var Migrations = []Migration{
	{Version: 1, Name: "baseline", Up: func(tx *gorm.DB) error { return nil }},
	{Version: 2, Name: "runs_and_usage", Up: migrateRuns},
	{Version: 3, Name: "coordinator", Up: migrateCoordinator},
	{Version: 4, Name: "durable_workflow", Up: migrateWorkflow},
	{Version: 5, Name: "coordinator_token_scope", Up: func(tx *gorm.DB) error {
		return tx.Exec("ALTER TABLE coordinator_tokens ADD COLUMN scope text NOT NULL DEFAULT 'worker' CHECK (scope IN ('worker', 'admin'))").Error
	}},
	{Version: 6, Name: "actions_evidence", Up: migrateActions},
	{Version: 7, Name: "scheduled_checks", Up: migrateSchedules},
}

type schemaMigration struct {
	Version   int       `gorm:"column:version;primaryKey"`
	Name      string    `gorm:"column:name"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (schemaMigration) TableName() string { return "schema_migrations" }

const createMigrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version int PRIMARY KEY,
	name text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// validateMigrations checks that versions are positive, unique and ascending
// and that every migration has an Up function.
func validateMigrations(migs []Migration) error {
	prev := 0
	for _, m := range migs {
		if m.Version <= 0 {
			return fmt.Errorf("migration %q: version must be positive, got %d", m.Name, m.Version)
		}
		if m.Version == prev {
			return fmt.Errorf("duplicate migration version %d (%q)", m.Version, m.Name)
		}
		if m.Version < prev {
			return fmt.Errorf("migration versions must be ascending: %d (%q) follows %d", m.Version, m.Name, prev)
		}
		if m.Up == nil {
			return fmt.Errorf("migration %d (%q) has no Up function", m.Version, m.Name)
		}
		prev = m.Version
	}
	return nil
}

// Migrate applies every pending migration in order. Each migration runs in
// its own transaction, under a transaction-scoped advisory lock, and is
// recorded in schema_migrations in that same transaction. It returns the
// versions applied by this call; repeated runs are no-ops.
func (s *Store) Migrate(ctx context.Context, migs []Migration) ([]int, error) {
	if err := validateMigrations(migs); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMigrations(tx); err != nil {
			return err
		}
		return tx.Exec(createMigrationsTable).Error
	}); err != nil {
		return nil, fmt.Errorf("prepare schema_migrations: %w", err)
	}

	var applied []int
	for _, m := range migs {
		done := false
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Taking the lock per transaction and re-checking afterwards
			// keeps concurrent migrators from applying a version twice.
			if err := lockMigrations(tx); err != nil {
				return err
			}
			var n int64
			if err := tx.Model(&schemaMigration{}).Where("version = ?", m.Version).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				return nil
			}
			if err := m.Up(tx); err != nil {
				return err
			}
			if err := tx.Create(&schemaMigration{Version: m.Version, Name: m.Name, AppliedAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
			done = true
			return nil
		})
		if err != nil {
			return applied, fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
		if done {
			applied = append(applied, m.Version)
		}
	}
	return applied, nil
}

func lockMigrations(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", migrationLockKey).Error
}

// Status reports the applied/pending state of each migration in migs. A
// database that was never migrated reports everything as pending.
func (s *Store) Status(ctx context.Context, migs []Migration) ([]MigrationStatus, error) {
	if err := validateMigrations(migs); err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx)

	var exists bool
	if err := db.Raw("SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists).Error; err != nil {
		return nil, fmt.Errorf("check schema_migrations: %w", err)
	}
	appliedAt := map[int]time.Time{}
	if exists {
		var rows []schemaMigration
		if err := db.Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("read schema_migrations: %w", err)
		}
		for _, r := range rows {
			appliedAt[r.Version] = r.AppliedAt
		}
	}

	out := make([]MigrationStatus, 0, len(migs))
	for _, m := range migs {
		st := MigrationStatus{Version: m.Version, Name: m.Name}
		if at, ok := appliedAt[m.Version]; ok {
			at := at
			st.Applied = true
			st.AppliedAt = &at
		}
		out = append(out, st)
	}
	return out, nil
}
