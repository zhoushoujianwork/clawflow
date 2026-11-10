package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/preflight"
	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

func TestPreflightReconciliationRequiresExactTerminalIdentity(t *testing.T) {
	for _, mode := range []string{"terminal", "active", "missing-active", "wrong-caller", "wrong-session", "unknown-status", "held-lock", "missing-owner", "runtime-changed", "query-failed", "missing-checkpoint", "lost-returned-checkpoint", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			j, _, _, _ := coordinationFixture(t)
			s := dispatchPreflightStore(j)
			now := time.Now()
			id, err := s.Begin("dispatch", now)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(s.Path()+".attempts", id)
			if err = writePreflightOwner(root, id, s); err != nil {
				t.Fatal(err)
			}
			if err = recordPreflightStage(root, "coordination", "started"); err != nil {
				t.Fatal(err)
			}
			cp := execution.Checkpoint{Version: 1, Workspace: t.TempDir(), Profile: "fixture", Command: "fixture-offload", RunID: "run-1", CallerRunID: "caller-1", Backend: "claude", Session: "session-1", ObservationOnly: true, Completed: true}
			raw, _ := json.Marshal(cp)
			path := filepath.Join(root, "coordination.json")
			if mode != "missing-checkpoint" {
				if err = os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "lost-returned-checkpoint" {
				if err = recordPreflightStage(root, "coordination", "returned"); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing-owner" {
				if err = os.Remove(filepath.Join(root, "owner.json")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "held-lock" {
				lock, e := filelock.Try(path+".lock", map[string]int{"PID": 0})
				if e != nil {
					t.Fatal(e)
				}
				defer filelock.Release(lock)
			}
			oldQuery, oldConfig := queryPreflightNative, preflightExpectedConfig
			t.Cleanup(func() { queryPreflightNative = oldQuery; preflightExpectedConfig = oldConfig })
			preflightExpectedConfig = func(*runJob, string) (execution.Config, error) {
				command := cp.Command
				if mode == "runtime-changed" {
					command = "different"
				}
				return execution.Config{Command: command, Profile: cp.Profile}, nil
			}
			calls := 0
			queryPreflightNative = func(context.Context, execution.Checkpoint) (preflightNativeStatus, error) {
				calls++
				active := mode == "active"
				st := preflightNativeStatus{Version: 1, RunID: cp.RunID, CallerRunID: cp.CallerRunID, Profile: cp.Profile, Backend: cp.Backend, Phase: "done", Status: "ok", Session: cp.Session, Active: &active}
				switch mode {
				case "missing-active":
					st.Active = nil
				case "wrong-caller":
					st.CallerRunID = "foreign"
				case "wrong-session":
					st.Session = "foreign"
				case "cancelled":
					st.Status = "canceled"
				case "unknown-status":
					st.Status = "unknown"
				case "query-failed":
					return st, errors.New("unavailable")
				}
				return st, nil
			}
			fence, err := filelock.Try(s.Path()+".execution.lock", map[string]int{"PID": 0})
			if err != nil {
				t.Fatal(err)
			}
			defer filelock.Release(fence)
			err = reconcileDispatchPreflight(context.Background(), j, s)
			l, readErr := s.Load()
			if readErr != nil {
				t.Fatal(readErr)
			}
			st, _ := l.Status()
			if mode == "terminal" {
				if err != nil || st.Failures != 1 || st.PendingID != "" {
					t.Fatal(st, err)
				}
				if _, err = os.Stat(filepath.Join(root, "reconciled-terminal.json")); err != nil {
					t.Fatal(err)
				}
				if err = reconcileDispatchPreflight(context.Background(), j, s); err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatal("terminal attempt reconciled twice")
				}
				if _, err = s.Begin("dispatch", now); !errors.Is(err, preflight.ErrCooldown) {
					t.Fatal("interruption skipped cooldown", err)
				}
			} else if err == nil || st.Failures != 0 || st.PendingID != id {
				t.Fatal("uncertain attempt settled", mode, st, err)
			}
			if mode == "held-lock" && calls != 0 {
				t.Fatal("queried a live owned invocation")
			}
		})
	}
}

func TestPreflightWholeAttemptFenceStopsDuplicate(t *testing.T) {
	j, _, tasks, calls := coordinationFixture(t)
	s := dispatchPreflightStore(j)
	fence, err := filelock.Try(s.Path()+".execution.lock", map[string]int{"PID": 0})
	if err != nil {
		t.Fatal(err)
	}
	defer filelock.Release(fence)
	if ok, err := prepareDispatch(context.Background(), j, tasks); ok || !errors.Is(err, filelock.ErrHeld) {
		t.Fatal(ok, err)
	}
	if *calls != 0 {
		t.Fatal("duplicate preflight called model")
	}
	if _, err = os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatal("lock competition consumed budget")
	}
}

func TestPreflightReconcileReturnedCacheWithoutNativeCall(t *testing.T) {
	j, _, _, _ := coordinationFixture(t)
	s := dispatchPreflightStore(j)
	id, err := s.Begin("dispatch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.Path()+".attempts", id)
	if err = writePreflightOwner(root, id, s); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"coordination", "plan"} {
		if err = recordPreflightStage(root, stage, "started"); err != nil {
			t.Fatal(err)
		}
		if err = recordPreflightStage(root, stage, "returned"); err != nil {
			t.Fatal(err)
		}
	}
	old := queryPreflightNative
	t.Cleanup(func() { queryPreflightNative = old })
	queryPreflightNative = func(context.Context, execution.Checkpoint) (preflightNativeStatus, error) {
		t.Fatal("cache-only attempt queried native runtime")
		return preflightNativeStatus{}, nil
	}
	fence, err := filelock.Try(s.Path()+".execution.lock", map[string]int{"PID": 0})
	if err != nil {
		t.Fatal(err)
	}
	defer filelock.Release(fence)
	if err = reconcileDispatchPreflight(context.Background(), j, s); err != nil {
		t.Fatal(err)
	}
	l, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, _ := l.Status()
	if st.Failures != 1 || st.PendingID != "" {
		t.Fatal(st)
	}
}

func TestPreflightStatusOutputIsBounded(t *testing.T) {
	var out preflightOutput
	input := strings.Repeat("x", 3<<20)
	if _, err := io.Copy(&out, io.LimitReader(strings.NewReader(input), int64(len(input)))); err != nil {
		t.Fatal(err)
	}
	if !out.truncated || out.buffer.Len() != 2<<20 {
		t.Fatal("status output limit bypassed", out.buffer.Len())
	}
}

// The library engine records Engine "library" and no command, while the
// expected configuration still carries the configured command path. Only the
// CLI engine compares the command; an engine switch is always a change.
func TestPreflightReconcileExecutionConfigurationComparison(t *testing.T) {
	const profile = "claude-cc-sonnet-5-5"
	cases := []struct {
		name     string
		recorded execution.Checkpoint
		expected execution.Config
		changed  bool
	}{
		{"library engine and profile match despite configured command", execution.Checkpoint{Engine: execution.EngineLibrary, Profile: profile}, execution.Config{Engine: execution.EngineLibrary, Command: "/usr/local/bin/offload", Profile: profile}, false},
		{"library engine without configured command", execution.Checkpoint{Engine: execution.EngineLibrary, Profile: profile}, execution.Config{Engine: execution.EngineLibrary, Profile: profile}, false},
		{"library profile changed", execution.Checkpoint{Engine: execution.EngineLibrary, Profile: profile}, execution.Config{Engine: execution.EngineLibrary, Command: "/usr/local/bin/offload", Profile: "other"}, true},
		{"engine changed from cli to library", execution.Checkpoint{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: profile}, execution.Config{Engine: execution.EngineLibrary, Command: "/usr/local/bin/offload", Profile: profile}, true},
		{"engine changed from library to cli", execution.Checkpoint{Engine: execution.EngineLibrary, Profile: profile}, execution.Config{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: profile}, true},
		{"cli command unchanged", execution.Checkpoint{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: profile}, execution.Config{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: profile}, false},
		{"cli command changed", execution.Checkpoint{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: profile}, execution.Config{Engine: execution.EngineCLI, Command: "/opt/offload", Profile: profile}, true},
		{"legacy checkpoint without engine matches default cli", execution.Checkpoint{Command: "/usr/local/bin/offload", Profile: profile}, execution.Config{Command: "/usr/local/bin/offload", Profile: profile}, false},
		{"legacy checkpoint without engine, command changed", execution.Checkpoint{Command: "/usr/local/bin/offload", Profile: profile}, execution.Config{Command: "/opt/offload", Profile: profile}, true},
		{"legacy checkpoint now library", execution.Checkpoint{Command: "/usr/local/bin/offload", Profile: profile}, execution.Config{Engine: execution.EngineLibrary, Profile: profile}, true},
		{"cli profile changed", execution.Checkpoint{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: profile}, execution.Config{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: "other"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPreflightRecoveryFixture(t, tc.recorded, "returned", "")
			preflightExpectedConfig = func(*runJob, string) (execution.Config, error) { return tc.expected, nil }
			// A matching library checkpoint reaches the reclaim confirmation.
			oldReclaim := reclaimOffloadRun
			t.Cleanup(func() { reclaimOffloadRun = oldReclaim })
			reclaimOffloadRun = func(context.Context, string) (offload.ReclaimOutcome, error) { return offload.ReclaimAlreadyEnded, nil }
			st, err := f.reconcile(t)
			if tc.changed {
				if !errors.Is(err, preflight.ErrPending) || !strings.Contains(err.Error(), "execution configuration changed") || *f.queried != 0 || st.PendingID != f.id || st.Failures != 0 {
					t.Fatalf("configuration change not detected: err=%v queried=%d status=%+v", err, *f.queried, st)
				}
				return
			}
			f.assertSettled(t, err, st)
		})
	}
}

// preflightRecoveryFixture records one coordination invocation for a pending
// preflight attempt and returns what reconciliation needs to settle it.
type preflightRecoveryFixture struct {
	job      *runJob
	store    preflight.Store
	id, root string
	cp       execution.Checkpoint
	queried  *int
}

// newPreflightRecoveryFixture writes the checkpoint the invocation left behind.
// stage is the recorded coordination stage state: "started" or "returned".
// record, when not empty, is the retained Offload result.json of a library run.
func newPreflightRecoveryFixture(t *testing.T, cp execution.Checkpoint, stage, record string) *preflightRecoveryFixture {
	t.Helper()
	j, _, _, _ := coordinationFixture(t)
	s := dispatchPreflightStore(j)
	id, err := s.Begin("dispatch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.Path()+".attempts", id)
	if err = writePreflightOwner(root, id, s); err != nil {
		t.Fatal(err)
	}
	if err = recordPreflightStage(root, "coordination", "started"); err != nil {
		t.Fatal(err)
	}
	cp.Version, cp.Workspace, cp.RunID, cp.CallerRunID, cp.Backend, cp.Session = 1, t.TempDir(), "run-1", "caller-1", "claude", "session-1"
	cp.ObservationOnly, cp.Completed = true, true
	if cp.Engine == execution.EngineLibrary {
		// Offload writes the run record into the artifact directory the
		// checkpoint names, beside the checkpoint under execution-artifacts.
		cp.ArtifactDir = filepath.Join(root, "execution-artifacts", "clawflow-offload-test")
		if err = os.MkdirAll(cp.ArtifactDir, 0700); err != nil {
			t.Fatal(err)
		}
		if record != "" {
			if err = os.WriteFile(filepath.Join(cp.ArtifactDir, "result.json"), []byte(record), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, _ := json.Marshal(cp)
	if err = os.WriteFile(filepath.Join(root, "coordination.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if stage == "returned" {
		if err = recordPreflightStage(root, "coordination", "returned"); err != nil {
			t.Fatal(err)
		}
	}
	oldQuery, oldConfig := queryPreflightNative, preflightExpectedConfig
	t.Cleanup(func() { queryPreflightNative = oldQuery; preflightExpectedConfig = oldConfig })
	preflightExpectedConfig = func(*runJob, string) (execution.Config, error) {
		return execution.Config{Engine: cp.Engine, Command: "/usr/local/bin/offload", Profile: cp.Profile}, nil
	}
	queried := 0
	queryPreflightNative = func(context.Context, execution.Checkpoint) (preflightNativeStatus, error) {
		queried++
		active := false
		return preflightNativeStatus{Version: 1, RunID: cp.RunID, CallerRunID: cp.CallerRunID, Profile: cp.Profile, Backend: cp.Backend, Phase: "done", Status: "ok", Session: cp.Session, Active: &active}, nil
	}
	return &preflightRecoveryFixture{job: j, store: s, id: id, root: root, cp: cp, queried: &queried}
}

// reconcile runs reconciliation under the execution fence and returns the
// attempt status it left with the reconciliation error.
func (f *preflightRecoveryFixture) reconcile(t *testing.T) (preflight.Status, error) {
	t.Helper()
	fence, err := filelock.Try(f.store.Path()+".execution.lock", map[string]int{"PID": 0})
	if err != nil {
		t.Fatal(err)
	}
	defer filelock.Release(fence)
	err = reconcileDispatchPreflight(context.Background(), f.job, f.store)
	l, readErr := f.store.Load()
	if readErr != nil {
		t.Fatal(readErr)
	}
	st, _ := l.Status()
	return st, err
}

func (f *preflightRecoveryFixture) assertSettled(t *testing.T, err error, st preflight.Status) {
	t.Helper()
	if err != nil || st.PendingID != "" || st.Failures != 1 {
		t.Fatalf("attempt not settled as interrupted: err=%v status=%+v", err, st)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, "reconciled-terminal.json")); statErr != nil {
		t.Fatalf("no reconciliation evidence: %v", statErr)
	}
}

func (f *preflightRecoveryFixture) assertPending(t *testing.T, err error, st preflight.Status) {
	t.Helper()
	if !errors.Is(err, preflight.ErrPending) || st.PendingID != f.id || st.Failures != 0 {
		t.Fatalf("attempt must stay pending: err=%v status=%+v", err, st)
	}
	if _, statErr := os.Stat(filepath.Join(f.root, "reconciled-terminal.json")); !os.IsNotExist(statErr) {
		t.Fatalf("pending attempt wrote reconciliation evidence: %v", statErr)
	}
}

// Offload's task registry does not list embedded library runs, so reconciliation
// confirms the run ended through Offload's reclaim primitive instead.
func TestPreflightReconcileLibraryRunUsesReclaimNotNativeQuery(t *testing.T) {
	const cleanup = `{"status":"timeout","exit_code":124,"error_kind":"timeout","cleanup":"unresolved: worker processes are still running in process group 4242"}`
	tests := []struct {
		name    string
		stage   string // recorded coordination stage state
		record  string // retained Offload result.json, if any
		reclaim func() (offload.ReclaimOutcome, error)
		settled bool
		wantErr error // preserved in the pending error
	}{
		{name: "returned and run not found without recorded cleanup", stage: "returned",
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunNotFound }, settled: true},
		{name: "returned and run not found with a clean run record", stage: "returned", record: libraryTimeoutRecord,
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunNotFound }, settled: true},
		{name: "not returned and run not found", stage: "started",
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunNotFound }, wantErr: offload.ErrRunNotFound},
		{name: "terminated after return", stage: "returned",
			reclaim: func() (offload.ReclaimOutcome, error) { return offload.ReclaimTerminated, nil }, settled: true},
		{name: "terminated without return", stage: "started",
			reclaim: func() (offload.ReclaimOutcome, error) { return offload.ReclaimTerminated, nil }, settled: true},
		{name: "already ended without return", stage: "started",
			reclaim: func() (offload.ReclaimOutcome, error) { return offload.ReclaimAlreadyEnded, nil }, settled: true},
		{name: "still owned after return", stage: "returned",
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunStillOwned }, wantErr: offload.ErrRunStillOwned},
		{name: "still owned without return", stage: "started",
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunStillOwned }, wantErr: offload.ErrRunStillOwned},
		{name: "start unrecorded", stage: "returned",
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunStartUnrecorded }, wantErr: offload.ErrRunStartUnrecorded},
		{name: "returned and run not found with unresolved cleanup", stage: "returned", record: cleanup,
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunNotFound }, wantErr: offload.ErrRunNotFound},
		{name: "returned and run not found with unreadable run record", stage: "returned", record: `{not json`,
			reclaim: func() (offload.ReclaimOutcome, error) { return "", offload.ErrRunNotFound }, wantErr: offload.ErrRunNotFound},
		{name: "unconfirmed reclaim outcome", stage: "returned",
			reclaim: func() (offload.ReclaimOutcome, error) { return "unconfirmed", nil }},
		{name: "other reclaim failure", stage: "returned",
			reclaim: func() (offload.ReclaimOutcome, error) { return "", errors.New("reclaim failed") }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newPreflightRecoveryFixture(t, execution.Checkpoint{Engine: execution.EngineLibrary, Profile: "claude-cc-sonnet-5-5"}, tc.stage, tc.record)
			oldReclaim := reclaimOffloadRun
			t.Cleanup(func() { reclaimOffloadRun = oldReclaim })
			var reclaimed []string
			reclaimOffloadRun = func(_ context.Context, runID string) (offload.ReclaimOutcome, error) {
				reclaimed = append(reclaimed, runID)
				return tc.reclaim()
			}
			st, err := f.reconcile(t)
			if *f.queried != 0 {
				t.Fatal("library run was queried through the native CLI")
			}
			if !reflect.DeepEqual(reclaimed, []string{"run-1"}) {
				t.Fatalf("reclaim calls: %v", reclaimed)
			}
			if tc.settled {
				f.assertSettled(t, err, st)
				raw, readErr := os.ReadFile(filepath.Join(f.root, "reconciled-terminal.json"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				var evidence struct {
					Native map[string]preflightNativeStatus
				}
				if json.Unmarshal(raw, &evidence) != nil {
					t.Fatalf("unreadable evidence: %s", raw)
				}
				got := evidence.Native["coordination"]
				if got.Status != "library-ended" || got.Phase != "done" || got.Active == nil || *got.Active || got.RunID != "run-1" || got.CallerRunID != "caller-1" || got.Profile != f.cp.Profile || got.Backend != "claude" || got.Session != "session-1" {
					t.Fatalf("evidence lacks library identity: %+v", got)
				}
				return
			}
			f.assertPending(t, err, st)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("pending error %v does not preserve %v", err, tc.wantErr)
			}
		})
	}
}

// Without the artifact directory the retained run record cannot be inspected.
func TestPreflightReconcileLibraryRunWithoutArtifactDirStaysPending(t *testing.T) {
	f := newPreflightRecoveryFixture(t, execution.Checkpoint{Engine: execution.EngineLibrary, Profile: "claude-cc-sonnet-5-5"}, "returned", "")
	f.cp.ArtifactDir = ""
	raw, _ := json.Marshal(f.cp)
	if err := os.WriteFile(filepath.Join(f.root, "coordination.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	old := abandonOrphanedRun
	t.Cleanup(func() { abandonOrphanedRun = old })
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) {
		t.Fatal("reclaimed a run whose artifact directory is unknown")
		return AbandonUnknown, nil
	}
	st, err := f.reconcile(t)
	f.assertPending(t, err, st)
}

// CLI invocations keep using the native task query and never touch reclaim.
func TestPreflightReconcileCLIRunStillUsesNativeQuery(t *testing.T) {
	f := newPreflightRecoveryFixture(t, execution.Checkpoint{Engine: execution.EngineCLI, Command: "/usr/local/bin/offload", Profile: "fixture"}, "returned", "")
	old := abandonOrphanedRun
	t.Cleanup(func() { abandonOrphanedRun = old })
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) {
		t.Fatal("CLI invocation was reclaimed as a library run")
		return AbandonUnknown, nil
	}
	st, err := f.reconcile(t)
	f.assertSettled(t, err, st)
	if *f.queried != 1 {
		t.Fatalf("native query calls: %d", *f.queried)
	}
	raw, _ := os.ReadFile(filepath.Join(f.root, "reconciled-terminal.json"))
	var evidence struct {
		Native map[string]preflightNativeStatus
	}
	if json.Unmarshal(raw, &evidence) != nil || evidence.Native["coordination"].Status != "ok" {
		t.Fatalf("CLI evidence changed: %s", raw)
	}
}
