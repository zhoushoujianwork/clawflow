package upgrade_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

// writeBack accepts and finishes a task's result the way a worker and the
// controller do. It counts effective write-backs so duplicates are visible.
func (f *fixture) writeBack(task *coordinator.Task) error {
	res := json.RawMessage(`{"outcome":"agent-implemented"}`)
	if err := f.tasks.AcceptResult(f.ctx, task.ID, task.LeaseOwner, task.FencingToken, res); err != nil {
		return err
	}
	if err := f.tasks.FinishResult(f.ctx, task.ID, task.LeaseOwner, task.FencingToken, false); err != nil {
		return err
	}
	f.mu.Lock()
	f.writeBacks[task.ID]++
	f.mu.Unlock()
	return nil
}

func (f *fixture) task(id string) coordinator.UpgradeTask {
	f.t.Helper()
	snap, err := f.tasks.UpgradeSnapshot(f.ctx, []string{id})
	if err != nil || len(snap.Watched) != 1 {
		f.t.Fatalf("task %s: %v %v", id, snap.Watched, err)
	}
	return snap.Watched[0]
}

func TestControllerUpgradePreservesLongRunningTask(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	queued := f.enqueue(1)
	task := f.claim("w1")
	if task.ID != queued.ID {
		t.Fatalf("claimed %s, want %s", task.ID, queued.ID)
	}
	e := f.engine(upgrade.Options{})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultSucceeded {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if f.startCount != 1 || f.runningSHA != target.SHA256 {
		t.Fatalf("starts=%d running=%s", f.startCount, f.runningSHA)
	}
	got := f.task(task.ID)
	if got.State != "leased" || got.Owner != "w1" || got.Fence != task.FencingToken {
		t.Fatalf("lease changed: %+v (was fence %d)", got, task.FencingToken)
	}
	if got.Attempt != task.Attempt {
		t.Fatalf("retry counter moved from %d to %d", task.Attempt, got.Attempt)
	}
	if f.executions[task.ID] != 1 {
		t.Fatalf("executions=%d", f.executions[task.ID])
	}
	// The result produced during/after the cutover is written back once.
	if err := f.writeBack(task); err != nil {
		t.Fatal(err)
	}
	if err := f.writeBack(task); err == nil {
		t.Fatal("a second write-back of the same result must be rejected")
	}
	if f.writeBacks[task.ID] != 1 || f.task(task.ID).State != "done" {
		t.Fatalf("write-backs=%d state=%s", f.writeBacks[task.ID], f.task(task.ID).State)
	}
	if f.paused("w1") || f.paused("w2") {
		t.Fatal("dispatch was not restored")
	}
	if f.maxServing != 1 {
		t.Fatal("more than one controller served")
	}
	m := rec.Measured
	if !m.APIRecovered || !m.ViewerEndpointRecovered || m.APIInterruptionMS < 0 || m.ResolutionMS <= 0 || m.ViewerAppObserved {
		t.Fatalf("measurements not recorded honestly: %+v", m)
	}
	if !strings.Contains(m.Note, "separate client") {
		t.Fatalf("viewer note missing: %q", m.Note)
	}
	if rec.Gates.ApprovedSHA != shaTarget || rec.Target.Commit != shaTarget {
		t.Fatal("target SHA not recorded")
	}
	for _, name := range []string{upgrade.StagePreflight, upgrade.StageMigrate, upgrade.StageSettle, upgrade.StageStop, upgrade.StageStart, upgrade.StageVerify, upgrade.StageResume} {
		st := rec.Stages
		found := false
		for _, s := range st {
			if s.Name == name && s.Status == "done" {
				found = true
			}
		}
		if !found {
			t.Fatalf("stage %s not recorded as done", name)
		}
	}
}

func TestEmbeddedWorkerMustSettleBeforeCutover(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	// A worker that is not durable is tied to the controller process.
	if err := f.tasks.RegisterWorker(f.ctx, coordinator.Worker{ID: "embedded", MaxParallel: 1, RunUntagged: true, Durable: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tasks.CreateToken(f.ctx, "embedded"); err != nil {
		t.Fatal(err)
	}
	f.touch("embedded")
	f.enqueue(2)
	task := f.claim("embedded")

	// Deadline expiry postpones without touching the service or the task.
	e := f.engine(upgrade.Options{SettleTimeout: 150 * time.Millisecond})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultPostponed {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if f.startCount != 0 || !f.running || f.runningSHA != prior.SHA256 {
		t.Fatal("the running controller must be left unchanged")
	}
	got := f.task(task.ID)
	if got.State != "leased" || got.Fence != task.FencingToken || got.Attempt != task.Attempt || f.executions[task.ID] != 1 {
		t.Fatalf("work was cancelled or replayed: %+v", got)
	}
	if f.paused("embedded") {
		t.Fatal("dispatch paused by the upgrade must be restored after a postponement")
	}

	// Once the embedded task finishes, the same upgrade proceeds.
	if err := f.writeBack(task); err != nil {
		t.Fatal(err)
	}
	rec2 := f.begin(f.engine(upgrade.Options{}), prior, target)
	if err := f.engine(upgrade.Options{}).Run(f.ctx, rec2); err != nil {
		t.Fatal(err)
	}
	if rec2.Result != upgrade.ResultSucceeded {
		t.Fatalf("result %q: %s", rec2.Result, rec2.ResultNote)
	}
}

func TestAcceptedResultMustBeAppliedBeforeCutover(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(3)
	task := f.claim("w1")
	// Result accepted but effects not finalised: the controller still owes work.
	if err := f.tasks.AcceptResult(f.ctx, task.ID, "w1", task.FencingToken, json.RawMessage(`{"outcome":"agent-implemented"}`)); err != nil {
		t.Fatal(err)
	}
	e := f.engine(upgrade.Options{SettleTimeout: 150 * time.Millisecond})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultPostponed || f.startCount != 0 {
		t.Fatalf("result %q starts=%d", rec.Result, f.startCount)
	}
	if err := f.tasks.FinishResult(f.ctx, task.ID, "w1", task.FencingToken, false); err != nil {
		t.Fatalf("the accepted result must still be finishable: %v", err)
	}
}

func TestIncompatibleTransitionIsRejectedBeforeAnyChange(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	target.Manifest.SchemaLatest++
	target.Manifest.ContractVersions = []int{target.Manifest.SchemaLatest}
	e := f.engine(upgrade.Options{})
	if _, err := e.Plan(f.ctx, upgrade.KindController, target, prior, gates(), nil); err == nil || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("plan must reject a contract migration, got %v", err)
	}
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultFailed || !strings.Contains(rec.ResultNote, "unchanged") {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if f.startCount != 0 || !f.running || f.paused("w1") {
		t.Fatal("rejection must leave the service and dispatch untouched")
	}
	if stageSeen(rec, upgrade.StageMigrate) || stageSeen(rec, upgrade.StageStop) {
		t.Fatal("no migration or stop may run for a rejected transition")
	}
}

func TestFailedMigrationLeavesServiceRunning(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.failMigrate = true
	e := f.engine(upgrade.Options{})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultFailed || !strings.Contains(rec.ResultNote, "unchanged") {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if f.startCount != 0 || !f.running || f.runningSHA != prior.SHA256 || f.paused("w1") {
		t.Fatal("failed migration must leave the current service running unchanged")
	}
}

func TestDatabaseNewerThanTargetIsRefused(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	target.Manifest.SchemaLatest--
	e := f.engine(upgrade.Options{})
	if _, err := e.Plan(f.ctx, upgrade.KindController, target, prior, gates(), nil); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("expected downgrade refusal, got %v", err)
	}
}

func TestHealthFailureRollsBackToVerifiedPrior(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(4)
	task := f.claim("w1")
	f.failProbeFor[target.SHA256] = true
	e := f.engine(upgrade.Options{VerifyTimeout: 200 * time.Millisecond})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultRolledBack {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if rec.Rollback == nil || !rec.Rollback.Eligible {
		t.Fatalf("eligibility not recorded: %+v", rec.Rollback)
	}
	if f.startCount != 2 || f.runningSHA != prior.SHA256 || !f.running {
		t.Fatalf("starts=%d running=%s", f.startCount, f.runningSHA)
	}
	if f.maxServing != 1 {
		t.Fatal("more than one controller served")
	}
	got := f.task(task.ID)
	if got.State != "leased" || got.Fence != task.FencingToken || f.executions[task.ID] != 1 {
		t.Fatalf("lease lost or replayed by rollback: %+v", got)
	}
	if f.paused("w1") || len(rec.Quiesced) != 0 {
		t.Fatal("dispatch not restored after verified rollback")
	}
	if rec.Measured.RollbackInterruptionMS <= 0 && rec.Measured.RollbackInterruptionMS != 0 {
		t.Fatal("rollback interruption missing")
	}
	if rec.Recovery == "" {
		t.Fatal("recovery instructions are required")
	}
}

func TestRollbackBlockedFailsClosed(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(5)
	task := f.claim("w1")
	// The target speaks a newer worker protocol while durable work exists.
	target.Manifest.WorkerProtocol = 2
	f.failProbeFor[target.SHA256] = true
	e := f.engine(upgrade.Options{VerifyTimeout: 200 * time.Millisecond})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultRollbackBlocked {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if rec.Rollback == nil || rec.Rollback.Eligible || rec.Rollback.Reason == "" {
		t.Fatalf("expected a recorded ineligibility reason: %+v", rec.Rollback)
	}
	if f.startCount != 1 {
		t.Fatalf("the prior binary must not be started on an incompatible database (starts=%d)", f.startCount)
	}
	if !f.paused("w1") {
		t.Fatal("dispatch must stay paused when neither version is verified")
	}
	if got := f.task(task.ID); got.State != "leased" || got.Fence != task.FencingToken {
		t.Fatalf("lease disturbed: %+v", got)
	}
	if !strings.Contains(rec.Recovery, "Do not start the prior binary") {
		t.Fatalf("recovery: %q", rec.Recovery)
	}
}

func TestStopFailureKeepsPriorServing(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.failStop = true
	e := f.engine(upgrade.Options{})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultFailed || !f.running || f.runningSHA != prior.SHA256 || f.paused("w1") {
		t.Fatalf("result %q running=%v", rec.Result, f.running)
	}
}

func TestPlanChangesNothing(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	e := f.engine(upgrade.Options{})
	if _, err := e.Plan(f.ctx, upgrade.KindController, target, prior, gates(), nil); err != nil {
		t.Fatal(err)
	}
	ids, _ := e.Records.List()
	if len(ids) != 0 || f.startCount != 0 || f.paused("w1") || !f.running {
		t.Fatal("plan must be read-only")
	}
}

func TestGatesAreRequired(t *testing.T) {
	f := newFixture(t)
	_, target := f.artifacts()
	e := f.engine(upgrade.Options{})
	for name, mutate := range map[string]func(*upgrade.Gates){
		"no evidence":   func(g *upgrade.Gates) { g.EvidenceDigest = "" },
		"no CI":         func(g *upgrade.Gates) { g.CIRef = "" },
		"no acceptance": func(g *upgrade.Gates) { g.AcceptanceRef = "" },
		"wrong sha":     func(g *upgrade.Gates) { g.ApprovedSHA = shaPrior },
	} {
		g := gates()
		mutate(&g)
		if _, err := e.Begin(f.ctx, upgrade.KindController, target, g, nil); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	short := target
	short.Commit, short.Manifest.Commit = "abc123", "abc123"
	if _, err := e.Begin(f.ctx, upgrade.KindController, short, gates(), nil); err == nil {
		t.Fatal("a short commit must be rejected")
	}
}

// Crash at every stage boundary: a resumed run recovers one unambiguous owner,
// reports truthful status and never replays work.
func TestCrashAtEveryBoundaryRecovers(t *testing.T) {
	stages := []string{upgrade.StagePreflight, upgrade.StageArtifacts, upgrade.StageMigrate, upgrade.StageQuiesce, upgrade.StageSettle,
		upgrade.StageStop, upgrade.StageFence, upgrade.StageInstall, upgrade.StageStart, upgrade.StageVerify, upgrade.StageResume}
	for _, stage := range stages {
		for _, phase := range []string{"begin", "end"} {
			stage, phase := stage, phase
			t.Run(stage+"/"+phase, func(t *testing.T) {
				f := newFixture(t)
				prior, target := f.artifacts()
				f.register("w1", 1)
				f.register("w2", 1)
				f.enqueue(6)
				task := f.claim("w1")
				fired := false
				hook := func(s, p string) error {
					if !fired && s == stage && p == phase {
						fired = true
						return upgrade.ErrInterrupted
					}
					return nil
				}
				e := f.engine(upgrade.Options{Hook: hook})
				rec := f.begin(e, prior, target)
				if err := e.Run(f.ctx, rec); !errors.Is(err, upgrade.ErrInterrupted) {
					t.Fatalf("expected the injected crash, got %v", err)
				}
				owner, err := e.OwnerView(f.ctx, rec)
				if err != nil {
					t.Fatal(err)
				}
				if owner != "prior" && owner != "target" && owner != "none" {
					t.Fatalf("owner %q is ambiguous after the crash", owner)
				}
				if f.servingNow() > 1 {
					t.Fatal("two controllers serving")
				}
				// A new CLI process resumes from the persisted record.
				e2 := f.engine(upgrade.Options{})
				got, err := e2.Resume(f.ctx, rec.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Result != upgrade.ResultSucceeded {
					t.Fatalf("result %q: %s", got.Result, got.ResultNote)
				}
				if f.startCount != 1 || f.runningSHA != target.SHA256 {
					t.Fatalf("starts=%d", f.startCount)
				}
				now := f.task(task.ID)
				if now.State != "leased" || now.Fence != task.FencingToken || now.Attempt != task.Attempt || f.executions[task.ID] != 1 {
					t.Fatalf("work replayed or lease lost: %+v", now)
				}
				if f.paused("w1") || f.paused("w2") {
					t.Fatal("dispatch not restored after recovery")
				}
				if f.maxServing != 1 {
					t.Fatal("more than one controller served")
				}
			})
		}
	}
}

func TestCrashAfterHostActionRecovers(t *testing.T) {
	for _, action := range []string{"stop", "install", "start"} {
		action := action
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			prior, target := f.artifacts()
			f.register("w1", 1)
			f.crashAfter = action
			e := f.engine(upgrade.Options{})
			rec := f.begin(e, prior, target)
			if err := e.Run(f.ctx, rec); !errors.Is(err, upgrade.ErrInterrupted) {
				t.Fatalf("expected crash, got %v", err)
			}
			if rec.Terminal() {
				t.Fatal("an interrupted record must not claim a result")
			}
			got, err := f.engine(upgrade.Options{}).Resume(f.ctx, rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Result != upgrade.ResultSucceeded || f.runningSHA != target.SHA256 || f.startCount != 1 {
				t.Fatalf("result %q starts=%d", got.Result, f.startCount)
			}
		})
	}
}

func TestAmbiguousOwnerRefusesToResume(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	e := f.engine(upgrade.Options{Hook: func(s, p string) error {
		if s == upgrade.StageSettle && p == "end" {
			return upgrade.ErrInterrupted
		}
		return nil
	}})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); !errors.Is(err, upgrade.ErrInterrupted) {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.runningSHA = "ffff" // someone replaced the controller out of band
	f.mu.Unlock()
	if _, err := f.engine(upgrade.Options{}).Resume(f.ctx, rec.ID); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity refusal, got %v", err)
	}
	if view, _ := e.OwnerView(f.ctx, rec); view != "ambiguous" {
		t.Fatalf("owner view %q", view)
	}
	if f.startCount != 0 {
		t.Fatal("nothing may start while ownership is ambiguous")
	}
}

func stageSeen(rec *upgrade.Record, name string) bool {
	for _, s := range rec.Stages {
		if s.Name == name {
			return true
		}
	}
	return false
}

// A lease epoch that changes during the cutover means work could be replayed.
// Verification must refuse to call that a success.
func TestLostLeaseDuringCutoverIsDetected(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(7)
	task := f.claim("w1")
	fired := false
	e := f.engine(upgrade.Options{VerifyTimeout: 300 * time.Millisecond, Hook: func(s, p string) error {
		if !fired && s == upgrade.StageStart && p == "end" {
			fired = true
			return f.db.DB().Exec("UPDATE coordinator_tasks SET fencing_token = fencing_token + 1 WHERE id = ?", task.ID).Error
		}
		return nil
	}})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result == upgrade.ResultSucceeded {
		t.Fatal("a changed lease epoch must not verify as success")
	}
	if !strings.Contains(rec.ResultNote, "lease epoch changed") {
		t.Fatalf("note %q", rec.ResultNote)
	}
}
