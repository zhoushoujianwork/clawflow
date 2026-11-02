package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Transcript content is execution output captured by Offload and relayed by the
// worker that owns the execution. It lives in its own tables, is never part of
// the board/dashboard model and is never copied to an issue or PR.

var (
	ErrTranscriptOrder    = errors.New("transcript page out of order")
	ErrTranscriptIdentity = errors.New("transcript identity conflict")
	ErrTaskNotFound       = errors.New("task not found")
)

// TranscriptAppender is the optional worker-store capability for execution output.
type TranscriptAppender interface {
	AppendTranscript(context.Context, string, string, TranscriptUpload) (TranscriptAck, error)
}

// TranscriptReader is the optional admin capability for reading execution output.
type TranscriptReader interface {
	ReadTranscript(context.Context, TranscriptQuery) (*TranscriptView, error)
}

const (
	MaxTranscriptRequestBytes = 2 << 20
	MaxTranscriptInvocations  = 4
	MaxTranscriptPageEvents   = 1000
	MaxTranscriptEventBytes   = 160 << 10
	MaxTranscriptStoredBytes  = 16 << 20
	TranscriptRetention       = 14 * 24 * time.Hour

	DefaultTranscriptReadEvents = 200
	MaxTranscriptReadEvents     = 500
	MaxTranscriptReadBytes      = 512 << 10
)

type TaskTranscript struct {
	TaskID        string `gorm:"primaryKey" json:"-"`
	LeaseEpoch    int64  `gorm:"primaryKey" json:"epoch"`
	Invocation    int    `gorm:"primaryKey" json:"invocation"`
	AttemptNumber int64  `json:"attempt"`
	WorkerID      string `json:"worker"`
	CallerRunID   string `json:"caller_run_id"`
	RunID         string `json:"run_id"`
	CaptureID     string `json:"capture_id"`
	Availability  string `json:"availability"`
	State         string `json:"state"`
	Completeness  string `json:"completeness"`
	Reason        string `json:"reason,omitempty"`
	// Summary is Offload's capture summary document, relayed verbatim.
	Summary     json.RawMessage `gorm:"type:jsonb" json:"summary,omitempty"`
	LastSeq     int64           `json:"last_seq"`
	EventCount  int64           `json:"event_count"`
	StoredBytes int64           `json:"stored_bytes"`
	Capped      bool            `json:"capped"`
	Cursor      string          `json:"-"`
	EndOfStream bool            `json:"end_of_stream"`
	CreatedAt   time.Time       `json:"started_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func (TaskTranscript) TableName() string { return "coordinator_task_transcripts" }

type TaskTranscriptEvent struct {
	TaskID     string          `gorm:"primaryKey"`
	LeaseEpoch int64           `gorm:"primaryKey"`
	Invocation int             `gorm:"primaryKey"`
	Seq        int64           `gorm:"primaryKey"`
	Kind       string          `gorm:"column:kind"`
	Body       json.RawMessage `gorm:"type:jsonb"`
	Size       int
}

func (TaskTranscriptEvent) TableName() string { return "coordinator_task_transcript_events" }

// TranscriptUpload is one worker page. Identity (task, worker, fence) comes
// from the URL, token and fencing token, never from the page body.
type TranscriptUpload struct {
	FencingToken int64             `json:"fencing_token"`
	CallerRunID  string            `json:"caller_run_id"`
	RunID        string            `json:"run_id"`
	CaptureID    string            `json:"capture_id"`
	Availability string            `json:"availability"`
	State        string            `json:"state"`
	Completeness string            `json:"completeness"`
	Reason       string            `json:"reason"`
	Summary      json.RawMessage   `json:"summary"`
	CursorIn     string            `json:"cursor_in"`
	CursorOut    string            `json:"cursor_out"`
	HasMore      bool              `json:"has_more"`
	EndOfStream  bool              `json:"end_of_stream"`
	Events       []json.RawMessage `json:"events"`
}

type TranscriptAck struct {
	OK              bool  `json:"ok"`
	AcceptedThrough int64 `json:"accepted_through"`
	Capped          bool  `json:"capped,omitempty"`
}

var (
	transcriptIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	transcriptAvailable   = map[string]bool{"available": true, "not_yet_available": true, "no_captured_content": true, "capture_disabled": true, "expired": true, "unreadable": true, "unsupported_backend": true, "unsupported_version": true, "not_found": true, "ambiguous": true, "cursor_invalid": true, "identity_mismatch": true}
	transcriptStates      = map[string]bool{"": true, "running": true, "ended": true, "abandoned": true}
	transcriptComplete    = map[string]bool{"": true, "in_progress": true, "complete": true, "bounded": true, "incomplete": true}
	transcriptEventKinds  = map[string]bool{"capture_start": true, "attempt_start": true, "message": true, "tool_call": true, "tool_result": true, "backend_error": true, "gap": true, "execution_end": true}
	transcriptSummaryKeep = 8 << 10
)

type transcriptEventHead struct {
	Seq         int64  `json:"seq"`
	Type        string `json:"type"`
	RunID       string `json:"run_id"`
	CallerRunID string `json:"caller_run_id"`
}

// validateTranscriptUpload checks shape and bounds only; authority is checked
// against the task row inside the transaction.
func validateTranscriptUpload(in *TranscriptUpload) ([]transcriptEventHead, error) {
	if !transcriptIDPattern.MatchString(in.CallerRunID) || (in.RunID != "" && !transcriptIDPattern.MatchString(in.RunID)) || (in.CaptureID != "" && !transcriptIDPattern.MatchString(in.CaptureID)) {
		return nil, ErrInvalid
	}
	if !transcriptAvailable[in.Availability] || !transcriptStates[in.State] || !transcriptComplete[in.Completeness] {
		return nil, ErrInvalid
	}
	if len(in.Reason) > 512 || len(in.CursorIn) > 256 || len(in.CursorOut) > 256 || len(in.Events) > MaxTranscriptPageEvents || len(in.Summary) > transcriptSummaryKeep {
		return nil, ErrInvalid
	}
	if len(in.Summary) > 0 && !json.Valid(in.Summary) {
		return nil, ErrInvalid
	}
	if len(in.Events) > 0 && in.Availability != "available" {
		return nil, ErrInvalid
	}
	heads := make([]transcriptEventHead, len(in.Events))
	var previous int64
	for i, raw := range in.Events {
		if len(raw) > MaxTranscriptEventBytes {
			return nil, ErrInvalid
		}
		var head transcriptEventHead
		d := json.NewDecoder(bytes.NewReader(raw))
		if d.Decode(&head) != nil || head.Seq < 1 || !transcriptEventKinds[head.Type] || head.CallerRunID != in.CallerRunID || !transcriptIDPattern.MatchString(head.RunID) || (in.RunID != "" && head.RunID != in.RunID) {
			return nil, ErrInvalid
		}
		if head.Seq <= previous {
			return nil, ErrTranscriptOrder
		}
		previous = head.Seq
		heads[i] = head
	}
	return heads, nil
}

// AppendTranscript stores a page for the caller's own fenced execution. Pages
// that were already accepted are acknowledged without change; a page that skips
// ahead of the stored sequence is refused so history is never silently joined.
func (s *Store) AppendTranscript(ctx context.Context, taskID, worker string, in TranscriptUpload) (TranscriptAck, error) {
	var ack TranscriptAck
	heads, err := validateTranscriptUpload(&in)
	if err != nil {
		return ack, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id = ?", taskID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStaleLease
			}
			return err
		}
		// Same authority as heartbeat and report: current owner, current epoch, no committed report.
		if task.State != "leased" || task.LeaseOwner != worker || task.FencingToken != in.FencingToken || len(task.Result) != 0 {
			return ErrStaleLease
		}
		var now time.Time
		if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return err
		}
		var row TaskTranscript
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "task_id = ? AND lease_epoch = ? AND caller_run_id = ?", taskID, task.FencingToken, in.CallerRunID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A new Offload invocation inside this fenced execution (for example a
			// fresh session after a rejected resume) is a separate, ordered log.
			var count int64
			if err := tx.Model(&TaskTranscript{}).Where("task_id = ? AND lease_epoch = ?", taskID, task.FencingToken).Count(&count).Error; err != nil {
				return err
			}
			if count >= MaxTranscriptInvocations {
				return ErrTranscriptIdentity
			}
			row = TaskTranscript{TaskID: taskID, LeaseEpoch: task.FencingToken, Invocation: int(count) + 1, AttemptNumber: int64(task.Attempt + 1), WorkerID: worker, CallerRunID: in.CallerRunID, CreatedAt: now}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if row.WorkerID != worker || (row.RunID != "" && in.RunID != "" && row.RunID != in.RunID) || (row.CaptureID != "" && in.CaptureID != "" && row.CaptureID != in.CaptureID) {
			return ErrTranscriptIdentity
		}
		newEvents := make([]TaskTranscriptEvent, 0, len(in.Events))
		next := row.LastSeq
		for i, raw := range in.Events {
			seq := heads[i].Seq
			if seq <= row.LastSeq {
				continue // already accepted
			}
			if row.Capped {
				break
			}
			if seq != next+1 {
				return ErrTranscriptOrder
			}
			if row.EndOfStream {
				return ErrTranscriptOrder
			}
			if row.StoredBytes+int64(len(raw)) > MaxTranscriptStoredBytes {
				row.Capped = true
				row.Reason = "controller storage limit reached; later content is not retained"
				break
			}
			newEvents = append(newEvents, TaskTranscriptEvent{TaskID: taskID, LeaseEpoch: row.LeaseEpoch, Invocation: row.Invocation, Seq: seq, Kind: heads[i].Type, Body: raw, Size: len(raw)})
			row.StoredBytes += int64(len(raw))
			next = seq
		}
		if len(newEvents) > 0 {
			if err := tx.CreateInBatches(newEvents, 100).Error; err != nil {
				return err
			}
			row.LastSeq, row.EventCount = next, row.EventCount+int64(len(newEvents))
		}
		if row.RunID == "" {
			row.RunID = in.RunID
		}
		if row.CaptureID == "" {
			row.CaptureID = in.CaptureID
		}
		// Later pages may refine, but never regress, what is known.
		if in.Availability == "available" || row.Availability == "" || row.Availability == "not_yet_available" {
			row.Availability = in.Availability
		}
		if !(row.State == "ended" && in.State != "ended") && in.State != "" {
			row.State = in.State
		}
		if in.Completeness != "" && !(row.Completeness == "incomplete" && in.Completeness != "incomplete") {
			row.Completeness = in.Completeness
		}
		if in.Reason != "" && !row.Capped {
			row.Reason = in.Reason
		}
		if len(in.Summary) > 0 && string(in.Summary) != "null" {
			row.Summary = in.Summary
		}
		if in.CursorOut != "" {
			row.Cursor = in.CursorOut
		}
		row.EndOfStream = row.EndOfStream || in.EndOfStream
		row.UpdatedAt = now
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		ack = TranscriptAck{OK: true, AcceptedThrough: row.LastSeq, Capped: row.Capped}
		return nil
	})
	return ack, err
}

// TranscriptQuery selects one execution (lease epoch) and a position in it.
type TranscriptQuery struct {
	TaskID string
	Epoch  int64 // 0 selects the latest execution of the task
	// Invocation 0 selects the latest Offload invocation of that execution.
	Invocation int
	After      int64 // last sequence already held by the client
	Limit      int
}

type TranscriptEpoch struct {
	Epoch        int64  `json:"epoch"`
	Attempt      int64  `json:"attempt"`
	Worker       string `json:"worker"`
	Status       string `json:"status"`
	EventCount   int64  `json:"event_count"`
	EndOfStream  bool   `json:"end_of_stream"`
	AttemptState string `json:"attempt_state,omitempty"`
}

// TranscriptView is the admin read model. Status is one of: available,
// no_output_yet, no_content, not_captured, unsupported, expired, unreadable.
type TranscriptView struct {
	SchemaVersion int               `json:"schema_version"`
	TaskID        string            `json:"task_id"`
	TaskState     string            `json:"task_state"`
	Epoch         int64             `json:"epoch"`
	Invocation    int               `json:"invocation"`
	Invocations   int               `json:"invocations"`
	Attempt       int64             `json:"attempt"`
	Worker        string            `json:"worker,omitempty"`
	Status        string            `json:"status"`
	Reason        string            `json:"reason,omitempty"`
	ExecutionDone bool              `json:"execution_done"`
	Live          bool              `json:"live"`
	Completeness  string            `json:"completeness,omitempty"`
	Gap           bool              `json:"gap"`
	GapReason     string            `json:"gap_reason,omitempty"`
	Capped        bool              `json:"capped"`
	Summary       json.RawMessage   `json:"summary,omitempty"`
	Events        []json.RawMessage `json:"events"`
	NextAfter     int64             `json:"next_after"`
	HasMore       bool              `json:"has_more"`
	Epochs        []TranscriptEpoch `json:"epochs"`
	UpdatedAt     *time.Time        `json:"updated_at,omitempty"`
}

func viewStatus(row *TaskTranscript) (status, reason string) {
	switch row.Availability {
	case "available":
		if row.EventCount == 0 {
			return "no_output_yet", "capture started; no content has been produced yet"
		}
		return "available", ""
	case "not_yet_available", "not_found":
		return "no_output_yet", "Offload has not written content for this execution yet"
	case "no_captured_content":
		return "no_content", "the execution ended without capturable content"
	case "capture_disabled":
		return "not_captured", "capture was not enabled for this execution"
	case "unsupported_backend", "unsupported_version":
		return "unsupported", row.Reason
	case "expired":
		return "expired", "the transcript was removed by retention"
	default:
		return "unreadable", row.Reason
	}
}

// ReadTranscript returns a bounded page of one execution's retained content.
// It reads only stored rows: it never contacts a worker, Offload or a provider.
func (s *Store) ReadTranscript(ctx context.Context, q TranscriptQuery) (*TranscriptView, error) {
	db := s.db.WithContext(ctx)
	var task Task
	if err := db.Select("id", "state", "attempt", "fencing_token", "lease_owner", "result").First(&task, "id = ?", q.TaskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultTranscriptReadEvents
	}
	if limit > MaxTranscriptReadEvents {
		limit = MaxTranscriptReadEvents
	}
	var attempts []TaskAttempt
	if err := db.Where("task_id = ?", q.TaskID).Order("lease_epoch").Find(&attempts).Error; err != nil {
		return nil, err
	}
	var rows []TaskTranscript
	if err := db.Where("task_id = ?", q.TaskID).Order("lease_epoch, invocation").Find(&rows).Error; err != nil {
		return nil, err
	}
	// Rows are ordered by epoch then invocation, so the last row per epoch is the latest.
	byEpoch := map[int64]*TaskTranscript{}
	invocations := map[int64]int{}
	for i := range rows {
		invocations[rows[i].LeaseEpoch]++
		byEpoch[rows[i].LeaseEpoch] = &rows[i]
	}
	view := &TranscriptView{SchemaVersion: 1, TaskID: q.TaskID, TaskState: task.State, Events: []json.RawMessage{}, Epochs: []TranscriptEpoch{}}
	attemptByEpoch := map[int64]TaskAttempt{}
	epochs := []int64{}
	for _, a := range attempts {
		attemptByEpoch[a.LeaseEpoch] = a
		epochs = append(epochs, a.LeaseEpoch)
	}
	for _, row := range rows {
		if _, ok := attemptByEpoch[row.LeaseEpoch]; !ok {
			epochs = append(epochs, row.LeaseEpoch)
		}
	}
	for i := 1; i < len(epochs); i++ { // small insertion sort; attempts are few
		for j := i; j > 0 && epochs[j] < epochs[j-1]; j-- {
			epochs[j], epochs[j-1] = epochs[j-1], epochs[j]
		}
	}
	for _, e := range epochs {
		entry := TranscriptEpoch{Epoch: e}
		if a, ok := attemptByEpoch[e]; ok {
			entry.Attempt, entry.Worker, entry.AttemptState = a.AttemptNumber, a.WorkerID, a.Status
		}
		if row := byEpoch[e]; row != nil {
			entry.Attempt, entry.Worker, entry.EventCount, entry.EndOfStream = row.AttemptNumber, row.WorkerID, row.EventCount, row.EndOfStream
			entry.Status, _ = viewStatus(row)
		} else {
			entry.Status = "not_captured"
		}
		view.Epochs = append(view.Epochs, entry)
	}
	epoch := q.Epoch
	if epoch == 0 && len(epochs) > 0 {
		epoch = epochs[len(epochs)-1]
	}
	if q.Epoch != 0 {
		found := false
		for _, e := range epochs {
			found = found || e == q.Epoch
		}
		if !found {
			return nil, ErrTaskNotFound
		}
	}
	view.Epoch, view.Invocations = epoch, invocations[epoch]
	if a, ok := attemptByEpoch[epoch]; ok {
		view.Attempt, view.Worker = a.AttemptNumber, a.WorkerID
		view.ExecutionDone = a.EndedAt != nil
	}
	row := byEpoch[epoch]
	if q.Invocation > 0 {
		row = nil
		for i := range rows {
			if rows[i].LeaseEpoch == epoch && rows[i].Invocation == q.Invocation {
				row = &rows[i]
			}
		}
	}
	if row == nil {
		// No transcript was ever reported for this execution. Do not invent one.
		switch {
		case len(epochs) == 0:
			view.Status, view.Reason = "no_output_yet", "the task has not started an execution"
		case !view.ExecutionDone && task.State == "leased":
			view.Status, view.Reason, view.Live = "no_output_yet", "the execution is running; no content has been reported yet", true
		default:
			view.Status, view.Reason = "not_captured", "no transcript was reported for this execution (capture not enabled, unsupported, or it ran before this feature)"
		}
		view.NextAfter = q.After
		return view, nil
	}
	view.Worker, view.Attempt, view.Invocation = row.WorkerID, row.AttemptNumber, row.Invocation
	view.Status, view.Reason = viewStatus(row)
	view.Completeness, view.Summary, view.Capped = row.Completeness, row.Summary, row.Capped
	view.Live = !row.EndOfStream && !view.ExecutionDone && task.State == "leased" && row.State != "ended"
	view.UpdatedAt = &row.UpdatedAt
	if row.Capped {
		view.Gap, view.GapReason = true, row.Reason
	}
	if row.Completeness == "incomplete" && !view.Gap {
		view.Gap, view.GapReason = true, "Offload reports the captured content is incomplete"
	}
	var stored []TaskTranscriptEvent
	if err := db.Where("task_id = ? AND lease_epoch = ? AND invocation = ? AND seq > ?", q.TaskID, epoch, row.Invocation, q.After).Order("seq").Limit(limit + 1).Find(&stored).Error; err != nil {
		return nil, err
	}
	budget := MaxTranscriptReadBytes
	view.NextAfter = q.After
	for i, ev := range stored {
		if i >= limit || (len(view.Events) > 0 && budget-ev.Size < 0) {
			view.HasMore = true
			break
		}
		budget -= ev.Size
		view.Events = append(view.Events, ev.Body)
		view.NextAfter = ev.Seq
		if ev.Kind == "gap" {
			view.Gap, view.GapReason = true, "Offload recorded content it could not capture"
		}
	}
	// Positions the client holds that retention removed are not silently skipped.
	if q.After > row.LastSeq {
		view.Gap, view.GapReason = true, "requested position is beyond the retained history"
	}
	if row.Availability == "expired" {
		view.Gap, view.GapReason = true, "history removed by retention"
	}
	return view, nil
}

// PruneTranscripts removes retained content older than the cutoff while keeping
// a header, so the viewer can say "expired" instead of "never captured".
func (s *Store) PruneTranscripts(ctx context.Context, cutoff time.Time) (int64, error) {
	var pruned int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old []TaskTranscript
		if err := tx.Where("updated_at < ? AND availability <> 'expired'", cutoff).Limit(200).Find(&old).Error; err != nil {
			return err
		}
		for _, row := range old {
			if err := tx.Where("task_id = ? AND lease_epoch = ? AND invocation = ?", row.TaskID, row.LeaseEpoch, row.Invocation).Delete(&TaskTranscriptEvent{}).Error; err != nil {
				return err
			}
			if err := tx.Model(&TaskTranscript{}).Where("task_id = ? AND lease_epoch = ? AND invocation = ?", row.TaskID, row.LeaseEpoch, row.Invocation).Updates(map[string]any{"availability": "expired", "reason": "removed by retention", "stored_bytes": 0}).Error; err != nil {
				return err
			}
			pruned++
		}
		return nil
	})
	return pruned, err
}
