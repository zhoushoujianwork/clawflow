package api

import (
	"bytes"
	"context"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"net/http"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// HandlePilotStream is a Server-Sent Events endpoint
// (`GET /api/project/pilot/stream?project=<name>`) that pushes the latest
// Pilot wake's meta.json to the project detail page whenever it changes, so
// the top "Pilot" badge reflects the live `running / done / error` state
// without manual refresh (issue #240).
//
// Polls PostgreSQL so standalone wakes and the dashboard share state.
func HandlePilotStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project := strings.TrimSpace(r.URL.Query().Get("project"))
	if project == "" {
		http.Error(w, "project is required", http.StatusBadRequest)
		return
	}
	if snapshot.RunStore() == nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable proxy buffering (nginx) so frames flush immediately.
	w.Header().Set("X-Accel-Buffering", "no")

	ctx := r.Context()

	var last []byte
	// send re-reads the latest pilot meta.json and emits a frame only when the
	// compacted bytes changed. Returns true if a data frame was written.
	send := func() bool {
		data, err := readLatestPilotMetaCompact(project)
		if err != nil || bytes.Equal(data, last) {
			return false
		}
		last = data
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		return true
	}

	// Emit the current snapshot immediately so a fresh connection paints right
	// away instead of waiting for the first change.
	send()

	poll := time.NewTicker(streamPollInterval)
	defer poll.Stop()
	lastBeat := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-poll.C:
			if send() {
				lastBeat = now
				continue
			}
			// No change: keep the connection warm with a comment ping.
			if now.Sub(lastBeat) >= streamHeartbeatEvery {
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
				lastBeat = now
			}
		}
	}
}

// readLatestPilotMetaCompact reads the most recent Pilot run's meta.json for a
// project and returns its JSON compacted onto a single line (keeping the SSE
// `data:` framing trivial). When no wake has happened yet it yields the JSON
// literal `null` so the stream still emits a valid initial frame and the
// client can distinguish "no runs" from a parse failure.
func readLatestPilotMetaCompact(project string) ([]byte, error) {
	s := snapshot.RunStore()
	if s == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.Runs(ctx, "pilot", store.RunFilter{Project: project, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return []byte("null"), nil
	}
	return []byte(rows[0].Payload), nil
}
