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

// MemoryStore runs of the shared suite.

func TestMemoryStoreLifecycle(t *testing.T) {
	runStoreLifecycle(t, func() Store { return NewMemoryStore() })
}

func TestMemoryStoreLeaseExpiry(t *testing.T) {
	runLeaseExpiry(t, func() Store { return NewMemoryStore() })
}
