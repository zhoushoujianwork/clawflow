package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/joblog"
)

// fakeJobs is a scripted JobReader. Logs can grow while a stream is open.
type fakeJobs struct {
	mu      sync.Mutex
	jobs    []coordinator.Job
	detail  *coordinator.JobDetail
	runners []coordinator.Runner
	entries []coordinator.LogEntry
	state   string // task state
	active  bool
	reads   []int64 // `after` of every ReadLog call
}

func (f *fakeJobs) Jobs(_ context.Context, flt coordinator.JobFilter) ([]coordinator.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if flt.Status == "bogus" {
		return nil, coordinator.ErrInvalid
	}
	return f.jobs, nil
}
func (f *fakeJobs) Job(_ context.Context, id string) (*coordinator.JobDetail, error) {
	if f.detail == nil || f.detail.ID != id {
		return nil, coordinator.ErrStaleLease
	}
	return f.detail, nil
}
func (f *fakeJobs) Runners(context.Context) ([]coordinator.Runner, time.Time, error) {
	return f.runners, time.Now(), nil
}
func (f *fakeJobs) ReadLog(_ context.Context, id string, epoch, after int64, limit int) (coordinator.LogRead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, after)
	if id != "task1" {
		return coordinator.LogRead{}, coordinator.ErrStaleLease
	}
	out := coordinator.LogRead{TaskID: id, TaskState: f.state, Epoch: 1, Epochs: []coordinator.LogEpoch{{Epoch: 1, Attempt: 1, Worker: "w1", Status: "running"}},
		Active: f.active, Availability: joblog.Available, Streams: []coordinator.LogStreamInfo{{Invocation: "inv", State: "live", Availability: joblog.Available, EventCount: len(f.entries)}},
		Entries: []coordinator.LogEntry{}, NextAfter: after}
	for _, e := range f.entries {
		if e.ID > after && len(out.Entries) < limit {
			out.Entries = append(out.Entries, e)
			out.NextAfter = e.ID
		}
	}
	return out, nil
}
func (f *fakeJobs) add(id int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(map[string]any{"seq": id, "type": "message", "text": text})
	f.entries = append(f.entries, coordinator.LogEntry{ID: id, Invocation: "inv", Event: raw})
}
func (f *fakeJobs) finish() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.state = false, "done"
}

func bindJobs(t *testing.T, f *fakeJobs) {
	t.Helper()
	JobSource = f
	t.Cleanup(func() { JobSource = nil })
}

func fastStream(t *testing.T) {
	t.Helper()
	oldPoll, oldBeat := streamPollInterval, streamHeartbeatEvery
	streamPollInterval, streamHeartbeatEvery = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { streamPollInterval, streamHeartbeatEvery = oldPoll, oldBeat })
}

func newJobsServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/jobs", HandleJobs)
	mux.HandleFunc("/api/jobs/", HandleJobTree)
	mux.HandleFunc("/api/runners", HandleRunners)
	return httptest.NewServer(mux)
}

type sseEvent struct{ id, data string }

// scanEvents parses an SSE body and hands each complete event to emit as soon
// as its terminating blank line arrives.
func scanEvents(body io.Reader, emit func(sseEvent)) {
	var cur sseEvent
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.data != "" {
				emit(cur)
			}
			cur = sseEvent{}
		}
	}
}

// readEvents collects every event until the stream ends.
func readEvents(t *testing.T, body io.Reader, _ int) []sseEvent {
	t.Helper()
	var out []sseEvent
	scanEvents(body, func(e sseEvent) { out = append(out, e) })
	return out
}

func decodeFrame(t *testing.T, e sseEvent) logFrame {
	t.Helper()
	var f logFrame
	if err := json.Unmarshal([]byte(e.data), &f); err != nil {
		t.Fatalf("frame %q: %v", e.data, err)
	}
	return f
}

func TestJobsEndpointsRequireCoordinator(t *testing.T) {
	JobSource = nil
	srv := newJobsServer()
	defer srv.Close()
	for _, path := range []string{"/api/jobs", "/api/runners", "/api/jobs/abc", "/api/jobs/abc/logs"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s = %d", path, resp.StatusCode)
		}
	}
}

func TestJobsEndpointsRejectWritesAndBadInput(t *testing.T) {
	bindJobs(t, &fakeJobs{})
	srv := newJobsServer()
	defer srv.Close()
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		for _, path := range []string{"/api/jobs", "/api/runners", "/api/jobs/abc", "/api/jobs/abc/logs"} {
			req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d", method, path, resp.StatusCode)
			}
		}
	}
	for path, want := range map[string]int{
		"/api/jobs?issue=abc": 400, "/api/jobs?limit=-1": 400, "/api/jobs?limit=99999": 400, "/api/jobs?status=bogus": 400,
		"/api/jobs/abc/logs?after=-1": 400, "/api/jobs/abc/logs?epoch=x": 400,
		"/api/jobs/ab%2Fc": 404, "/api/jobs/abc/cancel": 404, "/api/jobs/abc/logs/x": 404, "/api/jobs/": 404, "/api/jobs/missing": 404, "/api/jobs/missing/logs": 404,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestJobsListDetailAndRunners(t *testing.T) {
	started := time.Now().Add(-time.Minute).UTC()
	job := coordinator.Job{ID: "task1", Repo: "o/r", Issue: 5, Kind: "implement", Stage: "implement", Trigger: "issue_queued", Status: "running", Worker: "w1", QueuedAt: started, StartedAt: &started, Tags: []string{}}
	f := &fakeJobs{jobs: []coordinator.Job{job},
		detail:  &coordinator.JobDetail{Job: job, Attempts: []coordinator.JobAttempt{}, Timeline: []coordinator.TimelineEvent{{At: started, Event: "queued"}}, Related: []coordinator.Job{}},
		runners: []coordinator.Runner{{Name: "w1", Online: true, Tags: []string{"host-a"}, Roles: []string{}, Running: []coordinator.Job{job}}}}
	bindJobs(t, f)
	srv := newJobsServer()
	defer srv.Close()

	get := func(path string, v any) {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d cache=%q", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	var list struct{ Jobs []coordinator.Job }
	get("/api/jobs?repo=o/r&limit=10", &list)
	if len(list.Jobs) != 1 || list.Jobs[0].Worker != "w1" || list.Jobs[0].Status != "running" {
		t.Fatalf("list=%+v", list)
	}
	var detail coordinator.JobDetail
	get("/api/jobs/task1", &detail)
	if detail.ID != "task1" || len(detail.Timeline) != 1 {
		t.Fatalf("detail=%+v", detail)
	}
	var runners struct{ Runners []coordinator.Runner }
	get("/api/runners", &runners)
	if len(runners.Runners) != 1 || !runners.Runners[0].Online || runners.Runners[0].Running[0].ID != "task1" {
		t.Fatalf("runners=%+v", runners)
	}
}

// A viewer sees the backlog, then each new entry as it is stored, then a
// terminating [DONE] once the execution ends (AC-02).
func TestLogStreamFollowsLiveOutput(t *testing.T) {
	fastStream(t)
	f := &fakeJobs{state: "leased", active: true}
	f.add(1, "first")
	f.add(2, "second")
	bindJobs(t, f)
	srv := newJobsServer()
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/jobs/task1/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	events := make(chan sseEvent, 16)
	go func() {
		scanEvents(resp.Body, func(e sseEvent) { events <- e })
		close(events)
	}()
	next := func() sseEvent {
		t.Helper()
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("stream ended early")
			}
			return e
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for event")
		}
		return sseEvent{}
	}
	first := decodeFrame(t, next())
	if len(first.Entries) != 2 || first.NextAfter != 2 || !first.Active {
		t.Fatalf("backlog=%+v", first)
	}
	f.add(3, "live line")
	live := next()
	frame := decodeFrame(t, live)
	if live.id != "3" || len(frame.Entries) != 1 || frame.Entries[0].ID != 3 {
		t.Fatalf("live id=%q frame=%+v", live.id, frame)
	}
	f.finish()
	var sawDone bool
	for e := range events {
		if e.data == "[DONE]" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("no [DONE] after the execution ended")
	}
}

// Reconnecting with the last id received delivers no duplicate and no gap.
func TestLogStreamResumesAfterDisconnect(t *testing.T) {
	fastStream(t)
	f := &fakeJobs{state: "done", active: false}
	for i := int64(1); i <= 5; i++ {
		f.add(i, "line")
	}
	bindJobs(t, f)
	srv := newJobsServer()
	defer srv.Close()

	collect := func(url string, header string) []int64 {
		req, _ := http.NewRequest("GET", srv.URL+url, nil)
		if header != "" {
			req.Header.Set("Last-Event-ID", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var ids []int64
		for _, e := range readEvents(t, resp.Body, 0) {
			if e.data == "[DONE]" {
				continue
			}
			for _, entry := range decodeFrame(t, e).Entries {
				ids = append(ids, entry.ID)
			}
		}
		return ids
	}
	all := collect("/api/jobs/task1/logs", "")
	if len(all) != 5 {
		t.Fatalf("full replay=%v", all)
	}
	for _, tc := range []struct{ url, header string }{
		{"/api/jobs/task1/logs?after=3", ""},
		{"/api/jobs/task1/logs", "3"},
	} {
		got := collect(tc.url, tc.header)
		if len(got) != 2 || got[0] != 4 || got[1] != 5 {
			t.Fatalf("%v resumed=%v", tc, got)
		}
	}
	// An explicit `after` wins over Last-Event-ID.
	if got := collect("/api/jobs/task1/logs?after=4", "1"); len(got) != 1 || got[0] != 5 {
		t.Fatalf("precedence: %v", got)
	}
	// Resuming at the end of a finished execution yields nothing but [DONE].
	if got := collect("/api/jobs/task1/logs?after=5", ""); len(got) != 0 {
		t.Fatalf("at end: %v", got)
	}
}

func TestLogStreamKeepAliveAndCancel(t *testing.T) {
	fastStream(t)
	f := &fakeJobs{state: "leased", active: true}
	bindJobs(t, f)
	srv := newJobsServer()
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/jobs/task1/logs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	deadline := time.After(3 * time.Second)
	got := make(chan string, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, ": ping") {
				got <- line
				return
			}
		}
	}()
	select {
	case <-got:
	case <-deadline:
		t.Fatal("no keep-alive ping on an idle live stream")
	}
	cancel()
	// The handler must release its slot once the client goes away.
	until := time.Now().Add(3 * time.Second)
	for len(logSlots) != 0 {
		if time.Now().After(until) {
			t.Fatalf("slot still held: %d", len(logSlots))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLogStreamLimitsViewers(t *testing.T) {
	fastStream(t)
	f := &fakeJobs{state: "leased", active: true}
	bindJobs(t, f)
	srv := newJobsServer()
	defer srv.Close()
	for i := 0; i < maxLogStreams; i++ {
		logSlots <- struct{}{}
	}
	defer func() {
		for len(logSlots) > 0 {
			<-logSlots
		}
	}()
	resp, err := http.Get(srv.URL + "/api/jobs/task1/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("status=%d retry=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

func TestLogFinished(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  coordinator.LogRead
		want bool
	}{
		{"active", coordinator.LogRead{Active: true, Availability: joblog.Available}, false},
		{"ended", coordinator.LogRead{TaskState: "done", Availability: joblog.Available}, true},
		{"not started", coordinator.LogRead{TaskState: "pending", Availability: coordinator.AvailabilityPending}, false},
		{"leased no output yet", coordinator.LogRead{TaskState: "leased", Availability: coordinator.AvailabilityPending}, false},
		{"finished without output", coordinator.LogRead{TaskState: "failed", Availability: coordinator.AvailabilityPending}, true},
		{"cancelled", coordinator.LogRead{TaskState: "cancelled", Availability: coordinator.AvailabilityPending}, true},
	} {
		if got := logFinished(tc.out); got != tc.want {
			t.Errorf("%s: %v want %v", tc.name, got, tc.want)
		}
	}
}
