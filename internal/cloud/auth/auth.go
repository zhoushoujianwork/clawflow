// Package auth implements GitHub-App-OAuth identity for the ClawFlow cloud
// server. It exposes:
//
//   - Session middleware: reads the clawflow_session cookie, looks the row up
//     in cloud.Store, and injects *cloud.User into request context.
//   - API token middleware: reads Authorization: Bearer <token>, hashes it,
//     looks it up in cloud.Store.
//   - GitHub OAuth login init + callback: registers /api/v1/github/app/login
//     and /api/v1/github/app/callback. The App must have "Request user
//     authorization (OAuth) during installation" enabled.
//   - Device flow: POST /api/v1/auth/device + /api/v1/auth/device/poll for
//     CLI login from headless boxes.
//
// The auth package never talks to the GitHub installation API (App-level
// JWT, installation tokens). That stays in the webhook / future job-dispatch
// paths.
package auth

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// Config controls cloud-server auth behavior. Construct via NewHandler.
type Config struct {
	// AppID is the numeric GitHub App ID. Used only in display URLs today.
	AppID int64
	// AppSlug is the GitHub App slug (the URL fragment in
	// https://github.com/apps/<slug>). Surfaced to the Web UI so it can
	// build the "Install on more repos" link.
	AppSlug string
	// ClientID is the OAuth client ID issued by GitHub for this App.
	ClientID string
	// ClientSecret is the OAuth client secret. Required for the
	// authorization-code exchange and the device flow.
	ClientSecret string
	// PublicURL is the externally-visible base URL of the cloud server,
	// e.g. "https://clawflow.daboluo.cc". Used to build the OAuth
	// redirect_uri. Trailing slash is trimmed.
	PublicURL string
	// SessionKey is a long random secret used to HMAC the OAuth state
	// cookie. 32+ bytes recommended.
	SessionKey []byte
	// SessionTTL is the lifetime of a browser session. Defaults to 30 days.
	SessionTTL time.Duration
	// CookieSecure forces the Secure flag on cookies. Default true.
	// Tests can override.
	CookieSecure bool
	// HTTPClient is used for outbound GitHub API calls. Tests inject a
	// fake here. Defaults to a client with a 15s timeout.
	HTTPClient *http.Client
	// Now is the clock. Tests inject a fixed clock; production uses time.Now.
	Now func() time.Time
}

// withDefaults returns a copy of c with zero fields replaced by sensible
// defaults so handlers don't need nil checks at every call site.
func (c Config) withDefaults() Config {
	if c.SessionTTL <= 0 {
		c.SessionTTL = 30 * 24 * time.Hour
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	// PublicURL must not have a trailing slash so we can string-concat.
	for len(c.PublicURL) > 0 && c.PublicURL[len(c.PublicURL)-1] == '/' {
		c.PublicURL = c.PublicURL[:len(c.PublicURL)-1]
	}
	return c
}

// Handler is the auth-routed http.Handler plus the middleware helpers.
type Handler struct {
	cfg    Config
	store  cloud.Store
	mux    *http.ServeMux
	device *deviceCache
}

// NewHandler returns a Handler whose mux serves the auth routes and whose
// Middleware methods wrap arbitrary handlers with session / token checks.
//
// Routes registered on the embedded mux:
//
//	GET  /api/v1/github/app/login     — redirect to GitHub authorize
//	GET  /api/v1/github/app/callback  — user OAuth + install callback
//	POST /api/v1/auth/device          — start CLI device flow
//	POST /api/v1/auth/device/poll     — poll device flow for completion
//	POST /api/v1/auth/logout          — delete session + clear cookie
//	GET  /api/v1/auth/me              — current user info (or 401)
//
// Mount this handler's ServeHTTP under the cloud server's root mux.
func NewHandler(store cloud.Store, cfg Config) *Handler {
	h := &Handler{
		cfg:    cfg.withDefaults(),
		store:  store,
		mux:    http.NewServeMux(),
		device: newDeviceCache(),
	}
	h.mux.HandleFunc("GET /api/v1/github/app/login", h.handleLogin)
	h.mux.HandleFunc("GET /api/v1/github/app/callback", h.handleCallback)
	h.mux.HandleFunc("POST /api/v1/auth/device", h.handleDeviceStart)
	h.mux.HandleFunc("POST /api/v1/auth/device/poll", h.handleDevicePoll)
	h.mux.HandleFunc("POST /api/v1/auth/logout", h.handleLogout)
	h.mux.HandleFunc("GET /api/v1/auth/me", h.handleMe)
	return h
}

// ServeHTTP routes requests to the registered auth handlers. Requests that
// don't match any auth route return 404 (the caller's outer mux should fall
// through to it).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// RegisterRoutes mounts every auth route on the supplied outer mux. Use this
// when embedding the auth handler in a larger HTTP server so the routes share
// one mux rather than nesting two. Patterns match those registered on the
// handler's internal mux.
//
// Also registers a minimal landing page at GET /{$} (exact match for "/"
// only, leaving 404s for unknown paths intact). The page shows a sign-in
// link when anonymous, or a "you're signed in" panel otherwise. PR 2 will
// replace this with the actual React app.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/github/app/login", h.handleLogin)
	mux.HandleFunc("GET /api/v1/github/app/callback", h.handleCallback)
	mux.HandleFunc("POST /api/v1/auth/device", h.handleDeviceStart)
	mux.HandleFunc("POST /api/v1/auth/device/poll", h.handleDevicePoll)
	mux.HandleFunc("POST /api/v1/auth/logout", h.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", h.handleMe)
	mux.HandleFunc("GET /{$}", h.handleIndex)
}

// ---- Request context plumbing ----

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyToken
)

// UserFromContext returns the authenticated user, or nil if anonymous.
func UserFromContext(ctx context.Context) *cloud.User {
	v, _ := ctx.Value(ctxKeyUser).(*cloud.User)
	return v
}

// UserFromContext on *Handler satisfies cloud.AuthHandler so callers that
// only hold an AuthHandler interface (e.g. server.go) can still resolve the
// authenticated user without importing the auth package directly.
func (h *Handler) UserFromContext(ctx context.Context) *cloud.User {
	return UserFromContext(ctx)
}

// TokenFromContext returns the API token used to authenticate the request,
// or nil if the user authenticated via session cookie.
func TokenFromContext(ctx context.Context) *cloud.APIToken {
	v, _ := ctx.Value(ctxKeyToken).(*cloud.APIToken)
	return v
}

func withUser(ctx context.Context, u *cloud.User) context.Context {
	return context.WithValue(ctx, ctxKeyUser, u)
}

func withToken(ctx context.Context, t *cloud.APIToken) context.Context {
	return context.WithValue(ctx, ctxKeyToken, t)
}

// ---- Device flow cache ----

// deviceCache holds in-flight device codes between /device and /device/poll.
// Per-device entries expire when GitHub's device_code itself expires (max 15
// minutes), so this cache is bounded and survives normal load. A restart of
// the cloud server forces clients mid-flow to start over — acceptable.
type deviceCache struct {
	mu      sync.Mutex
	entries map[string]*deviceEntry // keyed by user_code (what the user types)
}

type deviceEntry struct {
	DeviceCode string
	UserCode   string
	Interval   int
	ExpiresAt  time.Time
}

func newDeviceCache() *deviceCache {
	return &deviceCache{entries: map[string]*deviceEntry{}}
}

func (c *deviceCache) put(e *deviceEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpired()
	c.entries[e.UserCode] = e
}

func (c *deviceCache) get(userCode string) *deviceEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpired()
	return c.entries[userCode]
}

func (c *deviceCache) delete(userCode string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, userCode)
}

func (c *deviceCache) evictExpired() {
	now := time.Now()
	for k, v := range c.entries {
		if now.After(v.ExpiresAt) {
			delete(c.entries, k)
		}
	}
}
