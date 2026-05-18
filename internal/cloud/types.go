package cloud

import "time"

// Capability describes a worker capability such as "go", "node", "darwin",
// "github", "gitlab", or "codex".
type Capability string

// ---- Identity / auth domain types ----

// User is one authenticated GitHub user. The cloud server uses (id, github_id)
// as the stable identity across sessions and API tokens.
type User struct {
	ID        string    `json:"id"`
	GitHubID  int64     `json:"github_id"`
	Login     string    `json:"login"`
	Name      string    `json:"name,omitempty"`
	AvatarURL string    `json:"avatar_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Session is a browser cookie session for a logged-in user. The cookie value
// IS the session id (random, opaque); the server lookups by id.
type Session struct {
	ID        string    `json:"-"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// APITokenKind discriminates between personal CLI tokens and worker tokens.
const (
	APITokenKindPersonal = "personal"
	APITokenKindMachine  = "machine"
)

// APIToken is a Bearer credential issued to a user (personal) or a worker
// (machine). The plaintext token is shown to the caller exactly once at
// creation; the database only stores its SHA-256 hash.
type APIToken struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	Kind       string     `json:"kind"`
	MachineID  string     `json:"machine_id,omitempty"`
	Label      string     `json:"label,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Installation is one row in the cached list of GitHub App installations a
// user has authorized. Refreshed on login.
type Installation struct {
	ID                   string    `json:"id"`
	GitHubInstallationID int64     `json:"github_installation_id"`
	AccountLogin         string    `json:"account_login"`
	AccountType          string    `json:"account_type"` // 'Organization' | 'User'
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// UpsertUserRequest captures the fields the cloud needs after GitHub OAuth.
type UpsertUserRequest struct {
	GitHubID  int64
	Login     string
	Name      string
	AvatarURL string
}

// CreateAPITokenRequest is the input to Store.CreateAPIToken. The caller
// supplies the random plaintext via Plaintext; the store hashes it before
// persisting and never echoes it back.
type CreateAPITokenRequest struct {
	UserID    string
	Kind      string
	Plaintext string
	MachineID string
	Label     string
}

// UpsertInstallationRequest is the input to Store.UpsertInstallation. The
// account_type should be 'Organization' or 'User'.
type UpsertInstallationRequest struct {
	GitHubInstallationID int64
	AccountLogin         string
	AccountType          string
}

// ---- Cloud config domain types ----

// Project is a top-level configuration unit that groups repos and automation
// settings. A repo may belong to at most one project; project_id is optional.
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Repo is a repository registered with the cloud config. It may optionally
// belong to a Project (project_id). Deleting a project does not cascade in
// this iteration — repos become orphans with project_id still set.
type Repo struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`            // "owner/repo" slug
	Platform   string    `json:"platform"`         // "github" | "gitlab"
	ProjectID  string    `json:"project_id,omitempty"`
	BaseBranch string    `json:"base_branch,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Binding links a repo or project to a specific registered machine. The
// binding is keyed by an opaque ID; update semantics are PATCH /bindings/{id}.
type Binding struct {
	ID        string    `json:"id"`
	MachineID string    `json:"machine_id"`
	RepoID    string    `json:"repo_id,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ---- Cloud config request/response DTOs ----

// CreateProjectRequest is the body for POST /api/cloud/projects.
type CreateProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CreateRepoRequest is the body for POST /api/cloud/repos.
type CreateRepoRequest struct {
	Name       string `json:"name"`
	Platform   string `json:"platform,omitempty"`
	ProjectID  string `json:"project_id,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// UpdateRepoRequest is the body for PATCH /api/cloud/repos/{id}.
// Pointer fields are omitted when nil, allowing partial updates.
// Setting project_id to "" unlinks the repo from its project.
type UpdateRepoRequest struct {
	ProjectID  *string `json:"project_id,omitempty"`
	BaseBranch *string `json:"base_branch,omitempty"`
}

// CreateBindingRequest is the body for POST /api/cloud/bindings.
// Exactly one of repo_id or project_id must be supplied.
type CreateBindingRequest struct {
	MachineID string `json:"machine_id"`
	RepoID    string `json:"repo_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

// UpdateBindingRequest is the body for PATCH /api/cloud/bindings/{id}.
// Only non-empty fields are applied.
type UpdateBindingRequest struct {
	MachineID string `json:"machine_id,omitempty"`
	RepoID    string `json:"repo_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

// ListMachinesResponse is the response for GET /api/cloud/machines.
type ListMachinesResponse struct {
	Machines []*Machine `json:"machines"`
}

// ListJobsResponse is the response for GET /api/cloud/jobs.
type ListJobsResponse struct {
	Jobs []*JobRecord `json:"jobs"`
}

// ListRunsResponse is the response for GET /api/cloud/runs.
type ListRunsResponse struct {
	Runs []*RunRecord `json:"runs"`
}

// ConfigCounts holds aggregate counts for all cloud resources.
type ConfigCounts struct {
	Projects int `json:"projects"`
	Repos    int `json:"repos"`
	Machines int `json:"machines"`
	Bindings int `json:"bindings"`
	Jobs     int `json:"jobs"`
	Runs     int `json:"runs"`
}

// CloudConfigSummary is the response for GET /api/cloud/config.
type CloudConfigSummary struct {
	Projects []*Project  `json:"projects"`
	Repos    []*Repo     `json:"repos"`
	Machines []*Machine  `json:"machines"`
	Bindings []*Binding  `json:"bindings"`
	Counts   ConfigCounts `json:"counts"`
}

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
