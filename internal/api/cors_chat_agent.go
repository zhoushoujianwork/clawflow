// Package api — cors_chat_agent gates cross-origin access to the local
// chat-spawn endpoint so a remote ClawFlow cloud bundle (e.g.
// clawflow.daboluo.cc) can detect this machine's local `clawflow web`
// and spawn a native terminal here instead of the in-browser drawer.
//
// Security model: only the EXACT cloud URL the user has explicitly
// configured via `clawflow login` (written to ~/.clawflow/config/
// worker.yaml as `saas_url`) is allowed. Random websites cannot reach
// `/api/chat/spawn` from a remote origin — the Access-Control-Allow-
// Origin header is only emitted when r.Origin matches that URL, so the
// browser blocks the request before it reaches the handler.
//
// This means the user must `clawflow login` once for the cloud → local
// terminal flow to work, which is already the prereq for cloud chat at
// all. No new config knob.
package api

import (
	"net/http"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// WithChatAgentCORS wraps the local-agent endpoints (/api/version,
// /api/chat/spawn) with origin-restricted CORS. Same-origin (no Origin
// header, or Origin matching this server) passes through unchanged.
func WithChatAgentCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && isAllowedAgentOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "300")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			// Preflight: 204 with whatever CORS headers were set above
			// (or none, which the browser interprets as "not allowed").
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

// isAllowedAgentOrigin returns true when origin exactly matches the
// configured cloud URL (no scheme/path tolerance — strict equality
// against the trimmed saas_url). Empty cloud config means no
// cross-origin access; the user must `clawflow login` first.
func isAllowedAgentOrigin(origin string) bool {
	cfg, err := cloud.LoadConfig()
	if err != nil {
		return false
	}
	cloudURL := strings.TrimRight(cfg.BaseURL, "/")
	if cloudURL == "" {
		return false
	}
	return origin == cloudURL
}
