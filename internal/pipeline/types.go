// Package pipeline derives the read-only delivery board served at
// GET /api/pipeline: for each tracked repository, which stage every item
// (issue → PR → acceptance → merge) is in, and why. It holds pure data types,
// stage derivation and run.log parsing; gathering the evidence from VCS,
// the acceptance store and local files is done by the caller.
package pipeline

// Stage is the delivery stage of one item.
type Stage string

const (
	StageQueued            Stage = "queued"
	StageImplementing      Stage = "implementing"
	StagePROpen            Stage = "pr_open"
	StageCIPending         Stage = "ci_pending"
	StageCIFailed          Stage = "ci_failed"
	StageAcceptancePending Stage = "acceptance_pending"
	StageAcceptanceRunning Stage = "acceptance_running"
	StageAccepted          Stage = "accepted"
	StageRejected          Stage = "rejected"
	StageInfraRetry        Stage = "infra_retry"
	StageMerged            Stage = "merged"
	StageFailed            Stage = "failed"
	StageBlocked           Stage = "blocked"
)

// Snapshot is the cacheable, request-independent part of the board.
type Snapshot struct {
	GeneratedAt string      `json:"generated_at"`
	Repos       []RepoBoard `json:"repos"`
	Events      []Event     `json:"events"`
	// GlobalQueue is the single cross-repository work queue in dispatch
	// order (priority tier, manual rank, age). Additive: the per-repo
	// Queue fields keep their shape.
	GlobalQueue []GlobalQueueEntry `json:"global_queue"`
}

// GlobalQueueEntry is one issue of the unified queue.
type GlobalQueueEntry struct {
	Position int    `json:"position"`
	Key      string `json:"key"` // owner/repo#N
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title,omitempty"`
	Tier     string `json:"tier"`           // P0..P3
	Rank     int    `json:"rank,omitempty"` // 1-based manual rank, 0 = unranked
	State    string `json:"state"`          // in_flight | eligible | blocked:<r> | parked:<r> | waiting:dependency
}

// Board is the full /api/pipeline response.
type Board struct {
	Stale      bool   `json:"stale"`
	Refreshing bool   `json:"refreshing"`
	Error      string `json:"error,omitempty"`
	Snapshot
	Runtime Runtime `json:"runtime"`
}

// Runtime describes the local runner, not any one repository.
type Runtime struct {
	Version   string    `json:"version"`
	Scheduler Scheduler `json:"scheduler"`
	Execution Execution `json:"execution"`
}

// Scheduler mirrors the existing /api/run/status payload.
type Scheduler struct {
	Status          string `json:"status"` // "running" | "idle"
	IntervalMinutes int    `json:"interval_minutes"`
	Paused          bool   `json:"paused"`
	NextFireUnixMs  int64  `json:"next_fire_unix_ms,omitempty"`
}

// Execution reports whether the Offload execution profile is usable.
type Execution struct {
	Ready bool   `json:"ready"`
	Error string `json:"error,omitempty"`
}

// RepoBoard is the board for one repository.
type RepoBoard struct {
	Stale              bool   `json:"stale"`
	Repo               string `json:"repo"`
	Enabled            bool   `json:"enabled"`
	Platform           string `json:"platform"`
	AutoMerge          bool   `json:"auto_merge"`
	CIRequired         bool   `json:"ci_required"`
	AcceptanceRequired bool   `json:"acceptance_required"`
	QueueIssue         int    `json:"queue_issue,omitempty"`
	Queue              *Queue `json:"queue,omitempty"`
	Items              []Item `json:"items"`
	// Error is set when the repository could not be read at all.
	Error string `json:"error,omitempty"`
}

// Queue is the repository work queue (queue_issue's ordered sub-issues).
type Queue struct {
	Issue    int            `json:"issue"`
	Entries  []QueueEntry   `json:"entries"`
	Decision *QueueDecision `json:"decision,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// QueueEntry is one sub-issue of the queue issue, in provider order.
type QueueEntry struct {
	Position int    `json:"position"`
	Number   int    `json:"number"`
	Title    string `json:"title"`
	State    string `json:"state"`
	// Action/Reason carry the runner's latest queue decision when it is about
	// this entry ("pick" | "wait" | "blocked").
	Action string `json:"action,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// QueueDecision is the runner's most recent queue decision, taken from run.log.
type QueueDecision struct {
	Action string `json:"action"` // "pick" | "wait" | "blocked"
	Issue  int    `json:"issue,omitempty"`
	Reason string `json:"reason,omitempty"`
	At     string `json:"at"`
}

// Item is one issue travelling through the delivery pipeline.
type Item struct {
	Issue         int      `json:"issue"`
	Title         string   `json:"title"`
	Stage         Stage    `json:"stage"`
	Why           string   `json:"why"`
	Evidence      []string `json:"evidence"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
	InQueue       bool     `json:"in_queue,omitempty"`
	QueuePosition int      `json:"queue_position,omitempty"`

	Run        *RunInfo        `json:"run,omitempty"`
	PR         *PRInfo         `json:"pr,omitempty"`
	Acceptance *AcceptanceInfo `json:"acceptance,omitempty"`
	Delivery   *Delivery       `json:"delivery,omitempty"`
}

// RunInfo is the latest local operator run for the issue (no prompts, no output).
type RunInfo struct {
	Operator  string `json:"operator"`
	Status    string `json:"status"`
	Stage     string `json:"stage,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Live      bool   `json:"live,omitempty"`
}

// PRInfo describes the item's pull request.
type PRInfo struct {
	Number     int    `json:"number"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
	State      string `json:"state"`
	Draft      bool   `json:"draft,omitempty"`
	HeadBranch string `json:"head_branch,omitempty"`
	HeadSHA    string `json:"head_sha,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	CI         string `json:"ci,omitempty"` // success | failure | pending | no-checks | unknown
	CIError    string `json:"ci_error,omitempty"`
	Mergeable  string `json:"mergeable,omitempty"` // clean | conflict | pending | unknown
}

// AcceptanceInfo is the latest acceptance record for the PR's current contract.
type AcceptanceInfo struct {
	Required        bool   `json:"required"`
	Running         bool   `json:"running,omitempty"`
	Verdict         string `json:"verdict,omitempty"` // passed | failed | blocked
	Valid           bool   `json:"valid,omitempty"`   // record satisfies the current contract
	Summary         string `json:"summary,omitempty"` // truncated reviewer summary
	Attempts        int    `json:"attempts"`
	InfraAttempts   int    `json:"infra_attempts"`
	MaxInfraRetries int    `json:"max_infra_attempts,omitempty"`
	LatestIsInfra   bool   `json:"latest_is_infra,omitempty"`
	NextRetryAt     string `json:"next_retry_at,omitempty"`
	CompletedAt     string `json:"completed_at,omitempty"`
	PlannerSession  string `json:"planner_session,omitempty"`
	ReviewerSession string `json:"reviewer_session,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Delivery is the merge result of a merged item.
type Delivery struct {
	PR             int    `json:"pr"`
	PRURL          string `json:"pr_url,omitempty"`
	MergeCommitSHA string `json:"merge_commit_sha,omitempty"`
	MergedAt       string `json:"merged_at,omitempty"`
}

// Event is one parsed run.log line.
type Event struct {
	Time   string            `json:"time"`
	Level  string            `json:"level"`
	Event  string            `json:"event"`
	Fields map[string]string `json:"fields"`
}
