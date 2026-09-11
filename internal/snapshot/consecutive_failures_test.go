package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRunWithStatus persists a minimal meta.json for (repo, issue) at the
// given age so ConsecutiveFailures has something to walk. Newer runs get a
// smaller `agoMin`.
func writeRunWithStatus(t *testing.T, repo string, issueNum int, agoMin int, status string) {
	t.Helper()
	started := time.Now().UTC().Add(-time.Duration(agoMin) * time.Minute)
	slug := "zhoushoujianwork__clawflow"
	if repo != "zhoushoujianwork/clawflow" {
		t.Fatalf("helper only supports the clawflow slug, got %q", repo)
	}
	dir := filepath.Join(DataDir(), "runs", slug, fmt.Sprintf("issue-%d", issueNum), started.Format("2006-01-02T15-04-05Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	ended := started.Add(time.Minute)
	meta := RunMeta{
		Operator:    "implement",
		Repo:        repo,
		IssueNumber: issueNum,
		StartedAt:   started,
		EndedAt:     &ended,
		Status:      status,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

// TestConsecutiveFailuresIgnoresInfraRefusals pins the circuit-breaker
// contract at the heart of issue #308: statuses that describe a provider
// refusal rather than a problem with the issue must break the failure streak.
// Before the fix a 402 billing cap landed as status="failed", so three capped
// passes were enough to label a healthy issue agent-failed.
func TestConsecutiveFailuresIgnoresInfraRefusals(t *testing.T) {
	repo := "zhoushoujianwork/clawflow"

	t.Run("cost-limit does not count and breaks the streak", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		// Newest first: cost-limit, cost-limit, then two real failures.
		writeRunWithStatus(t, repo, 306, 1, "cost-limit")
		writeRunWithStatus(t, repo, 306, 2, "cost-limit")
		writeRunWithStatus(t, repo, 306, 3, "failed")
		writeRunWithStatus(t, repo, 306, 4, "failed")
		if got := ConsecutiveFailures(repo, 306); got != 0 {
			t.Errorf("ConsecutiveFailures = %d, want 0 (a billing cap must not arm the breaker)", got)
		}
	})

	t.Run("three cost-limit passes stay below the default threshold", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		// The exact shape from the issue: the same issue re-queued across
		// three consecutive passes while the account was capped.
		writeRunWithStatus(t, repo, 307, 1, "cost-limit")
		writeRunWithStatus(t, repo, 307, 3, "cost-limit")
		writeRunWithStatus(t, repo, 307, 5, "cost-limit")
		if got := ConsecutiveFailures(repo, 307); got >= 3 {
			t.Errorf("ConsecutiveFailures = %d, want < 3 (must not trip max_consecutive_failures)", got)
		}
	})

	t.Run("rate-limited and auth-error also break the streak", func(t *testing.T) {
		for _, status := range []string{"rate-limited", "auth-error"} {
			t.Run(status, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				writeRunWithStatus(t, repo, 400, 1, status)
				writeRunWithStatus(t, repo, 400, 2, "failed")
				if got := ConsecutiveFailures(repo, 400); got != 0 {
					t.Errorf("ConsecutiveFailures with newest=%q = %d, want 0", status, got)
				}
			})
		}
	})

	t.Run("genuine failures still count", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		writeRunWithStatus(t, repo, 401, 1, "failed")
		writeRunWithStatus(t, repo, 401, 2, "no-marker")
		writeRunWithStatus(t, repo, 401, 3, "failed")
		writeRunWithStatus(t, repo, 401, 4, "success")
		if got := ConsecutiveFailures(repo, 401); got != 3 {
			t.Errorf("ConsecutiveFailures = %d, want 3 (real failures must still arm the breaker)", got)
		}
	})
}
