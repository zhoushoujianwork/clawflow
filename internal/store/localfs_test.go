//go:build darwin || linux

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenRefusesNetworkFilesystemBeforeCreatingDatabase(t *testing.T) {
	original := statFilesystem
	t.Cleanup(func() { statFilesystem = original })
	statFilesystem = func(_ string, st *syscall.Statfs_t) error {
		st.Flags = 0     // macOS: MNT_LOCAL absent
		st.Type = 0x6969 // Linux: NFS_SUPER_MAGIC
		return nil
	}
	path := filepath.Join(t.TempDir(), "database.sqlite3")
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrNetworkFilesystem) {
		t.Fatalf("network filesystem accepted: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected database created: %v", err)
	}
}

func TestOpenFailsClosedWhenFilesystemCannotBeInspected(t *testing.T) {
	original := statFilesystem
	t.Cleanup(func() { statFilesystem = original })
	failure := errors.New("statfs denied")
	statFilesystem = func(string, *syscall.Statfs_t) error { return failure }
	path := filepath.Join(t.TempDir(), "database.sqlite3")
	if _, err := Open(context.Background(), path); !errors.Is(err, failure) {
		t.Fatalf("statfs failure ignored: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("database created without filesystem verification: %v", err)
	}
}
