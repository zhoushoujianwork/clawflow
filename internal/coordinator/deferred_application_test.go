package coordinator

import (
	"errors"
	"testing"
	"time"
)

// A transient write-back failure of a succeeded task is recorded without
// charging the three-attempt bound, and leaves the result, intents, lease state
// and updated_at (the result_stuck/finalizing clock) untouched.
func TestRecordDeferredApplicationNeverFailsTheTask(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, effectReport("issue-comment")); err != nil {
		t.Fatal(err)
	}
	before, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Age the row so an accidental updated_at bump is visible.
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).UpdateColumn("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 10; i++ {
		if err := s.RecordDeferredApplication(ctx, task.ID, task.FencingToken, "Get https://api.github.com/repos/o/r/pulls: context deadline exceeded"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "leased" || got.LeaseExpiresAt != nil || got.ApplicationAttempts != 0 || len(got.Result) == 0 || got.FencingToken != before.FencingToken {
		t.Fatalf("task changed beyond the diagnostic: %+v", got)
	}
	if got.ApplicationError == "" {
		t.Fatal("diagnostic not recorded")
	}
	if !got.UpdatedAt.Equal(old) {
		t.Fatalf("updated_at moved from %s to %s; the stuck clock would restart on every retry", old, got.UpdatedAt)
	}
	var audits int64
	if err := db.DB().Model(&Audit{}).Where("task = ? AND action = ?", task.ID, "result-apply-deferred").Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("audit rows=%d err=%v; want one per stuck episode", audits, err)
	}
	var pending int64
	if err := db.DB().Model(&EffectIntent{}).Where("task_id = ? AND status = 'pending'", task.ID).Count(&pending).Error; err != nil || pending != 1 {
		t.Fatalf("pending intents=%d err=%v", pending, err)
	}
	// The same task can still be applied afterwards.
	if err := s.ApplyEffects(ctx, task.ID, task.FencingToken, func(EffectIntent) (EffectResult, error) { return EffectResult{Reference: "ok"}, nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	done, _ := s.Task(ctx, task.ID)
	if done.State != "done" || done.ApplicationError != "" {
		t.Fatalf("recovered task: %+v", done)
	}
}

func TestRecordDeferredApplicationRejectsStaleLease(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	// Still running: no accepted result to defer.
	if err := s.RecordDeferredApplication(ctx, task.ID, task.FencingToken, "x"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("running task: %v", err)
	}
	if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, effectReport("issue-comment")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeferredApplication(ctx, task.ID, task.FencingToken+1, "x"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("wrong epoch: %v", err)
	}
}

// The permanent path is unchanged: RecordApplicationFailure still fails the
// task on its third call.
func TestRecordApplicationFailureStillFailsAfterThree(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, effectReport("issue-comment")); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := s.RecordApplicationFailure(ctx, task.ID, task.FencingToken, "HTTP 422"); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Task(ctx, task.ID)
		want := "leased"
		if i == 3 {
			want = "failed"
		}
		if got.State != want || got.ApplicationAttempts != i {
			t.Fatalf("after %d failures: state=%s attempts=%d", i, got.State, got.ApplicationAttempts)
		}
	}
}

// The wait of a deferred result is reported by the result_stuck rule. Retries
// must not reset its clock, and the stored diagnostic must reach the board so
// the dashboard can show why.
func TestDeferredResultIsReportedByWatchdogWithoutResettingItsClock(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, effectReport("issue-comment")); err != nil {
		t.Fatal(err)
	}
	waited := time.Now().UTC().Add(-(finalizingStallAfter + 5*time.Minute)).Truncate(time.Second)
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).UpdateColumn("updated_at", waited).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.RecordDeferredApplication(ctx, task.ID, task.FencingToken, "context deadline exceeded"); err != nil {
			t.Fatal(err)
		}
	}
	board, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *Finding
	for i := range board.Findings {
		if board.Findings[i].TaskID == task.ID {
			found = &board.Findings[i]
		}
	}
	if found == nil || found.Rule != RuleResultStuck || found.AgeSec < int64((finalizingStallAfter+4*time.Minute).Seconds()) {
		t.Fatalf("finding after retries: %+v", board.Findings)
	}
	var row *BoardTask
	for i := range board.Tasks {
		if board.Tasks[i].ID == task.ID {
			row = &board.Tasks[i]
		}
	}
	if row == nil || row.State != "leased" || row.Stage != "finalizing" || row.ApplicationError != "context deadline exceeded" || row.ApplicationAttempts != 0 {
		t.Fatalf("board row: %+v", row)
	}
}
