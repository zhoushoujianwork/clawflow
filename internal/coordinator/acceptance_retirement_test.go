package coordinator

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRetireClosedAcceptancePreservesAuthority(t *testing.T) {
	for _, mode := range []string{"late", "wrong-owner", "wrong-epoch", "accepted", "other-kind"} {
		t.Run(mode, func(t *testing.T) {
			s, db, _ := setup(t)
			defer db.Close()
			payload, _ := json.Marshal(AcceptanceRouting{DevelopmentWorker: "dev", AcceptanceWorker: "different"})
			queued, err := s.Enqueue(ctx, Task{Repo: "o/r", PR: 1, Kind: "acceptance", Operator: "acceptance", Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RegisterWorker(ctx, Worker{ID: "review", Roles: []string{"acceptance"}, RunUntagged: true, MaxParallel: 1}); err != nil {
				t.Fatal(err)
			}
			task, err := s.Claim(ctx, "review")
			if err != nil {
				t.Fatal(err)
			}
			owner, epoch := "review", task.FencingToken
			switch mode {
			case "wrong-owner":
				owner = "another"
			case "wrong-epoch":
				epoch--
			case "accepted":
				if err := s.AcceptResult(ctx, task.ID, owner, epoch, json.RawMessage(`{"status":"success"}`)); err != nil {
					t.Fatal(err)
				}
			case "other-kind":
				if err := s.db.Model(&Task{}).Where("id = ?", task.ID).Update("kind", "repair").Error; err != nil {
					t.Fatal(err)
				}
			}
			err = s.RetireAcceptance(ctx, queued.ID, owner, epoch, AcceptanceRetiredClosed)
			current, readErr := s.Task(ctx, queued.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "late" {
				if err != nil || current.State != "cancelled" || current.FencingToken != task.FencingToken+1 || current.LeaseExpiresAt != nil {
					t.Fatalf("not retired: %+v %v", current, err)
				}
				if err := s.RetireAcceptance(ctx, queued.ID, owner, epoch, AcceptanceRetiredClosed); !errors.Is(err, ErrStaleLease) {
					t.Fatalf("second retirement changed authority: %v", err)
				}
			} else if !errors.Is(err, ErrStaleLease) || current.State != "leased" || current.FencingToken != task.FencingToken {
				t.Fatalf("unrelated authority changed: %+v %v", current, err)
			}
			if mode == "accepted" && string(current.Result) != `{"status": "success"}` && string(current.Result) != `{"status":"success"}` {
				t.Fatalf("accepted report changed: %s", current.Result)
			}
		})
	}
}

// A stale verdict retires the exact lease in one transaction: cancelled state,
// bumped fence, failed intents, ended attempts and an acceptance-stale audit.
func TestRetireAcceptanceStaleFencesLeaseAndRecordsReason(t *testing.T) {
	s, db, _ := setup(t)
	defer db.Close()
	payload, _ := json.Marshal(AcceptanceRouting{DevelopmentWorker: "dev", AcceptanceWorker: "different"})
	queued, err := s.Enqueue(ctx, Task{Repo: "o/r", PR: 1, Kind: "acceptance", Operator: "acceptance", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(ctx, Worker{ID: "review", Roles: []string{"acceptance"}, RunUntagged: true, MaxParallel: 1}); err != nil {
		t.Fatal(err)
	}
	task, err := s.Claim(ctx, "review")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var open int64
	if err := db.DB().Model(&TaskAttempt{}).Where("task_id = ? AND ended_at IS NULL", task.ID).Count(&open).Error; err != nil || open != 1 {
		t.Fatalf("claim left %d open attempts: %v", open, err)
	}
	if err := db.DB().Create(&EffectIntent{ID: "i1", TaskID: task.ID, LeaseEpoch: task.FencingToken, Kind: "pr-comment", Repo: "o/r", Number: 1, IdempotencyKey: "k", PayloadDigest: "d", Payload: json.RawMessage(`{}`), Status: "pending", UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.RetireAcceptance(ctx, queued.ID, "review", task.FencingToken, "bogus"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown reason accepted: %v", err)
	}
	if err := s.RetireAcceptance(ctx, queued.ID, "review", task.FencingToken, AcceptanceRetiredStale); err != nil {
		t.Fatal(err)
	}
	current, err := s.Task(ctx, queued.ID)
	if err != nil || current.State != "cancelled" || current.FencingToken != task.FencingToken+1 || current.LeaseExpiresAt != nil {
		t.Fatalf("not retired: %+v %v", current, err)
	}
	var intent EffectIntent
	if err := db.DB().First(&intent, "id = ?", "i1").Error; err != nil || intent.Status != "failed" {
		t.Fatalf("intent not failed: %+v %v", intent, err)
	}
	var attempt TaskAttempt
	if err := db.DB().First(&attempt, "task_id = ?", task.ID).Error; err != nil || attempt.Status != "cancelled" || attempt.EndedAt == nil {
		t.Fatalf("attempt not ended: %+v %v", attempt, err)
	}
	var audits []Audit
	if err := db.DB().Where("task = ?", task.ID).Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	var stale, closed int
	for _, a := range audits {
		switch a.Action {
		case "acceptance-stale":
			stale++
			if a.Worker != "review" {
				t.Fatalf("audit worker: %+v", a)
			}
		case "acceptance-closed":
			closed++
		}
	}
	if stale != 1 || closed != 0 {
		t.Fatalf("audit actions: %+v", audits)
	}
	if err := s.RetireAcceptance(ctx, queued.ID, "review", task.FencingToken, AcceptanceRetiredStale); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("second retirement changed authority: %v", err)
	}
}
