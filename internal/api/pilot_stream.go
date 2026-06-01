package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
// Design mirrors HandleRunStream exactly: `clawflow pilot wake` runs in a
// SEPARATE process (inside `clawflow run` or cron) and communicates wake
// state only through files under `~/.clawflow/data/pilot-runs/<project>/`.
// The web process therefore observes progress by polling the latest run's
// meta.json on the server side and fanning each change out over one
// long-lived SSE stream — no new dependency (no fsnotify) needed.
//
// Only the coarse-grained meta.json is streamed (status / started_at /
// ended_at / error); this MVP deliberately does NOT introduce per-stage
// events. The browser keeps its existing pilot-runs JSON polling as a
// graceful fallback, so a dropped stream never blanks the badge.
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
	path := snapshot.LatestPilotRunMetaPath(project)
	if path == "" {
		return []byte("null"), nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte("null"), nil
	}
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		// Malformed/partial read (e.g. mid-write) — skip this tick rather
		// than push a broken frame; the next poll picks up clean bytes.
		return nil, err
	}
	return buf.Bytes(), nil
}
