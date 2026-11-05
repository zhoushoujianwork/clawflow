package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/joblog"
	"gorm.io/gorm"
)

// Bounds that keep one execution's output from exhausting controller storage.
const (
	MaxLogPageBytes     = 4 << 20 // request body for one worker page
	MaxLogPageEvents    = 1000
	MaxLogStreamEvents  = 20000
	MaxLogStreamBytes   = 16 << 20
	MaxLogStreams       = 16 // Offload invocations per task epoch
	MaxLogReadEvents    = 500
	MaxLogReadBytes     = 512 << 10
	LogRetention        = 14 * 24 * time.Hour
	AvailabilityPending = "not_captured"
)

// LogStream and LogEvent are storage rows; neither is part of the dashboard snapshot.
type LogStream struct {
	TaskID        string `gorm:"primaryKey"`
	LeaseEpoch    int64  `gorm:"primaryKey"`
	Invocation    string `gorm:"primaryKey"`
	WorkerID      string
	OffloadRunID  string
	Backend       string
	Model         string
	Availability  string
	Reason        string
	State         string
	Completeness  string
	Summary       json.RawMessage `gorm:"serializer:rawjson"`
	LastSeq       int64
	EventCount    int
	BytesTotal    int64
	DroppedEvents int
	Limited       bool
	Ended         bool
	Purged        bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (LogStream) TableName() string { return "task_log_streams" }

type LogEvent struct {
	ID         int64 `gorm:"primaryKey;autoIncrement"`
	TaskID     string
	LeaseEpoch int64
	Invocation string
	Seq        int64
	Event      json.RawMessage `gorm:"serializer:rawjson"`
	Size       int
}

func (LogEvent) TableName() string { return "task_log_events" }

// LogPage is one worker upload. FromSeq/ToSeq are Offload sequence numbers
// covering every record the worker consumed, including records it did not
// forward, so the controller can acknowledge by sequence.
type LogPage struct {
	FencingToken int64           `json:"fencing_token"`
	Invocation   string          `json:"invocation"`
	OffloadRunID string          `json:"offload_run_id,omitempty"`
	Backend      string          `json:"backend,omitempty"`
	Model        string          `json:"model,omitempty"`
	Availability string          `json:"availability"`
	Reason       string          `json:"reason,omitempty"`
	State        string          `json:"state,omitempty"`
	Completeness string          `json:"completeness,omitempty"`
	Summary      *joblog.Summary `json:"summary,omitempty"`
	FromSeq      int64           `json:"from_seq"`
	ToSeq        int64           `json:"to_seq"`
	Events       []joblog.Event  `json:"events"`
	End          bool            `json:"end,omitempty"`
}

// LogAck tells the worker how far the controller has stored this invocation.
type LogAck struct {
	Accepted   bool  `json:"accepted"`
	LastSeq    int64 `json:"last_seq"`
	Stored     int   `json:"stored"`
	Duplicates int   `json:"duplicates"`
	Limited    bool  `json:"limited"`
}

// ErrLogIdentity reports an Offload run identity that differs from the stream's.
var ErrLogIdentity = errors.New("execution log identity mismatch")

func validateLogPage(p *LogPage) error {
	if !joblog.ValidID(p.Invocation) || !joblog.ValidAvailability(p.Availability) {
		return ErrInvalid
	}
	if p.OffloadRunID != "" && !joblog.ValidID(p.OffloadRunID) {
		return ErrInvalid
	}
	if p.FromSeq < 0 || p.ToSeq < p.FromSeq || len(p.Events) > MaxLogPageEvents || len(p.Reason) > 300 || len(p.Backend) > 32 || len(p.Model) > 128 || len(p.State) > 32 || len(p.Completeness) > 32 {
		return ErrInvalid
	}
	last := p.FromSeq
	for i := range p.Events {
		e := &p.Events[i]
		if !e.Valid() || e.Seq <= last || e.Seq > p.ToSeq {
			return ErrInvalid
		}
		last = e.Seq
		e.Bound()
	}
	return nil
}

// AppendLog stores one page. The caller must hold the current lease: the task
// must be leased to this worker at this fencing token, and the attempt row for
// that epoch must belong to the same worker. Events at or below the stored
// sequence are duplicates and ignored; a page that starts beyond the stored
// sequence is rejected with Accepted=false so the worker re-reads from the start.
func (s *Store) AppendLog(ctx context.Context, taskID, worker string, page LogPage) (LogAck, error) {
	var ack LogAck
	if err := validateLogPage(&page); err != nil {
		return ack, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t Task
		// The store's BEGIN IMMEDIATE transaction serializes every writer. Reading
		// the stream after acquiring that lock keeps sequence acceptance gap-free.
		if err := tx.First(&t, "id = ?", taskID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStaleLease
			}
			return err
		}
		now := controllerNow()
		if t.State != "leased" || t.LeaseOwner != worker || t.FencingToken != page.FencingToken || t.LeaseExpiresAt == nil || (!t.LeaseExpiresAt.After(now) && !DurableTask(&t)) {
			return ErrStaleLease
		}
		var attempts int64
		if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ? AND worker_id = ?", taskID, page.FencingToken, worker).Count(&attempts).Error; err != nil {
			return err
		}
		if attempts != 1 {
			return ErrStaleLease
		}
		var stream LogStream
		err := tx.Where("task_id = ? AND lease_epoch = ? AND invocation = ?", taskID, page.FencingToken, page.Invocation).First(&stream).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var n int64
			if err := tx.Model(&LogStream{}).Where("task_id = ? AND lease_epoch = ?", taskID, page.FencingToken).Count(&n).Error; err != nil {
				return err
			}
			if n >= MaxLogStreams {
				return ErrInvalid
			}
			stream = LogStream{TaskID: taskID, LeaseEpoch: page.FencingToken, Invocation: page.Invocation, WorkerID: worker, CreatedAt: now}
		} else if err != nil {
			return err
		}
		if stream.WorkerID != worker || stream.Purged {
			return ErrStaleLease
		}
		if stream.OffloadRunID != "" && page.OffloadRunID != "" && stream.OffloadRunID != page.OffloadRunID {
			return ErrLogIdentity
		}
		if stream.Ended && (page.Availability != stream.Availability || len(page.Events) > 0 || page.ToSeq > stream.LastSeq) {
			return ErrStaleLease
		}
		ack.LastSeq = stream.LastSeq
		if page.FromSeq > stream.LastSeq {
			// Ahead of what is stored: the worker must resume from the controller's position.
			ack.Limited = stream.Limited
			return nil
		}
		ack.Accepted = true
		if page.OffloadRunID != "" {
			stream.OffloadRunID = page.OffloadRunID
		}
		if page.Backend != "" {
			stream.Backend, stream.Model = page.Backend, page.Model
		}
		stream.Availability, stream.Reason = page.Availability, page.Reason
		if page.State != "" {
			stream.State = page.State
		}
		if page.Completeness != "" {
			stream.Completeness = page.Completeness
		}
		if page.Summary != nil {
			stream.Summary, _ = json.Marshal(page.Summary)
		}
		stream.UpdatedAt = now
		// The stream row must exist before events reference it.
		if err := tx.Save(&stream).Error; err != nil {
			return err
		}
		for _, e := range page.Events {
			if e.Seq <= stream.LastSeq {
				ack.Duplicates++
				continue
			}
			raw, err := json.Marshal(e)
			if err != nil {
				return ErrInvalid
			}
			size := len(raw)
			if stream.Limited || stream.EventCount >= MaxLogStreamEvents || stream.BytesTotal+int64(size) > MaxLogStreamBytes {
				stream.Limited = true
				stream.DroppedEvents++
				continue
			}
			if err := tx.Create(&LogEvent{TaskID: taskID, LeaseEpoch: page.FencingToken, Invocation: page.Invocation, Seq: e.Seq, Event: raw, Size: size}).Error; err != nil {
				return err
			}
			stream.EventCount++
			stream.BytesTotal += int64(size)
			ack.Stored++
		}
		if page.ToSeq > stream.LastSeq {
			stream.LastSeq = page.ToSeq
		}
		if page.End {
			stream.Ended = true
		}
		stream.UpdatedAt = now
		ack.LastSeq, ack.Limited = stream.LastSeq, stream.Limited
		return tx.Save(&stream).Error
	})
	return ack, err
}

// LogEpoch describes one execution attempt that may have output.
type LogEpoch struct {
	Epoch   int64  `json:"epoch"`
	Attempt int64  `json:"attempt"`
	Worker  string `json:"worker"`
	Status  string `json:"status"`
}

// LogStreamInfo is the per-invocation status shown above the output.
type LogStreamInfo struct {
	Invocation    string          `json:"invocation"`
	OffloadRunID  string          `json:"offload_run_id,omitempty"`
	Backend       string          `json:"backend,omitempty"`
	Model         string          `json:"model,omitempty"`
	Availability  string          `json:"availability"`
	Reason        string          `json:"reason,omitempty"`
	State         string          `json:"state"`
	Completeness  string          `json:"completeness,omitempty"`
	Summary       json.RawMessage `json:"summary,omitempty"`
	EventCount    int             `json:"event_count"`
	DroppedEvents int             `json:"dropped_events,omitempty"`
	Limited       bool            `json:"limited,omitempty"`
}

// LogEntry is one stored event with its read cursor.
type LogEntry struct {
	ID         int64           `json:"id"`
	Invocation string          `json:"invocation"`
	Event      json.RawMessage `json:"event"`
}

// LogRead is the administrator view of one execution's output.
type LogRead struct {
	TaskID       string          `json:"task_id"`
	Epoch        int64           `json:"epoch"`
	Epochs       []LogEpoch      `json:"epochs"`
	Active       bool            `json:"active"`
	Availability string          `json:"availability"`
	Streams      []LogStreamInfo `json:"streams"`
	Entries      []LogEntry      `json:"entries"`
	NextAfter    int64           `json:"next_after"`
	HasMore      bool            `json:"has_more"`
}

// ReadLog returns a bounded page of one epoch's output after the given entry id.
// epoch 0 selects the newest epoch. It never contacts a worker or provider.
func (s *Store) ReadLog(ctx context.Context, taskID string, epoch, after int64, limit int) (LogRead, error) {
	out := LogRead{TaskID: taskID, Epochs: []LogEpoch{}, Streams: []LogStreamInfo{}, Entries: []LogEntry{}}
	if limit < 1 || limit > MaxLogReadEvents {
		limit = MaxLogReadEvents
	}
	if after < 0 || epoch < 0 {
		return out, ErrInvalid
	}
	db := s.db.WithContext(ctx)
	var t Task
	if err := db.Select("id", "state", "fencing_token").First(&t, "id = ?", taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, ErrStaleLease
		}
		return out, err
	}
	var attempts []TaskAttempt
	if err := db.Where("task_id = ?", taskID).Order("lease_epoch").Find(&attempts).Error; err != nil {
		return out, err
	}
	for _, a := range attempts {
		out.Epochs = append(out.Epochs, LogEpoch{Epoch: a.LeaseEpoch, Attempt: a.AttemptNumber, Worker: a.WorkerID, Status: a.Status})
	}
	if epoch == 0 {
		if n := len(out.Epochs); n > 0 {
			epoch = out.Epochs[n-1].Epoch
		}
	}
	out.Epoch = epoch
	known := false
	for _, e := range out.Epochs {
		known = known || e.Epoch == epoch
	}
	if !known {
		out.Availability = AvailabilityPending
		return out, nil
	}
	out.Active = t.State == "leased" && t.FencingToken == epoch
	var streams []LogStream
	if err := db.Where("task_id = ? AND lease_epoch = ?", taskID, epoch).Order("created_at, invocation").Find(&streams).Error; err != nil {
		return out, err
	}
	if len(streams) == 0 {
		out.Availability = AvailabilityPending
		return out, nil
	}
	out.Availability = joblog.Available
	for _, st := range streams {
		state := "ended"
		switch {
		case st.Purged:
			state = "expired"
		case !st.Ended && out.Active:
			state = "live"
		case !st.Ended:
			state = "interrupted"
		}
		availability := st.Availability
		if st.Purged {
			availability = joblog.Expired
		}
		out.Streams = append(out.Streams, LogStreamInfo{Invocation: st.Invocation, OffloadRunID: st.OffloadRunID, Backend: st.Backend, Model: st.Model,
			Availability: availability, Reason: st.Reason, State: state, Completeness: st.Completeness, Summary: st.Summary,
			EventCount: st.EventCount, DroppedEvents: st.DroppedEvents, Limited: st.Limited})
	}
	var rows []LogEvent
	if err := db.Where("task_id = ? AND lease_epoch = ? AND id > ?", taskID, epoch, after).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return out, err
	}
	bytes := 0
	out.NextAfter = after
	for i, r := range rows {
		if i >= limit || (i > 0 && bytes+r.Size > MaxLogReadBytes) {
			out.HasMore = true
			break
		}
		bytes += r.Size
		out.Entries = append(out.Entries, LogEntry{ID: r.ID, Invocation: r.Invocation, Event: r.Event})
		out.NextAfter = r.ID
	}
	return out, nil
}

// PurgeLogs deletes event content of finished tasks last updated before cutoff.
// Stream rows remain so readers can report the output as expired.
func (s *Store) PurgeLogs(ctx context.Context, cutoff time.Time) (int64, error) {
	var removed int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ids := tx.Model(&Task{}).Select("id").Where("state IN ('done','failed','cancelled') AND updated_at < ?", cutoff)
		r := tx.Where("task_id IN (?)", ids).Delete(&LogEvent{})
		if r.Error != nil {
			return r.Error
		}
		removed = r.RowsAffected
		return tx.Model(&LogStream{}).Where("task_id IN (?) AND purged = false", ids).Updates(map[string]any{"purged": true, "updated_at": cutoff}).Error
	})
	return removed, err
}
