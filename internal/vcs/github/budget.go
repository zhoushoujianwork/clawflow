package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type cachedResponse struct {
	Body           []byte
	ETag, Modified string
}
type Budget = vcs.APIBudget
type requestState struct {
	sync.Mutex
	cache  map[string]cachedResponse
	budget Budget
	logged time.Time
	dir    string
}

var accounts sync.Map

func accountState(token, base string) *requestState {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(base+"\x00"+token)))
	state, _ := accounts.LoadOrStore(key, &requestState{cache: map[string]cachedResponse{}})
	return state.(*requestState)
}

// UseCacheDir enables private, credential-partitioned conditional caches across CLI passes.
func (c *Client) UseCacheDir(root string) {
	c.state = accountState(c.token, c.baseURL+"\x00"+root)
	s := c.state
	s.Lock()
	defer s.Unlock()
	if s.dir != "" {
		return
	}
	s.dir = filepath.Join(root, fmt.Sprintf("%x", sha256.Sum256([]byte(c.baseURL+"\x00"+c.token))))
	var disk struct {
		Budget
		Logged time.Time `json:"logged_until"`
	}
	if data, err := os.ReadFile(filepath.Join(s.dir, "budget.json")); err == nil && json.Unmarshal(data, &disk) == nil {
		s.budget, s.logged = disk.Budget, disk.Logged
	}
}
func privateJSON(path string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cache-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return
	}
	if f.Close() == nil {
		_ = os.Rename(f.Name(), path)
	}
}
func (s *requestState) cachePath(path string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(path))))
}
func (c *Client) Budget() Budget { c.state.Lock(); defer c.state.Unlock(); return c.state.budget }

// PollingAllowed reserves ten percent of the primary budget for delivery and writes.
func (c *Client) PollingAllowed() bool {
	b := c.Budget()
	if p, _ := c.reqContext().Value(passKey{}).(*Pass); p != nil {
		p.Lock()
		p.Budgets[c.baseURL] = b
		p.Unlock()
	}
	s := c.state
	s.Lock()
	defer s.Unlock()
	now := time.Now()
	until := b.Backoff
	if b.Limit > 0 && b.Remaining*10 < b.Limit && now.Before(b.Reset) && b.Reset.After(until) {
		until = b.Reset
	}
	if !now.Before(until) {
		return true
	}
	s.logLimited(until)
	return false
}

// The caller holds the account mutex for persistence and event deduplication.
func (s *requestState) saveBudget() {
	if s.dir != "" {
		privateJSON(filepath.Join(s.dir, "budget.json"), struct {
			Budget
			Logged time.Time `json:"logged_until"`
		}{s.budget, s.logged})
	}
}
func (s *requestState) logLimited(until time.Time) {
	if !time.Now().Before(s.logged) {
		s.logged = until
		lg, _ := clog.Open("run")
		lg.Warn("vcs/rate_limited", "remaining", s.budget.Remaining, "reset", until)
		lg.Close()
		s.saveBudget()
	}
}

func (c *Client) cached(path string) cachedResponse {
	s := c.state
	s.Lock()
	defer s.Unlock()
	if entry, ok := s.cache[path]; ok {
		return entry
	}
	var entry cachedResponse
	if s.dir != "" {
		if data, err := os.ReadFile(s.cachePath(path)); err == nil {
			_ = json.Unmarshal(data, &entry)
		}
	}
	return entry
}
func (c *Client) observe(path, method string, resp *http.Response, data []byte) ([]byte, int, error) {
	s := c.state
	s.Lock()
	defer s.Unlock()
	h := resp.Header
	status := resp.StatusCode
	// A 304 is free: do not let an intermediary's stale counters lower the budget.
	oldReset := s.budget.Reset
	reset, resetErr := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64)
	newWindow := resetErr == nil && time.Unix(reset, 0).After(oldReset)
	if status != http.StatusNotModified || newWindow {
		if n, err := strconv.Atoi(h.Get("X-RateLimit-Limit")); err == nil {
			s.budget.Limit = n
		}
		if n, err := strconv.Atoi(h.Get("X-RateLimit-Remaining")); err == nil {
			s.budget.Remaining = n
		}
		if n, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			s.budget.Reset = time.Unix(n, 0)
		}
	}
	if status == http.StatusNotModified && !newWindow {
		if n, err := strconv.Atoi(h.Get("X-RateLimit-Remaining")); err == nil && n > s.budget.Remaining {
			s.budget.Remaining = n
		}
	}
	err := retry.ResponseError(status, h, data)
	if e, ok := err.(*retry.HTTPError); ok && e.RateLimited {
		until := time.Now().Add(e.After)
		if until.After(s.budget.Backoff) {
			s.budget.Backoff = until
		}
		window := s.budget.Backoff
		if s.budget.Remaining*10 < s.budget.Limit && s.budget.Reset.After(window) {
			window = s.budget.Reset
		}
		s.logLimited(window)
	}
	if s.dir != "" {
		s.saveBudget()
	}
	if method == http.MethodGet && status == http.StatusOK && (h.Get("ETag") != "" || h.Get("Last-Modified") != "") && len(data) <= 4<<20 {
		entry := cachedResponse{Body: data, ETag: h.Get("ETag"), Modified: h.Get("Last-Modified")}
		if len(s.cache) >= 1024 {
			clear(s.cache)
		}
		s.cache[path] = entry
		if s.dir != "" {
			privateJSON(s.cachePath(path), entry)
		}
	}
	return data, status, err
}

type passKey struct{}
type stageKey struct{}
type passResponse struct {
	data   []byte
	status int
	err    error
}
type Pass struct {
	sync.Mutex
	responses map[string]passResponse
	vcs.APIUsage
}

func BeginPass(ctx context.Context) (context.Context, *Pass) {
	p := &Pass{responses: map[string]passResponse{}, APIUsage: vcs.APIUsage{Calls: map[string]int{}, Budgets: map[string]Budget{}, CapturedAt: time.Now()}}
	return context.WithValue(ctx, passKey{}, p), p
}
func WithStage(ctx context.Context, stage string) context.Context {
	return context.WithValue(ctx, stageKey{}, stage)
}

// Save publishes only public counters, never URLs, bodies or credential identifiers.
func (p *Pass) Save(path string) { p.Lock(); defer p.Unlock(); privateJSON(path, p) }
func openList(path string) bool {
	u, err := url.Parse(path)
	if err != nil || u.Query().Get("state") != "open" {
		return false
	}
	return strings.HasSuffix(u.Path, "/issues") || strings.HasSuffix(u.Path, "/pulls")
}
