package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/preflight"
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
