package coordinator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// transcriptAPIStore verifies identity and fence at the HTTP boundary without PostgreSQL.
type transcriptAPIStore struct {
	apiStore
	uploads []TranscriptUpload
	worker  string
}

func (s *transcriptAPIStore) VerifyToken(_ context.Context, secret string) (*Token, error) {
	switch secret {
	case "valid":
		return &Token{Name: "worker"}, nil
	case "other":
		return &Token{Name: "intruder"}, nil
	case "admin":
		return &Token{Name: "ops", Scope: ScopeAdmin}, nil
	}
	return nil, ErrToken
}
func (s *transcriptAPIStore) AppendTranscript(_ context.Context, task, worker string, in TranscriptUpload) (TranscriptAck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if task != "task" || worker != "worker" || in.FencingToken != 7 {
		return TranscriptAck{}, ErrStaleLease
	}
	if in.CursorIn == "bad-order" {
		return TranscriptAck{}, ErrTranscriptOrder
	}
	s.uploads = append(s.uploads, in)
	s.worker = worker
	return TranscriptAck{OK: true, AcceptedThrough: int64(len(in.Events))}, nil
}

type transcriptAdmin struct {
	adminTestService
	queries []TranscriptQuery
}

func (a *transcriptAdmin) ReadTranscript(_ context.Context, q TranscriptQuery) (*TranscriptView, error) {
	a.queries = append(a.queries, q)
	if q.TaskID == "missing" {
		return nil, ErrTaskNotFound
	}
	return &TranscriptView{SchemaVersion: 1, TaskID: q.TaskID, Status: "available", Events: []json.RawMessage{}}, nil
}

func TestWorkerTranscriptUploadBindsIdentityAndFence(t *testing.T) {
	store := &transcriptAPIStore{}
	server := httptest.NewTLSServer(NewHandler(store))
	defer server.Close()
	page := `{"fencing_token":7,"caller_run_id":"call-1","availability":"available","events":[]}`
	for _, tc := range []struct {
		name, path, auth, body string
		status                 int
	}{
		{"accepted", "tasks/task/transcript", "Bearer valid", page, 200},
		{"other worker", "tasks/task/transcript", "Bearer other", page, 409},
		{"stale fence", "tasks/task/transcript", "Bearer valid", strings.Replace(page, `"fencing_token":7`, `"fencing_token":6`, 1), 409},
		{"no token", "tasks/task/transcript", "", page, 401},
		{"body cannot pick identity", "tasks/task/transcript", "Bearer valid", `{"fencing_token":7,"worker":"victim","caller_run_id":"call-1"}`, 400},
		{"unknown task", "tasks/elsewhere/transcript", "Bearer valid", page, 409},
		{"oversized", "tasks/task/transcript", "Bearer valid", `{"fencing_token":7,"reason":"` + strings.Repeat("x", MaxTranscriptRequestBytes) + `"}`, 413},
		{"order refusal", "tasks/task/transcript", "Bearer valid", strings.Replace(page, `"caller_run_id"`, `"cursor_in":"bad-order","caller_run_id"`, 1), 422},
	} {
		if status, body := request(t, server, tc.path, tc.auth, tc.body); status != tc.status {
			t.Errorf("%s: %d want %d: %s", tc.name, status, tc.status, body)
		}
	}
	if len(store.uploads) != 1 || store.worker != "worker" {
		t.Fatalf("uploads=%d worker=%q", len(store.uploads), store.worker)
	}
	// Routine accepted pages are not audited; refusals are.
	refusals := 0
	for _, a := range store.audits {
		if strings.Contains(a.Action, "transcript:200") {
			t.Fatalf("accepted page audited: %v", a)
		}
		if strings.Contains(a.Action, "transcript:") {
			refusals++
		}
	}
	if refusals == 0 {
		t.Fatal("refusals were not audited")
	}
}

func TestAdminTranscriptRequiresAdminAndIsReadOnly(t *testing.T) {
	store, service := &transcriptAPIStore{}, &transcriptAdmin{}
	h := NewPlatformHandler(store, service)
	do := func(method, path, token string) int {
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := do("GET", "/admin/v1/tasks/t1/transcript", ""); got != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", got)
	}
	if got := do("GET", "/admin/v1/tasks/t1/transcript", "valid"); got != http.StatusForbidden {
		t.Errorf("worker token: %d", got)
	}
	if got := do("POST", "/admin/v1/tasks/t1/transcript", "admin"); got != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", got)
	}
	if got := do("GET", "/admin/v1/tasks/t1/transcript?after=-1", "admin"); got != http.StatusBadRequest {
		t.Errorf("negative cursor: %d", got)
	}
	if got := do("GET", "/admin/v1/tasks/missing/transcript", "admin"); got != http.StatusNotFound {
		t.Errorf("missing: %d", got)
	}
	if got := do("GET", "/admin/v1/tasks/t1/transcript?epoch=3&after=12&limit=50&invocation=2", "admin"); got != http.StatusOK {
		t.Fatalf("read: %d", got)
	}
	last := service.queries[len(service.queries)-1]
	if last.TaskID != "t1" || last.Epoch != 3 || last.After != 12 || last.Limit != 50 || last.Invocation != 2 {
		t.Fatalf("query=%+v", last)
	}
	// Opening is audited; a follow-up incremental poll is not.
	store.mu.Lock()
	before := len(store.audits)
	store.mu.Unlock()
	do("GET", "/admin/v1/tasks/t1/transcript?after=12", "admin")
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.audits) != before {
		t.Errorf("incremental poll audited")
	}
}

func TestDashboardModelCarriesNoTranscriptFields(t *testing.T) {
	raw, _ := json.Marshal(Board{Tasks: []BoardTask{{ID: "t"}}})
	for _, forbidden := range []string{"transcript", "events", "output", "arguments"} {
		if strings.Contains(strings.ToLower(string(raw)), `"`+forbidden+`"`) {
			t.Errorf("board exposes %q", forbidden)
		}
	}
}
