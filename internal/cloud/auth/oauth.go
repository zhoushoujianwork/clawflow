package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

const (
	stateCookieName   = "clawflow_oauth_state"
	sessionCookieName = "clawflow_session"
	stateCookieMaxAge = 600 // 10 minutes — covers a normal GitHub authorize round-trip
)

// handleLogin initiates the GitHub OAuth flow.
//
//	GET /api/v1/github/app/login?next=/repos
//
// Generates a random nonce, HMACs it with cfg.SessionKey, drops the cookie
// `clawflow_oauth_state=<nonce>.<hmac>.<next_b64>`, and 302-redirects the
// user to GitHub's authorize URL.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ClientID == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "oauth not configured"})
		return
	}
	nonce := randomToken(16)
	next := sanitizeNext(r.URL.Query().Get("next"))

	state := encodeState(nonce, next, h.cfg.SessionKey)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/",
		MaxAge:   stateCookieMaxAge,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})

	redirectURI := h.cfg.PublicURL + "/api/v1/github/app/callback"
	q := url.Values{
		"client_id":    {h.cfg.ClientID},
		"state":        {state},
		"redirect_uri": {redirectURI},
	}
	authorizeURL := "https://github.com/login/oauth/authorize?" + q.Encode()
	http.Redirect(w, r, authorizeURL, http.StatusFound)
}

// handleCallback completes the OAuth flow.
//
//	GET /api/v1/github/app/callback?code=...&state=...&installation_id=...
//
// 1. Verify the state cookie matches the query state.
// 2. Exchange the code for a user access token.
// 3. Fetch /user and /user/installations.
// 4. Upsert the user, refresh the user→installations cache.
// 5. Create a session, set cookie, redirect to `next` (defaults to "/").
func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing code"})
		return
	}
	cookie, err := r.Cookie(stateCookieName)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing state cookie"})
		return
	}
	if !hmac.Equal([]byte(cookie.Value), []byte(state)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "state mismatch"})
		return
	}
	_, next, ok := decodeState(state, h.cfg.SessionKey)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid state"})
		return
	}
	// State cookie is single-use.
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: "", Path: "/", MaxAge: -1})

	tok, err := h.exchangeCode(r.Context(), code, h.cfg.PublicURL+"/api/v1/github/app/callback")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if tok.AccessToken == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("github exchange: %s %s", tok.Error, tok.ErrorDescription)})
		return
	}

	user, err := h.loginGitHubUser(r.Context(), tok.AccessToken)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	sess, err := h.store.CreateSession(user.ID, h.cfg.SessionTTL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	// If the caller asked us to land on a specific path (e.g. /dashboard
	// once the Web UI is mounted), redirect there. Otherwise render a
	// terminal page — the cloud server in this PR does not yet bundle a
	// Web UI, so blindly redirecting to "/" would 404.
	if next != "" && next != "/" {
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	renderLoginSuccess(w, user)
}

// renderLoginSuccess writes a self-contained HTML page confirming the user
// is signed in. Intentionally style-free and dependency-free; PR 2 will
// replace this with a real Web UI redirect.
func renderLoginSuccess(w http.ResponseWriter, user *cloud.User) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>ClawFlow Login</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
         max-width: 480px; margin: 8rem auto; padding: 0 1rem; color: #1f2328; }
  h1 { font-size: 1.3rem; margin-bottom: 0.5rem; }
  p { color: #57606a; line-height: 1.5; }
  code { background: #f6f8fa; padding: 0.1rem 0.3rem; border-radius: 4px; }
</style></head>
<body>
  <h1>Signed in as %s</h1>
  <p>The ClawFlow Web UI is not deployed on this cloud yet.
     You can close this window and continue from the terminal:</p>
  <p><code>clawflow worker register</code></p>
  <p style="margin-top:2rem; color:#8c959f; font-size:0.85rem;">
    Browser session is active for 30 days. To sign out:
    <code>curl -X POST -b clawflow_session=&lt;cookie&gt; ./api/v1/auth/logout</code>
  </p>
</body></html>`, htmlEscape(user.Login))
}

// htmlEscape is a tiny stdlib-free escape for the user login string. Login
// values are GitHub-validated to [a-zA-Z0-9-], so this is defense in depth.
func htmlEscape(s string) string {
	r := s
	for _, pair := range [][2]string{
		{"&", "&amp;"}, {"<", "&lt;"}, {">", "&gt;"}, {`"`, "&quot;"},
	} {
		r = strings.ReplaceAll(r, pair[0], pair[1])
	}
	return r
}

// loginGitHubUser is the shared completion path for both browser OAuth and
// CLI device flow once we have a GitHub user access token in hand. Upserts
// the user, refreshes the cached installation list, and returns the user.
func (h *Handler) loginGitHubUser(ctx context.Context, githubUserToken string) (*cloud.User, error) {
	gu, err := h.fetchUser(ctx, githubUserToken)
	if err != nil {
		return nil, err
	}
	user, err := h.store.UpsertUser(cloud.UpsertUserRequest{
		GitHubID:  gu.ID,
		Login:     gu.Login,
		Name:      gu.Name,
		AvatarURL: gu.AvatarURL,
	})
	if err != nil {
		return nil, fmt.Errorf("upsert user: %w", err)
	}

	// Best-effort installation refresh. Failure here should not block login.
	installs, err := h.fetchUserInstallations(ctx, githubUserToken)
	if err == nil {
		for _, inst := range installs {
			rec, err := h.store.UpsertInstallation(cloud.UpsertInstallationRequest{
				GitHubInstallationID: inst.ID,
				AccountLogin:         inst.Account.Login,
				AccountType:          inst.Account.Type,
			})
			if err != nil {
				continue
			}
			_ = h.store.LinkUserInstallation(user.ID, rec.ID)
		}
	}
	return user, nil
}

// handleLogout deletes the session row and clears the cookie.
func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		_ = h.store.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleMe returns the currently authenticated user, or 401.
func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	user, _ := h.resolveUser(r)
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         user.ID,
		"github_id":  user.GitHubID,
		"login":      user.Login,
		"name":       user.Name,
		"avatar_url": user.AvatarURL,
	})
}

// ---- State cookie codec ----

// encodeState packages the OAuth state cookie value as nonce.next_b64.hmac.
// The HMAC binds nonce and next so neither can be tampered with.
func encodeState(nonce, next string, key []byte) string {
	nextB64 := base64.RawURLEncoding.EncodeToString([]byte(next))
	payload := nonce + "." + nextB64
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// decodeState reverses encodeState. Returns (nonce, next, ok).
func decodeState(state string, key []byte) (string, string, bool) {
	parts := strings.Split(state, ".")
	if len(parts) != 3 {
		return "", "", false
	}
	nonce, nextB64, sig := parts[0], parts[1], parts[2]
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(nonce + "." + nextB64))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", "", false
	}
	next, err := base64.RawURLEncoding.DecodeString(nextB64)
	if err != nil {
		return "", "", false
	}
	return nonce, string(next), true
}

// sanitizeNext restricts the `next` parameter to relative paths starting
// with "/" so an attacker cannot use the login flow as an open redirector.
func sanitizeNext(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") {
		return "/"
	}
	return s
}

// randomToken returns a hex-encoded random string of n bytes.
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeJSON is a small helper to keep handlers tidy.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

