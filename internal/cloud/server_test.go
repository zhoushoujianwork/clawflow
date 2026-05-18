package cloud

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMemoryStoreLeaseHonorsMachineBinding(t *testing.T) {
	store := NewMemoryStore()
	a, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "b"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.EnqueueJob(JobSpec{
		DedupeKey: "github:o/r:issue:1:evaluate-bug",
		Repo:      "o/r",
		Platform:  "github",
		Operator:  "evaluate-bug",
		Target:    "issue",
		Number:    1,
	}, a.MachineID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Lease(LeaseRequest{MachineID: b.MachineID, WorkerID: b.WorkerID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("machine b leased bound job: %#v", got)
	}
	got, err = store.Lease(LeaseRequest{MachineID: a.MachineID, WorkerID: a.WorkerID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.JobID != rec.Spec.JobID || got.RunID == "" {
		t.Fatalf("machine a lease = %#v, want job %s with run id", got, rec.Spec.JobID)
	}
}

func TestMemoryStoreDedupeReturnsExistingPendingJob(t *testing.T) {
	store := NewMemoryStore()
	spec := JobSpec{
		DedupeKey: "github:o/r:issue:2:evaluate-bug",
		Repo:      "o/r",
		Operator:  "evaluate-bug",
		Target:    "issue",
		Number:    2,
	}
	first, err := store.EnqueueJob(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueJob(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Spec.JobID != second.Spec.JobID {
		t.Fatalf("dedupe created new job %s, want %s", second.Spec.JobID, first.Spec.JobID)
	}
}

func TestMemoryStoreLeaseExpiryReturnsJobToPending(t *testing.T) {
	store := NewMemoryStore()
	reg, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.EnqueueJob(JobSpec{
		Repo:     "o/r",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   3,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lease(LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID}, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	got, err := store.Lease(LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.JobID != rec.Spec.JobID {
		t.Fatalf("expired job was not leased again: %#v", got)
	}
	if job := store.GetJob(rec.Spec.JobID); job.AttemptCount != 2 {
		t.Fatalf("attempt count = %d, want 2", job.AttemptCount)
	}
}

func TestServerWorkerLifecycle(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()
	client, err := NewClient(Config{BaseURL: srv.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := client.RegisterWorker(t.Context(), RegisterWorkerRequest{Hostname: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Heartbeat(t.Context(), HeartbeatRequest{
		MachineID: reg.MachineID,
		WorkerID:  reg.WorkerID,
		Status:    WorkerStatusOnline,
		Capacity:  1,
	}); err != nil {
		t.Fatal(err)
	}
	store.EnqueueJob(JobSpec{
		Repo:     "o/r",
		Platform: "github",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   4,
	}, reg.MachineID)
	lease, err := client.Lease(t.Context(), LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID, Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Job == nil || lease.Job.RunID == "" {
		t.Fatalf("lease = %#v", lease)
	}
	if err := client.AppendRunEvents(t.Context(), lease.Job.RunID, RunEventsRequest{Events: []RunEvent{{Message: "started"}}}); err != nil {
		t.Fatal(err)
	}
	if err := client.FinishRun(t.Context(), lease.Job.RunID, FinishRunRequest{Status: JobStatusSucceeded, Outcome: "agent-evaluated"}); err != nil {
		t.Fatal(err)
	}
	run := store.GetRun(lease.Job.RunID)
	if run == nil || run.Status != JobStatusSucceeded || len(run.Events) != 1 {
		t.Fatalf("run = %#v", run)
	}
}

func TestServerDevJobEndpoint(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"job": map[string]any{
			"repo":     "o/r",
			"operator": "evaluate-bug",
			"number":   5,
		},
	})
	resp, err := http.Post(srv.URL+"/api/cloud/dev/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
