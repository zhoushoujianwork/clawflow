package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// ---- Test-only auth plumbing ----

// fakeAuth is the test-side AuthExtractor that pulls the user out of
// the request context (set by withUser). Production uses auth.Handler.
type fakeAuth struct{}

type ctxKey int

const ctxKeyUser ctxKey = 0

func (fakeAuth) UserFromContext(ctx context.Context) *cloud.User {
	u, _ := ctx.Value(ctxKeyUser).(*cloud.User)
	return u
}

// TokenFromContext returns nil in test mode (no machine token is injected
// by fakeAuth), so the machine ownership guard is a no-op in existing tests.
func (fakeAuth) TokenFromContext(_ context.Context) *cloud.APIToken { return nil }

// RequireUser / RequireMachine are pass-through in tests: the user is
// already in context via the withUser wrapper below. Production uses
// auth.Handler's real session/Bearer validation.
func (fakeAuth) RequireUser(next http.Handler) http.Handler    { return next }
func (fakeAuth) RequireMachine(next http.Handler) http.Handler { return next }

func withUser(h http.Handler, u *cloud.User) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyUser, u)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---- Fixture helpers ----

// makeBoundRepo creates a repo + binding in the store so a chat
// session against `repo` resolves to the supplied machineID.
func makeBoundRepo(t *testing.T, store cloud.Store, repo, machineID string) {
	t.Helper()
	// We need a Machine row for CreateBinding's existence check.
	// Skip RegisterWorker (it mints tokens we don't need) and inject
	// directly via the typed Store path.
	mem, ok := store.(*cloud.MemoryStore)
	if !ok {
		t.Fatalf("makeBoundRepo expects *MemoryStore, got %T", store)
	}
	mem.Machines[machineID] = &cloud.Machine{
		ID:         machineID,
		Hostname:   "fixture-host",
		LastSeenAt: time.Now(),
	}
	r, err := store.CreateRepo(cloud.CreateRepoRequest{Name: repo, Platform: "github"})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	if _, err := store.CreateBinding(cloud.CreateBindingRequest{
		MachineID: machineID,
		RepoID:    r.ID,
	}); err != nil {
		t.Fatalf("create binding: %v", err)
	}
}

func newTestHandler(t *testing.T) (*Handler, cloud.Store) {
	t.Helper()
	store := cloud.NewMemoryStore()
	h, err := NewHandler(Config{
		Store:          store,
		Now:            time.Now,
		HeartbeatEvery: 100 * time.Millisecond, // keep wall-clock test time low
	}, fakeAuth{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	t.Cleanup(h.Shutdown)
	return h, store
}

func newTestServer(t *testing.T, h *Handler, user *cloud.User) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewServer(withUser(mux, user))
	t.Cleanup(srv.Close)
	return srv
}

func jsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

// ---- Tests ----

// TestSessionCreateRequiresBinding: POST /sessions with no binding
// must return 409 with a "no machine bound" body.
func TestSessionCreateRequiresBinding(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	srv := newTestServer(t, h, &cloud.User{ID: "u1", Login: "alice"})

	body := jsonBody(t, map[string]string{"repo": "acme/widgets", "message": "hello"})
	resp, err := http.Post(srv.URL+"/api/cloud/chat/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var errBody map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if !strings.Contains(strings.ToLower(errBody["error"]), "no machine bound") &&
		!strings.Contains(strings.ToLower(errBody["error"]), "not registered") {
		t.Errorf("want binding error, got %q", errBody["error"])
	}
}

// TestEnqueueAndPoll: create a session for a repo with a binding, then
// poll as the worker for that machine and confirm we receive the
// assignment.
func TestEnqueueAndPoll(t *testing.T) {
	t.Parallel()
	h, store := newTestHandler(t)
	makeBoundRepo(t, store, "acme/widgets", "machine-1")
	srv := newTestServer(t, h, &cloud.User{ID: "u1", Login: "alice"})

	// Create session.
	body := jsonBody(t, map[string]string{"repo": "acme/widgets", "message": "hello world"})
	resp, err := http.Post(srv.URL+"/api/cloud/chat/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("post create: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct {
		ID      string `json:"id"`
		Repo    string `json:"repo"`
		WorkDir string `json:"work_dir"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	resp.Body.Close()
	if created.ID == "" {
		t.Fatal("created session has empty id")
	}
	if created.WorkDir != "" {
		t.Errorf("work_dir = %q, want empty (router model)", created.WorkDir)
	}

	// Poll as the bound machine.
	pollBody := jsonBody(t, cloud.ChatPollRequest{
		MachineID:   "machine-1",
		WorkerID:    "worker-1",
		WaitSeconds: 2,
	})
	pResp, err := http.Post(srv.URL+"/api/worker/chat/poll", "application/json", pollBody)
	if err != nil {
		t.Fatalf("post poll: %v", err)
	}
	defer pResp.Body.Close()
	if pResp.StatusCode != http.StatusOK {
		t.Fatalf("poll status = %d, want 200", pResp.StatusCode)
	}
	var pollResp cloud.ChatPollResponse
	if err := json.NewDecoder(pResp.Body).Decode(&pollResp); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	if pollResp.Assignment == nil {
		t.Fatal("poll returned no assignment")
	}
	if pollResp.Assignment.SessionID != created.ID {
		t.Errorf("assignment session_id = %q, want %q",
			pollResp.Assignment.SessionID, created.ID)
	}
	if pollResp.Assignment.Repo != "acme/widgets" {
		t.Errorf("assignment repo = %q", pollResp.Assignment.Repo)
	}
	if pollResp.Assignment.Message != "hello world" {
		t.Errorf("assignment message = %q", pollResp.Assignment.Message)
	}
	if pollResp.Assignment.Platform != "github" {
		t.Errorf("assignment platform = %q, want github", pollResp.Assignment.Platform)
	}
}

// TestWorkerEventsRelay: the worker POSTs a batch of events; the
// browser SSE stream sees them in order, and an `end` event closes the
// stream.
func TestWorkerEventsRelay(t *testing.T) {
	t.Parallel()
	h, store := newTestHandler(t)
	makeBoundRepo(t, store, "acme/widgets", "machine-1")
	srv := newTestServer(t, h, &cloud.User{ID: "u1", Login: "alice"})

	// 1. Create session.
	body := jsonBody(t, map[string]string{"repo": "acme/widgets", "message": "go"})
	cResp, err := http.Post(srv.URL+"/api/cloud/chat/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(cResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	cResp.Body.Close()

	// 2. Subscribe to SSE in a goroutine.
	streamReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/api/cloud/chat/sessions/"+created.ID+"/stream", nil)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", streamResp.StatusCode)
	}

	streamCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var collected strings.Builder
		for {
			n, err := streamResp.Body.Read(buf)
			if n > 0 {
				collected.Write(buf[:n])
				if strings.Contains(collected.String(), `"type":"end"`) {
					streamCh <- collected.String()
					return
				}
			}
			if err != nil {
				streamCh <- collected.String()
				return
			}
		}
	}()

	// Give the SSE handler a moment to attach to the session's
	// eventCh before the worker writes — otherwise the buffered
	// channel will accept the writes regardless, but we'd rather
	// exercise the realistic ordering.
	time.Sleep(50 * time.Millisecond)

	// 3. Worker pushes events.
	now := time.Now().UTC()
	evBody := jsonBody(t, cloud.WorkerEventsRequest{
		Events: []cloud.ChatEvent{
			{Type: cloud.ChatEventOutput, Text: "first", Time: now},
			{Type: cloud.ChatEventOutput, Text: "second", Time: now.Add(time.Millisecond)},
			{Type: cloud.ChatEventEnd, Time: now.Add(2 * time.Millisecond)},
		},
	})
	eResp, err := http.Post(
		srv.URL+"/api/worker/chat/sessions/"+created.ID+"/events",
		"application/json", evBody)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	eResp.Body.Close()
	if eResp.StatusCode != http.StatusNoContent {
		t.Fatalf("events status = %d", eResp.StatusCode)
	}

	// 4. Verify the SSE stream got all three frames in order.
	var collected string
	select {
	case collected = <-streamCh:
	case <-time.After(3 * time.Second):
		t.Fatal("stream read timed out")
	}
	firstIdx := strings.Index(collected, `"text":"first"`)
	secondIdx := strings.Index(collected, `"text":"second"`)
	endIdx := strings.Index(collected, `"type":"end"`)
	if firstIdx < 0 || secondIdx < 0 || endIdx < 0 {
		t.Fatalf("missing event(s) in stream:\n%s", collected)
	}
	if !(firstIdx < secondIdx && secondIdx < endIdx) {
		t.Errorf("events out of order: first=%d second=%d end=%d",
			firstIdx, secondIdx, endIdx)
	}
}

// TestPollTimeout: no work queued → 204 after wait_seconds.
func TestPollTimeout(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	srv := newTestServer(t, h, &cloud.User{ID: "u1", Login: "alice"})

	start := time.Now()
	body := jsonBody(t, cloud.ChatPollRequest{
		MachineID:   "machine-without-work",
		WorkerID:    "worker-1",
		WaitSeconds: 1,
	})
	resp, err := http.Post(srv.URL+"/api/worker/chat/poll", "application/json", body)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed < 800*time.Millisecond {
		t.Errorf("poll returned in %v, want >= ~1s long-poll wait", elapsed)
	}
}

// TestDeleteSendsErrorAndClosesStream: DELETE on an active session
// must surface an error frame to the SSE consumer and shut the stream.
func TestDeleteSendsErrorAndClosesStream(t *testing.T) {
	t.Parallel()
	h, store := newTestHandler(t)
	makeBoundRepo(t, store, "acme/widgets", "machine-1")
	srv := newTestServer(t, h, &cloud.User{ID: "u1", Login: "alice"})

	body := jsonBody(t, map[string]string{"repo": "acme/widgets", "message": "go"})
	cResp, err := http.Post(srv.URL+"/api/cloud/chat/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(cResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cResp.Body.Close()

	streamReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/api/cloud/chat/sessions/"+created.ID+"/stream", nil)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer streamResp.Body.Close()

	streamCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var collected strings.Builder
		for {
			n, err := streamResp.Body.Read(buf)
			if n > 0 {
				collected.Write(buf[:n])
				if strings.Contains(collected.String(), `"type":"error"`) {
					streamCh <- collected.String()
					return
				}
			}
			if err != nil {
				streamCh <- collected.String()
				return
			}
		}
	}()

	// Let the SSE handler latch onto the eventCh.
	time.Sleep(50 * time.Millisecond)

	delReq, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/api/cloud/chat/sessions/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", delResp.StatusCode)
	}

	var collected string
	select {
	case collected = <-streamCh:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not close after DELETE")
	}
	if !strings.Contains(collected, `"type":"error"`) {
		t.Errorf("missing error frame:\n%s", collected)
	}
}

// TestListSessionsScopedToUser: List endpoint must not leak other
// users' sessions.
func TestListSessionsScopedToUser(t *testing.T) {
	t.Parallel()
	h, store := newTestHandler(t)
	makeBoundRepo(t, store, "acme/widgets", "machine-1")

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	alice := &cloud.User{ID: "u-alice", Login: "alice"}
	bob := &cloud.User{ID: "u-bob", Login: "bob"}

	// Two muxed servers, one wrapped per user.
	aSrv := httptest.NewServer(withUser(mux, alice))
	defer aSrv.Close()
	bSrv := httptest.NewServer(withUser(mux, bob))
	defer bSrv.Close()

	// Alice creates a session.
	body := jsonBody(t, map[string]string{"repo": "acme/widgets", "message": "hi"})
	resp, err := http.Post(aSrv.URL+"/api/cloud/chat/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("alice create: %v", err)
	}
	resp.Body.Close()

	// Bob lists — should see zero sessions.
	lResp, err := http.Get(bSrv.URL + "/api/cloud/chat/sessions")
	if err != nil {
		t.Fatalf("bob list: %v", err)
	}
	defer lResp.Body.Close()
	var listed struct {
		Sessions []any `json:"sessions"`
	}
	if err := json.NewDecoder(lResp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Sessions) != 0 {
		t.Errorf("bob sees %d sessions, want 0", len(listed.Sessions))
	}
}

// TestWorkerUsageEndpoint: worker POSTs the terminal token / cost
// breakdown for a chat session; the store row gets denorm fields
// (user_id, machine_id, repo) from the session registry; 404 on unknown
// session; 400 on missing Usage body.
func TestWorkerUsageEndpoint(t *testing.T) {
	t.Parallel()
	h, store := newTestHandler(t)
	makeBoundRepo(t, store, "acme/widgets", "machine-1")
	srv := newTestServer(t, h, &cloud.User{ID: "u-alice", Login: "alice"})

	// Create a session (this populates the in-memory session registry
	// with user/machine/repo the usage endpoint will look up).
	body := jsonBody(t, map[string]string{"repo": "acme/widgets", "message": "hi"})
	cResp, err := http.Post(srv.URL+"/api/cloud/chat/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(cResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cResp.Body.Close()

	// Happy path.
	usageReq := cloud.ChatUsageRequest{
		Usage: &cloud.Usage{
			DurationMs:   4500,
			NumTurns:     1,
			TotalCostUSD: 0.0321,
			InputTokens:  420,
			OutputTokens: 88,
			ModelUsage: map[string]cloud.ModelUsage{
				"claude-opus-4-7": {InputTokens: 420, OutputTokens: 88, CostUSD: 0.0321},
			},
		},
	}
	uResp, err := http.Post(
		srv.URL+"/api/worker/chat/sessions/"+created.ID+"/usage",
		"application/json", jsonBody(t, usageReq))
	if err != nil {
		t.Fatalf("usage post: %v", err)
	}
	uResp.Body.Close()
	if uResp.StatusCode != http.StatusNoContent {
		t.Fatalf("usage status = %d, want 204", uResp.StatusCode)
	}

	got := store.GetChatUsage(created.ID)
	if got == nil {
		t.Fatal("GetChatUsage returned nil after worker upload")
	}
	if got.UserID != "u-alice" {
		t.Errorf("denorm user_id = %q, want u-alice", got.UserID)
	}
	if got.MachineID != "machine-1" {
		t.Errorf("denorm machine_id = %q, want machine-1", got.MachineID)
	}
	if got.Repo != "acme/widgets" {
		t.Errorf("denorm repo = %q", got.Repo)
	}
	if got.TotalCostUSD != 0.0321 || got.InputTokens != 420 {
		t.Errorf("usage round-trip mismatch: %+v", got)
	}
	if got.ModelUsage["claude-opus-4-7"].CostUSD != 0.0321 {
		t.Errorf("model usage lost: %+v", got.ModelUsage)
	}

	// Unknown session.
	resp404, err := http.Post(
		srv.URL+"/api/worker/chat/sessions/does-not-exist/usage",
		"application/json", jsonBody(t, usageReq))
	if err != nil {
		t.Fatalf("404 post: %v", err)
	}
	resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session status = %d, want 404", resp404.StatusCode)
	}

	// Missing usage body.
	resp400, err := http.Post(
		srv.URL+"/api/worker/chat/sessions/"+created.ID+"/usage",
		"application/json", jsonBody(t, cloud.ChatUsageRequest{Usage: nil}))
	if err != nil {
		t.Fatalf("400 post: %v", err)
	}
	resp400.Body.Close()
	if resp400.StatusCode != http.StatusBadRequest {
		t.Errorf("missing usage status = %d, want 400", resp400.StatusCode)
	}
}
