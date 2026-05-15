package cloud

import (
	"testing"
)

// SQLiteStore runs of the shared lifecycle suite, using an ephemeral
// in-process ":memory:" database so tests are hermetic and fast.

func TestSQLiteStoreLifecycle(t *testing.T) {
	runStoreLifecycle(t, func() Store {
		s, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

func TestSQLiteStoreLeaseExpiry(t *testing.T) {
	runLeaseExpiry(t, func() Store {
		s, err := NewSQLiteStore(":memory:")
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
