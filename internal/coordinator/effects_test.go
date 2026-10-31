package coordinator

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func effectReport(kind string) json.RawMessage {
	return json.RawMessage(`{"status":"success","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","effects":[{"kind":"` + kind + `","repo":"owner/repo","number":1,"body":"hello","head":"fix/issue-1","base":"main","title":"fix"}]}`)
}
func TestEffectCrashReplay(t *testing.T) {
	for _, kind := range []string{"issue-comment", "pr-create"} {
		for _, afterWrite := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/before", true: "/after"}[afterWrite], func(t *testing.T) {
				s, db, _ := setup(t)
				register(t, s, "w", 1)
				enqueue(t, s, 1)
				task := claim(t, s, "w")
				if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, effectReport(kind)); err != nil {
					t.Fatal(err)
				}
				remote := map[string]bool{}
				writes := 0
				apply := func(e EffectIntent) (EffectResult, error) {
					if !remote[e.IdempotencyKey] {
						remote[e.IdempotencyKey] = true
						writes++
					}
					return EffectResult{Reference: "remote-1"}, nil
				}
				func() {
					defer func() {
						if recover() == nil {
							t.Error("crash fixture did not panic")
						}
					}()
					_ = s.ApplyEffects(ctx, task.ID, task.FencingToken, func(e EffectIntent) (EffectResult, error) {
						if afterWrite {
							_, _ = apply(e)
						}
						panic("process crash before local commit")
					})
				}()
				var intent EffectIntent
				if err := db.DB().First(&intent, "task_id = ?", task.ID).Error; err != nil {
					t.Fatal(err)
				}
				if intent.Status != "pending" {
					t.Fatalf("crash did not roll back: %s", intent.Status)
				}
				restarted := New(db.DB())
				for i := 0; i < 2; i++ {
					if err := restarted.ApplyEffects(ctx, task.ID, task.FencingToken, apply); err != nil {
						t.Fatal(err)
					}
				}
				if writes != 1 {
					t.Fatalf("logical writes=%d", writes)
				}
				if err := restarted.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error { return nil }); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestEffectUncertainAndCancellation(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, effectReport("pr-create")); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEffects(ctx, task.ID, task.FencingToken, func(EffectIntent) (EffectResult, error) { return EffectResult{}, errors.New("response lost") }); err == nil {
		t.Fatal("missing remote error")
	}
	var intent EffectIntent
	if err := db.DB().First(&intent, "task_id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if intent.Status != "uncertain" {
		t.Fatal(intent.Status)
	}
	if err := s.Cancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.ApplyEffects(ctx, task.ID, task.FencingToken, func(EffectIntent) (EffectResult, error) { called = true; return EffectResult{}, nil }); !errors.Is(err, ErrStaleLease) || called {
		t.Fatalf("cancelled effect executed: %v", err)
	}
	if err := db.DB().First(&intent, "id = ?", intent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if intent.Status != "failed" {
		t.Fatal(intent.Status)
	}
	current, _ := s.Task(ctx, task.ID)
	if current.FencingToken <= task.FencingToken {
		t.Fatal("cancellation did not revoke epoch")
	}
}
func TestExpiredPRIntentRejected(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, effectReport("pr-create")); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	if _, err := s.ExpireLeases(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	next := claim(t, s, "w")
	if err := s.AcceptResult(ctx, next.ID, "w", task.FencingToken, effectReport("pr-create")); !errors.Is(err, ErrStaleLease) {
		t.Fatal(err)
	}
	var count int64
	if err := db.DB().Model(&EffectIntent{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expired PR intents=%d error=%v", count, err)
	}
}
func TestInboxDuplicateKeepsGeneration(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	input := Task{Repo: "owner/repo", Issue: 1, Kind: "implement", Operator: "implement", EventSource: "github", ExternalID: "event-1"}
	first, err := s.Enqueue(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	task := claim(t, s, "w")
	if err := s.Complete(ctx, task.ID, "w", task.FencingToken, nil); err != nil {
		t.Fatal(err)
	}
	again, err := s.EnqueuePrepared(ctx, input, func(*Task) error { t.Fatal("duplicate prepared new work"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID {
		t.Fatal("duplicate created a new generation")
	}
	input.ExternalID = "event-2"
	next, err := s.Enqueue(ctx, input)
	if err != nil || next.ID == first.ID {
		t.Fatalf("new event: %v %v", next, err)
	}
	var count int64
	if err := db.DB().Model(&InboxEvent{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("events=%d error=%v", count, err)
	}
}
func TestInfrastructureAndProductAttempts(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	for i := 0; i < 2; i++ {
		task := claim(t, s, "w")
		raw := json.RawMessage(`{"status":"failed","infra_kind":"launch"}`)
		if i == 1 {
			raw = json.RawMessage(`{"status":"failed"}`)
		}
		if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
			t.Fatal(err)
		}
		if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(Task) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	var attempts []TaskAttempt
	if err := db.DB().Order("attempt_number").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[0].Kind != "infra" || attempts[1].Kind != "product" {
		t.Fatalf("attempt accounting: %+v", attempts)
	}
	if attempts[0].EndedAt == nil || attempts[1].EndedAt == nil {
		t.Fatal("attempt completion times missing")
	}
}
func TestInfrastructureExpiryBound(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	for i := 0; i < MaxInfrastructureAttempts; i++ {
		task := claim(t, s, "w")
		if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := s.ExpireLeases(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Claim(ctx, "w"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("retry not bounded: %v", err)
	}
}
func TestHeadMismatchStopsLaterEffects(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	raw := json.RawMessage(`{"status":"success","head_sha":"a","effects":[{"kind":"add-label","repo":"owner/repo","number":1},{"kind":"pr-create","repo":"owner/repo","head":"fix","base":"main","title":"fix"}]}`)
	if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := s.ApplyEffects(ctx, task.ID, task.FencingToken, func(e EffectIntent) (EffectResult, error) {
		calls++
		if e.Kind != "pr-create" {
			t.Fatal("label before head verification")
		}
		return EffectResult{PR: 10, Reference: "10", HeadMismatch: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Task(ctx, task.ID)
	if calls != 1 || current.State != "head-mismatch" || current.PR != 10 {
		t.Fatalf("task=%+v calls=%d", current, calls)
	}
	blocked, err := s.PRBlocked(ctx, task.Repo, 10)
	if err != nil || !blocked {
		t.Fatalf("PR not gated: %v", err)
	}
	var applied int64
	if err := db.DB().Model(&EffectIntent{}).Where("status = 'applied'").Count(&applied).Error; err != nil || applied != 1 {
		t.Fatalf("applied=%d %v", applied, err)
	}
}

func TestPRIntentRechecksHeadOnInfrastructureRetry(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	var firstID string
	for i := 0; i < 2; i++ {
		task := claim(t, s, "w")
		report := effectReport("pr-create")
		if i == 0 {
			var raw map[string]any
			_ = json.Unmarshal(report, &raw)
			raw["infra_kind"] = "transport"
			report, _ = json.Marshal(raw)
		}
		if err := s.AcceptResult(ctx, task.ID, "w", task.FencingToken, report); err != nil {
			t.Fatal(err)
		}
		calls := 0
		if err := s.ApplyEffects(ctx, task.ID, task.FencingToken, func(e EffectIntent) (EffectResult, error) {
			calls++
			if e.LeaseEpoch != task.FencingToken {
				t.Fatal("old epoch authorized")
			}
			if i == 0 {
				firstID = e.ID
			} else if e.ID != firstID {
				t.Fatal("new logical PR identity")
			}
			return EffectResult{PR: 10, Reference: "10"}, nil
		}); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatal("new head was not verified")
		}
		if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	if err := db.DB().Model(&EffectIntent{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("intents=%d %v", n, err)
	}
}

func TestRepairInfrastructureResultReturnsThroughLedgerScheduler(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w", 1)
	task, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, PR: 11, Kind: "repair", Operator: "repair-ci"})
	if err != nil {
		t.Fatal(err)
	}
	leased := claim(t, s, "w")
	if err := s.AcceptResult(ctx, task.ID, "w", leased.FencingToken, json.RawMessage(`{"status":"failed","infra_kind":"launch"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, leased.FencingToken, true, func(Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	current, err := s.Task(ctx, task.ID)
	if err != nil || current.State != "failed" {
		t.Fatalf("repair bypassed ledger scheduler: %+v %v", current, err)
	}
	if _, err := s.Claim(ctx, "w"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("repair immediately reclaimed: %v", err)
	}
}

func TestExpiredRepairReturnsThroughLedgerScheduler(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	task, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, PR: 11, Kind: "repair", Operator: "repair-ci"})
	if err != nil {
		t.Fatal(err)
	}
	leased := claim(t, s, "w")
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExpireLeases(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingResults(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	var result struct {
		InfraKind string `json:"infra_kind"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(pending[0].Result, &result); err != nil || result.InfraKind != "lease-expired" || result.Error == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, leased.FencingToken, true, func(Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "w"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("repair bypassed ledger: %v", err)
	}
}
