package commands

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Regression tests for issue #216: `clawflow run` hung indefinitely after
// reconcile (most likely a git subprocess blocked on a credential prompt),
// which in turn permanently stalled the web auto-run scheduler waiting on
// the run subprocess to exit. These cover the watchdog/budget math and the
// per-attempt git kill-on-timeout that prevent the hang.

func TestOverallRunBudget(t *testing.T) {
	cases := []struct {
		name  string
		perOp time.Duration
		want  time.Duration
	}{
		// 2× per-op dominates once per-op is large enough.
		{"default-hour", time.Hour, 2 * time.Hour},
		// Floor (per-op + 30m) dominates for small per-op timeouts.
		{"small-perop", 10 * time.Minute, 40 * time.Minute},
		// Zero/negative falls back to a 60m per-op assumption → 2h.
		{"zero", 0, 2 * time.Hour},
		{"negative", -5 * time.Minute, 2 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := overallRunBudget(c.perOp); got != c.want {
				t.Errorf("overallRunBudget(%s) = %s, want %s", c.perOp, got, c.want)
			}
		})
	}
}

func TestOverallRunBudgetAlwaysExceedsPerOp(t *testing.T) {
	// The whole-run budget must never be tighter than a single operator's
	// timeout, or a legitimate long operator would be killed mid-flight.
	for _, perOp := range []time.Duration{time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour} {
		if got := overallRunBudget(perOp); got <= perOp {
			t.Errorf("overallRunBudget(%s) = %s, must be > per-op timeout", perOp, got)
		}
	}
}

// Issue #221: the VCS scan/poll phase (after run/reconcile, before the first
// run/lock) had no bound of its own — one ListSubIssues per open issue, each
// capped only by the 30s HTTP timeout, so a throttled endpoint amplified N
// sequential calls into a multi-hour silent hang that held run.lock. The scan
// phase now gets its own deadline; this verifies the budget math.
func TestScanPhaseBudget(t *testing.T) {
	cases := []struct {
		name  string
		perOp time.Duration
		want  time.Duration
	}{
		// perOp/4, clamped to [5m, 15m].
		{"default-hour", time.Hour, 15 * time.Minute},  // 15m hits the cap
		{"two-hours", 2 * time.Hour, 15 * time.Minute}, // still capped at 15m
		{"forty-min", 40 * time.Minute, 10 * time.Minute},
		{"small-perop", 10 * time.Minute, 5 * time.Minute}, // floor
		{"zero", 0, 15 * time.Minute},                      // falls back to 60m → cap
		{"negative", -5 * time.Minute, 15 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scanPhaseBudget(c.perOp); got != c.want {
				t.Errorf("scanPhaseBudget(%s) = %s, want %s", c.perOp, got, c.want)
			}
		})
	}
}

func TestScanPhaseBudgetAlwaysBelowOverallBudget(t *testing.T) {
	// The scan budget must always fire well before the overall-run watchdog,
	// otherwise a hung poll could still pin run.lock until the hard kill.
	for _, perOp := range []time.Duration{time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour} {
		scan := scanPhaseBudget(perOp)
		overall := overallRunBudget(perOp)
		if scan >= overall {
			t.Errorf("scanPhaseBudget(%s)=%s must be < overallRunBudget(%s)=%s", perOp, scan, perOp, overall)
		}
	}
}

func TestHardenedGitEnvDisablesPrompts(t *testing.T) {
	env := hardenedGitEnv([]string{"PATH=/usr/bin"})

	var gotTerminal, gotSSH bool
	for _, e := range env {
		if e == "GIT_TERMINAL_PROMPT=0" {
			gotTerminal = true
		}
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") && strings.Contains(e, "BatchMode=yes") {
			gotSSH = true
		}
	}
	if !gotTerminal {
		t.Error("hardenedGitEnv must set GIT_TERMINAL_PROMPT=0 to block HTTP credential prompts")
	}
	if !gotSSH {
		t.Error("hardenedGitEnv must set GIT_SSH_COMMAND with BatchMode=yes to block SSH passphrase prompts")
	}
	// Base env must be preserved.
	if env[0] != "PATH=/usr/bin" {
		t.Errorf("hardenedGitEnv dropped base env: got first entry %q", env[0])
	}
}

func TestHardenedGitEnvNilBaseUsesEnviron(t *testing.T) {
	// nil base must not panic and must still carry the guards.
	env := hardenedGitEnv(nil)
	if len(env) < 2 {
		t.Fatalf("expected at least the two guard vars, got %d entries", len(env))
	}
}

func TestRunBoundedGitKillsOnTimeout(t *testing.T) {
	// A command that outlives the ceiling must be killed and return a
	// timeout error well before its natural completion — this is the core
	// guarantee that a wedged git fetch can't pin the run forever.
	start := time.Now()
	cmd := exec.Command("sleep", "30")
	err := runBoundedGit(cmd, 200*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("expected an 'exceeded' timeout error, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("runBoundedGit did not kill the process promptly: took %s", elapsed)
	}
}

func TestRunBoundedGitFastCommandSucceeds(t *testing.T) {
	// A command that finishes within the ceiling must return its own
	// (nil) result, not a spurious timeout.
	cmd := exec.Command("true")
	if err := runBoundedGit(cmd, 5*time.Second); err != nil {
		t.Errorf("fast command should succeed, got %v", err)
	}
}

func TestRunBoundedGitPropagatesCommandError(t *testing.T) {
	// A command that fails fast must surface its own error, not a timeout.
	cmd := exec.Command("false")
	err := runBoundedGit(cmd, 5*time.Second)
	if err == nil {
		t.Fatal("expected the command's own non-zero exit error, got nil")
	}
	if strings.Contains(err.Error(), "exceeded") {
		t.Errorf("a fast failure must not be reported as a timeout: %v", err)
	}
}

func TestRunGitWithRetryTimesOutHangingCommand(t *testing.T) {
	// End-to-end: a git command that hangs (simulated by sleep) must be
	// bounded by gitAttemptTimeout rather than blocking indefinitely, and
	// a timeout must NOT be retried (retrying a hang only delays failure).
	orig := gitAttemptTimeout
	gitAttemptTimeout = 200 * time.Millisecond
	defer func() { gitAttemptTimeout = orig }()

	start := time.Now()
	err := runGitWithRetry(func() *exec.Cmd {
		return exec.Command("sleep", "30")
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error from a hanging git command, got nil")
	}
	// One attempt (~200ms) + no retries. Generous ceiling for CI jitter,
	// but far below the 30s the command would otherwise take.
	if elapsed > 5*time.Second {
		t.Errorf("runGitWithRetry should fail fast on timeout, took %s", elapsed)
	}
}
