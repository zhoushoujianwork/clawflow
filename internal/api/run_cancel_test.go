package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// writeLock plants a lock file mimicking what a live `clawflow run` would
// have written. Tests use this to set up the world before exercising the
// cancel handler — we never want the test to depend on AcquireLock's
// internal flow because that flow would attribute the lock to the test's
// own PID and we need to test "locked by a foreign PID" too.
func writeLock(t *testing.T, repo string, issue int, info snapshot.LockInfo) string {
	t.Helper()
	path := snapshot.LockPath(repo, issue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal lock: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	return path
}

func postCancel(t *testing.T, repo string, issue int) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(cancelRequest{Repo: repo, Issue: issue})
	req := httptest.NewRequest(http.MethodPost, "/api/run/cancel", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	HandleRunCancel(rr, req)
	return rr
}

func TestRunCancel_NotLocked(t *testing.T) {
	// No lock file → handler returns 200 with status="not_locked" so the
	// dashboard can show a reassuring "nothing to cancel, refresh" toast
	// instead of a scary error.
	t.Setenv("HOME", t.TempDir())

	rr := postCancel(t, "owner/repo", 1)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp cancelResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "not_locked" {
		t.Errorf("status = %q, want %q", resp.Status, "not_locked")
	}
}

func TestRunCancel_StaleLockOwnerDead(t *testing.T) {
	// Lock owned by a PID that is no longer running. Cancel should clean
	// the lock file (so the next run isn't blocked) and report
	// "already_dead" — no signal sent because there's nothing to signal.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	deadPID := findDeadPID(t)
	repo, issue := "owner/repo", 7
	path := writeLock(t, repo, issue, snapshot.LockInfo{
		PID:       deadPID,
		StartedAt: time.Now().Add(-time.Minute).UTC(),
		Operator:  "evaluate-bug",
		Repo:      repo,
		Issue:     issue,
	})

	rr := postCancel(t, repo, issue)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp cancelResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "already_dead" {
		t.Errorf("status = %q, want %q (body=%s)", resp.Status, "already_dead", rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file still present after cancel: %v", err)
	}
}

func TestRunCancel_LiveProcess(t *testing.T) {
	// Spawn a real `sleep 30` child, register a lock pointing at it, fire
	// cancel, and verify the child died and the lock was cleaned. This is
	// the path the dashboard's Cancel button actually hits in production.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		// Belt-and-braces: if the test fails before cancel fires the cleanup
		// must still kill the child so the test runner doesn't leak PIDs.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	repo, issue := "owner/repo", 11
	path := writeLock(t, repo, issue, snapshot.LockInfo{
		PID:       pid,
		StartedAt: time.Now().UTC(),
		Operator:  "implement",
		Repo:      repo,
		Issue:     issue,
	})

	rr := postCancel(t, repo, issue)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp cancelResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "cancelled" {
		t.Errorf("status = %q, want %q", resp.Status, "cancelled")
	}
	if resp.PID != pid {
		t.Errorf("PID in response = %d, want %d", resp.PID, pid)
	}

	// Reap the killed process so the test runner doesn't leak a zombie.
	_, _ = cmd.Process.Wait()

	if processAlive(pid) {
		t.Errorf("PID %d still alive after cancel", pid)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file still present after cancel: %v", err)
	}
}

func TestRunCancel_BadRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cases := []struct {
		name string
		body string
	}{
		{"missing repo", `{"issue":1}`},
		{"missing issue", `{"repo":"owner/repo"}`},
		{"bad JSON", `not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/run/cancel", bytes.NewReader([]byte(tc.body)))
			rr := httptest.NewRecorder()
			HandleRunCancel(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rr.Code)
			}
		})
	}
}

func TestRunCancel_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/run/cancel", nil)
	rr := httptest.NewRecorder()
	HandleRunCancel(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// findDeadPID returns a PID guaranteed to not be running. We start a tiny
// child, wait for it, then return its (now-recycled-but-not-yet-reused)
// PID. There's a theoretical race where the kernel reuses the PID before
// the test reads it, but on macOS/Linux PID rollover is in the tens of
// thousands so the test is stable in practice.
func findDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn true: %v", err)
	}
	pid := cmd.ProcessState.Pid()
	if pid <= 0 {
		t.Fatalf("got non-positive pid: %d", pid)
	}
	return pid
}

// Compile-time guard: cancelResponse must serialize a numeric PID — tests
// downstream rely on this. If the field type changes the test compile will
// catch it instead of silently breaking the dashboard contract.
var _ = strconv.Itoa(cancelResponse{}.PID)
