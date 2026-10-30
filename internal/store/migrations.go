package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Migration is one versioned, forward-only schema change.
type Migration struct {
	Version int
	Name    string
	Up      func(tx *gorm.DB) error
	// Contract marks a destructive change (drop, rename, type change, new
	// NOT NULL without default) that a binary from before the migration cannot
	// tolerate. Unmarked migrations are expand steps: additive and safe while
	// old and new processes share one database. A contract migration must ship
	// in a later release than the code that stopped using the old shape; the
	// rolling upgrade refuses to apply it. See docs/controller-upgrade.md.
	Contract bool
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
	{Version: 8, Name: "offload_execution_evidence", Up: migrateExecutionEvidence},
	{Version: 9, Name: "coordinator_application_errors", Up: func(tx *gorm.DB) error {
		return execAll(tx,
			"ALTER TABLE coordinator_tasks ADD COLUMN application_attempts integer NOT NULL DEFAULT 0",
			"ALTER TABLE coordinator_tasks ADD COLUMN application_error text NOT NULL DEFAULT ''")
	}},
	{Version: 10, Name: "native_worker_onboarding", Up: func(tx *gorm.DB) error {
		return execAll(tx,
			"ALTER TABLE coordinator_workers ADD COLUMN paused boolean NOT NULL DEFAULT false",
			"CREATE TABLE coordinator_invitations (name text PRIMARY KEY, hash blob NOT NULL, expires_at timestamp NOT NULL, consumed boolean NOT NULL DEFAULT false)")
	}},
	{Version: 11, Name: "durable_worker_execution", Up: func(tx *gorm.DB) error {
		return execAll(tx,
			"ALTER TABLE coordinator_workers ADD COLUMN durable boolean NOT NULL DEFAULT false",
			"ALTER TABLE coordinator_tasks ADD COLUMN result_digest text NOT NULL DEFAULT ''",
			"ALTER TABLE task_attempts ADD COLUMN result_digest text NOT NULL DEFAULT ''")
	}},
	{Version: 12, Name: "import_provenance", Up: migrateProvenance},
}

// execAll runs statements one at a time: the SQLite driver executes a single
// statement per Exec.
func execAll(tx *gorm.DB, statements ...string) error {
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

// migrateProvenance records where imported data came from, separately from the
// SQLite schema versions in schema_migrations.
func migrateProvenance(tx *gorm.DB) error {
	return execAll(tx, `CREATE TABLE import_provenance (
 id integer PRIMARY KEY AUTOINCREMENT, source_kind text NOT NULL, source_schema_version integer NOT NULL,
 source_snapshot_id text NOT NULL, imported_at timestamp NOT NULL, tables text NOT NULL CHECK (json_valid(tables))
)`)
}

// LatestVersion is the highest version in migs, or 0 when empty.
func LatestVersion(migs []Migration) int {
	latest := 0
	for _, m := range migs {
		if m.Version > latest {
			latest = m.Version
		}
	}
	return latest
}

// ContractVersions lists the destructive migrations in migs.
func ContractVersions(migs []Migration) []int {
	out := []int{}
	for _, m := range migs {
		if m.Contract {
			out = append(out, m.Version)
		}
	}
	return out
}

// AppliedVersions returns every version recorded in schema_migrations,
// including versions this binary does not know. A database that was never
// migrated yields no versions.
func (s *Store) AppliedVersions(ctx context.Context) ([]int, error) {
	db := s.db.WithContext(ctx)
	var exists bool
	if err := db.Raw("SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists).Error; err != nil {
		return nil, fmt.Errorf("check schema_migrations: %w", err)
	}
	if !exists {
		return []int{}, nil
	}
	var rows []schemaMigration
	if err := db.Order("version").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Version)
	}
	return out, nil
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
	applied_at timestamp NOT NULL
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
// its own immediate transaction (one writer at a time across processes) and is
// recorded in schema_migrations in that same transaction. It returns the
// versions applied by this call; repeated runs are no-ops.
func (s *Store) Migrate(ctx context.Context, migs []Migration) ([]int, error) {
	if err := validateMigrations(migs); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Exec(createMigrationsTable).Error
	}); err != nil {
		return nil, fmt.Errorf("prepare schema_migrations: %w", err)
	}

	var applied []int
	for _, m := range migs {
		done := false
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// The immediate transaction already holds the writer lock; the
			// re-check keeps concurrent migrators from applying a version twice.
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

// Status reports the applied/pending state of each migration in migs. A
// database that was never migrated reports everything as pending.
func (s *Store) Status(ctx context.Context, migs []Migration) ([]MigrationStatus, error) {
	if err := validateMigrations(migs); err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx)

	var exists bool
	if err := db.Raw("SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations')").Scan(&exists).Error; err != nil {
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
