package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestConnectionPolicyAppliesToEveryConnection(t *testing.T) {
	s := mustMigrate(t, storetest.New(t))
	ctx := context.Background()
	// Hold several connections open at once so the pool must create new ones.
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var fk int
			var mode string
			var timeout int
			var sync int
			c, err := s.DB().DB()
			if err != nil {
				t.Error(err)
				return
			}
			conn, err := c.Conn(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk)
			conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
			conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout)
			conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&sync)
			time.Sleep(50 * time.Millisecond)
			if fk != 1 || !strings.EqualFold(mode, "wal") || timeout != 10000 || sync != 2 {
				t.Errorf("connection policy: foreign_keys=%d journal=%s busy_timeout=%d synchronous=%d", fk, mode, timeout, sync)
			}
		}()
	}
	wg.Wait()
}

func TestForeignKeysAreEnforced(t *testing.T) {
	s := mustMigrate(t, storetest.New(t))
	err := s.DB().Exec(`INSERT INTO task_attempts (task_id,attempt_number,lease_epoch,worker_id,kind,status,started_at) VALUES ('missing',1,1,'w','infra','x',CURRENT_TIMESTAMP)`).Error
	if err == nil {
		t.Fatal("an attempt for an unknown task must be rejected")
	}
}

func TestDatabaseAndDirectoryAreCreatedPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "data", store.FileName)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustMigrate(t, s)
	for _, p := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s must be private, got %v (%v)", p, info.Mode(), err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s must be private, got %v", suffix, info.Mode())
		}
	}
}

func TestWriterContentionIsBoundedByBusyTimeout(t *testing.T) {
	s, path := storetest.NewWithPath(t)
	mustMigrate(t, s)
	// A second pool stands in for another process.
	other, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	hold := make(chan struct{})
	release := make(chan struct{})
	go func() {
		s.DB().Transaction(func(tx *gorm.DB) error {
			tx.Exec("INSERT INTO schema_migrations (version,name,applied_at) VALUES (900,'hold',CURRENT_TIMESTAMP)")
			close(hold)
			<-release
			return errors.New("abort")
		})
	}()
	<-hold
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- other.DB().Exec("INSERT INTO schema_migrations (version,name,applied_at) VALUES (901,'wait',CURRENT_TIMESTAMP)").Error
	}()
	select {
	case err := <-done:
		t.Fatalf("second writer must wait for the first, returned after %v: %v", time.Since(start), err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("writer should succeed once the first releases: %v", err)
	}
}

func TestBackupIncludesWALAndRefusesOverwrite(t *testing.T) {
	s := mustMigrate(t, storetest.New(t))
	ctx := context.Background()
	if err := s.DB().Exec(`INSERT INTO scheduled_checks (id,cron,timezone,prompt,ops,timeout_seconds,enabled,owner,created_at,updated_at,failures) VALUES ('c','* * * * *','UTC','p',1,1,true,'o',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,0)`).Error; err != nil {
		t.Fatal(err)
	}
	// The row is still in the WAL: copying only the main file would lose it.
	dest := filepath.Join(t.TempDir(), "backup.sqlite3")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dest); info.Mode().Perm()&0o077 != 0 {
		t.Fatal("backup must be private")
	}
	b, err := store.Open(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int64
	b.DB().Raw("SELECT count(*) FROM scheduled_checks").Scan(&n)
	if n != 1 {
		t.Fatalf("backup lost the WAL-resident row (rows=%d)", n)
	}
	if err := b.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, dest); err == nil {
		t.Fatal("backup must not overwrite an existing file")
	}
}

func TestOpenRefusesUnusablePaths(t *testing.T) {
	ctx := context.Background()
	if _, err := store.Open(ctx, ""); err == nil {
		t.Fatal("empty path accepted")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "blocker")
	os.WriteFile(file, nil, 0o600)
	if _, err := store.Open(ctx, filepath.Join(file, "data", store.FileName)); err == nil {
		t.Fatal("a path below a regular file must fail, not fall back to another location")
	}
	if os.Geteuid() != 0 {
		ro := filepath.Join(dir, "ro")
		os.Mkdir(ro, 0o500)
		if _, err := store.Open(ctx, filepath.Join(ro, "data", store.FileName)); err == nil {
			t.Fatal("an unwritable directory must fail")
		}
	}
}

func TestOpenRefusesCorruptDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", store.FileName)
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(strings.Repeat("not a sqlite database ", 200)), 0o600)
	s, err := store.Open(context.Background(), path)
	if err == nil {
		defer s.Close()
		if err = s.IntegrityCheck(context.Background()); err == nil {
			if _, err = s.Status(context.Background(), store.Migrations); err == nil {
				t.Fatal("a corrupt database must never look usable")
			}
		}
	}
}
