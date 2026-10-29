package coordinator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBoardDerivation(t *testing.T) {
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	recent, boundary, future := now.Add(-time.Second), now.Add(-LeaseTTL), now.Add(time.Second)
	workers := []Worker{{ID: "live"}, {ID: "stale"}, {ID: "revoked"}, {ID: "missing"}, {ID: "future"}}
	tokens := []Token{{Name: "live", LastSeen: &recent}, {Name: "stale", LastSeen: &boundary}, {Name: "revoked", LastSeen: &recent, Revoked: true}, {Name: "future", LastSeen: &future}}
	tasks := []BoardTask{
		{ID: "run", State: "leased", Worker: "live", LeaseExpiresAt: &future},
		{ID: "expired", State: "leased", Worker: "live", LeaseExpiresAt: &now},
		{ID: "finalize", State: "leased", Worker: "live"},
		{ID: "pending", State: "pending"},
		{ID: "done", State: "done", UpdatedAt: recent},
		{ID: "failed", State: "failed", UpdatedAt: now},
		{ID: "old", State: "done", UpdatedAt: now.Add(-time.Hour)},
		{ID: "cancelled", State: "cancelled", UpdatedAt: recent},
		{ID: "future", State: "done", UpdatedAt: future},
	}
	b := deriveBoard(now, workers, tokens, map[string]time.Time{"live": recent}, tasks)
	for i, stage := range []string{"running", "lease_expired", "finalizing", "pending", "done", "failed", "done", "cancelled", "done"} {
		if b.Tasks[i].Stage != stage {
			t.Fatalf("task %d: %+v", i, b.Tasks[i])
		}
	}
	for i, w := range b.Workers {
		if w.Live != (i == 0) {
			t.Fatalf("worker: %+v", w)
		}
		if w.Tags == nil || w.Roles == nil || w.RunningTasks == nil {
			t.Fatalf("null arrays: %+v", w)
		}
	}
	if w := b.Workers[0]; len(w.RunningTasks) != 1 || w.RunningTasks[0] != "run" || w.LastHeartbeat == nil || !w.LastHeartbeat.Equal(recent) {
		t.Fatalf("worker: %+v", w)
	}
	if b.Throughput.Done != 1 || b.Throughput.Failed != 1 {
		t.Fatalf("throughput: %+v", b.Throughput)
	}
}

func TestBoardStoreProjection(t *testing.T) {
	s, db, _ := setup(t)
	secret, err := s.CreateToken(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	register(t, s, "worker", 1)
	if _, err = s.VerifyToken(ctx, secret); err != nil {
		t.Fatal(err)
	}
	task, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"secret":"payload-secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	leased := claim(t, s, "worker")
	if err = s.Heartbeat(ctx, leased.ID, "worker", leased.FencingToken); err != nil {
		t.Fatal(err)
	}
	b, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Workers) != 1 || !b.Workers[0].Live || b.Workers[0].LastHeartbeat == nil || len(b.Tasks) != 1 || b.Tasks[0].Worker != "worker" || b.Tasks[0].Stage != "running" {
		t.Fatalf("board: %+v", b)
	}
	data, _ := json.Marshal(b)
	for _, forbidden := range []string{secret, "payload-secret", "payload", "result", "fencing_token", "Hash"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("exposed %s", forbidden)
		}
	}
	var persisted Task
	if err = db.DB().First(&persisted, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.FencingToken != leased.FencingToken || !persisted.LeaseExpiresAt.Equal(*b.Tasks[0].LeaseExpiresAt) {
		t.Fatal("read changed lease")
	}
	if err = s.Complete(ctx, leased.ID, "worker", leased.FencingToken, json.RawMessage(`{"secret":"result-secret"}`)); err != nil {
		t.Fatal(err)
	}
	b, err = s.Board(ctx)
	if err != nil || b.Throughput.Done != 1 || len(b.Workers[0].RunningTasks) != 0 {
		t.Fatalf("completed board: %+v, %v", b, err)
	}
}

func TestBoardWaitingForCredentialWorker(t *testing.T) {
	now := time.Now()
	recent := now.Add(-time.Second)
	task := BoardTask{State: "pending", Kind: "implement", Tags: []string{"github", "creds-laptop"}}
	workers := []Worker{{ID: "local-worker", Tags: []string{"github"}}, {ID: "laptop", Tags: task.Tags}}
	tokens := []Token{{Name: "local-worker", LastSeen: &recent}}
	b := deriveBoard(now, workers, tokens, nil, []BoardTask{task})
	if !strings.Contains(b.Tasks[0].WaitingReason, "creds-laptop") {
		t.Fatalf("missing waiting reason: %+v", b)
	}
	tokens = append(tokens, Token{Name: "laptop", LastSeen: &recent})
	b = deriveBoard(now, workers, tokens, nil, []BoardTask{task})
	if b.Tasks[0].WaitingReason != "" {
		t.Fatalf("eligible live worker ignored: %+v", b)
	}
}
