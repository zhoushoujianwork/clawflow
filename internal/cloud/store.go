package cloud

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"time"
)

const (
	WorkerStatusOnline  = "online"
	WorkerStatusOffline = "offline"

	JobStatusPending   = "pending"
	JobStatusLeased    = "leased"
	JobStatusRunning   = "running"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"
	JobStatusCancelled = "cancelled"

	DefaultLeaseDuration = 10 * time.Minute
)

// Machine is the stable identity for a registered device.
type Machine struct {
	ID           string       `json:"id"`
	Hostname     string       `json:"hostname"`
	DisplayName  string       `json:"display_name,omitempty"`
	Version      string       `json:"version,omitempty"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	LastSeenAt   time.Time    `json:"last_seen_at"`
}

// Worker is one running process attached to a machine.
type Worker struct {
	ID         string    `json:"id"`
	MachineID  string    `json:"machine_id"`
	Token      string    `json:"-"`
	Status     string    `json:"status"`
	Capacity   int       `json:"capacity"`
	ActiveRuns []string  `json:"active_runs,omitempty"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// JobRecord is the server-side state around a leased JobSpec.
type JobRecord struct {
	Spec           JobSpec   `json:"spec"`
	Status         string    `json:"status"`
	BoundMachineID string    `json:"bound_machine_id,omitempty"`
	LeaseWorkerID  string    `json:"lease_worker_id,omitempty"`
	LeaseExpiresAt time.Time `json:"lease_expires_at,omitempty"`
	AttemptCount   int       `json:"attempt_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// RunRecord records one execution attempt for a job.
type RunRecord struct {
	ID        string     `json:"id"`
	JobID     string     `json:"job_id"`
	Status    string     `json:"status"`
	Outcome   string     `json:"outcome,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Error     string     `json:"error,omitempty"`
	Events    []RunEvent `json:"events,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// MemoryStore is the first server-side implementation of the worker protocol.
// It is useful for local development and tests; production storage should keep
// this behavior but replace the in-memory maps with a database-backed store.
type MemoryStore struct {
	mu sync.Mutex

	Machines map[string]*Machine
	Workers  map[string]*Worker
	Jobs     map[string]*JobRecord
	Runs     map[string]*RunRecord

	dedupe map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		Machines: make(map[string]*Machine),
		Workers:  make(map[string]*Worker),
		Jobs:     make(map[string]*JobRecord),
		Runs:     make(map[string]*RunRecord),
		dedupe:   make(map[string]string),
	}
}

func (s *MemoryStore) RegisterWorker(req RegisterWorkerRequest) (RegisterWorkerResponse, error) {
	if req.Hostname == "" {
		return RegisterWorkerResponse{}, fmt.Errorf("hostname is required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	machineID := newID("machine")
	machine := &Machine{
		ID:           machineID,
		Hostname:     req.Hostname,
		DisplayName:  req.DisplayName,
		Version:      req.Version,
		Capabilities: append([]Capability(nil), req.Capabilities...),
		LastSeenAt:   now,
	}
	s.Machines[machineID] = machine

	workerID := newID("worker")
	token := newID("wtoken")
	s.Workers[workerID] = &Worker{
		ID:         workerID,
		MachineID:  machineID,
		Token:      token,
		Status:     WorkerStatusOnline,
		Capacity:   1,
		LastSeenAt: now,
	}
	return RegisterWorkerResponse{MachineID: machineID, WorkerID: workerID, WorkerToken: token}, nil
}

func (s *MemoryStore) Heartbeat(req HeartbeatRequest) (HeartbeatResponse, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	worker, ok := s.Workers[req.WorkerID]
	if !ok || worker.MachineID != req.MachineID {
		return HeartbeatResponse{}, fmt.Errorf("worker not registered")
	}
	worker.Status = req.Status
	if worker.Status == "" {
		worker.Status = WorkerStatusOnline
	}
	worker.Capacity = req.Capacity
	worker.ActiveRuns = append([]string(nil), req.ActiveRun...)
	worker.LastSeenAt = now
	if machine := s.Machines[req.MachineID]; machine != nil {
		machine.LastSeenAt = now
	}
	return HeartbeatResponse{ServerTime: now}, nil
}

func (s *MemoryStore) EnqueueJob(spec JobSpec, boundMachineID string) (*JobRecord, error) {
	if spec.Repo == "" || spec.Operator == "" || spec.Number == 0 {
		return nil, fmt.Errorf("job spec requires repo, operator, and number")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	if spec.DedupeKey != "" {
		if existingID, ok := s.dedupe[spec.DedupeKey]; ok {
			return cloneJob(s.Jobs[existingID]), nil
		}
	}
	spec.JobID = newID("job")
	rec := &JobRecord{
		Spec:           spec,
		Status:         JobStatusPending,
		BoundMachineID: boundMachineID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.Jobs[spec.JobID] = rec
	if spec.DedupeKey != "" {
		s.dedupe[spec.DedupeKey] = spec.JobID
	}
	return cloneJob(rec), nil
}

func (s *MemoryStore) Lease(req LeaseRequest, leaseFor time.Duration) (*JobSpec, error) {
	if leaseFor <= 0 {
		leaseFor = DefaultLeaseDuration
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	worker, ok := s.Workers[req.WorkerID]
	if !ok || worker.MachineID != req.MachineID {
		return nil, fmt.Errorf("worker not registered")
	}
	for _, job := range s.Jobs {
		if job.Status == JobStatusLeased && now.After(job.LeaseExpiresAt) {
			job.Status = JobStatusPending
			job.LeaseWorkerID = ""
			job.LeaseExpiresAt = time.Time{}
		}
		if job.Status != JobStatusPending {
			continue
		}
		if job.BoundMachineID != "" && job.BoundMachineID != req.MachineID {
			continue
		}
		if !supportsPlatform(req.Capabilities, job.Spec.Platform) {
			continue
		}
		runID := newID("run")
		job.Status = JobStatusLeased
		job.Spec.RunID = runID
		job.LeaseWorkerID = req.WorkerID
		job.LeaseExpiresAt = now.Add(leaseFor)
		job.AttemptCount++
		job.UpdatedAt = now
		s.Runs[runID] = &RunRecord{
			ID:        runID,
			JobID:     job.Spec.JobID,
			Status:    JobStatusRunning,
			StartedAt: now,
		}
		spec := job.Spec
		return &spec, nil
	}
	return nil, nil
}

func (s *MemoryStore) AppendRunEvents(runID string, events []RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.Runs[runID]
	if !ok {
		return fmt.Errorf("run not found")
	}
	run.Events = append(run.Events, events...)
	return nil
}

func (s *MemoryStore) FinishRun(runID string, req FinishRunRequest) error {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.Runs[runID]
	if !ok {
		return fmt.Errorf("run not found")
	}
	status := req.Status
	if status == "" {
		status = JobStatusFailed
	}
	run.Status = status
	run.Outcome = req.Outcome
	run.Summary = req.Summary
	run.Error = req.Error
	run.EndedAt = &now

	if job := s.Jobs[run.JobID]; job != nil {
		job.Status = status
		job.LeaseWorkerID = ""
		job.LeaseExpiresAt = time.Time{}
		job.UpdatedAt = now
	}
	return nil
}

func (s *MemoryStore) GetJob(id string) *JobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneJob(s.Jobs[id])
}

func (s *MemoryStore) GetRun(id string) *RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.Runs[id]
	if run == nil {
		return nil
	}
	cp := *run
	cp.Events = append([]RunEvent(nil), run.Events...)
	return &cp
}

func supportsPlatform(caps []Capability, platform string) bool {
	if platform == "" || platform == "github" {
		return true
	}
	return slices.Contains(caps, Capability(platform))
}

func cloneJob(job *JobRecord) *JobRecord {
	if job == nil {
		return nil
	}
	cp := *job
	cp.Spec.Labels = append([]string(nil), job.Spec.Labels...)
	if job.Spec.Metadata != nil {
		cp.Spec.Metadata = make(map[string]string, len(job.Spec.Metadata))
		for k, v := range job.Spec.Metadata {
			cp.Spec.Metadata[k] = v
		}
	}
	return &cp
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
