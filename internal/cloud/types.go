package cloud

import "time"

// Capability describes a worker capability such as "go", "node", "darwin",
// "github", "gitlab", or "codex".
type Capability string

type RegisterWorkerRequest struct {
	Hostname     string       `json:"hostname"`
	DisplayName  string       `json:"display_name,omitempty"`
	Version      string       `json:"version"`
	Capabilities []Capability `json:"capabilities,omitempty"`
}

type RegisterWorkerResponse struct {
	MachineID   string `json:"machine_id"`
	WorkerID    string `json:"worker_id"`
	WorkerToken string `json:"worker_token"`
}

type HeartbeatRequest struct {
	MachineID string   `json:"machine_id"`
	WorkerID  string   `json:"worker_id"`
	Status    string   `json:"status"`
	Capacity  int      `json:"capacity"`
	ActiveRun []string `json:"active_runs,omitempty"`
}

type HeartbeatResponse struct {
	ServerTime           time.Time `json:"server_time"`
	DesiredConfigVersion string    `json:"desired_config_version,omitempty"`
}

type LeaseRequest struct {
	MachineID    string       `json:"machine_id"`
	WorkerID     string       `json:"worker_id"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	Capacity     int          `json:"capacity"`
}

type LeaseResponse struct {
	Job *JobSpec `json:"job,omitempty"`
}

// JobSpec is the cloud-to-worker contract. It carries enough context for the
// worker to build the existing local runJob without duplicating operator logic.
type JobSpec struct {
	JobID       string            `json:"job_id"`
	RunID       string            `json:"run_id"`
	DedupeKey   string            `json:"dedupe_key,omitempty"`
	WorkspaceID string            `json:"workspace_id,omitempty"`
	ProjectID   string            `json:"project_id,omitempty"`
	Repo        string            `json:"repo"`
	Platform    string            `json:"platform"`
	BaseURL     string            `json:"base_url,omitempty"`
	BaseBranch  string            `json:"base_branch,omitempty"`
	LocalPath   string            `json:"local_path,omitempty"`
	Operator    string            `json:"operator"`
	Target      string            `json:"target"` // "issue" or "pr"
	Number      int               `json:"number"`
	Title       string            `json:"title,omitempty"`
	Body        string            `json:"body,omitempty"`
	Labels      []string          `json:"labels,omitempty"`
	State       string            `json:"state,omitempty"`
	Binding     string            `json:"binding,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type RunEvent struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level,omitempty"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type RunEventsRequest struct {
	Events []RunEvent `json:"events"`
}

type FinishRunRequest struct {
	Status  string `json:"status"`
	Outcome string `json:"outcome,omitempty"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}
