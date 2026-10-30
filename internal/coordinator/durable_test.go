package coordinator

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

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
