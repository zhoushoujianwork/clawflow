package budget

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestReserve_NoEnvVar_IsNoOp(t *testing.T) {
	t.Setenv(EnvPath, "")
	if err := Reserve("CreateIssue"); err != nil {
		t.Fatalf("Reserve with unset env var should be no-op, got: %v", err)
	}
}

func TestReserve_UnderCap_Succeeds(t *testing.T) {
	path := newBudgetFile(t, 3)
	t.Setenv(EnvPath, path)

	for i := range 3 {
		if err := Reserve("CreateIssue"); err != nil {
			t.Fatalf("Reserve #%d should succeed under cap, got: %v", i+1, err)
		}
	}

	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Used != 3 {
		t.Errorf("Used = %d, want 3", s.Used)
	}
	if len(s.Ops) != 3 {
		t.Errorf("len(Ops) = %d, want 3", len(s.Ops))
	}
	for i, op := range s.Ops {
		if op.Name != "CreateIssue" {
			t.Errorf("Ops[%d].Name = %q, want CreateIssue", i, op.Name)
		}
		if op.At == "" {
			t.Errorf("Ops[%d].At is empty", i)
		}
	}
}

func TestReserve_AtCap_Errors(t *testing.T) {
	path := newBudgetFile(t, 2)
	t.Setenv(EnvPath, path)

	if err := Reserve("CreateIssue"); err != nil {
		t.Fatalf("Reserve #1: %v", err)
	}
	if err := Reserve("AddLabel"); err != nil {
		t.Fatalf("Reserve #2: %v", err)
	}
	err := Reserve("CloseIssue")
	if err == nil {
		t.Fatal("Reserve #3 should fail (budget exhausted), got nil")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Errorf("error message should mention 'exhausted', got: %v", err)
	}
	if !strings.Contains(err.Error(), "CloseIssue") {
		t.Errorf("error message should name the rejected op, got: %v", err)
	}

	// Verify rejected op did NOT increment Used.
	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Used != 2 {
		t.Errorf("Used = %d after rejected attempt, want 2", s.Used)
	}
}

func TestReserve_Concurrent_NoOvercommit(t *testing.T) {
	const cap = 10
	const goroutines = 50
	path := newBudgetFile(t, cap)
	t.Setenv(EnvPath, path)

	var ok, fail int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range goroutines {
		wg.Go(func() {
			<-start
			if err := Reserve("AddLabel"); err != nil {
				atomic.AddInt64(&fail, 1)
			} else {
				atomic.AddInt64(&ok, 1)
			}
		})
	}
	close(start)
	wg.Wait()

	if ok != cap {
		t.Errorf("succeeded = %d, want exactly %d (the cap)", ok, cap)
	}
	if fail != goroutines-cap {
		t.Errorf("failed = %d, want %d", fail, goroutines-cap)
	}

	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Used != cap {
		t.Errorf("file Used = %d, want %d (no overcommit allowed)", s.Used, cap)
	}
	if len(s.Ops) != cap {
		t.Errorf("len(Ops) = %d, want %d (all successful reserves should be recorded)", len(s.Ops), cap)
	}
}

func TestInit_DefaultsMaxWhenZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	if err := Init(path, 0); err != nil {
		t.Fatalf("Init: %v", err)
	}
	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.Max != DefaultMax {
		t.Errorf("Max = %d, want %d (DefaultMax)", s.Max, DefaultMax)
	}
}

func TestReserve_MissingFile_Errors(t *testing.T) {
	t.Setenv(EnvPath, filepath.Join(t.TempDir(), "does-not-exist.json"))
	err := Reserve("CreateIssue")
	if err == nil {
		t.Fatal("Reserve with missing budget file should error, got nil")
	}
}

// newBudgetFile creates a budget file in a fresh tempdir with the given cap.
func newBudgetFile(t *testing.T, max int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.json")
	if err := Init(path, max); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// sanity: file exists and parses
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("budget file not created: %v", err)
	}
	return path
}
