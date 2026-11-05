package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMigrateWithBackupOnlyForExistingPendingSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data", FileName)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(ctx, Migrations[:1]); err != nil {
		t.Fatal(err)
	}
	backups := 0
	applied, err := s.migrateWithBackup(ctx, func(context.Context, string) error {
		backups++
		return nil
	})
	if err != nil || backups != 1 || len(applied) != len(Migrations)-1 {
		t.Fatalf("pending migration: applied=%v backups=%d err=%v", applied, backups, err)
	}
	applied, err = s.migrateWithBackup(ctx, func(context.Context, string) error {
		backups++
		return nil
	})
	if err != nil || len(applied) != 0 || backups != 1 {
		t.Fatalf("current schema: applied=%v backups=%d err=%v", applied, backups, err)
	}
}

func TestMigrateWithBackupStopsBeforeMigrationWhenBackupFails(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "data", FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Migrate(ctx, Migrations[:1]); err != nil {
		t.Fatal(err)
	}
	want := errors.New("backup unavailable")
	if _, err := s.migrateWithBackup(ctx, func(context.Context, string) error { return want }); !errors.Is(err, want) {
		t.Fatalf("want backup error, got %v", err)
	}
	versions, err := s.AppliedVersions(ctx)
	if err != nil || len(versions) != 1 || versions[0] != Migrations[0].Version {
		t.Fatalf("migration ran after backup failure: versions=%v err=%v", versions, err)
	}
}
