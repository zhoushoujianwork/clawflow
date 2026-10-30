package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func noop(*gorm.DB) error { return nil }

func TestRegistryPreservesWorkflowBeforeTokenScope(t *testing.T) {
	previous := 0
	names := map[int]string{}
	for _, migration := range store.Migrations {
		if migration.Version <= previous || migration.Up == nil {
			t.Fatalf("invalid migration after version %d: %+v", previous, migration)
		}
		names[migration.Version] = migration.Name
		previous = migration.Version
	}
	if names[4] != "durable_workflow" || names[5] != "coordinator_token_scope" {
		t.Fatalf("workflow and token scope migrations must retain distinct versions: %v", names)
	}
}

func TestRegistryAppendsScheduledChecksAfterActionsEvidence(t *testing.T) {
	if len(store.Migrations) < 7 {
		t.Fatal("scheduled checks migration missing")
	}
	for i, name := range []string{"coordinator_token_scope", "actions_evidence", "scheduled_checks"} {
		migration := store.Migrations[4+i]
		if migration.Version != 5+i || migration.Name != name {
			t.Fatalf("migration %d must be %s: %+v", 5+i, name, migration)
		}
	}
}

func TestRegistryAppendsExecutionEvidenceAfterScheduledChecks(t *testing.T) {
	if len(store.Migrations) < 8 {
		t.Fatal("execution evidence migration missing")
	}
	migration := store.Migrations[7]
	if migration.Version != 8 || migration.Name != "offload_execution_evidence" {
		t.Fatalf("execution evidence must follow scheduled checks: %+v", migration)
	}
}

func countRows(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var n int64
	if err := s.DB().Raw("SELECT count(*) FROM schema_migrations").Scan(&n).Error; err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	return n
}

func TestMigrateAppliesOnceAndIsIdempotent(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()

	applied, err := s.Migrate(ctx, store.Migrations)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) != len(store.Migrations) {
		t.Fatalf("applied %v, want %d versions", applied, len(store.Migrations))
	}
	applied, err = s.Migrate(ctx, store.Migrations)
	if err != nil || len(applied) != 0 {
		t.Fatalf("second run: applied=%v err=%v", applied, err)
	}
	if got := countRows(t, s); got != int64(len(store.Migrations)) {
		t.Fatalf("rows = %d, want %d", got, len(store.Migrations))
	}
	var name string
	if err := s.DB().Raw("SELECT name FROM schema_migrations WHERE version = 1").Scan(&name).Error; err != nil || name != "baseline" {
		t.Fatalf("version 1 name = %q, err %v", name, err)
	}
}

func TestStatusPendingVsApplied(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	migs := []store.Migration{{Version: 1, Name: "one", Up: noop}, {Version: 2, Name: "two", Up: noop}}

	st, err := s.Status(ctx, migs)
	if err != nil {
		t.Fatalf("Status before migrate: %v", err)
	}
	if len(st) != 2 || st[0].Applied || st[1].Applied {
		t.Fatalf("fresh database should be all pending: %+v", st)
	}

	if _, err := s.Migrate(ctx, migs[:1]); err != nil {
		t.Fatalf("Migrate first: %v", err)
	}
	st, err = s.Status(ctx, migs)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st[0].Applied || st[0].AppliedAt == nil || st[1].Applied || st[1].Name != "two" {
		t.Fatalf("unexpected status: %+v", st)
	}

	applied, err := s.Migrate(ctx, migs)
	if err != nil || len(applied) != 1 || applied[0] != 2 {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
}

func TestMigrateRejectsBadVersions(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	cases := map[string][]store.Migration{
		"duplicate": {{Version: 1, Name: "a", Up: noop}, {Version: 1, Name: "b", Up: noop}},
		"unordered": {{Version: 2, Name: "a", Up: noop}, {Version: 1, Name: "b", Up: noop}},
		"zero":      {{Version: 0, Name: "a", Up: noop}},
		"nil up":    {{Version: 1, Name: "a"}},
	}
	for name, migs := range cases {
		if _, err := s.Migrate(ctx, migs); err == nil {
			t.Errorf("%s: Migrate should fail", name)
		}
		if _, err := s.Status(ctx, migs); err == nil {
			t.Errorf("%s: Status should fail", name)
		}
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	boom := errors.New("boom")
	migs := []store.Migration{
		{Version: 1, Name: "ok", Up: noop},
		{Version: 2, Name: "bad", Up: func(tx *gorm.DB) error {
			if err := tx.Exec("CREATE TABLE half_done (id int)").Error; err != nil {
				return err
			}
			return boom
		}},
		{Version: 3, Name: "never", Up: noop},
	}
	applied, err := s.Migrate(ctx, migs)
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if len(applied) != 1 || applied[0] != 1 {
		t.Fatalf("applied = %v, want [1]", applied)
	}
	if got := countRows(t, s); got != 1 {
		t.Fatalf("rows = %d, want 1 (failed migration must not be recorded)", got)
	}
	var exists bool
	if err := s.DB().Raw("SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name='half_done')").Scan(&exists).Error; err != nil || exists {
		t.Fatalf("failed migration DDL must roll back (exists=%v err=%v)", exists, err)
	}
}

func TestMigrateConcurrentRunnersApplyOnce(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	runs := 0
	var mu sync.Mutex
	migs := []store.Migration{{Version: 1, Name: "counted", Up: func(*gorm.DB) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	}}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Migrate(ctx, migs); err != nil {
				t.Errorf("Migrate: %v", err)
			}
		}()
	}
	wg.Wait()
	if runs != 1 {
		t.Fatalf("Up ran %d times, want 1", runs)
	}
}

func TestEnsureReadyFreshHostNeedsNoDSN(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	t.Setenv(store.LegacyDSNEnv, "")
	ctx := context.Background()
	s, err := store.EnsureReady(ctx, &config.Config{})
	if err != nil {
		t.Fatalf("fresh host must start without PostgreSQL or a DSN: %v", err)
	}
	defer s.Close()
	statuses, err := s.Status(ctx, store.Migrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range statuses {
		if !st.Applied {
			t.Fatalf("migration %d pending after EnsureReady", st.Version)
		}
	}
	info, err := os.Stat(s.Path())
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("database file must be private: %v %v", info, err)
	}
	if want := filepath.Join(os.Getenv("CLAWFLOW_STATE_DIR"), "data", store.FileName); s.Path() != want {
		t.Fatalf("path = %s, want %s", s.Path(), want)
	}
}

func TestEnsureReadyRefusesLegacyDSNWithoutCutover(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	t.Setenv(store.LegacyDSNEnv, "")
	cfg := &config.Config{}
	cfg.Settings.Database.DSN = "postgres://u:p@127.0.0.1:1/db"
	if _, err := store.EnsureReady(context.Background(), cfg); !errors.Is(err, store.ErrCutoverRequired) {
		t.Fatalf("want ErrCutoverRequired, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("CLAWFLOW_STATE_DIR"), "data", store.FileName)); err == nil {
		t.Fatal("an empty SQLite store must not be created beside live PostgreSQL data")
	}
	t.Setenv(store.LegacyDSNEnv, "postgres://u:p@127.0.0.1:1/db")
	if _, err := store.EnsureReady(context.Background(), &config.Config{}); !errors.Is(err, store.ErrCutoverRequired) {
		t.Fatalf("env DSN: want ErrCutoverRequired, got %v", err)
	}
}

func TestEnsureReadyCutoverHostRejectsContradictionsAndMissingFile(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	t.Setenv(store.LegacyDSNEnv, "")
	cfg := &config.Config{}
	cfg.Settings.Database.Engine = store.EngineSQLite
	if _, err := store.EnsureReady(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("missing database must not be recreated empty: %v", err)
	}
	s, err := store.Open(context.Background(), store.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	cfg.Settings.Database.DSN = "postgres://u:p@h/db"
	if _, err := store.EnsureReady(context.Background(), cfg); err == nil {
		t.Fatal("leftover DSN after cutover must be refused")
	}
	cfg.Settings.Database.DSN = ""
	t.Setenv(store.LegacyDSNEnv, "postgres://u:p@h/db")
	if _, err := store.EnsureReady(context.Background(), cfg); err == nil {
		t.Fatal("environment DSN after cutover must be refused")
	}
}

// TestActionsMigrationAfterTokenScope covers upgrades from the published base.
func TestActionsMigrationAfterTokenScope(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx, store.Migrations[:5]); err != nil {
		t.Fatal(err)
	}
	applied, err := s.Migrate(ctx, store.Migrations[:6])
	if err != nil || len(applied) != 1 || applied[0] != 6 {
		t.Fatalf("Actions upgrade: applied=%v err=%v", applied, err)
	}
	for _, table := range []string{"actions_attachments", "actions_evidence"} {
		var exists bool
		if err := s.DB().Raw("SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists).Error; err != nil || !exists {
			t.Fatalf("missing %s: exists=%v err=%v", table, exists, err)
		}
	}
	var scope string
	if err := s.DB().Raw("SELECT dflt_value FROM pragma_table_info('coordinator_tokens') WHERE name = 'scope'").Scan(&scope).Error; err != nil || !strings.Contains(scope, "worker") {
		t.Fatalf("token scope migration lost: default=%q err=%v", scope, err)
	}
}

func TestScheduledChecksMigrationAfterActionsEvidence(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx, store.Migrations[:6]); err != nil {
		t.Fatal(err)
	}
	applied, err := s.Migrate(ctx, store.Migrations[:7])
	if err != nil || len(applied) != 1 || applied[0] != 7 {
		t.Fatalf("scheduled checks upgrade: applied=%v err=%v", applied, err)
	}
	for _, table := range []string{"actions_attachments", "actions_evidence", "scheduled_checks", "scheduled_check_runs"} {
		var exists bool
		if err := s.DB().Raw("SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)", table).Scan(&exists).Error; err != nil || !exists {
			t.Fatalf("missing %s: exists=%v err=%v", table, exists, err)
		}
	}
}

// Simulate tables created by the released schema, before evidence columns existed.
func TestExecutionEvidenceUpgradePreservesLegacy(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx, store.Migrations[:7]); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"runs", "pilot_runs"} {
		if err := s.DB().Exec("INSERT INTO "+table+" (run_dir, status, payload) VALUES (?, ?, ?)", "/legacy", "success", `{"status":"success"}`).Error; err != nil {
			t.Fatal(err)
		}
	}
	applied, err := s.Migrate(ctx, store.Migrations[:8])
	if err != nil || len(applied) != 1 || applied[0] != 8 {
		t.Fatalf("upgrade=%v err=%v", applied, err)
	}
	for _, kind := range []string{"run", "pilot"} {
		rows, err := s.Runs(ctx, kind, store.RunFilter{})
		if err != nil || len(rows) != 1 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		if rows[0].ExitCode != nil || rows[0].ElapsedMs != nil || rows[0].Status != "success" {
			t.Fatalf("legacy evidence inferred: %+v", rows[0])
		}
	}
}

func TestCoordinatorApplicationErrorsMigrationAfterExecutionEvidence(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx, store.Migrations[:8]); err != nil {
		t.Fatal(err)
	}
	applied, err := s.Migrate(ctx, store.Migrations[:9])
	if err != nil || len(applied) != 1 || applied[0] != 9 {
		t.Fatalf("application errors upgrade: applied=%v err=%v", applied, err)
	}
	var columns int64
	if err := s.DB().Raw("SELECT count(*) FROM pragma_table_info('coordinator_tasks') WHERE name IN ('application_attempts', 'application_error')").Scan(&columns).Error; err != nil || columns != 2 {
		t.Fatalf("application error columns: count=%d err=%v", columns, err)
	}
}

func TestNativeWorkerMigrationPreservesExistingEligibility(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx, store.Migrations[:9]); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Exec("INSERT INTO coordinator_workers (id,max_parallel,created_at,updated_at) VALUES ('existing',2,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)").Error; err != nil {
		t.Fatal(err)
	}
	applied, err := s.Migrate(ctx, store.Migrations[:10])
	if err != nil || len(applied) != 1 || applied[0] != 10 {
		t.Fatal(applied, err)
	}
	var paused bool
	if err := s.DB().Raw("SELECT paused FROM coordinator_workers WHERE id = 'existing'").Scan(&paused).Error; err != nil || paused {
		t.Fatal("existing eligibility changed", err)
	}
}

func TestAppliedVersionsAndContractMarking(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	got, err := s.AppliedVersions(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("an unmigrated database has no versions: %v %v", got, err)
	}
	if _, err := s.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	got, err = s.AppliedVersions(ctx)
	if err != nil || len(got) != len(store.Migrations) || got[len(got)-1] != store.LatestVersion(store.Migrations) {
		t.Fatalf("applied %v for %d migrations (%v)", got, len(store.Migrations), err)
	}
	// A version this binary does not know is still reported, so a rolling
	// upgrade can tell the database moved ahead of the running build.
	if err := s.DB().Exec("INSERT INTO schema_migrations (version, name) VALUES (9999, 'future')").Error; err != nil {
		t.Fatal(err)
	}
	got, _ = s.AppliedVersions(ctx)
	if got[len(got)-1] != 9999 {
		t.Fatalf("unknown version missing: %v", got)
	}
	for _, m := range store.Migrations {
		if m.Contract {
			t.Fatalf("migration %d: contract migrations must not ship until a documented two-release contract step", m.Version)
		}
	}
	if len(store.ContractVersions([]store.Migration{{Version: 1}, {Version: 2, Contract: true}})) != 1 {
		t.Fatal("contract versions not reported")
	}
}
