package coordinator

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestReportedInterruptionRetainsReservationAndRecoversBoundedly(t *testing.T) {
	s, db, _ := setup(t)
	for _, w := range []string{"w", "other"} {
		if err := s.RegisterWorker(ctx, Worker{ID: w, Durable: true, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
			t.Fatal(err)
		}
	}
	input := Task{Repo: "o/r", Issue: 646, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}
	if _, err := s.Enqueue(ctx, input); err != nil {
		t.Fatal(err)
	}
	task := claim(t, s, "w")
	raw := json.RawMessage(`{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"retained-native"}}`)
	for attempt := 0; attempt < MaxInfrastructureAttempts; attempt++ {
		if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
			t.Fatal("recovered before finalization", err)
		}
		if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(Task) error { return nil }); err != nil {
			t.Fatal(err)
		}
		s = New(db.DB()) // Restart must neither lose the reservation nor reapply the result.
		current, err := s.Task(ctx, task.ID)
		if err != nil || current.State != "leased" || current.Attempt != attempt || current.LeaseOwner != "w" || current.FencingToken != task.FencingToken || !MatchingReport(current, "w", task.FencingToken, raw, true) {
			t.Fatalf("lost reserved interruption: %+v, %v", current, err)
		}
		pending, err := s.PendingResults(ctx)
		if err != nil || len(pending) != 0 {
			t.Fatal("finalized interruption reapplied", pending, err)
		}
		if _, err := s.Claim(ctx, "other"); !errors.Is(err, ErrNoTask) {
			t.Fatal("interruption was reassigned", err)
		}
		duplicate, err := s.Enqueue(ctx, input)
		if err != nil || duplicate.ID != task.ID {
			t.Fatal("scheduler started replacement", duplicate, err)
		}
		if _, err := s.RecoverExecution(ctx, task.ID, "other", task.FencingToken); !errors.Is(err, ErrStaleLease) {
			t.Fatal("other worker recovered session", err)
		}
		next, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken)
		if attempt == MaxInfrastructureAttempts-1 {
			if !errors.Is(err, ErrStaleLease) {
				t.Fatal("recovery exceeded budget", err)
			}
			break
		}
		if err != nil || next.FencingToken != task.FencingToken+1 || next.Attempt != attempt+1 || len(next.Result) != 0 || next.ResultDigest != "" {
			t.Fatal("bad recovered authority", next, err)
		}
		replay, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken)
		if err != nil || replay.FencingToken != next.FencingToken || replay.Attempt != next.Attempt {
			t.Fatal("lost response consumed another attempt", replay, err)
		}
		ack, err := s.ReportAcknowledged(ctx, task.ID, "w", task.FencingToken, raw, true)
		if err != nil || !ack {
			t.Fatal("historical receipt lost", ack, err)
		}
		if err := s.Heartbeat(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
			t.Fatal("old authority survived", err)
		}
		task = next
	}
}

func TestReportedInterruptionRefusesMissingSessionAndCancellation(t *testing.T) {
	for _, mode := range []string{"missing-session", "cancelled", "superseded"} {
		t.Run(mode, func(t *testing.T) {
			s, db, _ := setup(t)
			if err := s.RegisterWorker(ctx, Worker{ID: "w", Durable: true, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
				t.Fatal(err)
			}
			task := claim(t, s, "w")
			raw := json.RawMessage(`{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"native"}}`)
			if mode == "missing-session" {
				raw = json.RawMessage(`{"status":"failed","infra_kind":"execution-interrupted"}`)
			}
			if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
				t.Fatal(err)
			}
			if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(Task) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if mode == "cancelled" {
				if err := s.Cancel(ctx, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "superseded" {
				if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"state": "failed", "fencing_token": task.FencingToken + 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
				t.Fatal("unsafe recovery authorized", err)
			}
		})
	}
}

func TestOlderExecutorCannotReleaseBlockedRecovery(t *testing.T) {
	for _, kind := range []string{"launch", "coordination-changed"} {
		t.Run(kind, func(t *testing.T) {
			s, db, _ := setup(t)
			if err := s.RegisterWorker(ctx, Worker{ID: "w", Durable: true, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 646, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
				t.Fatal(err)
			}
			task := claim(t, s, "w")
			if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
				t.Fatal(err)
			}
			next, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]string{"status": "blocked", "infra_kind": kind, "next_owner": "scheduling"})
			if err := s.AcceptResult(WithReportDigest(ctx, raw, true), next.ID, "w", next.FencingToken, raw); err != nil {
				t.Fatal(err)
			}
			if err := s.FinalizeApplication(ctx, next.ID, next.FencingToken, true, func(Task) error { return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := s.Task(ctx, next.ID)
			if err != nil || got.State != "leased" || got.Attempt != 1 || !MatchingReport(got, "w", next.FencingToken, raw, true) {
				t.Fatal("blocked recovery released retained work", got, err)
			}
			if _, err := s.RecoverExecution(ctx, next.ID, "w", next.FencingToken); !errors.Is(err, ErrStaleLease) {
				t.Fatal("missing session evidence resumed", err)
			}
		})
	}
}

func TestMatchingReportDoesNotAcknowledgePreviousAttemptDigest(t *testing.T) {
	raw := json.RawMessage(`{"status":"blocked","infra_kind":"launch","error":"setup unavailable"}`)
	task := &Task{ID: "task", State: "leased", LeaseOwner: "worker", FencingToken: 3, ResultDigest: reportDigest(raw, true)}
	if MatchingReport(task, "worker", 3, raw, true) {
		t.Fatal("retry acknowledged an uncommitted report using the previous attempt's digest")
	}
	task.Result = raw
	if !MatchingReport(task, "worker", 3, raw, true) {
		t.Fatal("committed same-epoch report was not acknowledged")
	}
	if MatchingReport(task, "worker", 1, raw, true) {
		t.Fatal("old epoch matched the current receipt")
	}
}

func TestDurableAuthorityAndRecovery(t *testing.T) {
	s, db, _ := setup(t)
	if err := s.RegisterWorker(ctx, Worker{ID: "durable", Durable: true, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
		t.Fatal(err)
	}
	task, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":3600}`)})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := s.Claim(ctx, "durable")
	if err != nil {
		t.Fatal(err)
	}
	if !DurableTask(leased) || time.Until(*leased.LeaseExpiresAt) < time.Hour {
		t.Fatal("missing bounded durable authority", leased)
	}
	deadline := *leased.LeaseExpiresAt
	if err := s.Heartbeat(ctx, task.ID, "durable", leased.FencingToken); err != nil {
		t.Fatal(err)
	}
	persisted, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.LeaseExpiresAt.Equal(deadline) {
		t.Fatal("heartbeat extended execution authority")
	}
	if _, err := s.RecoverExecution(ctx, task.ID, "durable", leased.FencingToken); !errors.Is(err, ErrStaleLease) {
		t.Fatal("recovered live authority", err)
	}
	past := time.Now().Add(-time.Second)
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	expired, err := s.ExpireLeases(ctx, time.Now())
	if err != nil || len(expired) != 0 {
		t.Fatal("durable work blindly requeued", expired, err)
	}
	if _, err := s.Claim(ctx, "durable"); !errors.Is(err, ErrNoTask) {
		t.Fatal("lost reservation", err)
	}
	recovered, err := s.RecoverExecution(ctx, task.ID, "durable", leased.FencingToken)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.FencingToken != leased.FencingToken+1 || recovered.Attempt != 1 {
		t.Fatal(recovered)
	}
	replay, err := s.RecoverExecution(ctx, task.ID, "durable", leased.FencingToken)
	if err != nil || replay.FencingToken != recovered.FencingToken || replay.Attempt != recovered.Attempt {
		t.Fatal("lost recovery response allocated another attempt", replay, err)
	}
	if err := s.Heartbeat(ctx, task.ID, "durable", leased.FencingToken); !errors.Is(err, ErrStaleLease) {
		t.Fatal("old attempt retained authority", err)
	}
}

func TestDurableReportAcknowledgement(t *testing.T) {
	s, db, _ := setup(t)
	register(t, s, "w", 1)
	task, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	leased := claim(t, s, "w")
	raw := json.RawMessage(`{"status":"success"}`)
	if err := s.AcceptResult(WithReportDigest(ctx, raw, false), task.ID, "w", leased.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	committed, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !MatchingReport(committed, "w", leased.FencingToken, raw, false) {
		t.Fatal("lost ack cannot replay")
	}
	if MatchingReport(committed, "other", leased.FencingToken, raw, false) || MatchingReport(committed, "w", leased.FencingToken+1, raw, false) || MatchingReport(committed, "w", leased.FencingToken, raw, true) || MatchingReport(committed, "w", leased.FencingToken, json.RawMessage(`{"status":"failed"}`), false) {
		t.Fatal("conflicting report acknowledged")
	}
	if err := s.FinishResult(ctx, task.ID, "w", leased.FencingToken, false); err != nil {
		t.Fatal(err)
	}
	committed, err = s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !MatchingReport(committed, "w", leased.FencingToken, raw, false) {
		t.Fatal("finalized receipt lost")
	}
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"state": "pending", "fencing_token": leased.FencingToken + 1, "result": nil}).Error; err != nil {
		t.Fatal(err)
	}
	acknowledged, err := s.ReportAcknowledged(ctx, task.ID, "w", leased.FencingToken, raw, false)
	if err != nil || !acknowledged {
		t.Fatal("historical receipt lost after epoch advance", acknowledged, err)
	}
	acknowledged, err = s.ReportAcknowledged(ctx, task.ID, "other", leased.FencingToken, raw, false)
	if err != nil || acknowledged {
		t.Fatal("another worker acknowledged historical receipt", acknowledged, err)
	}
}

func TestDurableLateReportBeforeRecoveryIsAccepted(t *testing.T) {
	s, db, _ := setup(t)
	if err := s.RegisterWorker(ctx, Worker{ID: "w", Durable: true, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
		t.Fatal(err)
	}
	task, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)})
	if err != nil {
		t.Fatal(err)
	}
	leased := claim(t, s, "w")
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"status":"success"}`)
	if err := s.AcceptResult(WithReportDigest(ctx, raw, false), task.ID, "w", leased.FencingToken, raw); err != nil {
		t.Fatal("result lost during controller outage", err)
	}
	if _, err := s.RecoverExecution(ctx, task.ID, "w", leased.FencingToken); !errors.Is(err, ErrStaleLease) {
		t.Fatal("accepted result resumed", err)
	}
}

func TestDurableClaimReplaySharesCapacity(t *testing.T) {
	s, _, _ := setup(t)
	if err := s.RegisterWorker(ctx, Worker{ID: "w", Durable: true, MaxParallel: 2, RunUntagged: true}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: i, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ClaimWithID(ctx, "w", "claim-one")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.ClaimWithID(ctx, "w", "claim-one")
	if err != nil || replay.ID != first.ID {
		t.Fatal("lost claim response duplicated work", replay, err)
	}
	second, err := s.ClaimWithID(ctx, "w", "claim-two")
	if err != nil || second.ID == first.ID {
		t.Fatal(second, err)
	}
	if _, err := s.ClaimWithID(ctx, "w", "claim-three"); !errors.Is(err, ErrNoTask) {
		t.Fatal("generation overlap exceeded shared worker capacity", err)
	}
	if _, err := s.ClaimWithID(ctx, "w", "../bad"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid identity accepted", err)
	}
}
