package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// streamPollInterval is how often HandleRunStream re-reads runs.json to detect
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
// Cross-process design: `clawflow run` is a SEPARATE process from `clawflow
// web` and communicates run state only through `~/.clawflow/data/runs.json`
// (rewritten on every lifecycle stage — see cmd/clawflow/commands/run.go). The
// web process therefore observes progress by polling that file on the server
// side and fanning each change out to connected browsers over one long-lived
// SSE stream. This needs no new dependency (no fsnotify) and is naturally
// compatible with standalone runs: when no web server is up, nobody watches and
// the next `clawflow web` simply reads the final runs.json.
//
// The browser keeps its existing JSON polling as a graceful fallback, so a
// dropped stream never blanks the dashboard.
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

	runsPath := filepath.Join(snapshot.DataDir(), "runs.json")
	ctx := r.Context()

	var last []byte
	// send re-reads runs.json and emits a frame only when the compacted bytes
	// changed. Returns true if a data frame was written.
	send := func() bool {
		data, err := readRunsCompact(runsPath)
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

// readRunsCompact reads runs.json and returns its JSON compacted onto a single
// line (no embedded newlines), which keeps the SSE `data:` framing trivial. A
// missing file yields an empty JSON array so the stream still emits a valid
// initial frame before any run exists.
func readRunsCompact(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []byte("[]"), nil
	}
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		// Malformed/partial read (e.g. mid-rename) — skip this tick rather
		// than push a broken frame; the next poll will pick up clean bytes.
		return nil, err
	}
	return buf.Bytes(), nil
}
