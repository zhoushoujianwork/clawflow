// Local HTTP proxy that the SaaS browser UI can call on 127.0.0.1:38400 to:
//
//  1. List projects on a private-network GitLab the SaaS can't reach directly.
//  2. Run the "Test connection" for a repo bound to this worker (skipping the
//     SaaS which also can't reach private GitLab).
//
// Why localhost: the SaaS page lives on HTTPS; private GitLab is usually HTTP.
// HTTPS → HTTP is blocked by browser Mixed Content policy and nothing on the
// server side unlocks it. HTTPS → http://127.0.0.1 is specifically exempt, so
// we run the bridge here.
//
// Security: bind to 127.0.0.1 only (loopback), require a SaaS-issued JWT in
// Authorization header, verify by round-tripping to {saas_url}/api/auth/me,
// refuse if the returned org_id doesn't match the one our worker_token was
// registered under. CORS allow-list = exactly the configured saas_url — no
// wildcards.
package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// ProxyPort is fixed for this release. A flag can be added later if anyone
// hits a port-conflict; localhost-only means collisions are only with other
// local dev tools.
const ProxyPort = 38400

// authCacheTTL caps how long we trust a validated JWT. Short enough that a
// revoked session can't keep using the proxy for long; long enough to avoid
// hitting SaaS on every browser fetch. 5 minutes mirrors the CSRF TTL used in
// the SaaS App install flow — similar trust window.
const authCacheTTL = 5 * time.Minute

type authCacheEntry struct {
	orgID     string
	validated time.Time
}

// orgID fetched from /api/auth/me against our worker_token at proxy start.
// We compare incoming-JWT's org against this — if they don't match the caller
// belongs to a different org than this CLI is registered to, so refuse.
type proxyDeps struct {
	wc           *config.WorkerConfig
	workerOrgID  string // cached — populated in startLocalProxy
	saasOrigin   string // exact CORS allow-origin (stripped of path)
	httpClient   *http.Client
	authCacheMu  sync.RWMutex
	authCache    map[string]authCacheEntry
}

// startLocalProxy spins up the proxy server as a goroutine. Returns a shutdown
// func; caller is expected to defer it. An error on bind is fatal — the rest
// of the worker loop keeps polling and we just log the proxy is off.
func startLocalProxy(wc *config.WorkerConfig) (shutdown func(), err error) {
	deps := &proxyDeps{
		wc:         wc,
		saasOrigin: stripToOrigin(wc.SaasURL),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		authCache:  make(map[string]authCacheEntry),
	}

	// Fail fast if the SaaS doesn't know us — a running proxy whose
	// worker_token is invalid can't authorize anything anyway. Resolved once
	// at boot; retries happen at request time if this initial lookup fails.
	if orgID, err := deps.lookupWorkerOrgID(); err == nil {
		deps.workerOrgID = orgID
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/local/health", deps.handleHealth)
	mux.HandleFunc("/local/gitlab/projects", deps.handleGitLabProjects)
	mux.HandleFunc("/local/test-repo", deps.handleTestRepo)
	mux.HandleFunc("/local/test-integration", deps.handleTestIntegration)

	addr := fmt.Sprintf("127.0.0.1:%d", ProxyPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           deps.corsMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Bind synchronously so we can surface port-in-use errors back to the
	// caller before the worker loop prints its banner.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("proxy listen %s: %w", addr, err)
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[proxy] serve error: %v\n", err)
		}
	}()
	fmt.Printf("  proxy:    http://%s (browser bridge for private GitLab)\n", addr)

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}

// ── CORS ──────────────────────────────────────────────────────────────────

// corsMiddleware restricts Access-Control-Allow-Origin to the configured
// saas_url exactly — no wildcards, no dynamic reflection. Preflight (OPTIONS)
// is answered directly here so handlers don't have to special-case method.
func (d *proxyDeps) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && origin == d.saasOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			// `PRIVATE-TOKEN` is here because browsers send it during preflight
			// when the GitLab-listing fetch passes an optional PAT override —
			// without it, the preflight succeeds but the real request is blocked.
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, PRIVATE-TOKEN")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// stripToOrigin reduces a URL to the scheme + host[:port] form a browser sends
// in the Origin header. Trailing path and query are dropped.
func stripToOrigin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimSuffix(rawURL, "/")
	}
	return u.Scheme + "://" + u.Host
}

// ── Auth ──────────────────────────────────────────────────────────────────

type authedUser struct {
	userID string
	orgID  string
}

// authenticate parses the Authorization header and validates against SaaS. On
// success, also enforces that the caller's org_id matches the worker's — a
// coworker signed into the same SaaS can't piggyback on our running proxy.
func (d *proxyDeps) authenticate(r *http.Request) (*authedUser, int, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, http.StatusUnauthorized, fmt.Errorf("missing Bearer token")
	}
	jwt := strings.TrimPrefix(h, "Bearer ")
	if jwt == "" {
		return nil, http.StatusUnauthorized, fmt.Errorf("empty Bearer token")
	}

	// Cache key = hash of the JWT so we never log the raw token.
	sum := sha256.Sum256([]byte(jwt))
	key := hex.EncodeToString(sum[:])

	d.authCacheMu.RLock()
	e, ok := d.authCache[key]
	d.authCacheMu.RUnlock()
	if ok && time.Since(e.validated) < authCacheTTL {
		return d.bindOrReject(e.orgID)
	}

	// Hit SaaS. Timeout is baked into httpClient.
	req, _ := http.NewRequest(http.MethodGet, d.wc.SaasURL+"/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("auth verify: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("saas rejected token (%d)", resp.StatusCode)
	}
	var me struct {
		UserID string `json:"user_id"`
		OrgID  string `json:"org_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return nil, http.StatusBadGateway, fmt.Errorf("saas returned malformed /auth/me: %w", err)
	}
	if me.OrgID == "" {
		return nil, http.StatusUnauthorized, fmt.Errorf("no org in token")
	}

	d.authCacheMu.Lock()
	d.authCache[key] = authCacheEntry{orgID: me.OrgID, validated: time.Now()}
	d.authCacheMu.Unlock()

	return d.bindOrReject(me.OrgID)
}

// bindOrReject runs the org-pinning check after we know the caller's org.
// If the proxy's own org_id hasn't been resolved yet (boot-time SaaS error),
// try once more — otherwise accept, since the worker_token itself authenticates
// the proxy to this org.
func (d *proxyDeps) bindOrReject(callerOrg string) (*authedUser, int, error) {
	if d.workerOrgID == "" {
		if orgID, err := d.lookupWorkerOrgID(); err == nil {
			d.workerOrgID = orgID
		}
	}
	if d.workerOrgID != "" && d.workerOrgID != callerOrg {
		return nil, http.StatusForbidden, fmt.Errorf("token org does not match worker org")
	}
	return &authedUser{orgID: callerOrg}, 0, nil
}

// lookupWorkerOrgID hits /api/auth/me with the worker_token (which is
// org-scoped on the SaaS side) and returns the org it belongs to.
func (d *proxyDeps) lookupWorkerOrgID() (string, error) {
	req, _ := http.NewRequest(http.MethodGet, d.wc.SaasURL+"/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+d.wc.WorkerToken)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("saas returned %d for worker_token", resp.StatusCode)
	}
	var me struct {
		OrgID string `json:"org_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		return "", err
	}
	return me.OrgID, nil
}

// ── Handlers ──────────────────────────────────────────────────────────────

// GET /local/health — no auth. The browser calls this first as a feature probe
// to decide whether to prefer the local path over browser-direct. Intentionally
// does not leak sensitive identity; it's a feature flag.
func (d *proxyDeps) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"cli_version": cliVersionString(),
		"agent_id":    agentIDHeader(),
	})
}

// GET /local/gitlab/projects?base_url=<encoded>
// Auth required. Uses stored GitLab PAT from credentials.yaml; falls back to
// the `PRIVATE-TOKEN` header passed by the browser if the CLI has no PAT set
// (keeps the browser-entry path usable even before `clawflow config` is run).
func (d *proxyDeps) handleGitLabProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "GET only"})
		return
	}
	if _, status, err := d.authenticate(r); err != nil {
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	baseURL := r.URL.Query().Get("base_url")
	if baseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "base_url required"})
		return
	}
	token, err := resolveGitLabToken(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	// Same query the browser would have sent — keeps the frontend mapper stable.
	target := strings.TrimRight(baseURL, "/") + "/api/v4/projects?membership=true&simple=true&per_page=100"
	proxyReq, _ := http.NewRequest(http.MethodGet, target, nil)
	proxyReq.Header.Set("PRIVATE-TOKEN", token)
	resp, err := d.httpClient.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("gitlab unreachable: %v", err)})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// POST /local/test-repo {platform, full_name, base_url?}
// Auth required. Returns {ok: bool, message: string} — shape matches SaaS
// TestConnectionResponse so the frontend renders either path identically.
func (d *proxyDeps) handleTestRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
		return
	}
	if _, status, err := d.authenticate(r); err != nil {
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	var body struct {
		Platform string `json:"platform"`
		FullName string `json:"full_name"`
		BaseURL  string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	creds, _ := config.LoadCredentials()
	switch body.Platform {
	case "github":
		tok := creds.GHToken
		if tok == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "no GitHub PAT configured on CLI (~/.clawflow/config/credentials.yaml)"})
			return
		}
		ok, msg := testGitHubRepo(d.httpClient, body.BaseURL, tok, body.FullName)
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})
	case "gitlab":
		tok := creds.GitLabToken
		if tok == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "no GitLab PAT configured on CLI (~/.clawflow/config/credentials.yaml)"})
			return
		}
		// Resolve base_url: browser-supplied → local config.yaml by full_name
		// → gitlab.com default. The browser can't know the per-repo host
		// (it's not stored on SaaS), so falling through to the CLI's
		// config.yaml is how self-hosted instances actually get probed.
		host := body.BaseURL
		if host == "" {
			if cfg, err := config.Load(); err == nil {
				if r, ok := cfg.Repos[body.FullName]; ok && r.BaseURL != "" {
					host = r.BaseURL
				}
			}
		}
		if host == "" {
			host = "https://gitlab.com"
		}
		ok, msg := testGitLabRepo(d.httpClient, host, tok, body.FullName)
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown platform"})
	}
}

// POST /local/test-integration {platform, base_url}
// Auth required. Validates that the CLI's stored PAT works against the given
// instance — same need as /local/test-repo but scoped to the org-level
// integration (no specific repo). Hits `/api/v4/user` for GitLab, `/user` for
// GitHub, which only need read-user scope and are the cheapest "am I
// authenticated" check. Returns {ok, message}.
func (d *proxyDeps) handleTestIntegration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST only"})
		return
	}
	if _, status, err := d.authenticate(r); err != nil {
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	var body struct {
		Platform string `json:"platform"`
		BaseURL  string `json:"base_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	creds, _ := config.LoadCredentials()
	switch body.Platform {
	case "gitlab":
		tok := creds.GitLabToken
		if tok == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "no GitLab PAT configured on CLI"})
			return
		}
		host := body.BaseURL
		if host == "" {
			host = "https://gitlab.com"
		}
		ok, msg := testGitLabHost(d.httpClient, host, tok)
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})
	case "github":
		tok := creds.GHToken
		if tok == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "no GitHub PAT configured on CLI"})
			return
		}
		ok, msg := testGitHubHost(d.httpClient, body.BaseURL, tok)
		writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown platform"})
	}
}

func testGitLabHost(h *http.Client, baseURL, token string) (bool, string) {
	target := strings.TrimRight(baseURL, "/") + "/api/v4/user"
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := h.Do(req)
	if err != nil {
		return false, fmt.Sprintf("gitlab unreachable: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		var u struct {
			Username string `json:"username"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&u)
		if u.Username != "" {
			return true, fmt.Sprintf("OK — authenticated as %s on %s", u.Username, baseURL)
		}
		return true, fmt.Sprintf("OK — token valid on %s", baseURL)
	case 401:
		return false, "token rejected by GitLab (401)"
	default:
		return false, fmt.Sprintf("gitlab returned %d", resp.StatusCode)
	}
}

func testGitHubHost(h *http.Client, baseURL, token string) (bool, string) {
	host := baseURL
	if host == "" || host == "https://github.com" {
		host = "https://api.github.com"
	} else {
		host = strings.TrimRight(host, "/") + "/api/v3"
	}
	req, _ := http.NewRequest(http.MethodGet, host+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.Do(req)
	if err != nil {
		return false, fmt.Sprintf("github unreachable: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		var u struct {
			Login string `json:"login"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&u)
		if u.Login != "" {
			return true, fmt.Sprintf("OK — authenticated as %s", u.Login)
		}
		return true, "OK — token valid"
	case 401:
		return false, "token rejected by GitHub (401)"
	default:
		return false, fmt.Sprintf("github returned %d", resp.StatusCode)
	}
}

// resolveGitLabToken prefers the credentials.yaml value; if absent it falls
// back to an explicit PRIVATE-TOKEN header (so the browser can still supply
// one during a flow where the CLI hasn't been configured yet).
func resolveGitLabToken(r *http.Request) (string, error) {
	creds, _ := config.LoadCredentials()
	if creds.GitLabToken != "" {
		return creds.GitLabToken, nil
	}
	if h := r.Header.Get("PRIVATE-TOKEN"); h != "" {
		return h, nil
	}
	return "", fmt.Errorf("no GitLab PAT: set one via `clawflow config set --gitlab-token <glpat>` or pass PRIVATE-TOKEN header")
}

// ── Connection tests ──────────────────────────────────────────────────────
//
// These mirror the Rust-side test logic in
// `backend/crates/services/src/repo_service.rs::test_github_repo` /
// `test_gitlab_repo`. Kept intentionally minimal — they return ok=true when
// the repo exists and the token has read access, nothing else.

func testGitHubRepo(h *http.Client, baseURL, token, fullName string) (bool, string) {
	host := baseURL
	if host == "" || host == "https://github.com" {
		host = "https://api.github.com"
	} else {
		host = strings.TrimRight(host, "/") + "/api/v3"
	}
	req, _ := http.NewRequest(http.MethodGet, host+"/repos/"+fullName, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := h.Do(req)
	if err != nil {
		return false, fmt.Sprintf("github unreachable: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return true, fmt.Sprintf("OK — %s is reachable with the stored token", fullName)
	case 401:
		return false, "token rejected by GitHub (401)"
	case 404:
		return false, fmt.Sprintf("repo %s not found or token lacks access", fullName)
	default:
		return false, fmt.Sprintf("github returned %d", resp.StatusCode)
	}
}

func testGitLabRepo(h *http.Client, baseURL, token, fullName string) (bool, string) {
	encoded := strings.ReplaceAll(fullName, "/", "%2F")
	target := strings.TrimRight(baseURL, "/") + "/api/v4/projects/" + encoded
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := h.Do(req)
	if err != nil {
		return false, fmt.Sprintf("gitlab unreachable: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return true, fmt.Sprintf("OK — %s is reachable with the stored token", fullName)
	case 401:
		return false, "token rejected by GitLab (401)"
	case 404:
		return false, fmt.Sprintf("repo %s not found or token lacks access", fullName)
	default:
		return false, fmt.Sprintf("gitlab returned %d", resp.StatusCode)
	}
}

// ── utilities ─────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
