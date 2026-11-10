package coordinator

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Public job statuses. They describe the coordinator task, not the delivery:
// a successful implementation job does not mean CI, review or merge finished.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSuccess   = "success"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

const (
	MaxJobList     = 500
	DefaultJobList = 100
	maxJobSummary  = 4000
)

// JobFilter narrows a job listing. Zero fields do not filter.
type JobFilter struct {
	Repo   string
	Issue  int
	PR     int
	Status string
	Limit  int
}

// Job is one coordinator task as a read-only run row. It carries no payload,
// prompt, credential or filesystem path.
type Job struct {
	ID          string     `json:"id"`
	Repo        string     `json:"repo"`
	Issue       int        `json:"issue"`
	PR          int        `json:"pr"`
	Title       string     `json:"title,omitempty"`
	Kind        string     `json:"kind"`
	Operator    string     `json:"operator"`
	Stage       string     `json:"stage"`
	Trigger     string     `json:"trigger"`
	Status      string     `json:"status"`
	Detail      string     `json:"detail,omitempty"`
	Worker      string     `json:"worker,omitempty"`
	Attempt     int        `json:"attempt"`
	Epoch       int64      `json:"epoch"`
	Tags        []string   `json:"tags"`
	QueuedAt    time.Time  `json:"queued_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ElapsedMs   *int64     `json:"elapsed_ms,omitempty"`
	Waiting     string     `json:"waiting_reason,omitempty"`
	ErrorKind   string     `json:"error_kind,omitempty"`
	Outcome     string     `json:"outcome,omitempty"`
	PRURL       string     `json:"pr_url,omitempty"`
	LogAvail    bool       `json:"has_logs"`
	GeneratedAt time.Time  `json:"generated_at"`
}

// JobAttempt is one execution attempt of a task.
type JobAttempt struct {
	Attempt   int64      `json:"attempt"`
	Epoch     int64      `json:"epoch"`
	Worker    string     `json:"worker"`
	Class     string     `json:"class"`
	Status    string     `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	ElapsedMs *int64     `json:"elapsed_ms,omitempty"`
	ErrorKind string     `json:"error_kind,omitempty"`
}

// TimelineEvent is one dated step of a job's life.
type TimelineEvent struct {
	At      time.Time `json:"at"`
	Event   string    `json:"event"`
	Worker  string    `json:"worker,omitempty"`
	Attempt int64     `json:"attempt,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// JobArtifacts are the links and identifiers a finished job produced.
type JobArtifacts struct {
	PRNumber  int    `json:"pr_number,omitempty"`
	PRURL     string `json:"pr_url,omitempty"`
	Branch    string `json:"branch,omitempty"`
	HeadSHA   string `json:"head_sha,omitempty"`
	Verdict   string `json:"verdict,omitempty"`
	NextOwner string `json:"next_owner,omitempty"`
}

// JobDetail is the page behind one job.
type JobDetail struct {
	Job
	Attempts  []JobAttempt    `json:"attempts"`
	Timeline  []TimelineEvent `json:"timeline"`
	Artifacts JobArtifacts    `json:"artifacts"`
	Summary   string          `json:"summary,omitempty"`
	Error     string          `json:"error,omitempty"`
	Related   []Job           `json:"related"`
}

type jobRow struct {
	ID                  string
	Repo                string
	Issue               int
	Pr                  int
	Kind                string
	Operator            string
	State               string
	Tags                []string `gorm:"serializer:json"`
	Attempt             int
	LeaseOwner          string
	LeaseExpiresAt      *time.Time
	FencingToken        int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Title               string
	RepairReason        string
	Reopen              string
	Durable             bool
	ResultStatus        string
	ResultOutcome       string
	ResultError         string
	ResultInfra         string
	ResultPRNumber      int
	ResultPRURL         string
	ResultBranch        string
	ResultHead          string
	ResultVerdict       string
	ResultNextOwner     string
	ResultSummary       string
	ApplicationAttempts int
	ApplicationError    string
}

const jobSelect = "id, repo, issue, pr, kind, operator, state, tags, attempt, lease_owner, lease_expires_at, fencing_token, created_at, updated_at, " +
	"COALESCE(json_extract(payload, '$.title'), '') AS title, " +
	"COALESCE(json_extract(payload, '$.repair.reason'), '') AS repair_reason, " +
	"COALESCE(json_extract(payload, '$.reopen_event_id'), '') AS reopen, " +
	"COALESCE(json_extract(payload, '$.durable_execution') = 1, 0) AS durable, " +
	"COALESCE(json_extract(result, '$.status'), '') AS result_status, " +
	"COALESCE(json_extract(result, '$.outcome'), '') AS result_outcome, " +
	"COALESCE(json_extract(result, '$.error'), '') AS result_error, " +
	"COALESCE(json_extract(result, '$.infra_kind'), '') AS result_infra, " +
	"COALESCE(json_extract(result, '$.pr_number'), 0) AS result_pr_number, " +
	"COALESCE(json_extract(result, '$.pr_url'), '') AS result_pr_url, " +
	"COALESCE(json_extract(result, '$.branch'), '') AS result_branch, " +
	"COALESCE(json_extract(result, '$.head_sha'), '') AS result_head, " +
	"COALESCE(json_extract(result, '$.acceptance.verdict'), '') AS result_verdict, " +
	"COALESCE(json_extract(result, '$.next_owner'), '') AS result_next_owner, " +
	"substr(COALESCE(json_extract(result, '$.output'), ''), 1, 4000) AS result_summary, " +
	"application_attempts, application_error"

// jobStage names the pipeline stage a task kind belongs to.
func jobStage(kind, repairReason string) string {
	switch kind {
	case "implement":
		return "implement"
	case "acceptance":
		return "acceptance"
	case "repair":
		switch repairReason {
		case "ci", "":
			return "repair-ci"
		case "acceptance":
			return "repair-acceptance"
		case "pr_feedback":
			return "repair-feedback"
		case "merge_conflict":
			return "repair-conflict"
		}
		return "repair"
	case "reply":
		return "reply"
	}
	return kind
}

// jobTrigger states why the task exists, from facts recorded at enqueue time.
func jobTrigger(kind, repairReason, reopen string) string {
	switch kind {
	case "implement":
		if reopen != "" {
			return "issue_reopened"
		}
		return "issue_queued"
	case "acceptance":
		return "pr_ready_for_review"
	case "reply":
		// Reply-kind tasks cover answers, progress checks and custom operators;
		// the task records no finer cause, so the operator name is shown instead.
		return "operator"
	case "repair":
		switch repairReason {
		case "", "ci":
			return "ci_failed"
		case "acceptance":
			return "acceptance_failed"
		case "pr_feedback":
			return "pr_feedback"
		case "merge_conflict":
			return "merge_conflict"
		case "development_verification":
			return "development_verification"
		}
		return "repair"
	}
	return "unknown"
}

func publicURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := max
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func attemptDuration(start time.Time, end *time.Time) *int64 {
	if start.IsZero() || start.Year() < 2000 || end == nil || end.Before(start) {
		return nil
	}
	ms := end.Sub(start).Milliseconds()
	return &ms
}

func toJob(r jobRow, latest *TaskAttempt, logs bool, now time.Time) Job {
	j := Job{ID: r.ID, Repo: r.Repo, Issue: r.Issue, PR: r.Pr, Title: r.Title, Kind: r.Kind, Operator: r.Operator,
		Stage: jobStage(r.Kind, r.RepairReason), Trigger: jobTrigger(r.Kind, r.RepairReason, r.Reopen),
		Worker: r.LeaseOwner, Attempt: r.Attempt, Epoch: r.FencingToken, Tags: r.Tags, QueuedAt: r.CreatedAt,
		LogAvail: logs, GeneratedAt: now}
	if j.Tags == nil {
		j.Tags = []string{}
	}
	if latest != nil {
		start := latest.StartedAt
		j.StartedAt, j.EndedAt = &start, latest.EndedAt
		j.Epoch = latest.LeaseEpoch
		j.Attempt = int(latest.AttemptNumber)
		if j.Worker == "" {
			j.Worker = latest.WorkerID
		}
		j.ErrorKind = latest.ErrorKind
	}
	switch r.State {
	case "pending":
		j.Status = JobQueued
		j.Worker = ""
		j.StartedAt, j.EndedAt = nil, nil
	case "leased":
		j.Status = JobRunning
		switch {
		case r.LeaseExpiresAt == nil:
			j.Detail = "finalizing"
		case !r.LeaseExpiresAt.After(now) && r.Durable:
			j.Detail = "recovery_pending"
		case !r.LeaseExpiresAt.After(now):
			j.Detail = "lease_expired"
		}
	case "done":
		j.Status = JobSuccess
		j.Outcome = r.ResultStatus
	case "failed":
		j.Status = JobFailed
		j.Outcome = r.ResultStatus
	case "cancelled":
		j.Status = JobCancelled
	default:
		j.Status = r.State
	}
	if j.StartedAt != nil {
		if j.Status == JobRunning {
			// Live duration is derived by the reader from the recorded start.
			j.ElapsedMs = nil
		} else {
			j.ElapsedMs = attemptDuration(*j.StartedAt, j.EndedAt)
		}
	}
	if r.ResultInfra != "" && j.ErrorKind == "" {
		j.ErrorKind = r.ResultInfra
	}
	j.PRURL = publicURL(r.ResultPRURL)
	return j
}

// latestAttempts returns the newest attempt of each task.
func latestAttempts(db *gorm.DB, ids []string) (map[string]*TaskAttempt, error) {
	out := map[string]*TaskAttempt{}
	if len(ids) == 0 {
		return out, nil
	}
	var attempts []TaskAttempt
	if err := db.Where("task_id IN ?", ids).Order("attempt_number, lease_epoch").Find(&attempts).Error; err != nil {
		return nil, err
	}
	for i := range attempts {
		a := attempts[i]
		out[a.TaskID] = &a
	}
	return out, nil
}

func logTasks(db *gorm.DB, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	var have []string
	if err := db.Model(&LogStream{}).Distinct().Where("task_id IN ?", ids).Pluck("task_id", &have).Error; err != nil {
		return nil, err
	}
	for _, id := range have {
		out[id] = true
	}
	return out, nil
}

// Jobs lists tasks newest first. It never contacts a worker or provider and
// never renews or expires a lease.
func (s *Store) Jobs(ctx context.Context, f JobFilter) ([]Job, error) {
	limit := f.Limit
	if limit < 1 {
		limit = DefaultJobList
	}
	if limit > MaxJobList {
		limit = MaxJobList
	}
	q := s.db.WithContext(ctx).Table("coordinator_tasks").Select(jobSelect)
	if f.Repo != "" {
		q = q.Where("repo = ?", f.Repo)
	}
	if f.Issue > 0 {
		q = q.Where("issue = ?", f.Issue)
	}
	if f.PR > 0 {
		q = q.Where("pr = ?", f.PR)
	}
	switch f.Status {
	case "":
	case JobQueued:
		q = q.Where("state = 'pending'")
	case JobRunning:
		q = q.Where("state = 'leased'")
	case JobSuccess:
		q = q.Where("state = 'done'")
	case JobFailed:
		q = q.Where("state = 'failed'")
	case JobCancelled:
		q = q.Where("state = 'cancelled'")
	default:
		return nil, ErrInvalid
	}
	var rows []jobRow
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return s.jobsFromRows(ctx, rows)
}

func (s *Store) jobsFromRows(ctx context.Context, rows []jobRow) ([]Job, error) {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	db := s.db.WithContext(ctx)
	attempts, err := latestAttempts(db, ids)
	if err != nil {
		return nil, err
	}
	logs, err := logTasks(db, ids)
	if err != nil {
		return nil, err
	}
	now := controllerNow()
	out := make([]Job, 0, len(rows))
	for _, r := range rows {
		out = append(out, toJob(r, attempts[r.ID], logs[r.ID], now))
	}
	return out, nil
}

// timelineActions maps audit actions that mark a visible step to their label.
// Heartbeats, claims and enqueues are covered by attempts and creation time.
var timelineActions = map[string]string{
	"result-applied":            "result_applied",
	"result-apply-failed":       "result_apply_failed",
	"result-apply-deferred":     "result_apply_deferred",
	"cancel":                    "cancelled",
	"expire":                    "lease_expired",
	"recover-execution":         "recovery_started",
	"recovery-exhausted":        "recovery_exhausted",
	"interruption-retained":     "interruption_retained",
	"acceptance-superseded":     "acceptance_superseded",
	"adopt-legacy-interruption": "interruption_adopted",
}

// Job returns one task with its attempts, timeline, artifacts and sibling runs
// for the same issue or pull request.
func (s *Store) Job(ctx context.Context, id string) (*JobDetail, error) {
	if id == "" || len(id) > 128 {
		return nil, ErrStaleLease
	}
	var rows []jobRow
	if err := s.db.WithContext(ctx).Table("coordinator_tasks").Select(jobSelect).Where("id = ?", id).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, ErrStaleLease
	}
	r := rows[0]
	db := s.db.WithContext(ctx)
	var attempts []TaskAttempt
	if err := db.Where("task_id = ?", id).Order("attempt_number, lease_epoch").Find(&attempts).Error; err != nil {
		return nil, err
	}
	var latest *TaskAttempt
	if n := len(attempts); n > 0 {
		latest = &attempts[n-1]
	}
	logs, err := logTasks(db, []string{id})
	if err != nil {
		return nil, err
	}
	now := controllerNow()
	d := &JobDetail{Job: toJob(r, latest, logs[id], now), Attempts: []JobAttempt{}, Timeline: []TimelineEvent{}, Related: []Job{}}
	if d.Status == JobQueued {
		if reason := s.waitingReason(ctx, id); reason != "" {
			d.Waiting = reason
		}
	}
	d.Timeline = append(d.Timeline, TimelineEvent{At: r.CreatedAt, Event: "queued"})
	for _, a := range attempts {
		d.Attempts = append(d.Attempts, JobAttempt{Attempt: a.AttemptNumber, Epoch: a.LeaseEpoch, Worker: a.WorkerID, Class: a.Kind,
			Status: a.Status, StartedAt: a.StartedAt, EndedAt: a.EndedAt, ElapsedMs: attemptDuration(a.StartedAt, a.EndedAt), ErrorKind: a.ErrorKind})
		d.Timeline = append(d.Timeline, TimelineEvent{At: a.StartedAt, Event: "started", Worker: a.WorkerID, Attempt: a.AttemptNumber})
		if a.EndedAt != nil {
			d.Timeline = append(d.Timeline, TimelineEvent{At: *a.EndedAt, Event: "attempt_" + a.Status, Worker: a.WorkerID, Attempt: a.AttemptNumber, Detail: a.ErrorKind})
		}
	}
	var audits []Audit
	if err := db.Where("task = ? AND action <> 'heartbeat'", id).Order("id").Limit(200).Find(&audits).Error; err != nil {
		return nil, err
	}
	for _, a := range audits {
		label, ok := timelineActions[a.Action]
		if !ok {
			if reason, rel := strings.CutPrefix(a.Action, "release:"); rel {
				label, ok = "released", true
				d.Timeline = append(d.Timeline, TimelineEvent{At: a.CreatedAt, Event: label, Worker: a.Worker, Detail: clip(reason, 64)})
			}
			continue
		}
		d.Timeline = append(d.Timeline, TimelineEvent{At: a.CreatedAt, Event: label, Worker: a.Worker})
	}
	sort.SliceStable(d.Timeline, func(i, j int) bool { return d.Timeline[i].At.Before(d.Timeline[j].At) })
	if r.State == "done" || r.State == "failed" || r.State == "cancelled" {
		d.Summary = clip(r.ResultSummary, maxJobSummary)
		d.Error = clip(r.ResultError, maxJobSummary)
		d.Artifacts = JobArtifacts{PRNumber: r.ResultPRNumber, PRURL: publicURL(r.ResultPRURL), Branch: r.ResultBranch,
			HeadSHA: r.ResultHead, Verdict: r.ResultVerdict, NextOwner: r.ResultNextOwner}
		if d.Artifacts.PRNumber == 0 && r.Pr > 0 {
			d.Artifacts.PRNumber = r.Pr
		}
	}
	if r.ApplicationError != "" && r.State != "pending" {
		d.Error = clip(strings.TrimSpace(d.Error+"\n"+r.ApplicationError), maxJobSummary)
	}
	q := s.db.WithContext(ctx).Table("coordinator_tasks").Select(jobSelect).Where("repo = ?", r.Repo)
	if r.Pr > 0 {
		q = q.Where("(pr = ? OR (issue = ? AND ? > 0))", r.Pr, r.Issue, r.Issue)
	} else {
		q = q.Where("issue = ?", r.Issue)
	}
	var siblings []jobRow
	if err := q.Order("created_at, id").Limit(50).Scan(&siblings).Error; err != nil {
		return nil, err
	}
	related, err := s.jobsFromRows(ctx, siblings)
	if err != nil {
		return nil, err
	}
	d.Related = related
	return d, nil
}

// waitingReason explains why a queued task has not started, from the same
// board rules the pipeline page uses.
func (s *Store) waitingReason(ctx context.Context, id string) string {
	board, err := s.Board(ctx)
	if err != nil {
		return ""
	}
	for _, t := range board.Tasks {
		if t.ID == id {
			return t.WaitingReason
		}
	}
	return ""
}

// Runner is one worker as a read-only row.
type Runner struct {
	Name          string     `json:"name"`
	Online        bool       `json:"online"`
	Revoked       bool       `json:"revoked"`
	Paused        bool       `json:"paused"`
	Tags          []string   `json:"tags"`
	Roles         []string   `json:"roles"`
	MaxParallel   int        `json:"max_parallel"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	LastHeartbeat *time.Time `json:"last_heartbeat,omitempty"`
	Running       []Job      `json:"running"`
}

// Runners lists every worker with the jobs it currently holds.
func (s *Store) Runners(ctx context.Context) ([]Runner, time.Time, error) {
	board, err := s.Board(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	var ids []string
	for _, w := range board.Workers {
		ids = append(ids, w.RunningTasks...)
	}
	byID := map[string]Job{}
	if len(ids) > 0 {
		var rows []jobRow
		if err := s.db.WithContext(ctx).Table("coordinator_tasks").Select(jobSelect).Where("id IN ?", ids).Scan(&rows).Error; err != nil {
			return nil, time.Time{}, err
		}
		jobs, err := s.jobsFromRows(ctx, rows)
		if err != nil {
			return nil, time.Time{}, err
		}
		for _, j := range jobs {
			byID[j.ID] = j
		}
	}
	out := make([]Runner, 0, len(board.Workers))
	for _, w := range board.Workers {
		r := Runner{Name: w.Name, Online: w.Live, Revoked: w.Revoked, Paused: w.Paused, Tags: w.Tags, Roles: w.Roles, MaxParallel: w.MaxParallel,
			LastSeen: w.LastSeen, LastHeartbeat: w.LastHeartbeat, Running: []Job{}}
		for _, id := range w.RunningTasks {
			if j, ok := byID[id]; ok {
				r.Running = append(r.Running, j)
			}
		}
		out = append(out, r)
	}
	return out, board.GeneratedAt, nil
}
