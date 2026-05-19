package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// AuthExtractor is the slice of the cloud auth handler the chat router
// needs. The chat package mounts its own routes (via RegisterRoutes)
// instead of relying on the cloud server's outer mux to apply auth, so
// it has to gate browser routes with RequireUser and worker routes with
// RequireMachine itself. The real implementation is auth.Handler; tests
// inject a stub.
type AuthExtractor interface {
	UserFromContext(ctx context.Context) *cloud.User
	RequireUser(http.Handler) http.Handler
	RequireMachine(http.Handler) http.Handler
}

// Handler owns the in-memory map of active chat sessions, the
// per-machine ready queues, and the seven HTTP routes (see
// RegisterRoutes). It runs no subprocesses — workers do that.
type Handler struct {
	cfg  Config
	auth AuthExtractor

	sessionsMu sync.Mutex
	sessions   map[string]*Session // by Session.ID

	queuesMu sync.Mutex
	queues   map[string]chan *cloud.ChatAssignment // by machine_id

	gcCancel context.CancelFunc
}

// NewHandler builds a chat Handler. The (currently nil-tolerant) error
// return is preserved for forward-compat with config validation.
func NewHandler(cfg Config, auth AuthExtractor) (*Handler, error) {
	cfg = cfg.withDefaults()
	h := &Handler{
		cfg:      cfg,
		auth:     auth,
		sessions: make(map[string]*Session),
		queues:   make(map[string]chan *cloud.ChatAssignment),
	}
	gcCtx, cancel := context.WithCancel(context.Background())
	h.gcCancel = cancel
	go h.gcLoop(gcCtx)
	return h, nil
}

// Shutdown stops the GC goroutine and tears down every active session.
// The cloud server should call this on graceful stop.
func (h *Handler) Shutdown() {
	if h.gcCancel != nil {
		h.gcCancel()
	}
	h.sessionsMu.Lock()
	sessions := make([]*Session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.sessions = map[string]*Session{}
	h.sessionsMu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}

// RegisterRoutes mounts the chat router's routes on mux. Browser routes
// are wrapped in RequireUser (session cookie or personal Bearer); worker
// routes in RequireMachine (kind=machine Bearer). The wrapping happens
// here, not at the outer mux — cloud server's NewServerWithExtras only
// calls RegisterRoutes(mux), it doesn't introspect or rewrap chat paths.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mountUser := func(pattern string, fn http.HandlerFunc) {
		if h.auth == nil {
			mux.HandleFunc(pattern, fn)
			return
		}
		mux.Handle(pattern, h.auth.RequireUser(fn))
	}
	mountMachine := func(pattern string, fn http.HandlerFunc) {
		if h.auth == nil {
			mux.HandleFunc(pattern, fn)
			return
		}
		mux.Handle(pattern, h.auth.RequireMachine(fn))
	}

	// Browser side — user-auth (session cookie or personal Bearer).
	mountUser("POST /api/cloud/chat/sessions", h.handleCreate)
	mountUser("GET /api/cloud/chat/sessions", h.handleList)
	mountUser("GET /api/cloud/chat/sessions/{id}/stream", h.handleStream)
	mountUser("POST /api/cloud/chat/sessions/{id}/message", h.handleMessage)
	mountUser("DELETE /api/cloud/chat/sessions/{id}", h.handleDelete)

	// Worker side — machine-auth (kind=machine Bearer).
	mountMachine("POST /api/worker/chat/poll", h.handlePoll)
	mountMachine("POST /api/worker/chat/sessions/{id}/events", h.handleWorkerEvents)
	mountMachine("POST /api/worker/chat/sessions/{id}/usage", h.handleWorkerUsage)
}

// ChatPath is the URL prefix the handler serves under. Kept for
// callers that introspect routes (none today; reserved for future
// per-prefix middleware).
const ChatPath = "/api/cloud/chat"

// ---- Browser handlers ----

// handleCreate spawns a new session.
//
// Body:     {"repo":"owner/name","message":"..."}
// Response: {"id":"...","repo":"...","work_dir":"","created_at":"..."}
//
// work_dir is always empty in the new router model; we keep the field
// in the JSON for backward compat with the existing browser client.
func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	user := h.user(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	var req struct {
		Repo    string `json:"repo"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Repo = strings.TrimSpace(req.Repo)
	if req.Repo == "" {
		writeError(w, http.StatusBadRequest, "repo is required")
		return
	}

	machineID, platform, baseURL, err := h.resolveBinding(req.Repo)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	now := h.cfg.Now()
	s := newSession(user.ID, req.Repo, machineID, now)
	h.sessionsMu.Lock()
	h.sessions[s.ID] = s
	h.sessionsMu.Unlock()

	h.enqueue(machineID, &cloud.ChatAssignment{
		SessionID: s.ID,
		UserID:    user.ID,
		Repo:      req.Repo,
		Platform:  platform,
		BaseURL:   baseURL,
		Message:   req.Message,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         s.ID,
		"repo":       s.Repo,
		"work_dir":   "", // legacy field; cloud no longer owns a clone
		"created_at": s.CreatedAt,
	})
}

// handleList returns the current user's active sessions.
func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	user := h.user(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	h.sessionsMu.Lock()
	out := make([]map[string]any, 0)
	for _, s := range h.sessions {
		if s.UserID != user.ID {
			continue
		}
		out = append(out, map[string]any{
			"id":         s.ID,
			"repo":       s.Repo,
			"created_at": s.CreatedAt,
		})
	}
	h.sessionsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleStream is the SSE endpoint. Browser opens with
// EventSource(`/api/cloud/chat/sessions/${id}/stream`) and receives
// `data: {...}\n\n` frames until the worker emits an end / error.
func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request) {
	user := h.user(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	id := r.PathValue("id")
	s := h.lookup(id, user.ID)
	if s == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	heartbeat := time.NewTicker(h.cfg.HeartbeatEvery)
	defer heartbeat.Stop()

	events := s.Events()
	for {
		select {
		case <-r.Context().Done():
			// Browser disconnected; leave the session alive for the
			// GC loop to reap on idle.
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
			if ev.Type == cloud.ChatEventEnd || ev.Type == cloud.ChatEventError {
				return
			}
		}
	}
}

// handleMessage queues a follow-up. MVP semantics: each follow-up
// becomes a fresh assignment on the same machine, reusing the existing
// session id. The browser stays subscribed to the same SSE stream and
// sees new events arrive in-order. If the existing session has already
// closed we 410 — the browser is expected to create a new session in
// that case.
func (h *Handler) handleMessage(w http.ResponseWriter, r *http.Request) {
	user := h.user(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	id := r.PathValue("id")
	s := h.lookup(id, user.ID)
	if s == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if s.Closed() {
		writeError(w, http.StatusGone, "session closed")
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	// Re-resolve binding in case the operator rebound the repo since
	// the session was created.
	machineID, platform, baseURL, err := h.resolveBinding(s.Repo)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	h.enqueue(machineID, &cloud.ChatAssignment{
		SessionID: s.ID,
		UserID:    user.ID,
		Repo:      s.Repo,
		Platform:  platform,
		BaseURL:   baseURL,
		Message:   req.Message,
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	user := h.user(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	id := r.PathValue("id")
	s := h.lookup(id, user.ID)
	if s == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	h.sessionsMu.Lock()
	delete(h.sessions, id)
	h.sessionsMu.Unlock()
	s.terminate("session terminated", h.cfg.Now())
	w.WriteHeader(http.StatusNoContent)
}

// ---- Worker handlers ----

// handlePoll long-polls for the next ChatAssignment targeted at the
// caller's machine. The worker authenticates via RequireMachine in the
// outer mux; we accept the machine_id off the request body and trust
// it (in test mode there is no middleware to enforce a match).
func (h *Handler) handlePoll(w http.ResponseWriter, r *http.Request) {
	var req cloud.ChatPollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.MachineID == "" {
		writeError(w, http.StatusBadRequest, "machine_id is required")
		return
	}
	wait := defaultPollWait
	if req.WaitSeconds > 0 {
		wait = time.Duration(req.WaitSeconds) * time.Second
		if wait > maxPollWait {
			wait = maxPollWait
		}
	}

	q := h.queueFor(req.MachineID)
	select {
	case a := <-q:
		writeJSON(w, http.StatusOK, cloud.ChatPollResponse{Assignment: a})
	case <-time.After(wait):
		// No work in this window; tell the worker to re-poll.
		w.WriteHeader(http.StatusNoContent)
	case <-r.Context().Done():
		// Client gave up first (e.g. shutdown). 499-ish.
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleWorkerUsage receives the terminal token / cost breakdown for
// one chat session, posted by the worker after its claude subprocess
// exits. The session_id comes from the URL; user_id / machine_id / repo
// are looked up server-side from the active session registry so the
// worker can't claim usage for a session it doesn't own.
func (h *Handler) handleWorkerUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req cloud.ChatUsageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Usage == nil {
		writeError(w, http.StatusBadRequest, "usage is required")
		return
	}

	h.sessionsMu.Lock()
	s, ok := h.sessions[id]
	h.sessionsMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if err := h.cfg.Store.AddChatUsage(cloud.AddChatUsageInput{
		SessionID: s.ID,
		UserID:    s.UserID,
		MachineID: s.MachineID,
		Repo:      s.Repo,
		Usage:     req.Usage,
		EndedAt:   h.cfg.Now(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWorkerEvents accepts a batch of ChatEvents pushed by the
// worker and fans them out onto the session's SSE channel. An
// end-typed event in the batch closes the session.
func (h *Handler) handleWorkerEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var req cloud.WorkerEventsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	h.sessionsMu.Lock()
	s, ok := h.sessions[id]
	h.sessionsMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	terminal := false
	for _, ev := range req.Events {
		if ev.Time.IsZero() {
			ev.Time = h.cfg.Now()
		}
		s.emit(ev)
		if ev.Type == cloud.ChatEventEnd || ev.Type == cloud.ChatEventError {
			terminal = true
		}
	}
	if terminal {
		s.Close()
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers ----

func (h *Handler) user(r *http.Request) *cloud.User {
	if h.auth == nil {
		return nil
	}
	return h.auth.UserFromContext(r.Context())
}

// lookup returns the session if it exists and belongs to userID;
// nil otherwise (treated as "not found" to avoid leaking session
// existence across users).
func (h *Handler) lookup(id, userID string) *Session {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	s, ok := h.sessions[id]
	if !ok || s.UserID != userID {
		return nil
	}
	return s
}

// queueFor returns the per-machine ready channel, lazily creating it.
// Buffer size 64 means a single worker that pauses polling can have
// up to 64 queued assignments before enqueue starts dropping — well
// past any realistic chat burst.
func (h *Handler) queueFor(machineID string) chan *cloud.ChatAssignment {
	h.queuesMu.Lock()
	defer h.queuesMu.Unlock()
	q, ok := h.queues[machineID]
	if !ok {
		q = make(chan *cloud.ChatAssignment, 64)
		h.queues[machineID] = q
	}
	return q
}

// enqueue pushes an assignment onto a machine's ready queue. Drops
// (with a session-error emit) if the queue is full — better than
// blocking the create handler.
func (h *Handler) enqueue(machineID string, a *cloud.ChatAssignment) {
	q := h.queueFor(machineID)
	select {
	case q <- a:
	default:
		// Backpressure: tell the browser the assignment was dropped.
		h.sessionsMu.Lock()
		s := h.sessions[a.SessionID]
		h.sessionsMu.Unlock()
		if s != nil {
			s.emit(cloud.ChatEvent{
				Type: cloud.ChatEventError,
				Text: "machine queue full; try again later",
				Time: h.cfg.Now(),
			})
			s.Close()
		}
	}
}

// resolveBinding finds the machine that owns `repo` via the store's
// bindings table. Returns (machine_id, platform, base_url). Returns
// an error suitable for a 409 response when no binding exists.
//
// Today the bindings table only carries machine + repo/project IDs.
// To map "owner/name" → repo_id we walk the repo list once per call.
// That's O(repos) but the cloud config is small; if it grows we can
// add a Store method later.
func (h *Handler) resolveBinding(repo string) (machineID, platform, baseURL string, err error) {
	if h.cfg.Store == nil {
		return "", "", "", errors.New("no store configured")
	}
	sum := h.cfg.Store.Summary()
	var repoRec *cloud.Repo
	for _, r := range sum.Repos {
		if r.Name == repo {
			repoRec = r
			break
		}
	}
	if repoRec == nil {
		return "", "", "", fmt.Errorf("repo %q not registered: add it under Cloud → Repos and bind a machine", repo)
	}
	platform = repoRec.Platform
	if platform == "" {
		platform = "github"
	}

	// Prefer a direct repo binding; fall back to a project binding.
	for _, b := range sum.Bindings {
		if b.RepoID != "" && b.RepoID == repoRec.ID {
			return b.MachineID, platform, baseURL, nil
		}
	}
	if repoRec.ProjectID != "" {
		for _, b := range sum.Bindings {
			if b.ProjectID != "" && b.ProjectID == repoRec.ProjectID {
				return b.MachineID, platform, baseURL, nil
			}
		}
	}
	return "", "", "", fmt.Errorf("no machine bound to repo %q", repo)
}

// gcLoop wakes every defaultGCSweepEvery and reaps sessions that have
// gone idle past defaultSessionIdle.
func (h *Handler) gcLoop(ctx context.Context) {
	tick := time.NewTicker(defaultGCSweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			h.gcSweep(now)
		}
	}
}

func (h *Handler) gcSweep(now time.Time) {
	h.sessionsMu.Lock()
	stale := make([]*Session, 0)
	for id, s := range h.sessions {
		if s.Idle(now) > defaultSessionIdle {
			stale = append(stale, s)
			delete(h.sessions, id)
		}
	}
	h.sessionsMu.Unlock()
	for _, s := range stale {
		s.Close()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
