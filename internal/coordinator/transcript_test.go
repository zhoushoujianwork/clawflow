package coordinator

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func tev(seq int64, caller, kind string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"schema_version":1,"seq":%d,"ts":"2026-11-02T00:00:00Z","run_id":"run1","caller_run_id":%q,"type":%q,"text":"e%d"}`, seq, caller, kind, seq))
}

func upload(fence int64, caller string, events ...json.RawMessage) TranscriptUpload {
	return TranscriptUpload{FencingToken: fence, CallerRunID: caller, RunID: "run1", CaptureID: "cap1", Availability: "available", State: "running", Completeness: "in_progress", Events: events}
}

func leased(t *testing.T) (*Store, *Task) {
	t.Helper()
	s, _, _ := setup(t)
	register(t, s, "w1", 1)
	register(t, s, "w2", 1)
	enqueue(t, s, 1)
	return s, claim(t, s, "w1")
}

func TestTranscriptRequiresCurrentOwnerAndFence(t *testing.T) {
	s, task := leased(t)
	good := upload(task.FencingToken, "call-1", tev(1, "call-1", "capture_start"))
	if _, err := s.AppendTranscript(ctx, task.ID, "w2", good); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("other worker: %v", err)
	}
	stale := good
	stale.FencingToken = task.FencingToken - 1
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", stale); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale fence: %v", err)
	}
	ahead := good
	ahead.FencingToken = task.FencingToken + 1
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", ahead); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("future fence: %v", err)
	}
	if _, err := s.AppendTranscript(ctx, "missing", "w1", good); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("unknown task: %v", err)
	}
	if ack, err := s.AppendTranscript(ctx, task.ID, "w1", good); err != nil || ack.AcceptedThrough != 1 {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	var count int64
	if err := s.db.Model(&TaskTranscriptEvent{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("rejected pages stored content: count=%d err=%v", count, err)
	}
}

func TestTranscriptDuplicateAndOutOfOrderPages(t *testing.T) {
	s, task := leased(t)
	f := task.FencingToken
	first := upload(f, "call-1", tev(1, "call-1", "capture_start"), tev(2, "call-1", "message"))
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", first); err != nil {
		t.Fatal(err)
	}
	// An identical retry (lost acknowledgement) changes nothing.
	ack, err := s.AppendTranscript(ctx, task.ID, "w1", first)
	if err != nil || ack.AcceptedThrough != 2 {
		t.Fatalf("duplicate: ack=%+v err=%v", ack, err)
	}
	// A page that skips ahead is refused, so history is never silently joined.
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", tev(4, "call-1", "message"))); !errors.Is(err, ErrTranscriptOrder) {
		t.Fatalf("gap page: %v", err)
	}
	// Overlap with new tail is accepted exactly once.
	ack, err = s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", tev(2, "call-1", "message"), tev(3, "call-1", "tool_call")))
	if err != nil || ack.AcceptedThrough != 3 {
		t.Fatalf("overlap: ack=%+v err=%v", ack, err)
	}
	// Events inside one page must be strictly increasing.
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", tev(5, "call-1", "message"), tev(4, "call-1", "message"))); !errors.Is(err, ErrTranscriptOrder) {
		t.Fatalf("unordered page: %v", err)
	}
	view, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID})
	if err != nil || len(view.Events) != 3 || view.NextAfter != 3 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	for i, raw := range view.Events {
		if !strings.Contains(string(raw), fmt.Sprintf(`"seq": %d`, i+1)) && !strings.Contains(string(raw), fmt.Sprintf(`"seq":%d`, i+1)) {
			t.Fatalf("event %d out of order: %s", i, raw)
		}
	}
}

func TestTranscriptIdentityBindingAndBounds(t *testing.T) {
	s, task := leased(t)
	f := task.FencingToken
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", tev(1, "call-1", "message"))); err != nil {
		t.Fatal(err)
	}
	// Events must carry the page's own caller run ID.
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", tev(2, "other", "message"))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign event: %v", err)
	}
	// Same invocation cannot change its Offload run identity.
	changed := upload(f, "call-1", tev(2, "call-1", "message"))
	changed.RunID = "run2"
	for i := range changed.Events {
		changed.Events[i] = json.RawMessage(strings.ReplaceAll(string(changed.Events[i]), "run1", "run2"))
	}
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", changed); !errors.Is(err, ErrTranscriptIdentity) {
		t.Fatalf("changed run: %v", err)
	}
	// Unknown vocabulary and oversized content are rejected before storage.
	bad := upload(f, "call-1", tev(2, "call-1", "message"))
	bad.Availability = "bogus"
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("availability: %v", err)
	}
	huge := json.RawMessage(fmt.Sprintf(`{"seq":2,"run_id":"run1","caller_run_id":"call-1","type":"message","text":%q}`, strings.Repeat("x", MaxTranscriptEventBytes)))
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", huge)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized event: %v", err)
	}
	many := make([]json.RawMessage, MaxTranscriptPageEvents+1)
	for i := range many {
		many[i] = tev(int64(i+2), "call-1", "message")
	}
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", many...)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too many events: %v", err)
	}
	nonAvailable := upload(f, "call-1", tev(2, "call-1", "message"))
	nonAvailable.Availability = "capture_disabled"
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", nonAvailable); !errors.Is(err, ErrInvalid) {
		t.Fatalf("content with non-available status: %v", err)
	}
}

func TestTranscriptStorageCapReportsGap(t *testing.T) {
	s, task := leased(t)
	f := task.FencingToken
	body := strings.Repeat("y", 100<<10)
	seq := int64(0)
	var capped bool
	for i := 0; i < 400 && !capped; i++ {
		var evs []json.RawMessage
		for j := 0; j < 10; j++ {
			seq++
			evs = append(evs, json.RawMessage(fmt.Sprintf(`{"seq":%d,"run_id":"run1","caller_run_id":"call-1","type":"message","text":%q}`, seq, body)))
		}
		ack, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", evs...))
		if err != nil {
			t.Fatal(err)
		}
		capped = ack.Capped
	}
	if !capped {
		t.Fatal("storage cap never reached")
	}
	view, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID, After: 0, Limit: 500})
	if err != nil || !view.Gap || !view.Capped || !strings.Contains(view.GapReason, "limit") {
		t.Fatalf("view gap=%v capped=%v reason=%q err=%v", view.Gap, view.Capped, view.GapReason, err)
	}
}

func TestTranscriptReadIsBoundedPagedAndResumable(t *testing.T) {
	s, task := leased(t)
	f := task.FencingToken
	var evs []json.RawMessage
	for i := int64(1); i <= 25; i++ {
		evs = append(evs, tev(i, "call-1", "message"))
	}
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(f, "call-1", evs...)); err != nil {
		t.Fatal(err)
	}
	var after int64
	var seen []int64
	for pages := 0; pages < 10; pages++ {
		view, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID, After: after, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(view.Events) > 10 {
			t.Fatalf("unbounded page: %d", len(view.Events))
		}
		for _, raw := range view.Events {
			var h struct{ Seq int64 }
			_ = json.Unmarshal(raw, &h)
			seen = append(seen, h.Seq)
		}
		after = view.NextAfter
		if !view.HasMore {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("seen=%v", seen)
	}
	for i, seq := range seen {
		if seq != int64(i+1) {
			t.Fatalf("seen=%v", seen)
		}
	}
	over, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID, After: 999})
	if err != nil || !over.Gap || len(over.Events) != 0 {
		t.Fatalf("after beyond history: %+v err=%v", over, err)
	}
	if _, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: "nope"}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("unknown task: %v", err)
	}
}

func TestTranscriptToolResultCorrelationIsPreserved(t *testing.T) {
	s, task := leased(t)
	call := json.RawMessage(`{"seq":1,"run_id":"run1","caller_run_id":"call-1","type":"tool_call","tool_call_id":"item_1","tool_name":"command_execution","arguments":"ls"}`)
	result := json.RawMessage(`{"seq":2,"run_id":"run1","caller_run_id":"call-1","type":"tool_result","tool_call_id":"item_1","output":"a\n","is_error":false,"exit_code":0}`)
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(task.FencingToken, "call-1", call, result)); err != nil {
		t.Fatal(err)
	}
	view, _ := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID})
	if len(view.Events) != 2 {
		t.Fatalf("events=%d", len(view.Events))
	}
	var a, b struct {
		ToolCallID string `json:"tool_call_id"`
		Type       string `json:"type"`
	}
	_ = json.Unmarshal(view.Events[0], &a)
	_ = json.Unmarshal(view.Events[1], &b)
	if a.ToolCallID != "item_1" || a.ToolCallID != b.ToolCallID || a.Type != "tool_call" || b.Type != "tool_result" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestTranscriptEpochsAreIsolatedAcrossRetries(t *testing.T) {
	s, task := leased(t)
	first := task.FencingToken
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(first, "call-old", tev(1, "call-old", "message"))); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, task.ID, "w1", first, "retry"); err != nil {
		t.Fatal(err)
	}
	second := claim(t, s, "w2")
	if second.FencingToken <= first {
		t.Fatalf("fence did not advance: %d -> %d", first, second.FencingToken)
	}
	// The old owner and epoch can no longer write.
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(first, "call-old", tev(2, "call-old", "message"))); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("old epoch: %v", err)
	}
	if _, err := s.AppendTranscript(ctx, task.ID, "w2", upload(second.FencingToken, "call-new", tev(1, "call-new", "message"))); err != nil {
		t.Fatal(err)
	}
	view, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID})
	if err != nil || view.Epoch != second.FencingToken || len(view.Events) != 1 || !strings.Contains(string(view.Events[0]), "call-new") {
		t.Fatalf("latest epoch leaked: %+v err=%v", view, err)
	}
	old, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID, Epoch: first})
	if err == nil && len(old.Events) > 0 && strings.Contains(string(old.Events[0]), "call-new") {
		t.Fatalf("epochs mixed: %+v", old)
	}
	if _, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID, Epoch: 9999}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("unknown epoch: %v", err)
	}
}

func TestTranscriptStatusesAreDistinct(t *testing.T) {
	s, task := leased(t)
	f := task.FencingToken
	view, _ := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID})
	if view.Status != "no_output_yet" || !view.Live {
		t.Fatalf("running, nothing reported: %+v", view)
	}
	cases := []struct{ availability, want string }{
		{"capture_disabled", "not_captured"}, {"unsupported_version", "unsupported"}, {"unsupported_backend", "unsupported"},
		{"expired", "expired"}, {"unreadable", "unreadable"}, {"no_captured_content", "no_content"}, {"not_yet_available", "no_output_yet"},
	}
	for _, c := range cases {
		s2, task2 := leased(t)
		u := upload(task2.FencingToken, "call-1")
		u.Availability = c.availability
		u.Reason = "reason for " + c.availability
		if _, err := s2.AppendTranscript(ctx, task2.ID, "w1", u); err != nil {
			t.Fatalf("%s: %v", c.availability, err)
		}
		v, err := s2.ReadTranscript(ctx, TranscriptQuery{TaskID: task2.ID})
		if err != nil || v.Status != c.want {
			t.Fatalf("%s -> %q want %q (%v)", c.availability, v.Status, c.want, err)
		}
	}
	_ = f
}

func TestTranscriptNeverCapturedExecutionIsNotInvented(t *testing.T) {
	s, task := leased(t)
	if err := s.Fail(ctx, task.ID, "w1", task.FencingToken, json.RawMessage(`{"status":"failed"}`)); err != nil {
		t.Fatal(err)
	}
	view, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID})
	if err != nil || view.Status != "not_captured" || len(view.Events) != 0 || view.Live {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(task.FencingToken, "call-1", tev(1, "call-1", "message"))); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("append after report: %v", err)
	}
}

func TestTranscriptRetentionKeepsExpiredHeader(t *testing.T) {
	s, task := leased(t)
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(task.FencingToken, "call-1", tev(1, "call-1", "message"))); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneTranscripts(ctx, time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned=%d err=%v", n, err)
	}
	view, err := s.ReadTranscript(ctx, TranscriptQuery{TaskID: task.ID})
	if err != nil || view.Status != "expired" || len(view.Events) != 0 || !view.Gap {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

func TestTranscriptNotInBoardOrTaskPayload(t *testing.T) {
	s, task := leased(t)
	secret := json.RawMessage(`{"seq":1,"run_id":"run1","caller_run_id":"call-1","type":"message","text":"UNIQUE-OUTPUT-MARKER"}`)
	if _, err := s.AppendTranscript(ctx, task.ID, "w1", upload(task.FencingToken, "call-1", secret)); err != nil {
		t.Fatal(err)
	}
	board, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(board)
	if strings.Contains(string(raw), "UNIQUE-OUTPUT-MARKER") {
		t.Fatal("execution output leaked into the global board")
	}
	events, _ := s.TaskEvents(ctx, task.ID)
	raw, _ = json.Marshal(events)
	if strings.Contains(string(raw), "UNIQUE-OUTPUT-MARKER") {
		t.Fatal("execution output leaked into scheduling events")
	}
}
