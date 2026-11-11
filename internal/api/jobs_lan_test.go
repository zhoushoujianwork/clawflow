package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/joblog"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/webguard"
)

const (
	lanSecret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	lanPath   = "/Users/bob/projects/secret-repo"
)

// lanStack serves the real handlers over a real coordinator store behind the
// real read-only policy, with the connection made to look like a LAN peer.
func lanStack(t *testing.T, peer string) (*httptest.Server, *coordinator.Store, *atomic.Int32) {
	t.Helper()
	db := storetest.New(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	s := coordinator.New(db.DB())
	JobSource = s
	t.Cleanup(func() { JobSource = nil })

	var writes atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/jobs", HandleJobs)
	mux.HandleFunc("/api/jobs/", HandleJobTree)
	mux.HandleFunc("/api/runners", HandleRunners)
	for _, p := range []string{"/api/run", "/api/run/cancel", "/api/settings/tokens", "/api/labels/add"} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { writes.Add(1) })
	}
	policy, err := webguard.NewRemotePolicy("readonly", []string{"192.168.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	policy.WithRedactor(webguard.NewRedactor(
		func() []string { return []string{"registered-credential-9f8e7d"} },
		func() []string { return []string{"/Users/bob"} },
	))
	handler := policy.Middleware(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = peer
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, s, &writes
}

func seedRunning(t *testing.T, s *coordinator.Store) *coordinator.Task {
	t.Helper()
	ctx := context.Background()
	if err := s.RegisterWorker(ctx, coordinator.Worker{ID: "worker-a", Tags: []string{"host-lab"}, RunUntagged: true, MaxParallel: 2}); err != nil {
		t.Fatal(err)
	}
	payload := `{"title":"Fix the thing","testing":{"command":"PRIVATE_TEST_COMMAND"}}`
	if _, err := s.Enqueue(ctx, coordinator.Task{Kind: "implement", Repo: "owner/repo", Issue: 800, Operator: "implement", Payload: json.RawMessage(payload)}); err != nil {
		t.Fatal(err)
	}
	task, err := s.Claim(ctx, "worker-a")
	if err != nil || task == nil || task.ID == "" {
		t.Fatalf("claim: %v %v", task, err)
	}
	return task
}

func appendLog(t *testing.T, s *coordinator.Store, task *coordinator.Task, from, to int64, events ...joblog.Event) {
	t.Helper()
	ack, err := s.AppendLog(context.Background(), task.ID, "worker-a", coordinator.LogPage{FencingToken: task.FencingToken, Invocation: "inv-1",
		OffloadRunID: "run-1", Availability: joblog.Available, FromSeq: from, ToSeq: to, Events: events})
	if err != nil || !ack.Accepted {
		t.Fatalf("append: %+v %v", ack, err)
	}
}

// A LAN browser sees a running job's log appear live, with the worker holding
// it, and sees neither a credential nor a local path anywhere (AC-02, 05, 07,
// 11, 13).
func TestLANViewerFollowsLiveLogWithoutSecrets(t *testing.T) {
	fastStream(t)
	srv, s, _ := lanStack(t, "192.168.1.50:41000")
	task := seedRunning(t, s)
	appendLog(t, s, task, 0, 2,
		joblog.Event{Seq: 1, Type: joblog.TypeMessage, Role: "assistant", Text: "opening " + lanPath + "/main.go"},
		joblog.Event{Seq: 2, Type: joblog.TypeToolCall, ToolCallID: "c1", ToolName: "Bash", Arguments: `{"command":"git push https://x:` + lanSecret + `@github.com/o/r.git"}`})

	resp, err := http.Get(srv.URL + "/api/jobs/" + task.ID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := make(chan sseEvent, 32)
	go func() { scanEvents(resp.Body, func(e sseEvent) { events <- e }); close(events) }()
	var raw strings.Builder
	next := func() sseEvent {
		t.Helper()
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("stream ended early")
			}
			raw.WriteString(e.id + "|" + e.data + "\n")
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
		}
		return sseEvent{}
	}
	first := decodeFrame(t, next())
	if len(first.Entries) != 2 || !first.Active {
		t.Fatalf("backlog %+v", first)
	}

	// New output written while the viewer is connected arrives without reconnecting.
	appendLog(t, s, task, 2, 3, joblog.Event{Seq: 3, Type: joblog.TypeToolResult, ToolCallID: "c1", Output: "pushed using registered-credential-9f8e7d from " + lanPath})
	live := decodeFrame(t, next())
	if len(live.Entries) != 1 || live.Entries[0].ID <= first.NextAfter {
		t.Fatalf("live frame %+v after %d", live, first.NextAfter)
	}

	// The job list and detail show the running job and its worker.
	var list struct{ Jobs []coordinator.Job }
	getJSON(t, srv.URL+"/api/jobs?status=running", &list, &raw)
	if len(list.Jobs) != 1 || list.Jobs[0].Worker != "worker-a" || list.Jobs[0].Status != "running" || list.Jobs[0].Title != "Fix the thing" {
		t.Fatalf("jobs %+v", list.Jobs)
	}
	var runners struct{ Runners []coordinator.Runner }
	getJSON(t, srv.URL+"/api/runners", &runners, &raw)
	if len(runners.Runners) != 1 || runners.Runners[0].Name != "worker-a" || len(runners.Runners[0].Running) != 1 || runners.Runners[0].Running[0].ID != task.ID {
		t.Fatalf("runners %+v", runners.Runners)
	}
	var detail coordinator.JobDetail
	getJSON(t, srv.URL+"/api/jobs/"+task.ID, &detail, &raw)
	if detail.Worker != "worker-a" || len(detail.Timeline) < 2 {
		t.Fatalf("detail %+v", detail)
	}

	body := raw.String()
	for _, leak := range []string{lanSecret, "registered-credential-9f8e7d", "/Users/bob", "secret-repo", "PRIVATE_TEST_COMMAND", "x:ghp_"} {
		if strings.Contains(body, leak) {
			t.Fatalf("LAN viewer received %q:\n%s", leak, body)
		}
	}
	if !strings.Contains(body, "worker-a") || !strings.Contains(body, "[path]") {
		t.Fatalf("expected worker and a path placeholder:\n%s", body)
	}
}

func getJSON(t *testing.T, url string, v any, log *strings.Builder) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d %s", url, resp.StatusCode, b)
	}
	log.Write(b)
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v\n%s", url, err, b)
	}
}

// Every way of changing state is refused for a LAN peer before it reaches a
// handler, whatever the method, and the same requests work from this machine
// (AC-06, 08, 12).
func TestLANPeerCannotWrite(t *testing.T) {
	srv, s, writes := lanStack(t, "192.168.1.50:41000")
	task := seedRunning(t, s)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/api/run", "/api/run/cancel", "/api/settings/tokens", "/api/labels/add", "/api/jobs", "/api/jobs/" + task.ID, "/api/jobs/" + task.ID + "/logs", "/api/runners"} {
			req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"repo":"owner/repo","issue":800}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s = %d %s", method, path, resp.StatusCode, body)
			}
		}
	}
	// GET on a write endpoint is refused too: it is not on the read allowlist.
	for _, path := range []string{"/api/run", "/api/run/cancel", "/api/settings/tokens"} {
		resp, _ := http.Get(srv.URL + path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
	}
	if n := writes.Load(); n != 0 {
		t.Fatalf("%d write handlers ran for a LAN peer", n)
	}
}

func TestLANPeerOutsideAllowedNetworkIsRefused(t *testing.T) {
	srv, s, _ := lanStack(t, "10.9.9.9:41000")
	task := seedRunning(t, s)
	for _, path := range []string{"/api/jobs", "/api/runners", "/api/jobs/" + task.ID, "/api/jobs/" + task.ID + "/logs"} {
		resp, _ := http.Get(srv.URL + path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s = %d", path, resp.StatusCode)
		}
	}
}

// The machine's own browser keeps the unfiltered view and write access.
func TestLoopbackViewerIsNotRedacted(t *testing.T) {
	srv, s, writes := lanStack(t, "127.0.0.1:41000")
	task := seedRunning(t, s)
	appendLog(t, s, task, 0, 1, joblog.Event{Seq: 1, Type: joblog.TypeMessage, Role: "assistant", Text: "in " + lanPath})
	resp, err := http.Get(srv.URL + "/api/jobs/" + task.ID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 1)
	go func() {
		scanEvents(resp.Body, func(e sseEvent) {
			select {
			case got <- e.data:
			default:
			}
		})
	}()
	select {
	case data := <-got:
		if !strings.Contains(data, lanPath) {
			t.Fatalf("local viewer lost data: %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no frame")
	}
	resp2, _ := http.Post(srv.URL+"/api/run", "application/json", strings.NewReader("{}"))
	resp2.Body.Close()
	if resp2.StatusCode != 200 || writes.Load() != 1 {
		t.Fatalf("local write = %d writes=%d", resp2.StatusCode, writes.Load())
	}
}

// After the task finishes the stream replays what was stored and closes.
func TestLANViewerReplaysFinishedLogAndCloses(t *testing.T) {
	fastStream(t)
	srv, s, _ := lanStack(t, "192.168.1.50:41000")
	task := seedRunning(t, s)
	appendLog(t, s, task, 0, 2,
		joblog.Event{Seq: 1, Type: joblog.TypeMessage, Role: "assistant", Text: "one"},
		joblog.Event{Seq: 2, Type: joblog.TypeMessage, Role: "assistant", Text: "two"})
	if err := s.Complete(context.Background(), task.ID, "worker-a", task.FencingToken, json.RawMessage(`{"status":"success","pr_url":"https://github.com/owner/repo/pull/9","pr_number":9}`)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/api/jobs/" + task.ID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var ids []int64
	var done bool
	for _, e := range readEvents(t, resp.Body, 0) {
		if e.data == "[DONE]" {
			done = true
			continue
		}
		for _, en := range decodeFrame(t, e).Entries {
			ids = append(ids, en.ID)
		}
	}
	if len(ids) != 2 || !done {
		t.Fatalf("ids=%v done=%v", ids, done)
	}
	var detail coordinator.JobDetail
	var sink strings.Builder
	getJSON(t, srv.URL+"/api/jobs/"+task.ID, &detail, &sink)
	if detail.Status != "success" || detail.Artifacts.PRURL != "https://github.com/owner/repo/pull/9" || detail.EndedAt == nil {
		t.Fatalf("detail %+v", detail)
	}
}
