package commands

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/preflight"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
)

func TestDispatchPreflightPlanFailureSharesBudgetAndStopsCalls(t *testing.T) {
	j, _, tasks, coordCalls := coordinationFixture(t)
	savedPlan, savedNow := dispatchPreflightPlan, dispatchPreflightNow
	t.Cleanup(func() { dispatchPreflightPlan = savedPlan; dispatchPreflightNow = savedNow })
	now := time.Now().UTC()
	dispatchPreflightNow = func() time.Time { return now }
	planCalls := 0
	dispatchPreflightPlan = func(context.Context, *runJob) (*acceptance.Plan, error) {
		planCalls++
		return nil, errors.New("private upstream details")
	}
	store := dispatchPreflightStore(j)
	for i := 0; i < preflight.MaxFailures; i++ {
		ok, err := prepareDispatch(context.Background(), j, tasks)
		if ok || err == nil {
			t.Fatal("plan failure dispatched", err)
		}
		l, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		st, err := l.Status()
		if err != nil {
			t.Fatal(err)
		}
		if st.Failures != i+1 {
			t.Fatalf("coordination reset plan failures: %+v", st)
		}
		before := planCalls
		if ok, err = prepareDispatch(context.Background(), j, tasks); ok || err == nil {
			t.Fatal("cooldown bypassed")
		}
		if planCalls != before {
			t.Fatal("model called during cooldown")
		}
		q, err := queue.LoadState(j.repo)
		if err != nil {
			t.Fatal(err)
		}
		wantState := "waiting"
		if i == preflight.MaxFailures-1 {
			wantState = "blocked"
		}
		if len(q.Entries) != 1 || q.Entries[0].State != wantState || strings.Contains(q.Entries[0].Reason, "private upstream") {
			t.Fatalf("queue=%+v", q)
		}

		now = st.NotBefore
	}
	if planCalls != preflight.MaxFailures || *coordCalls != 1 {
		t.Fatalf("plan=%d coordination=%d", planCalls, *coordCalls)
	}
	st, err := queue.LoadState(j.repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Entries) != 1 || st.Entries[0].State != "blocked" {
		t.Fatalf("queue=%+v", st)
	}
}

func TestDispatchPreflightNormalWaitAndWholeSuccess(t *testing.T) {
	j, _, tasks, _ := coordinationFixture(t)
	oldCoord, oldPlan := coordinateDispatch, dispatchPreflightPlan
	t.Cleanup(func() { coordinateDispatch = oldCoord; dispatchPreflightPlan = oldPlan })
	coordinateDispatch = func(context.Context, *runJob, coordinationTasks) (bool, error) { return false, nil }
	dispatchPreflightPlan = func(context.Context, *runJob) (*acceptance.Plan, error) {
		t.Fatal("plan called while waiting")
		return nil, nil
	}
	if ok, err := prepareDispatch(context.Background(), j, tasks); ok || err != nil {
		t.Fatal(ok, err)
	}
	l, err := dispatchPreflightStore(j).Load()
	if err != nil {
		t.Fatal(err)
	}
	st, _ := l.Status()
	if st.Failures != 0 || st.PendingID != "" {
		t.Fatal(st)
	}
	coordinateDispatch = oldCoord
	dispatchPreflightPlan = func(context.Context, *runJob) (*acceptance.Plan, error) { return &acceptance.Plan{}, nil }
	if ok, err := prepareDispatch(context.Background(), j, tasks); !ok || err != nil {
		t.Fatal(ok, err)
	}
	l, err = dispatchPreflightStore(j).Load()
	if err != nil {
		t.Fatal(err)
	}
	if l.Events[len(l.Events)-1].Outcome != "success" {
		t.Fatal("whole preflight not settled")
	}
}

func TestDispatchPreflightInheritedLockKeepsPending(t *testing.T) {
	j, _, tasks, _ := coordinationFixture(t)
	old := dispatchCoordinationExecute
	var unlock func()
	t.Cleanup(func() {
		dispatchCoordinationExecute = old
		if unlock != nil {
			unlock()
		}
	})
	dispatchCoordinationExecute = func(context.Context, string, execution.Options) (execution.Result, error) {
		store := dispatchPreflightStore(j)
		l, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		st, _ := l.Status()
		lock, err := filelock.Try(filepath.Join(store.Path()+".attempts", st.PendingID, "coordination.json.lock"), map[string]int{"PID": 0})
		if err != nil {
			t.Fatal(err)
		}
		unlock = func() { filelock.Release(lock) }
		return execution.Result{}, context.DeadlineExceeded
	}
	if ok, err := prepareDispatch(context.Background(), j, tasks); ok || !errors.Is(err, preflight.ErrPending) {
		t.Fatal(ok, err)
	}
	l, err := dispatchPreflightStore(j).Load()
	if err != nil {
		t.Fatal(err)
	}
	st, _ := l.Status()
	if st.PendingID == "" || st.Failures != 0 {
		t.Fatal("live ownership settled", st)
	}
}

func TestDispatchPreflightRechecksAfterPlanning(t *testing.T) {
	j, c, tasks, _ := coordinationFixture(t)
	old := dispatchPreflightPlan
	t.Cleanup(func() { dispatchPreflightPlan = old })
	dispatchPreflightPlan = func(context.Context, *runJob) (*acceptance.Plan, error) {
		c.issue.Body = "changed while planning"
		return &acceptance.Plan{}, nil
	}
	if ok, err := prepareDispatch(context.Background(), j, tasks); ok || !errors.Is(err, errCoordinationChanged) {
		t.Fatal(ok, err)
	}
	l, err := dispatchPreflightStore(j).Load()
	if err != nil {
		t.Fatal(err)
	}
	st, _ := l.Status()
	if st.Failures != 1 || st.PendingID != "" {
		t.Fatal("stale approval treated as success", st)
	}
}

func TestDispatchPreflightLockContentionDoesNotConsumeBudget(t *testing.T) {
	j, _, tasks, _ := coordinationFixture(t)
	old := coordinateDispatch
	t.Cleanup(func() { coordinateDispatch = old })
	coordinateDispatch = func(context.Context, *runJob, coordinationTasks) (bool, error) { return false, filelock.ErrHeld }
	if ok, err := prepareDispatch(context.Background(), j, tasks); ok || !errors.Is(err, filelock.ErrHeld) {
		t.Fatal(ok, err)
	}
	l, err := dispatchPreflightStore(j).Load()
	if err != nil {
		t.Fatal(err)
	}
	st, _ := l.Status()
	if st.Failures != 0 || st.PendingID != "" {
		t.Fatal(st)
	}
}
