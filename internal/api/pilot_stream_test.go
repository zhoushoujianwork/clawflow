package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// plantPilotMeta writes a meta.json for a single Pilot run under
// data/pilot-runs/<project>/<stamp>/. stamp must be the UTC-timestamp format
// PilotRunDir uses so lexical ordering matches chronological ordering.
func plantPilotMeta(t *testing.T, project, stamp, body string) {
	t.Helper()
	dir := filepath.Join(snapshot.DataDir(), "pilot-runs", project, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir pilot run dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
}

func TestPilotStream_EmitsInitialSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plantPilotMeta(t, "demo", "2026-06-01T17-14-55Z",
		`{"project":"demo","status":"running","started_at":"2026-06-01T17:14:55Z"}`)

	srv := httptest.NewServer(http.HandlerFunc(HandlePilotStream))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"?project=demo", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	frame := readFirstDataFrame(t, bufio.NewReader(resp.Body))
	if !strings.Contains(frame, `"running"`) {
		t.Errorf("initial frame missing running status; got %q", frame)
	}
	if strings.Contains(frame, "\n") {
		t.Errorf("frame should be single-line JSON, got %q", frame)
	}
}

func TestPilotStream_PushesOnChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stamp := "2026-06-01T17-14-55Z"
	plantPilotMeta(t, "demo", stamp,
		`{"project":"demo","status":"running","started_at":"2026-06-01T17:14:55Z"}`)

	prev := streamPollInterval
	streamPollInterval = 20 * time.Millisecond
	defer func() { streamPollInterval = prev }()

	srv := httptest.NewServer(http.HandlerFunc(HandlePilotStream))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"?project=demo", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)

	first := readFirstDataFrame(t, reader)
	if !strings.Contains(first, `"running"`) {
		t.Fatalf("first frame = %q, want running", first)
	}

	// The wake finishes: meta.json for the SAME run is rewritten with the
	// terminal status. The handler should push the new frame.
	plantPilotMeta(t, "demo", stamp,
		`{"project":"demo","status":"success","started_at":"2026-06-01T17:14:55Z","ended_at":"2026-06-01T17:15:30Z"}`)
	second := readFirstDataFrame(t, reader)
	if !strings.Contains(second, `"success"`) {
		t.Errorf("second frame = %q, want success", second)
	}
}

func TestPilotStream_MethodNotAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/project/pilot/stream?project=demo", nil)
	rr := httptest.NewRecorder()
	HandlePilotStream(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestPilotStream_MissingProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	req := httptest.NewRequest(http.MethodGet, "/api/project/pilot/stream", nil)
	rr := httptest.NewRecorder()
	HandlePilotStream(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestPilotStream_NoRunsYieldsNull(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got, err := readLatestPilotMetaCompact("demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "null" {
		t.Errorf("got %q, want null", string(got))
	}
}

func TestPilotStream_PicksLatestRunDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	plantPilotMeta(t, "demo", "2026-06-01T10-00-00Z",
		`{"project":"demo","status":"success","started_at":"2026-06-01T10:00:00Z"}`)
	plantPilotMeta(t, "demo", "2026-06-01T17-14-55Z",
		`{"project":"demo","status":"running","started_at":"2026-06-01T17:14:55Z"}`)

	got, err := readLatestPilotMetaCompact("demo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(got), `"running"`) {
		t.Errorf("got %q, want the latest (running) run", string(got))
	}
}
