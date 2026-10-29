package store_test

import (
	"context"
	"errors"
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
	if err := s.DB().Raw("SELECT to_regclass('half_done') IS NOT NULL").Scan(&exists).Error; err != nil || exists {
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

func TestEnsureReady(t *testing.T) {
	s, dsn := storetest.NewWithDSN(t)
	ctx := context.Background()
	t.Setenv(store.DSNEnv, dsn)
	cfg := &config.Config{}

	_, err := store.EnsureReady(ctx, cfg)
	if err == nil || !strings.Contains(err.Error(), "clawflow db migrate") || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("want pending-migration error mentioning `clawflow db migrate`, got %v", err)
	}

	if _, err := s.Migrate(ctx, store.Migrations); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ready, err := store.EnsureReady(ctx, cfg)
	if err != nil {
		t.Fatalf("EnsureReady after migrate: %v", err)
	}
	defer ready.Close()
	if err := ready.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestEnsureReadyWithoutDSN(t *testing.T) {
	t.Setenv(store.DSNEnv, "")
	if _, err := store.EnsureReady(context.Background(), &config.Config{}); !errors.Is(err, store.ErrNoDSN) {
		t.Fatalf("want ErrNoDSN, got %v", err)
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
		if err := s.DB().Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&exists).Error; err != nil || !exists {
			t.Fatalf("missing %s: exists=%v err=%v", table, exists, err)
		}
	}
	var scope string
	if err := s.DB().Raw("SELECT column_default FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'coordinator_tokens' AND column_name = 'scope'").Scan(&scope).Error; err != nil || !strings.Contains(scope, "worker") {
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
		if err := s.DB().Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&exists).Error; err != nil || !exists {
			t.Fatalf("missing %s: exists=%v err=%v", table, exists, err)
		}
	}
}

func TestCoordinatorApplicationErrorsMigrationAfterScheduledChecks(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx, store.Migrations[:7]); err != nil {
		t.Fatal(err)
	}
	applied, err := s.Migrate(ctx, store.Migrations)
	if err != nil || len(applied) != 1 || applied[0] != 8 {
		t.Fatalf("application errors upgrade: applied=%v err=%v", applied, err)
	}
	var columns int64
	if err := s.DB().Raw("SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'coordinator_tasks' AND column_name IN ('application_attempts', 'application_error')").Scan(&columns).Error; err != nil || columns != 2 {
		t.Fatalf("application error columns: count=%d err=%v", columns, err)
	}
}
