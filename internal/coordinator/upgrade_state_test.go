package coordinator

import (
	"encoding/json"
	"testing"
	"time"
)

// An upgrade may tolerate a lease epoch change only for a lease that had
// already expired and that RecoverExecution would resume. The snapshot marks
// exactly those, and never a lease that is still valid.
func TestUpgradeSnapshotMarksOnlyRecoverableExpiredLeases(t *testing.T) {
	type tc struct {
		name    string
		durable bool
		expire  bool
		attempt int
		kind    string
		result  string
		digest  string
		want    bool
	}
	interrupted := `{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"native"}}`
	for _, c := range []tc{
		{name: "expired durable", durable: true, expire: true, kind: "implement", want: true},
		{name: "expired durable repair", durable: true, expire: true, kind: "repair", want: true},
		{name: "still valid durable", durable: true, kind: "implement"},
		{name: "expired non-durable", expire: true, kind: "implement"},
		{name: "retry budget spent", durable: true, expire: true, attempt: MaxInfrastructureAttempts - 1, kind: "implement"},
		{name: "kind never recovered", durable: true, expire: true, kind: "acceptance"},
		{name: "retained interruption", durable: true, expire: true, kind: "implement", result: interrupted, digest: "d", want: true},
		{name: "retained interruption without receipt", durable: true, expire: true, kind: "implement", result: interrupted},
		{name: "other failure report", durable: true, expire: true, kind: "implement", result: `{"status":"failed","error":"boom"}`, digest: "d"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, db, _ := setup(t)
			if err := s.RegisterWorker(ctx, Worker{ID: "w", Durable: c.durable, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 9, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
				t.Fatal(err)
			}
			task := claim(t, s, "w")
			// Claim as implement, then set the kind under test: routing rules keep
			// some kinds from being claimed by this worker.
			updates := map[string]any{"attempt": c.attempt, "kind": c.kind}
			if c.expire {
				updates["lease_expires_at"] = time.Now().UTC().Add(-time.Minute)
			}
			if c.result != "" {
				updates["result"], updates["result_digest"] = c.result, c.digest
			}
			if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Updates(updates).Error; err != nil {
				t.Fatal(err)
			}
			snap, err := s.UpgradeSnapshot(ctx, nil)
			if err != nil || len(snap.Leases) != 1 {
				t.Fatalf("snapshot: %+v %v", snap.Leases, err)
			}
			l := snap.Leases[0]
			if l.AwaitingRecovery != c.want {
				t.Fatalf("AwaitingRecovery=%v, want %v", l.AwaitingRecovery, c.want)
			}
			if l.Repo != "o/r" || l.Issue != 9 || l.Kind != c.kind || l.Owner != "w" {
				t.Fatalf("lease identity missing: %+v", l)
			}
			// The mark must agree with what recovery actually accepts.
			if c.want && c.result == "" {
				if _, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken); err != nil {
					t.Fatalf("marked recoverable but recovery refused: %v", err)
				}
			}
		})
	}
}
