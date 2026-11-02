package upgrade_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

func (f *fixture) workerRecord(e *upgrade.Engine, target upgrade.Artifact, names ...string) *upgrade.Record {
	f.t.Helper()
	var steps []upgrade.WorkerStep
	for _, n := range names {
		steps = append(steps, upgrade.WorkerStep{Name: n, Descriptor: n, Status: upgrade.WorkerPending})
	}
	rec, err := e.Begin(f.ctx, upgrade.KindWorkers, target, gates(), steps)
	if err != nil {
		f.t.Fatal(err)
	}
	return rec
}

func TestWorkersAreReplacedOneAtATimeAndDrainFirst(t *testing.T) {
	f := newFixture(t)
	_, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	f.register("w3", 1)
	f.enqueue(10)
	task := f.claim("w1") // w1 holds a lease that must settle before it is replaced

	var minOthers = 99
	e := f.engine(upgrade.Options{MinCapacity: 1, Hook: func(stage, phase string) error {
		snap, err := f.tasks.UpgradeSnapshot(f.ctx, nil)
		if err != nil {
			return err
		}
		ready := 0
		for _, w := range snap.Workers {
			if w.Live && !w.Paused && !w.Revoked {
				ready += w.MaxParallel
			}
		}
		if ready < minOthers {
			minOthers = ready
		}
		return nil
	}})
	rec := f.workerRecord(e, target, "w1", "w2", "w3")

	done := make(chan error, 1)
	go func() { done <- e.Run(f.ctx, rec) }()

	// While w1 still holds its lease, nothing may be rolled.
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	rolledEarly := len(f.rolled)
	f.mu.Unlock()
	if rolledEarly != 0 {
		t.Fatalf("a worker was replaced while its lease was active: %v", f.rolled)
	}
	if !f.paused("w1") {
		t.Fatal("w1 must stop receiving new assignments while draining")
	}
	if f.paused("w2") || f.paused("w3") {
		t.Fatal("only the worker being replaced may be drained")
	}
	if err := f.writeBack(task); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultSucceeded {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if strings.Join(f.rolled, ",") != "w1,w2,w3" {
		t.Fatalf("roll order %v", f.rolled)
	}
	if f.rollSnapshot["w1"] != 0 {
		t.Fatalf("w1 was rolled with %d active lease(s)", f.rollSnapshot["w1"])
	}
	if minOthers < 2 {
		t.Fatalf("ready capacity fell to %d; at least 2 of 3 workers must serve at all times", minOthers)
	}
	for _, n := range []string{"w1", "w2", "w3"} {
		if f.paused(n) {
			t.Fatalf("%s left paused", n)
		}
	}
	if f.writeBacks[task.ID] != 1 || f.executions[task.ID] != 1 {
		t.Fatal("task replayed or duplicated")
	}
	if f.startCount != 0 {
		t.Fatal("worker promotion must not restart the controller")
	}
}

func TestWorkerDrainDeadlinePostponesWithoutCancelling(t *testing.T) {
	f := newFixture(t)
	_, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	f.enqueue(11)
	task := f.claim("w1")
	e := f.engine(upgrade.Options{WorkerTimeout: 150 * time.Millisecond})
	rec := f.workerRecord(e, target, "w1", "w2")
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultPostponed {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if len(f.rolled) != 0 {
		t.Fatalf("nothing may be replaced after a drain timeout: %v", f.rolled)
	}
	got := f.task(task.ID)
	if got.State != "leased" || got.Owner != "w1" || got.Fence != task.FencingToken || got.Attempt != task.Attempt {
		t.Fatalf("the running task was disturbed: %+v", got)
	}
	if f.paused("w1") {
		t.Fatal("w1 must take work again after a postponement")
	}
	if rec.Workers[0].Status != upgrade.WorkerPostponed || rec.Workers[1].Status != upgrade.WorkerPending {
		t.Fatalf("worker statuses %+v", rec.Workers)
	}
}

func TestWorkerPromotionKeepsMinimumCapacity(t *testing.T) {
	f := newFixture(t)
	_, target := f.artifacts()
	f.register("solo", 1)
	e := f.engine(upgrade.Options{MinCapacity: 1})
	rec := f.workerRecord(e, target, "solo")
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultPostponed || len(f.rolled) != 0 || f.paused("solo") {
		t.Fatalf("result %q rolled=%v", rec.Result, f.rolled)
	}
	if !strings.Contains(rec.ResultNote, "below the required 1") {
		t.Fatalf("note %q", rec.ResultNote)
	}
}

func TestWorkerThatNeverReturnsStaysPausedAndOthersUntouched(t *testing.T) {
	f := newFixture(t)
	_, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	f.noRollTouch["w1"] = true
	e := f.engine(upgrade.Options{VerifyTimeout: 200 * time.Millisecond})
	rec := f.workerRecord(e, target, "w1", "w2")
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultFailed {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if !f.paused("w1") {
		t.Fatal("a worker whose new generation is not ready must not receive work")
	}
	if len(f.rolled) != 1 || f.paused("w2") {
		t.Fatalf("remaining workers must be untouched: rolled=%v", f.rolled)
	}
	if !strings.Contains(rec.Recovery, "dashboard resume w1") {
		t.Fatalf("recovery %q", rec.Recovery)
	}
}

func TestOperatorPausedWorkerStaysPaused(t *testing.T) {
	f := newFixture(t)
	_, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	if err := f.tasks.SetWorkerPaused(f.ctx, "w1", true); err != nil {
		t.Fatal(err)
	}
	e := f.engine(upgrade.Options{})
	rec := f.workerRecord(e, target, "w1", "w2")
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultSucceeded || !f.paused("w1") || f.paused("w2") {
		t.Fatalf("result %q; an operator pause must survive the upgrade", rec.Result)
	}
}

func TestWorkerCrashResumeDoesNotRepeatFinishedWorkers(t *testing.T) {
	f := newFixture(t)
	_, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	fired := false
	e := f.engine(upgrade.Options{Hook: func(s, p string) error {
		if !fired && s == "worker:w2:roll" && p == "begin" {
			fired = true
			return upgrade.ErrInterrupted
		}
		return nil
	}})
	rec := f.workerRecord(e, target, "w1", "w2")
	if err := e.Run(f.ctx, rec); !errors.Is(err, upgrade.ErrInterrupted) {
		t.Fatalf("expected crash, got %v", err)
	}
	if !f.paused("w2") {
		t.Fatal("w2 should still be drained at the crash")
	}
	got, err := f.engine(upgrade.Options{}).Resume(f.ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != upgrade.ResultSucceeded {
		t.Fatalf("result %q: %s", got.Result, got.ResultNote)
	}
	if strings.Join(f.rolled, ",") != "w1,w2" {
		t.Fatalf("each worker must be rolled exactly once, got %v", f.rolled)
	}
	if f.paused("w1") || f.paused("w2") {
		t.Fatal("workers left paused")
	}
}

func TestWorkerPlanRejectsUnknownWorker(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	e := f.engine(upgrade.Options{})
	steps := []upgrade.WorkerStep{{Name: "ghost", Descriptor: "ghost"}}
	if _, err := e.Plan(f.ctx, upgrade.KindWorkers, target, prior, gates(), steps); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("got %v", err)
	}
}
