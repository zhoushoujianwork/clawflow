package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// resolveUser returns the User authenticated by either the session cookie or
// a Bearer token, plus the APIToken row when authentication was via Bearer.
// Both return nil when no valid credential is present.
//
// Bearer takes precedence over the cookie so a CLI hitting an endpoint that
// the same machine happens to have a session for still gets attributed to
// the personal/machine token (matters for audit + revocation).
func (h *Handler) resolveUser(r *http.Request) (*cloud.User, *cloud.APIToken) {
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
		plain := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
		if plain != "" {
			tok, _ := h.store.LookupAPIToken(plain)
			if tok != nil {
				if u, _ := h.store.GetUser(tok.UserID); u != nil {
					return u, tok
				}
			}
		}
	}
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		sess, _ := h.store.GetSession(cookie.Value)
		if sess != nil {
			if u, _ := h.store.GetUser(sess.UserID); u != nil {
				return u, nil
			}
		}
	}
	return nil, nil
}

// RequireUser returns middleware that 401s when no credential resolves to a
// user. On success, the User (and APIToken, when applicable) are injected
// into the request context — downstream handlers read via UserFromContext.
func (h *Handler) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, tok := h.resolveUser(r)
		if user == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
			return
		}
		ctx := withUser(r.Context(), user)
		if tok != nil {
			ctx = withToken(ctx, tok)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireMachine is RequireUser plus the constraint that authentication was
// done via a kind="machine" API token. Used by the worker protocol
// (/api/worker/*) so a stolen personal token can't lease cloud jobs.
func (h *Handler) RequireMachine(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, tok := h.resolveUser(r)
		if user == nil || tok == nil || tok.Kind != cloud.APITokenKindMachine {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "machine credential required"})
			return
		}
		ctx := withUser(r.Context(), user)
		ctx = withToken(ctx, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TokenFromContext on *Handler satisfies cloud.AuthHandler so server.go can
// read the authenticated machine token without importing the auth package.
func (h *Handler) TokenFromContext(ctx context.Context) *cloud.APIToken {
	return TokenFromContext(ctx)
}
