package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// plantRuns updates one SQL row to exercise stream change detection.
func plantRuns(t *testing.T, body string) {
	t.Helper()
	if snapshot.RunStore() == nil {
		historyDB(t)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatal(err)
	}
	for _, m := range rows {
		m["repo"] = "o/r"
		m["started_at"] = "2026-10-28T00:00:00Z"
		raw, _ := json.Marshal(m)
		if _, err := snapshot.RunStore().UpsertRun(context.Background(), filepath.Join(snapshot.DataDir(), "runs", "one"), "run", raw); err != nil {
			t.Fatal(err)
		}
	}
}

// readFirstDataFrame reads SSE lines until the first `data:` payload, returning
// the payload (without the prefix). Fails the test if the stream ends first.
func readFirstDataFrame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE stream: %v (last line %q)", err, line)
		}
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		}
	}
}

func TestRunStream_EmitsInitialSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plantRuns(t, `[{"operator":"classify","status":"running","stage":"claude-started"}]`)

	srv := httptest.NewServer(http.HandlerFunc(HandleRunStream))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	frame := readFirstDataFrame(t, bufio.NewReader(resp.Body))
	if !strings.Contains(frame, "claude-started") {
		t.Errorf("initial frame missing stage; got %q", frame)
	}
	if !strings.Contains(frame, "classify") {
		t.Errorf("initial frame missing operator; got %q", frame)
	}
	// Compacted to a single line — no embedded newline should reach the client.
	if strings.Contains(frame, "\n") {
		t.Errorf("frame should be single-line JSON, got %q", frame)
	}
}

func TestRunStream_PushesOnChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plantRuns(t, `[{"operator":"classify","status":"running","stage":"lock-acquired"}]`)

	// Speed up the poll so the test doesn't wait a full second per tick.
	prev := streamPollInterval
	streamPollInterval = 20 * time.Millisecond
	defer func() { streamPollInterval = prev }()

	srv := httptest.NewServer(http.HandlerFunc(HandleRunStream))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)

	// First frame = initial snapshot.
	first := readFirstDataFrame(t, reader)
	if !strings.Contains(first, "lock-acquired") {
		t.Fatalf("first frame = %q, want lock-acquired", first)
	}

	// Advance the run's stage on disk; the handler should push a new frame.
	plantRuns(t, `[{"operator":"classify","status":"running","stage":"applying-label"}]`)
	second := readFirstDataFrame(t, reader)
	if !strings.Contains(second, "applying-label") {
		t.Errorf("second frame = %q, want applying-label", second)
	}
}

func TestRunStream_MethodNotAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/run/stream", nil)
	rr := httptest.NewRecorder()
	HandleRunStream(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestRunStream_EmptyDatabaseYieldsEmptyArray(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	historyDB(t)
	got, err := readRunsCompact(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "[]" {
		t.Errorf("got %q, want []", string(got))
	}
}
