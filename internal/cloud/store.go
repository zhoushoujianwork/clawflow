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
	// MachineID is the machine that leased and is executing this run.
	// Populated by Lease(); used by worker-protocol handlers to verify
	// that only the owning machine can post events or finish the run.
	MachineID string     `json:"machine_id,omitempty"`
	Status    string     `json:"status"`
	Outcome   string     `json:"outcome,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Error     string     `json:"error,omitempty"`
	Events    []RunEvent `json:"events,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Store is the server-side persistence interface for the worker protocol.
// MemoryStore and SQLiteStore are both implementations; swap them via NewServer.
type Store interface {
	// Worker protocol.
	RegisterWorker(req RegisterWorkerRequest) (RegisterWorkerResponse, error)
	Heartbeat(req HeartbeatRequest) (HeartbeatResponse, error)
	EnqueueJob(spec JobSpec, boundMachineID string) (*JobRecord, error)
	Lease(req LeaseRequest, leaseFor time.Duration) (*JobSpec, error)
	AppendRunEvents(runID string, events []RunEvent) error
	FinishRun(runID string, req FinishRunRequest) error
	GetJob(id string) *JobRecord
	GetRun(id string) *RunRecord

	// Usage. AddChatUsage is called by the chat handler after a worker
	// POSTs to /api/worker/chat/sessions/{id}/usage. AddRunUsage is
	// called implicitly from FinishRun when the request carries a
	// non-nil Usage. Get* are for tests; ListUsageForUser backs the
	// /api/cloud/usage/summary aggregation endpoint.
	AddChatUsage(in AddChatUsageInput) error
	GetRunUsage(runID string) *UsageRecord
	GetChatUsage(sessionID string) *UsageRecord
	// ListUsageForUser returns every run_usage + chat_usage row whose
	// user_id matches the supplied user. Run usage rows that have an
	// empty user_id (single-user self-host where no owner has been
	// stamped) are also returned to keep that mode functional; pass
	// userID == "" to retrieve only the orphaned rows.
	ListUsageForUser(userID string) []*UsageRecord

	// VCS connections (used by the webhook handler).
	RegisterConnection(conn VCSConnection) (*VCSConnection, error)
	GetConnectionByRepo(repo string) *VCSConnection

	// Cloud config.
	Summary() CloudConfigSummary
	CreateProject(req CreateProjectRequest) (*Project, error)
	CreateRepo(req CreateRepoRequest) (*Repo, error)
	UpdateRepo(id string, req UpdateRepoRequest) (*Repo, error)
	DeleteProject(id string) error
	DeleteRepo(id string) error
	CreateBinding(req CreateBindingRequest) (*Binding, error)
	UpdateBinding(id string, req UpdateBindingRequest) (*Binding, error)
	DeleteBinding(id string) error
	ListMachines() []*Machine
	ListJobs() []*JobRecord
	ListRuns() []*RunRecord

	// Identity / auth.
	UpsertUser(req UpsertUserRequest) (*User, error)
	GetUserByGitHubID(githubID int64) (*User, error)
	GetUser(id string) (*User, error)

	// Sessions are opaque browser cookies. CreateSession returns the new
	// session id; the caller is responsible for placing it in a Set-Cookie
	// header. GetSession returns nil when expired or not found.
	CreateSession(userID string, ttl time.Duration) (*Session, error)
	GetSession(id string) (*Session, error)
	DeleteSession(id string) error

	// API tokens. CreateAPIToken hashes req.Plaintext before persisting and
	// returns the stored row (without the plaintext). LookupAPIToken hashes
	// the supplied plaintext and returns the matching non-revoked token, or
	// nil if no match. Hash collisions are not possible (SHA-256 over a
	// 32-byte random seed).
	CreateAPIToken(req CreateAPITokenRequest) (*APIToken, error)
	LookupAPIToken(plaintext string) (*APIToken, error)
	RevokeAPIToken(id string) error

	// Installations cache. ListUserInstallations returns the cached GitHub
	// installations the user has authorized via OAuth.
	UpsertInstallation(req UpsertInstallationRequest) (*Installation, error)
	LinkUserInstallation(userID, installationID string) error
	ListUserInstallations(userID string) []*Installation
}

// compile-time check
var _ Store = (*MemoryStore)(nil)

// MemoryStore is the first server-side implementation of the worker protocol.
// It is useful for local development and tests; production storage should keep
// this behavior but replace the in-memory maps with a database-backed store.
type MemoryStore struct {
	mu sync.Mutex

	Machines    map[string]*Machine
	Workers     map[string]*Worker
	Jobs        map[string]*JobRecord
	Runs        map[string]*RunRecord
	Connections map[string]*VCSConnection // keyed by VCSConnection.ID

	// Cloud config resources.
	Projects map[string]*Project
	Repos    map[string]*Repo
	Bindings map[string]*Binding

	// Usage by primary key. RunUsage is keyed by run_id, ChatUsage by
	// session_id.
	RunUsage  map[string]*UsageRecord
	ChatUsage map[string]*UsageRecord

	dedupe map[string]string
	// repoConn maps "owner/repo" → connection ID for O(1) webhook lookup.
	repoConn map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		Machines:    make(map[string]*Machine),
		Workers:     make(map[string]*Worker),
		Jobs:        make(map[string]*JobRecord),
		Runs:        make(map[string]*RunRecord),
		Connections: make(map[string]*VCSConnection),
		Projects:    make(map[string]*Project),
		Repos:       make(map[string]*Repo),
		Bindings:    make(map[string]*Binding),
		RunUsage:    make(map[string]*UsageRecord),
		ChatUsage:   make(map[string]*UsageRecord),
		dedupe:      make(map[string]string),
		repoConn:    make(map[string]string),
	}
}

// RegisterConnection upserts a VCSConnection and returns its ID.
func (s *MemoryStore) RegisterConnection(conn VCSConnection) (*VCSConnection, error) {
	if conn.Repo == "" {
		return nil, fmt.Errorf("connection requires a non-empty repo")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Upsert: if a connection for this repo already exists, update it.
	if existingID, ok := s.repoConn[conn.Repo]; ok {
		conn.ID = existingID
	}
	if conn.ID == "" {
		conn.ID = newID("conn")
	}
	if conn.Platform == "" {
		conn.Platform = "github"
	}
	cp := conn
	s.Connections[conn.ID] = &cp
	s.repoConn[conn.Repo] = conn.ID
	return &cp, nil
}

// GetConnectionByRepo looks up the VCSConnection for a given "owner/repo".
// Returns nil when no connection is registered for that repo.
func (s *MemoryStore) GetConnectionByRepo(repo string) *VCSConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.repoConn[repo]
	if !ok {
		return nil
	}
	c := s.Connections[id]
	if c == nil {
		return nil
	}
	cp := *c
	if c.GitHubApp != nil {
		app := *c.GitHubApp
		cp.GitHubApp = &app
	}
	return &cp
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
			MachineID: req.MachineID,
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

	var (
		repo     string
		operator string
	)
	if job := s.Jobs[run.JobID]; job != nil {
		job.Status = status
		job.LeaseWorkerID = ""
		job.LeaseExpiresAt = time.Time{}
		job.UpdatedAt = now
		repo = job.Spec.Repo
		operator = job.Spec.Operator
	}
	if req.Usage != nil {
		// Idempotent on duplicate run_id: later upload wins (a retry
		// usually means later events.jsonl is more complete).
		s.RunUsage[runID] = newUsageRecord("", runID, repo, operator, "", "", now, req.Usage)
	}
	return nil
}

// AddChatUsage stores a chat session's terminal token / cost breakdown.
// Idempotent on (session_id): retries from the worker simply overwrite.
func (s *MemoryStore) AddChatUsage(in AddChatUsageInput) error {
	if in.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if in.Usage == nil {
		return fmt.Errorf("usage is required")
	}
	endedAt := in.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ChatUsage[in.SessionID] = newUsageRecord(in.SessionID, "", in.Repo, "", in.UserID, in.MachineID, endedAt, in.Usage)
	return nil
}

// GetRunUsage returns a copy of the stored run usage, or nil.
func (s *MemoryStore) GetRunUsage(runID string) *UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneUsage(s.RunUsage[runID])
}

// GetChatUsage returns a copy of the stored chat usage, or nil.
func (s *MemoryStore) GetChatUsage(sessionID string) *UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneUsage(s.ChatUsage[sessionID])
}

// ListUsageForUser returns chat rows belonging to userID + run rows
// whose user_id is empty (single-user self-host) or matches.
func (s *MemoryStore) ListUsageForUser(userID string) []*UsageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*UsageRecord, 0, len(s.RunUsage)+len(s.ChatUsage))
	for _, r := range s.RunUsage {
		if r.UserID == "" || r.UserID == userID {
			out = append(out, cloneUsage(r))
		}
	}
	for _, c := range s.ChatUsage {
		if c.UserID == userID {
			out = append(out, cloneUsage(c))
		}
	}
	return out
}

func newUsageRecord(sessionID, runID, repo, operator, userID, machineID string, endedAt time.Time, u *Usage) *UsageRecord {
	rec := &UsageRecord{
		RunID:                    runID,
		SessionID:                sessionID,
		UserID:                   userID,
		MachineID:                machineID,
		Repo:                     repo,
		Operator:                 operator,
		DurationMs:               u.DurationMs,
		NumTurns:                 u.NumTurns,
		TotalCostUSD:             u.TotalCostUSD,
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		EndedAt:                  endedAt,
	}
	if len(u.ModelUsage) > 0 {
		rec.ModelUsage = make(map[string]ModelUsage, len(u.ModelUsage))
		for k, v := range u.ModelUsage {
			rec.ModelUsage[k] = v
		}
	}
	return rec
}

func cloneUsage(u *UsageRecord) *UsageRecord {
	if u == nil {
		return nil
	}
	cp := *u
	if u.ModelUsage != nil {
		cp.ModelUsage = make(map[string]ModelUsage, len(u.ModelUsage))
		for k, v := range u.ModelUsage {
			cp.ModelUsage[k] = v
		}
	}
	return &cp
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

// ---- Cloud config store methods ----

// CreateProject creates a new cloud project. Name is required.
func (s *MemoryStore) CreateProject(req CreateProjectRequest) (*Project, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := &Project{
		ID:          newID("proj"),
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.Projects[p.ID] = p
	cp := *p
	return &cp, nil
}

// GetProject returns a copy of the project with the given ID, or nil.
func (s *MemoryStore) GetProject(id string) *Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.Projects[id]
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// CreateRepo registers a new repository. Name is required.
// If project_id is supplied it must reference an existing project.
func (s *MemoryStore) CreateRepo(req CreateRepoRequest) (*Repo, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.ProjectID != "" {
		if _, ok := s.Projects[req.ProjectID]; !ok {
			return nil, fmt.Errorf("project %q not found", req.ProjectID)
		}
	}
	platform := req.Platform
	if platform == "" {
		platform = "github"
	}
	r := &Repo{
		ID:         newID("repo"),
		Name:       req.Name,
		Platform:   platform,
		ProjectID:  req.ProjectID,
		BaseBranch: req.BaseBranch,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	s.Repos[r.ID] = r
	cp := *r
	return &cp, nil
}

// GetRepo returns a copy of the repo with the given ID, or nil.
func (s *MemoryStore) GetRepo(id string) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.Repos[id]
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

// UpdateRepo applies a partial update to the named repo.
// Only non-nil fields are modified. Setting project_id to "" unlinks the repo.
func (s *MemoryStore) UpdateRepo(id string, req UpdateRepoRequest) (*Repo, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.Repos[id]
	if !ok {
		return nil, fmt.Errorf("repo %q not found", id)
	}
	if req.ProjectID != nil {
		if *req.ProjectID != "" {
			if _, ok := s.Projects[*req.ProjectID]; !ok {
				return nil, fmt.Errorf("project %q not found", *req.ProjectID)
			}
		}
		r.ProjectID = *req.ProjectID
	}
	if req.BaseBranch != nil {
		r.BaseBranch = *req.BaseBranch
	}
	r.UpdatedAt = now
	cp := *r
	return &cp, nil
}

// CreateBinding creates a binding that assigns a repo or project to a machine.
// machine_id is required; exactly one of repo_id or project_id must be set.
// All referenced IDs must exist.
func (s *MemoryStore) CreateBinding(req CreateBindingRequest) (*Binding, error) {
	if req.MachineID == "" {
		return nil, fmt.Errorf("machine_id is required")
	}
	if req.RepoID == "" && req.ProjectID == "" {
		return nil, fmt.Errorf("one of repo_id or project_id is required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Machines[req.MachineID]; !ok {
		return nil, fmt.Errorf("machine %q not found", req.MachineID)
	}
	if req.RepoID != "" {
		if _, ok := s.Repos[req.RepoID]; !ok {
			return nil, fmt.Errorf("repo %q not found", req.RepoID)
		}
	}
	if req.ProjectID != "" {
		if _, ok := s.Projects[req.ProjectID]; !ok {
			return nil, fmt.Errorf("project %q not found", req.ProjectID)
		}
	}
	b := &Binding{
		ID:        newID("binding"),
		MachineID: req.MachineID,
		RepoID:    req.RepoID,
		ProjectID: req.ProjectID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.Bindings[b.ID] = b
	cp := *b
	return &cp, nil
}

// GetBinding returns a copy of the binding with the given ID, or nil.
func (s *MemoryStore) GetBinding(id string) *Binding {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.Bindings[id]
	if b == nil {
		return nil
	}
	cp := *b
	return &cp
}

// UpdateBinding applies a partial update to the named binding.
// Non-empty fields are validated and applied.
func (s *MemoryStore) UpdateBinding(id string, req UpdateBindingRequest) (*Binding, error) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.Bindings[id]
	if !ok {
		return nil, fmt.Errorf("binding %q not found", id)
	}
	if req.MachineID != "" {
		if _, ok := s.Machines[req.MachineID]; !ok {
			return nil, fmt.Errorf("machine %q not found", req.MachineID)
		}
		b.MachineID = req.MachineID
	}
	if req.RepoID != "" {
		if _, ok := s.Repos[req.RepoID]; !ok {
			return nil, fmt.Errorf("repo %q not found", req.RepoID)
		}
		b.RepoID = req.RepoID
	}
	if req.ProjectID != "" {
		if _, ok := s.Projects[req.ProjectID]; !ok {
			return nil, fmt.Errorf("project %q not found", req.ProjectID)
		}
		b.ProjectID = req.ProjectID
	}
	b.UpdatedAt = now
	cp := *b
	return &cp, nil
}

// DeleteProject removes a project. Existing repos that reference the
// project keep their project_id pointing at the deleted ID — orphaning is
// considered acceptable for the first iteration.
func (s *MemoryStore) DeleteProject(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Projects[id]; !ok {
		return fmt.Errorf("project %q not found", id)
	}
	delete(s.Projects, id)
	return nil
}

// DeleteRepo removes a repo. Bindings pointing at the deleted repo are
// likewise orphaned (kept around so the UI can show "stale" entries).
func (s *MemoryStore) DeleteRepo(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Repos[id]; !ok {
		return fmt.Errorf("repo %q not found", id)
	}
	delete(s.Repos, id)
	return nil
}

// DeleteBinding removes a single binding row.
func (s *MemoryStore) DeleteBinding(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Bindings[id]; !ok {
		return fmt.Errorf("binding %q not found", id)
	}
	delete(s.Bindings, id)
	return nil
}

// ListMachines returns copies of all registered machines.
func (s *MemoryStore) ListMachines() []*Machine {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Machine, 0, len(s.Machines))
	for _, m := range s.Machines {
		cp := *m
		out = append(out, &cp)
	}
	return out
}

// ListJobs returns copies of all job records.
func (s *MemoryStore) ListJobs() []*JobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*JobRecord, 0, len(s.Jobs))
	for _, j := range s.Jobs {
		out = append(out, cloneJob(j))
	}
	return out
}

// ListRuns returns copies of all run records.
func (s *MemoryStore) ListRuns() []*RunRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*RunRecord, 0, len(s.Runs))
	for _, r := range s.Runs {
		cp := *r
		cp.Events = append([]RunEvent(nil), r.Events...)
		out = append(out, &cp)
	}
	return out
}

// Summary returns an aggregated snapshot of all cloud config resources.
func (s *MemoryStore) Summary() CloudConfigSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := CloudConfigSummary{
		Projects: make([]*Project, 0, len(s.Projects)),
		Repos:    make([]*Repo, 0, len(s.Repos)),
		Machines: make([]*Machine, 0, len(s.Machines)),
		Bindings: make([]*Binding, 0, len(s.Bindings)),
	}
	for _, p := range s.Projects {
		cp := *p
		sum.Projects = append(sum.Projects, &cp)
	}
	for _, r := range s.Repos {
		cp := *r
		sum.Repos = append(sum.Repos, &cp)
	}
	for _, m := range s.Machines {
		cp := *m
		sum.Machines = append(sum.Machines, &cp)
	}
	for _, b := range s.Bindings {
		cp := *b
		sum.Bindings = append(sum.Bindings, &cp)
	}
	sum.Counts = ConfigCounts{
		Projects: len(s.Projects),
		Repos:    len(s.Repos),
		Machines: len(s.Machines),
		Bindings: len(s.Bindings),
		Jobs:     len(s.Jobs),
		Runs:     len(s.Runs),
	}
	return sum
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
