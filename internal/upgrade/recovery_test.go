package upgrade_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

// expire ends a durable lease's authority the way a worker outage does, so the
// task is waiting for RecoverExecution.
func (f *fixture) expire(task *coordinator.Task) {
	f.t.Helper()
	if err := f.db.DB().Exec("UPDATE coordinator_tasks SET lease_expires_at = ? WHERE id = ?", time.Now().UTC().Add(-time.Minute), task.ID).Error; err != nil {
		f.t.Fatal(err)
	}
}

// recoverOnStart resumes the task once, right after the new controller starts,
// as a worker reconnecting to it does.
func (f *fixture) recoverOnStart(task *coordinator.Task, opt upgrade.Options) upgrade.Options {
	fired := false
	opt.Hook = func(s, p string) error {
		if !fired && s == upgrade.StageStart && p == "end" {
			fired = true
			_, err := f.tasks.RecoverExecution(f.ctx, task.ID, task.LeaseOwner, task.FencingToken)
			return err
		}
		return nil
	}
	return opt
}

// A lease that had already expired is waiting for its worker to resume it.
// The resume advances the epoch and the attempt together; that is expected.
func TestExpiredLeaseRecoveredAfterCutoverIsAccepted(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(7)
	task := f.claim("w1")
	f.expire(task)

	e := f.engine(f.recoverOnStart(task, upgrade.Options{}))
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultSucceeded {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	got := f.task(task.ID)
	if got.State != "leased" || got.Owner != "w1" || got.Fence != task.FencingToken+1 || got.Attempt != task.Attempt+1 {
		t.Fatalf("recovery did not happen as modeled: %+v (was fence %d attempt %d)", got, task.FencingToken, task.Attempt)
	}
	if len(rec.LeasesBefore) != 1 || !rec.LeasesBefore[0].AwaitingRecovery {
		t.Fatalf("pre-upgrade state not recorded: %+v", rec.LeasesBefore)
	}
	settle := ""
	for _, s := range rec.Stages {
		if s.Name == upgrade.StageSettle {
			settle = s.Detail
		}
	}
	if !strings.Contains(settle, "await recovery") {
		t.Fatalf("settle detail does not mention the expected recovery: %q", settle)
	}
}

// A lease that was still valid before the upgrade must not change, even if the
// change looks like a recovery.
func TestValidLeaseChangeFailsEvenWhenItLooksLikeRecovery(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(7)
	task := f.claim("w1")
	fired := false
	e := f.engine(upgrade.Options{VerifyTimeout: 300 * time.Millisecond, Hook: func(s, p string) error {
		if !fired && s == upgrade.StageStart && p == "end" {
			fired = true
			return f.db.DB().Exec("UPDATE coordinator_tasks SET fencing_token = fencing_token + 1, attempt = attempt + 1 WHERE id = ?", task.ID).Error
		}
		return nil
	}})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result == upgrade.ResultSucceeded || !strings.Contains(rec.ResultNote, "lease epoch changed") {
		t.Fatalf("a valid lease changing must fail: %q %q", rec.Result, rec.ResultNote)
	}
	if len(rec.LeasesBefore) != 1 || rec.LeasesBefore[0].AwaitingRecovery {
		t.Fatalf("a valid lease was recorded as awaiting recovery: %+v", rec.LeasesBefore)
	}
}

// An expired lease excuses only a real resume. An epoch bump with no matching
// attempt change, or with moved application counters, is still a failure.
func TestExpiredLeaseChangeThatIsNotARecoveryFails(t *testing.T) {
	for name, update := range map[string]string{
		"epoch only":           "fencing_token = fencing_token + 1",
		"attempt lags epoch":   "fencing_token = fencing_token + 2, attempt = attempt + 1",
		"application attempts": "fencing_token = fencing_token + 1, attempt = attempt + 1, application_attempts = application_attempts + 1",
		"epoch goes backwards": "fencing_token = fencing_token - 1, attempt = attempt + 1",
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			prior, target := f.artifacts()
			f.register("w1", 1)
			f.enqueue(7)
			task := f.claim("w1")
			f.expire(task)
			fired := false
			e := f.engine(upgrade.Options{VerifyTimeout: 300 * time.Millisecond, Hook: func(s, p string) error {
				if !fired && s == upgrade.StageStart && p == "end" {
					fired = true
					return f.db.DB().Exec("UPDATE coordinator_tasks SET "+update+" WHERE id = ?", task.ID).Error
				}
				return nil
			}})
			rec := f.begin(e, prior, target)
			if err := e.Run(f.ctx, rec); err != nil {
				t.Fatal(err)
			}
			if rec.Result == upgrade.ResultSucceeded {
				t.Fatalf("%s must not verify as success", name)
			}
		})
	}
}

// Two valid leases and one expired: recovering the expired one is fine, and
// the verdict for the others is unchanged.
func TestRecoveryOfExpiredLeaseDoesNotExcuseAValidOne(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	f.enqueue(1)
	f.enqueue(2)
	a := f.claim("w1")
	b := f.claim("w2")
	f.expire(a)
	fired := false
	e := f.engine(upgrade.Options{VerifyTimeout: 300 * time.Millisecond, Hook: func(s, p string) error {
		if !fired && s == upgrade.StageStart && p == "end" {
			fired = true
			if _, err := f.tasks.RecoverExecution(f.ctx, a.ID, a.LeaseOwner, a.FencingToken); err != nil {
				return err
			}
			// Task IDs are redacted in records, so give the valid lease an epoch
			// the recovered one cannot reach and look for it in the note.
			return f.db.DB().Exec("UPDATE coordinator_tasks SET fencing_token = fencing_token + 5, attempt = attempt + 5 WHERE id = ?", b.ID).Error
		}
		return nil
	}})
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("lease epoch changed from %d to %d", b.FencingToken, b.FencingToken+5)
	if rec.Result == upgrade.ResultSucceeded || !strings.Contains(rec.ResultNote, want) {
		t.Fatalf("the valid lease's change must be what fails (%q): %q %q", want, rec.Result, rec.ResultNote)
	}
	if got := f.task(a.ID); got.Fence != a.FencingToken+1 {
		t.Fatalf("the expired lease was not recovered: %+v", got)
	}
}

// The failure that motivated this: a legitimate recovery happened, the target
// then failed its health check, and rollback verification used to fail on the
// same epoch change. The rollback must verify and restore the prior controller.
func TestLegitimateRecoveryDoesNotFailRollbackVerification(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(7)
	task := f.claim("w1")
	f.expire(task)
	f.failProbeFor[target.SHA256] = true

	e := f.engine(f.recoverOnStart(task, upgrade.Options{VerifyTimeout: 200 * time.Millisecond}))
	rec := f.begin(e, prior, target)
	if err := e.Run(f.ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Result != upgrade.ResultRolledBack {
		t.Fatalf("result %q: %s", rec.Result, rec.ResultNote)
	}
	if strings.Contains(rec.ResultNote, "rollback also failed") {
		t.Fatalf("rollback verification failed on a legitimate recovery: %s", rec.ResultNote)
	}
	if f.runningSHA != prior.SHA256 || !f.running {
		t.Fatalf("prior controller not restored: running=%v sha=%s", f.running, f.runningSHA)
	}
	if got := f.task(task.ID); got.Fence != task.FencingToken+1 {
		t.Fatalf("recovery did not happen: %+v", got)
	}
	if f.paused("w1") || len(rec.Quiesced) != 0 {
		t.Fatal("dispatch not restored after verified rollback")
	}
}

// Rollback verification keeps its guard: a valid lease that changed is still a
// failure there, because the same check runs.
func TestRollbackStillFailsWhenAValidLeaseChanged(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.enqueue(7)
	task := f.claim("w1")
	f.failProbeFor[target.SHA256] = true
	fired := false
	e := f.engine(upgrade.Options{VerifyTimeout: 200 * time.Millisecond, Hook: func(s, p string) error {
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
	if rec.Result != upgrade.ResultFailed || !strings.Contains(rec.ResultNote, "rollback also failed at "+upgrade.StageRbVerify) {
		t.Fatalf("a changed valid lease must still fail rollback verification: %q %q", rec.Result, rec.ResultNote)
	}
}

// Plan names the tasks that are waiting for recovery and says they resume
// after the upgrade. It reports nothing when there are none, and it changes
// nothing either way.
func TestPlanListsTasksAwaitingRecovery(t *testing.T) {
	f := newFixture(t)
	prior, target := f.artifacts()
	f.register("w1", 1)
	f.register("w2", 1)
	f.enqueue(7)
	f.enqueue(8)
	waiting := f.claim("w1")
	f.claim("w2")
	e := f.engine(upgrade.Options{})

	detail, err := e.Plan(f.ctx, upgrade.KindController, target, prior, gates(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, "waiting for recovery") {
		t.Fatalf("nothing is waiting, but plan says so: %q", detail)
	}

	f.expire(waiting)
	detail, err = e.Plan(f.ctx, upgrade.KindController, target, prior, gates(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 task(s) are waiting for recovery", "owner/repo#7 implement", "worker w1", "resume", "still fails if any lease that is valid now changes"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("plan output lacks %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "owner/repo#8") {
		t.Fatalf("a still-valid lease was listed as waiting:\n%s", detail)
	}
	if got := f.task(waiting.ID); got.Fence != waiting.FencingToken || got.Attempt != waiting.Attempt {
		t.Fatalf("plan must not recover anything: %+v", got)
	}
	ids, _ := e.Records.List()
	if len(ids) != 0 || f.startCount != 0 || f.paused("w1") || !f.running {
		t.Fatal("plan must be read-only")
	}
}
