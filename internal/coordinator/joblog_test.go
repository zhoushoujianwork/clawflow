package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/joblog"
)

func logEvent(seq int64, typ, text string) joblog.Event {
	return joblog.Event{Seq: seq, Type: typ, Text: text}
}

// leased claims one task for a worker and returns it with its current fence.
func leased(t *testing.T, s *Store, worker string, issue int) *Task {
	t.Helper()
	register(t, s, worker, 2)
	enqueue(t, s, issue)
	task := claim(t, s, worker)
	if task.ID == "" {
		t.Fatal("no task claimed")
	}
	return task
}

func page(fence int64, from, to int64, events ...joblog.Event) LogPage {
	return LogPage{FencingToken: fence, Invocation: "inv-1", OffloadRunID: "run-1", Availability: joblog.Available, FromSeq: from, ToSeq: to, Events: events}
}

func TestAppendLogBindsWorkerTaskAndFence(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	register(t, s, "w2", 1)

	if _, err := s.AppendLog(ctx, task.ID, "w1", page(task.FencingToken, 0, 1, logEvent(1, "message", "hi"))); err != nil {
		t.Fatalf("owner upload: %v", err)
	}
	// Another authenticated worker cannot write to this execution.
	if _, err := s.AppendLog(ctx, task.ID, "w2", page(task.FencingToken, 0, 1, logEvent(1, "message", "intruder"))); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("other worker: %v", err)
	}
	// A stale or future fence is refused.
	for _, fence := range []int64{task.FencingToken - 1, task.FencingToken + 1, 0} {
		if _, err := s.AppendLog(ctx, task.ID, "w1", page(fence, 0, 1, logEvent(1, "message", "stale"))); !errors.Is(err, ErrStaleLease) {
			t.Fatalf("fence %d: %v", fence, err)
		}
	}
	// An unknown task is refused.
	if _, err := s.AppendLog(ctx, "missing", "w1", page(1, 0, 1)); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("unknown task: %v", err)
	}
	read, err := s.ReadLog(ctx, task.ID, 0, 0, 100)
	if err != nil || len(read.Entries) != 1 || !strings.Contains(string(read.Entries[0].Event), `"hi"`) {
		t.Fatalf("read=%+v err=%v", read, err)
	}
}

func TestAppendLogRejectsAfterLeaseEndsOrExpires(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	if err := s.Complete(ctx, task.ID, "w1", task.FencingToken, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendLog(ctx, task.ID, "w1", page(task.FencingToken, 0, 1, logEvent(1, "message", "late"))); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("after completion: %v", err)
	}

	expired := leased(t, s, "w3", 2)
	if err := s.db.Exec("UPDATE coordinator_tasks SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = ?", expired.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendLog(ctx, expired.ID, "w3", page(expired.FencingToken, 0, 1, logEvent(1, "message", "late"))); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired lease: %v", err)
	}
}

func TestAppendLogDuplicatesOrderingAndResume(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	f := task.FencingToken

	ack, err := s.AppendLog(ctx, task.ID, "w1", page(f, 0, 3, logEvent(1, "message", "a"), logEvent(2, "tool_call", ""), logEvent(3, "tool_result", "out")))
	if err != nil || !ack.Accepted || ack.Stored != 3 || ack.LastSeq != 3 {
		t.Fatalf("first: %+v %v", ack, err)
	}
	// Replaying the same page after a dropped acknowledgement stores nothing new.
	ack, err = s.AppendLog(ctx, task.ID, "w1", page(f, 0, 3, logEvent(1, "message", "a"), logEvent(2, "tool_call", ""), logEvent(3, "tool_result", "out")))
	if err != nil || !ack.Accepted || ack.Stored != 0 || ack.Duplicates != 3 || ack.LastSeq != 3 {
		t.Fatalf("replay: %+v %v", ack, err)
	}
	// Overlapping page stores only the new tail.
	ack, err = s.AppendLog(ctx, task.ID, "w1", page(f, 2, 4, logEvent(3, "tool_result", "out"), logEvent(4, "message", "b")))
	if err != nil || ack.Stored != 1 || ack.LastSeq != 4 {
		t.Fatalf("overlap: %+v %v", ack, err)
	}
	// A page that starts beyond the stored position is not accepted: the worker must resume.
	ack, err = s.AppendLog(ctx, task.ID, "w1", page(f, 9, 10, logEvent(10, "message", "skip")))
	if err != nil || ack.Accepted || ack.LastSeq != 4 {
		t.Fatalf("ahead: %+v %v", ack, err)
	}
	// Out-of-order events inside one page are invalid input.
	if _, err = s.AppendLog(ctx, task.ID, "w1", page(f, 4, 6, logEvent(6, "message", "x"), logEvent(5, "message", "y"))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unordered: %v", err)
	}
	read, err := s.ReadLog(ctx, task.ID, 0, 0, 100)
	if err != nil || len(read.Entries) != 4 {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	var seqs []int64
	for _, e := range read.Entries {
		var ev joblog.Event
		_ = json.Unmarshal(e.Event, &ev)
		seqs = append(seqs, ev.Seq)
	}
	for i, want := range []int64{1, 2, 3, 4} {
		if seqs[i] != want {
			t.Fatalf("order %v", seqs)
		}
	}
}

func TestAppendLogBoundsAndGapMarker(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	f := task.FencingToken

	big := logEvent(1, "tool_result", strings.Repeat("é", 100000))
	big.Output = strings.Repeat("x", 200000)
	ack, err := s.AppendLog(ctx, task.ID, "w1", page(f, 0, 2, big, joblog.Event{Seq: 2, Type: "gap", Reason: "event_too_large", Bytes: 999999}))
	if err != nil || ack.Stored != 2 {
		t.Fatalf("%+v %v", ack, err)
	}
	read, _ := s.ReadLog(ctx, task.ID, 0, 0, 100)
	var stored joblog.Event
	_ = json.Unmarshal(read.Entries[0].Event, &stored)
	if len(stored.Output) > joblog.MaxOutputBytes || len(stored.Text) > joblog.MaxTextBytes || stored.Truncated["output"] != 200000 {
		t.Fatalf("not bounded: out=%d text=%d trunc=%v", len(stored.Output), len(stored.Text), stored.Truncated)
	}
	var gap joblog.Event
	_ = json.Unmarshal(read.Entries[1].Event, &gap)
	if gap.Type != "gap" || gap.Bytes != 999999 {
		t.Fatalf("gap lost: %+v", gap)
	}
	// Too many events in one page, and unknown event types, are rejected.
	many := make([]joblog.Event, MaxLogPageEvents+1)
	for i := range many {
		many[i] = logEvent(int64(i+3), "message", "x")
	}
	if _, err := s.AppendLog(ctx, task.ID, "w1", page(f, 2, int64(len(many)+3), many...)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized page: %v", err)
	}
	if _, err := s.AppendLog(ctx, task.ID, "w1", page(f, 2, 3, logEvent(3, "thinking", "secret"))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown type: %v", err)
	}
}

func TestAppendLogStreamLimitMarksLimited(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	f := task.FencingToken
	if _, err := s.AppendLog(ctx, task.ID, "w1", page(f, 0, 1, logEvent(1, "message", "kept"))); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec("UPDATE task_log_streams SET bytes_total = ? WHERE task_id = ?", MaxLogStreamBytes, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	ack, err := s.AppendLog(ctx, task.ID, "w1", page(f, 1, 2, logEvent(2, "message", "dropped")))
	if err != nil || !ack.Limited || ack.Stored != 0 || ack.LastSeq != 2 {
		t.Fatalf("%+v %v", ack, err)
	}
	read, _ := s.ReadLog(ctx, task.ID, 0, 0, 100)
	if len(read.Entries) != 1 || !read.Streams[0].Limited || read.Streams[0].DroppedEvents != 1 {
		t.Fatalf("%+v", read)
	}
}

func TestAppendLogRunIdentityAndStreamCap(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	f := task.FencingToken
	if _, err := s.AppendLog(ctx, task.ID, "w1", page(f, 0, 1, logEvent(1, "message", "a"))); err != nil {
		t.Fatal(err)
	}
	other := page(f, 1, 2, logEvent(2, "message", "b"))
	other.OffloadRunID = "run-2"
	if _, err := s.AppendLog(ctx, task.ID, "w1", other); !errors.Is(err, ErrLogIdentity) {
		t.Fatalf("identity: %v", err)
	}
	for i := 0; i < MaxLogStreams; i++ {
		p := page(f, 0, 0)
		p.Invocation = "inv-extra-" + string(rune('a'+i))
		p.OffloadRunID = ""
		p.Availability = joblog.CaptureDisabled
		_, err := s.AppendLog(ctx, task.ID, "w1", p)
		if i < MaxLogStreams-1 && err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		if i == MaxLogStreams-1 && !errors.Is(err, ErrInvalid) {
			t.Fatalf("stream cap: %v", err)
		}
	}
}

func TestLogEpochsAreIsolatedAcrossRetries(t *testing.T) {
	s, _, _ := setup(t)
	first := leased(t, s, "w1", 1)
	if _, err := s.AppendLog(ctx, first.ID, "w1", page(first.FencingToken, 0, 1, logEvent(1, "message", "first-attempt"))); err != nil {
		t.Fatal(err)
	}
	// The lease expires and the task is retried on another worker with a new fence.
	if err := s.db.Exec("UPDATE coordinator_tasks SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	register(t, s, "w2", 2)
	second := claim(t, s, "w2")
	if second.ID != first.ID || second.FencingToken <= first.FencingToken {
		t.Fatalf("second claim %+v", second)
	}
	// The old worker's late upload with the old fence is refused.
	if _, err := s.AppendLog(ctx, first.ID, "w1", page(first.FencingToken, 1, 2, logEvent(2, "message", "zombie"))); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("zombie upload: %v", err)
	}
	p := page(second.FencingToken, 0, 1, logEvent(1, "message", "second-attempt"))
	p.Invocation = "inv-2"
	p.OffloadRunID = "run-9"
	if _, err := s.AppendLog(ctx, second.ID, "w2", p); err != nil {
		t.Fatal(err)
	}
	latest, err := s.ReadLog(ctx, first.ID, 0, 0, 100)
	if err != nil || latest.Epoch != second.FencingToken || len(latest.Entries) != 1 || !strings.Contains(string(latest.Entries[0].Event), "second-attempt") || !latest.Active {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	if len(latest.Epochs) != 2 {
		t.Fatalf("epochs %+v", latest.Epochs)
	}
	old, err := s.ReadLog(ctx, first.ID, first.FencingToken, 0, 100)
	if err != nil || old.Active || len(old.Entries) != 1 || !strings.Contains(string(old.Entries[0].Event), "first-attempt") {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	if old.Streams[0].State != "interrupted" {
		t.Fatalf("an unfinished stream of an ended epoch must read as interrupted: %+v", old.Streams)
	}
}

func TestReadLogPaginationAvailabilityAndPurge(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	f := task.FencingToken

	pending, err := s.ReadLog(ctx, task.ID, 0, 0, 10)
	if err != nil || pending.Availability != AvailabilityPending || len(pending.Entries) != 0 {
		t.Fatalf("before any upload: %+v %v", pending, err)
	}
	if _, err := s.ReadLog(ctx, "missing", 0, 0, 10); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("unknown task: %v", err)
	}
	if _, err := s.ReadLog(ctx, task.ID, 0, -1, 10); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative cursor: %v", err)
	}
	events := make([]joblog.Event, 5)
	for i := range events {
		events[i] = logEvent(int64(i+1), "message", string(rune('a'+i)))
	}
	if _, err := s.AppendLog(ctx, task.ID, "w1", page(f, 0, 5, events...)); err != nil {
		t.Fatal(err)
	}
	var after int64
	var texts []string
	for i := 0; i < 5; i++ {
		r, err := s.ReadLog(ctx, task.ID, 0, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range r.Entries {
			var ev joblog.Event
			_ = json.Unmarshal(e.Event, &ev)
			texts = append(texts, ev.Text)
		}
		after = r.NextAfter
		if !r.HasMore {
			break
		}
	}
	if strings.Join(texts, "") != "abcde" {
		t.Fatalf("paged read: %v", texts)
	}
	// Reading never reports a live stream once the task has finished and the worker closed it.
	end := page(f, 5, 5)
	end.End = true
	if _, err := s.AppendLog(ctx, task.ID, "w1", end); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, task.ID, "w1", f, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	r, _ := s.ReadLog(ctx, task.ID, 0, 0, 10)
	if r.Active || r.Streams[0].State != "ended" || len(r.Entries) != 5 {
		t.Fatalf("after completion: %+v", r)
	}
	// Retention removes content but keeps the stream so it reads as expired.
	if n, err := s.PurgeLogs(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("recent logs must stay: n=%d err=%v", n, err)
	}
	if n, err := s.PurgeLogs(ctx, time.Now().Add(time.Hour)); err != nil || n != 5 {
		t.Fatalf("purge: n=%d err=%v", n, err)
	}
	r, _ = s.ReadLog(ctx, task.ID, 0, 0, 10)
	if len(r.Entries) != 0 || r.Streams[0].State != "expired" || r.Streams[0].Availability != joblog.Expired {
		t.Fatalf("after purge: %+v", r)
	}
}

// httpLogStore routes the HTTP handler tests to a real store with fixed tokens.
type httpLogStore struct {
	*Store
}

func (h httpLogStore) VerifyToken(_ context.Context, secret string) (*Token, error) {
	switch secret {
	case "w1", "w2":
		return &Token{Name: secret, Scope: ScopeWorker}, nil
	case "admin":
		return &Token{Name: "admin", Scope: ScopeAdmin}, nil
	}
	return nil, ErrToken
}

func TestLogHTTPAuthorizationAndBounds(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	register(t, s, "w2", 1)
	h := NewPlatformHandler(httpLogStore{s}, &adminTestService{})
	do := func(method, path, token, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	body, _ := json.Marshal(page(task.FencingToken, 0, 1, logEvent(1, "message", "hello")))
	up := "/worker/v1/tasks/" + task.ID + "/logs"

	if code, _ := do("POST", up, "", string(body)); code != 401 {
		t.Fatalf("anonymous upload: %d", code)
	}
	if code, _ := do("POST", up, "admin", string(body)); code != 401 {
		t.Fatalf("admin token must not upload worker output: %d", code)
	}
	if code, _ := do("POST", up, "w2", string(body)); code != 409 {
		t.Fatalf("other worker: %d", code)
	}
	if code, _ := do("GET", up, "w1", ""); code != 405 {
		t.Fatalf("upload is POST only: %d", code)
	}
	if code, _ := do("POST", up, "w1", `{"fencing_token":1,"unknown":true}`); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	huge := `{"fencing_token":1,"invocation":"` + strings.Repeat("a", MaxLogPageBytes) + `"}`
	if code, _ := do("POST", up, "w1", huge); code != 413 {
		t.Fatalf("oversize body: %d", code)
	}
	if code, out := do("POST", up, "w1", string(body)); code != 200 || !strings.Contains(out, `"stored":1`) {
		t.Fatalf("owner upload: %d %s", code, out)
	}

	read := "/admin/v1/tasks/" + task.ID + "/logs"
	if code, _ := do("GET", read, "", ""); code != 401 {
		t.Fatalf("anonymous read: %d", code)
	}
	if code, _ := do("GET", read, "w1", ""); code != 403 {
		t.Fatalf("worker token must not read via admin API: %d", code)
	}
	if code, _ := do("POST", read, "admin", ""); code != 405 {
		t.Fatalf("read is GET only: %d", code)
	}
	for _, bad := range []string{"?limit=x", "?after=-1", "?extra=1&epoch=1&after=0&limit=1"} {
		if code, _ := do("GET", read+bad, "admin", ""); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	if code, _ := do("GET", "/admin/v1/tasks/missing/logs", "admin", ""); code != 404 {
		t.Fatalf("unknown task: %d", code)
	}
	code, out := do("GET", read+"?limit=1", "admin", "")
	if code != http.StatusOK || !strings.Contains(out, "hello") {
		t.Fatalf("admin read: %d %s", code, out)
	}
	// The dashboard snapshot path must never carry raw output.
	board, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(board)
	if strings.Contains(string(raw), "hello") {
		t.Fatal("board leaked execution output")
	}
}
