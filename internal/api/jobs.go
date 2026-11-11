package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
)

// JobReader is the read-only view of the coordinator used by the run
// observation pages. *coordinator.Store satisfies it.
type JobReader interface {
	Jobs(context.Context, coordinator.JobFilter) ([]coordinator.Job, error)
	Job(context.Context, string) (*coordinator.JobDetail, error)
	Runners(context.Context) ([]coordinator.Runner, time.Time, error)
	ReadLog(ctx context.Context, taskID string, epoch, after int64, limit int) (coordinator.LogRead, error)
}

// JobSource is bound when the coordinator starts in this process and cleared
// when it is disabled.
var JobSource JobReader

const (
	jobReadTimeout = 10 * time.Second
	logFrameLimit  = 200
	// maxLogStreams bounds simultaneous live log connections so a crowd of
	// viewers cannot starve the controller's database.
	maxLogStreams = 32
)

var logSlots = make(chan struct{}, maxLogStreams)

func jobSource(w http.ResponseWriter) JobReader {
	if JobSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "coordinator unavailable"})
		return nil
	}
	return JobSource
}

func validJobID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func queryInt64(q map[string][]string, name string) (int64, bool) {
	vals, ok := q[name]
	if !ok || len(vals) == 0 || vals[0] == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(vals[0], 10, 64)
	return n, err == nil && n >= 0
}

// HandleJobs serves GET /api/jobs?repo=&issue=&pr=&status=&limit=.
func HandleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	src := jobSource(w)
	if src == nil {
		return
	}
	q := r.URL.Query()
	f := coordinator.JobFilter{Repo: q.Get("repo"), Status: q.Get("status")}
	issue, ok1 := queryInt64(q, "issue")
	pr, ok2 := queryInt64(q, "pr")
	limit, ok3 := queryInt64(q, "limit")
	if !ok1 || !ok2 || !ok3 || len(f.Repo) > 200 || issue > 1<<31 || pr > 1<<31 || limit > coordinator.MaxJobList {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid query"})
		return
	}
	f.Issue, f.PR, f.Limit = int(issue), int(pr), int(limit)
	ctx, cancel := context.WithTimeout(r.Context(), jobReadTimeout)
	defer cancel()
	jobs, err := src.Jobs(ctx, f)
	if errors.Is(err, coordinator.ErrInvalid) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid status"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read jobs failed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"generated_at": time.Now().UTC(), "jobs": jobs})
}

// HandleRunners serves GET /api/runners.
func HandleRunners(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	src := jobSource(w)
	if src == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), jobReadTimeout)
	defer cancel()
	runners, at, err := src.Runners(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read runners failed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"generated_at": at, "runners": runners})
}

// HandleJobTree serves GET /api/jobs/{id} and GET /api/jobs/{id}/logs.
func HandleJobTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	id, tail, _ := strings.Cut(rest, "/")
	if !validJobID(id) || (tail != "" && tail != "logs") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	src := jobSource(w)
	if src == nil {
		return
	}
	if tail == "logs" {
		streamJobLogs(w, r, src, id)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), jobReadTimeout)
	defer cancel()
	d, err := src.Job(ctx, id)
	if errors.Is(err, coordinator.ErrStaleLease) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read job failed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, d)
}

// logFrame is one SSE payload. Entries carry the store's cursor ids, so a
// client that reconnects passes the last id it received as `after` and gets
// no duplicates and no gap.
type logFrame struct {
	TaskID       string                      `json:"task_id"`
	TaskState    string                      `json:"task_state,omitempty"`
	Epoch        int64                       `json:"epoch"`
	Epochs       []coordinator.LogEpoch      `json:"epochs"`
	Active       bool                        `json:"active"`
	Availability string                      `json:"availability"`
	Streams      []coordinator.LogStreamInfo `json:"streams"`
	Entries      []coordinator.LogEntry      `json:"entries"`
	NextAfter    int64                       `json:"next_after"`
}

// streamJobLogs is a Server-Sent Events endpoint. It pushes stored execution
// output as it arrives, closes with `data: [DONE]` once the execution has
// ended, and resumes from `after` (or Last-Event-ID) after a reconnect. It
// contacts only the controller's database: no worker, provider or Offload.
func streamJobLogs(w http.ResponseWriter, r *http.Request, src JobReader, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	epoch, ok1 := queryInt64(q, "epoch")
	after, ok2 := queryInt64(q, "after")
	if !ok1 || !ok2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid query"})
		return
	}
	if _, has := q["after"]; !has {
		if last := r.Header.Get("Last-Event-ID"); last != "" {
			n, err := strconv.ParseInt(last, 10, 64)
			if err != nil || n < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid Last-Event-ID"})
				return
			}
			after = n
		}
	}
	select {
	case logSlots <- struct{}{}:
		defer func() { <-logSlots }()
	default:
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "too many live log viewers"})
		return
	}
	// Fail before streaming when the task does not exist.
	probeCtx, cancel := context.WithTimeout(r.Context(), jobReadTimeout)
	first, err := src.ReadLog(probeCtx, id, epoch, after, logFrameLimit)
	cancel()
	if errors.Is(err, coordinator.ErrStaleLease) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read logs failed"})
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	var lastSig string
	sent := false
	// emit writes a frame when something changed and reports whether the
	// execution has finished and everything was delivered.
	emit := func(out coordinator.LogRead) (finished bool) {
		sig := frameSignature(out)
		if len(out.Entries) > 0 || sig != lastSig || !sent {
			frame := logFrame{TaskID: out.TaskID, TaskState: out.TaskState, Epoch: out.Epoch, Epochs: out.Epochs, Active: out.Active,
				Availability: out.Availability, Streams: out.Streams, Entries: out.Entries, NextAfter: out.NextAfter}
			data, err := json.Marshal(frame)
			if err != nil {
				return true
			}
			if out.NextAfter > 0 {
				fmt.Fprintf(w, "id: %d\n", out.NextAfter)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			lastSig, sent = sig, true
		}
		after = out.NextAfter
		if epoch == 0 && out.Epoch != 0 {
			// Pin the execution so a later retry does not switch the view.
			epoch = out.Epoch
		}
		return !out.HasMore && logFinished(out)
	}
	if emit(first) {
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	poll := time.NewTicker(streamPollInterval)
	defer poll.Stop()
	lastBeat := time.Now()
	for {
		// Drain a backlog without waiting a full tick between pages.
		for {
			readCtx, cancel := context.WithTimeout(ctx, jobReadTimeout)
			out, err := src.ReadLog(readCtx, id, epoch, after, logFrameLimit)
			cancel()
			if err != nil {
				return
			}
			before := after
			if emit(out) {
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			if after != before {
				lastBeat = time.Now()
			}
			if !out.HasMore {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case now := <-poll.C:
			if now.Sub(lastBeat) >= streamHeartbeatEvery {
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
				lastBeat = now
			}
		}
	}
}

// logFinished reports that no further output can arrive for this execution.
func logFinished(out coordinator.LogRead) bool {
	if out.Active {
		return false
	}
	// Nothing captured yet, and the task may still start or be retried.
	if out.Availability == coordinator.AvailabilityPending && (out.TaskState == "pending" || out.TaskState == "leased") {
		return false
	}
	return true
}

func frameSignature(out coordinator.LogRead) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%v|%s|%s|%d", out.Epoch, out.Active, out.Availability, out.TaskState, len(out.Epochs))
	for _, s := range out.Streams {
		fmt.Fprintf(&b, "|%s,%s,%s,%s,%d,%d,%v", s.Invocation, s.State, s.Availability, s.Completeness, s.EventCount, s.DroppedEvents, s.Limited)
	}
	return b.String()
}
