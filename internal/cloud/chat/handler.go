package chat

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// AuthExtractor is the minimal interface the chat handler needs to
// know which user owns a request. The cloud server passes its real
// auth.Handler in via NewHandler — we accept the narrow interface
// rather than importing the auth package.
type AuthExtractor interface {
	UserFromContext(ctx context.Context) *cloud.User
}

// Handler owns the in-memory map of active chat sessions and serves
// the five HTTP routes (see RegisterRoutes).
type Handler struct {
	cfg  Config
	auth AuthExtractor

	// appPrivateKey is parsed once at NewHandler time. nil when no
	// GitHub App is configured (only public repos can be cloned).
	appPrivateKey *rsa.PrivateKey

	sessionsMu sync.Mutex
	sessions   map[string]*Session

	tokenMu    sync.Mutex
	tokenCache map[int64]installationTokenEntry

	cloneLocksMu sync.Mutex
	cloneLocks   map[string]*sync.Mutex

	gcCancel context.CancelFunc
}

// NewHandler builds a chat Handler. Returns an error only when the
// configured GitHub App key file is unreadable — every other
// misconfiguration is reported per-request (e.g. no anthropic key
// → 503).
func NewHandler(cfg Config, auth AuthExtractor) (*Handler, error) {
	cfg = cfg.withDefaults()
	h := &Handler{
		cfg:        cfg,
		auth:       auth,
		sessions:   make(map[string]*Session),
		tokenCache: make(map[int64]installationTokenEntry),
		cloneLocks: make(map[string]*sync.Mutex),
	}
	if cfg.GitHubAppPrivateKeyPath != "" {
		key, err := loadRSAPrivateKey(cfg.GitHubAppPrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load github app private key: %w", err)
		}
		h.appPrivateKey = key
	}

	gcCtx, cancel := context.WithCancel(context.Background())
	h.gcCancel = cancel
	go h.gcLoop(gcCtx)
	return h, nil
}

// Shutdown stops the GC goroutine and kills every active session.
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

// RegisterRoutes mounts the chat routes on mux. All require an
// authenticated user.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/cloud/chat/sessions", h.handleCreate)
	mux.HandleFunc("GET /api/cloud/chat/sessions", h.handleList)
	mux.HandleFunc("GET /api/cloud/chat/sessions/{id}/stream", h.handleStream)
	mux.HandleFunc("POST /api/cloud/chat/sessions/{id}/message", h.handleMessage)
	mux.HandleFunc("DELETE /api/cloud/chat/sessions/{id}", h.handleDelete)
}

// ChatPath is the URL prefix the handler serves under.
const ChatPath = "/api/cloud/chat"

// ---- handlers ----

// handleCreate spawns a new session.
// Body: {"repo":"owner/name","message":"..."}
// Response: {"id":"...","repo":"...","work_dir":"...","created_at":"..."}
func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	user := h.user(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "auth required")
		return
	}
	if h.cfg.AnthropicAPIKey == "" {
		writeError(w, http.StatusServiceUnavailable, "cloud chat disabled: no ANTHROPIC_API_KEY configured")
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
	if req.Repo == "" {
		writeError(w, http.StatusBadRequest, "repo is required")
		return
	}

	path, err := h.EnsureClone(r.Context(), req.Repo)
	if err != nil {
		writeError(w, http.StatusBadRequest, "clone: "+err.Error())
		return
	}

	now := h.cfg.Now()
	s := newSession(user.ID, req.Repo, path, now)

	// Subprocess context is independent of the HTTP request: the
	// request closes once we return the session id, but the
	// subprocess must outlive it.
	procCtx, cancel := context.WithTimeout(context.Background(), defaultSubprocessTTL)
	s.procCancel = cancel

	if err := s.start(procCtx, h.cfg.ClaudeBin, h.cfg.AnthropicAPIKey, req.Message); err != nil {
		cancel()
		writeError(w, http.StatusInternalServerError, "spawn: "+err.Error())
		return
	}

	h.sessionsMu.Lock()
	h.sessions[s.ID] = s
	h.sessionsMu.Unlock()

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         s.ID,
		"repo":       s.Repo,
		"work_dir":   s.WorkDir,
		"created_at": s.CreatedAt,
	})
}

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
// `data: {...}\n\n` frames until the subprocess exits.
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
	// Disable nginx proxy buffering.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	heartbeat := time.NewTicker(defaultSSEHeartbeatEvery)
	defer heartbeat.Stop()

	events := s.Events()
	for {
		select {
		case <-r.Context().Done():
			// Browser disconnected; leave the session alive for the
			// GC loop to reap on idle.
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-events:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
			if ev.Type == "end" || ev.Type == "error" {
				return
			}
		}
	}
}

// handleMessage queues a follow-up. The SSE stream keeps emitting (or,
// more precisely, the previous stream ends and EventSource auto-
// reconnects to a fresh one — see Session.send).
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
	s.touch(h.cfg.Now())

	if s.procCancel != nil {
		s.procCancel()
	}
	procCtx, cancel := context.WithTimeout(context.Background(), defaultSubprocessTTL)
	s.procCancel = cancel
	if err := s.send(procCtx, h.cfg.ClaudeBin, h.cfg.AnthropicAPIKey, req.Message); err != nil {
		cancel()
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
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
	s.Close()
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
