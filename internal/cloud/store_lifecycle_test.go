package cloud

import (
	"testing"
	"time"
)

// runStoreLifecycle runs the full worker lifecycle against any Store
// implementation: register → heartbeat → enqueue (with dedupe) → lease (with
// machine-binding) → append events → finish → verify.
func runStoreLifecycle(t *testing.T, newStore func() Store) {
	t.Helper()
	store := newStore()

	// Register two workers on distinct machines.
	regA, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host-a"})
	if err != nil {
		t.Fatalf("RegisterWorker A: %v", err)
	}
	regB, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host-b"})
	if err != nil {
		t.Fatalf("RegisterWorker B: %v", err)
	}

	// Heartbeat from machine A.
	if _, err := store.Heartbeat(HeartbeatRequest{
		MachineID: regA.MachineID,
		WorkerID:  regA.WorkerID,
		Status:    WorkerStatusOnline,
		Capacity:  2,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// Enqueue a job bound to machine A.
	spec := JobSpec{
		DedupeKey: "lifecycle:dedupe:1",
		Repo:      "o/r",
		Platform:  "github",
		Operator:  "evaluate-bug",
		Target:    "issue",
		Number:    1,
	}
	rec, err := store.EnqueueJob(spec, regA.MachineID)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if rec.Spec.JobID == "" {
		t.Fatal("EnqueueJob returned empty job ID")
	}

	// Enqueue the same job again — dedupe must return the existing record.
	rec2, err := store.EnqueueJob(spec, regA.MachineID)
	if err != nil {
		t.Fatalf("EnqueueJob (dedupe): %v", err)
	}
	if rec.Spec.JobID != rec2.Spec.JobID {
		t.Fatalf("dedupe created new job %s, want %s", rec2.Spec.JobID, rec.Spec.JobID)
	}

	// Machine B must NOT be able to lease the bound job.
	got, err := store.Lease(LeaseRequest{MachineID: regB.MachineID, WorkerID: regB.WorkerID}, time.Minute)
	if err != nil {
		t.Fatalf("Lease (machine B): %v", err)
	}
	if got != nil {
		t.Fatalf("machine B should not lease bound job, got %#v", got)
	}

	// Machine A MUST be able to lease the bound job.
	got, err = store.Lease(LeaseRequest{MachineID: regA.MachineID, WorkerID: regA.WorkerID}, time.Minute)
	if err != nil {
		t.Fatalf("Lease (machine A): %v", err)
	}
	if got == nil || got.JobID != rec.Spec.JobID || got.RunID == "" {
		t.Fatalf("machine A lease = %#v, want job %s with run id", got, rec.Spec.JobID)
	}
	runID := got.RunID

	// Append two events to the run.
	if err := store.AppendRunEvents(runID, []RunEvent{
		{Message: "started"},
		{Message: "done"},
	}); err != nil {
		t.Fatalf("AppendRunEvents: %v", err)
	}

	// Finish the run.
	if err := store.FinishRun(runID, FinishRunRequest{
		Status:  JobStatusSucceeded,
		Outcome: "agent-evaluated",
		Summary: "all good",
	}); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	// Verify run state.
	run := store.GetRun(runID)
	if run == nil {
		t.Fatal("GetRun returned nil after FinishRun")
	}
	if run.Status != JobStatusSucceeded {
		t.Fatalf("run.Status = %q, want %q", run.Status, JobStatusSucceeded)
	}
	if run.Outcome != "agent-evaluated" {
		t.Fatalf("run.Outcome = %q, want agent-evaluated", run.Outcome)
	}
	if len(run.Events) != 2 {
		t.Fatalf("len(run.Events) = %d, want 2", len(run.Events))
	}
	if run.EndedAt == nil {
		t.Fatal("run.EndedAt is nil after FinishRun")
	}

	// Verify job state.
	job := store.GetJob(rec.Spec.JobID)
	if job == nil {
		t.Fatal("GetJob returned nil after FinishRun")
	}
	if job.Status != JobStatusSucceeded {
		t.Fatalf("job.Status = %q, want %q", job.Status, JobStatusSucceeded)
	}
}

// runLeaseExpiry verifies that an expired lease returns the job to pending so
// it can be re-leased, and that AttemptCount is incremented each time.
func runLeaseExpiry(t *testing.T, newStore func() Store) {
	t.Helper()
	store := newStore()

	reg, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host"})
	if err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	rec, err := store.EnqueueJob(JobSpec{
		Repo:     "o/r",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   10,
	}, "")
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// Lease with a 1ns TTL so it expires immediately.
	if _, err := store.Lease(
		LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID},
		time.Nanosecond,
	); err != nil {
		t.Fatalf("Lease (first): %v", err)
	}

	// Wait just long enough for the lease to have expired.
	time.Sleep(time.Millisecond)

	// Second lease should succeed because the first has expired.
	got, err := store.Lease(
		LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID},
		time.Minute,
	)
	if err != nil {
		t.Fatalf("Lease (second): %v", err)
	}
	if got == nil || got.JobID != rec.Spec.JobID {
		t.Fatalf("expired job not re-leased: %#v", got)
	}

	// AttemptCount must reflect both lease attempts.
	job := store.GetJob(rec.Spec.JobID)
	if job == nil {
		t.Fatal("GetJob returned nil")
	}
	if job.AttemptCount != 2 {
		t.Fatalf("AttemptCount = %d, want 2", job.AttemptCount)
	}
}

// runStoreUsage exercises the usage write/read path for any Store:
// FinishRun-with-usage persists a run_usage row; AddChatUsage persists
// a chat_usage row; a duplicate run_id upload overwrites (idempotent).
func runStoreUsage(t *testing.T, newStore func() Store) {
	t.Helper()
	store := newStore()

	// Stand up a registered worker + job + leased run so FinishRun has
	// a row to update.
	reg, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host-usage"})
	if err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	rec, err := store.EnqueueJob(JobSpec{
		Repo:     "acme/widget",
		Platform: "github",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   42,
	}, reg.MachineID)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	spec, err := store.Lease(LeaseRequest{
		MachineID: reg.MachineID,
		WorkerID:  reg.WorkerID,
	}, time.Minute)
	if err != nil || spec == nil {
		t.Fatalf("Lease: spec=%v err=%v", spec, err)
	}
	runID := spec.RunID

	// FinishRun with usage attached.
	usage := &Usage{
		DurationMs:               12_345,
		NumTurns:                 7,
		TotalCostUSD:             0.1234,
		InputTokens:              1000,
		OutputTokens:             200,
		CacheReadInputTokens:     5000,
		CacheCreationInputTokens: 50,
		ModelUsage: map[string]ModelUsage{
			"claude-opus-4-7": {
				InputTokens:  900,
				OutputTokens: 180,
				CostUSD:      0.10,
			},
			"claude-haiku-4-5-20251001": {
				InputTokens:  100,
				OutputTokens: 20,
				CostUSD:      0.02,
			},
		},
	}
	if err := store.FinishRun(runID, FinishRunRequest{
		Status:  JobStatusSucceeded,
		Outcome: "agent-evaluated",
		Usage:   usage,
	}); err != nil {
		t.Fatalf("FinishRun with usage: %v", err)
	}

	got := store.GetRunUsage(runID)
	if got == nil {
		t.Fatal("GetRunUsage returned nil after FinishRun with usage")
	}
	if got.TotalCostUSD != 0.1234 {
		t.Fatalf("TotalCostUSD = %v, want 0.1234", got.TotalCostUSD)
	}
	if got.Repo != "acme/widget" || got.Operator != "evaluate-bug" {
		t.Fatalf("denorm fail: repo=%q operator=%q", got.Repo, got.Operator)
	}
	if got.NumTurns != 7 || got.InputTokens != 1000 || got.OutputTokens != 200 {
		t.Fatalf("scalar mismatch: %+v", got)
	}
	if len(got.ModelUsage) != 2 || got.ModelUsage["claude-opus-4-7"].CostUSD != 0.10 {
		t.Fatalf("model usage round-trip lost data: %+v", got.ModelUsage)
	}
	if got.EndedAt.IsZero() {
		t.Fatal("EndedAt is zero")
	}

	// Idempotency: a retry with different numbers overwrites.
	updated := *usage
	updated.TotalCostUSD = 0.9999
	updated.InputTokens = 9999
	if err := store.FinishRun(runID, FinishRunRequest{
		Status: JobStatusSucceeded,
		Usage:  &updated,
	}); err != nil {
		t.Fatalf("FinishRun (retry): %v", err)
	}
	got = store.GetRunUsage(runID)
	if got == nil || got.TotalCostUSD != 0.9999 || got.InputTokens != 9999 {
		t.Fatalf("idempotent overwrite did not take: %+v", got)
	}

	// FinishRun without Usage on a different run must NOT crash and
	// must leave run_usage empty for that run.
	rec2, err := store.EnqueueJob(JobSpec{
		Repo:      "acme/widget",
		Platform:  "github",
		Operator:  "evaluate-bug",
		Target:    "issue",
		Number:    43,
		DedupeKey: "no-usage-run",
	}, reg.MachineID)
	if err != nil {
		t.Fatalf("EnqueueJob 2: %v", err)
	}
	spec2, err := store.Lease(LeaseRequest{
		MachineID: reg.MachineID,
		WorkerID:  reg.WorkerID,
	}, time.Minute)
	if err != nil || spec2 == nil {
		t.Fatalf("Lease 2: spec=%v err=%v", spec2, err)
	}
	if err := store.FinishRun(spec2.RunID, FinishRunRequest{
		Status: JobStatusFailed,
	}); err != nil {
		t.Fatalf("FinishRun no-usage: %v", err)
	}
	if store.GetRunUsage(spec2.RunID) != nil {
		t.Fatal("GetRunUsage non-nil for run without uploaded usage")
	}
	_ = rec
	_ = rec2

	// AddChatUsage path. user_id is required by SQLite (FK to users).
	// MemoryStore doesn't enforce that; we use a non-empty string so
	// the same test body works on both implementations. The lifecycle
	// suite that calls this seeds a real user in SQLite mode.
	chatUsage := &Usage{
		DurationMs:   3_400,
		NumTurns:     1,
		TotalCostUSD: 0.0042,
		InputTokens:  300,
		OutputTokens: 80,
	}
	if err := store.AddChatUsage(AddChatUsageInput{
		SessionID: "sess-abc",
		UserID:    "user-test",
		MachineID: reg.MachineID,
		Repo:      "acme/widget",
		Usage:     chatUsage,
	}); err != nil {
		t.Fatalf("AddChatUsage: %v", err)
	}
	gotChat := store.GetChatUsage("sess-abc")
	if gotChat == nil {
		t.Fatal("GetChatUsage returned nil")
	}
	if gotChat.TotalCostUSD != 0.0042 || gotChat.InputTokens != 300 {
		t.Fatalf("chat usage round-trip mismatch: %+v", gotChat)
	}
	if gotChat.UserID != "user-test" || gotChat.Repo != "acme/widget" {
		t.Fatalf("chat denorm fail: %+v", gotChat)
	}

	// Missing usage / id rejected.
	if err := store.AddChatUsage(AddChatUsageInput{SessionID: "x", UserID: "user-test"}); err == nil {
		t.Fatal("AddChatUsage with nil Usage should error")
	}
	if err := store.AddChatUsage(AddChatUsageInput{UserID: "user-test", Usage: chatUsage}); err == nil {
		t.Fatal("AddChatUsage with empty session id should error")
	}
}

// MemoryStore runs of the shared suite.

func TestMemoryStoreLifecycle(t *testing.T) {
	runStoreLifecycle(t, func() Store { return NewMemoryStore() })
}

func TestMemoryStoreLeaseExpiry(t *testing.T) {
	runLeaseExpiry(t, func() Store { return NewMemoryStore() })
}

func TestMemoryStoreUsage(t *testing.T) {
	runStoreUsage(t, func() Store { return NewMemoryStore() })
}
