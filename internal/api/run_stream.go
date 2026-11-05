package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"net/http"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// streamPollInterval is how often HandleRunStream queries the database to detect
// changes. Declared as a var so tests can shrink it. 1s gives sub-second-feel
// latency for the dashboard while keeping disk reads cheap (the file is a few
// KB and only re-emitted when its bytes actually change).
var streamPollInterval = time.Second

// streamHeartbeatEvery controls how often an SSE comment ping is sent when the
// data is unchanged, so idle connections survive proxy/idle timeouts.
var streamHeartbeatEvery = 15 * time.Second

// HandleRunStream is a Server-Sent Events endpoint (`GET /api/run/stream`) that
// pushes the current runs index to the dashboard whenever it changes, so the
// operator-status page reflects lifecycle stage transitions in real time
// without manual refresh (issue #199).
//
// Separate runner and web processes observe shared database state.
func HandleRunStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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

	if snapshot.RunStore() == nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	ctx := r.Context()

	var last []byte
	// send queries the database and emits a frame only when the compacted bytes
	// changed. Returns true if a data frame was written.
	send := func() bool {
		data, err := readRunsCompact(ctx)
		if err != nil || bytes.Equal(data, last) {
			return false
		}
		last = data
		// Compacted to a single line, so a one-line SSE `data:` frame is safe.
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

// readRunsCompact reads current SQL state for an SSE frame.
func readRunsCompact(ctx context.Context) ([]byte, error) {
	s := snapshot.RunStore()
	if s == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	rows, err := s.Runs(ctx, "run", store.RunFilter{})
	if err != nil {
		return nil, err
	}
	payload, err := store.RunPayloads(rows, snapshot.DataDir())
	if err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}
