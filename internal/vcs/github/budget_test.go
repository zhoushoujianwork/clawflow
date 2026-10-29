package github

import (
	"context"
	"errors"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestConditionalReads(t *testing.T) {
	for _, validator := range []string{"ETag", "Last-Modified"} {
		t.Run(validator, func(t *testing.T) {
			for _, path := range []string{"issues?state=open", "pulls?state=open", "issues/1", "pulls/1", "issues/1/comments", "labels", "commits/abc/check-runs", "commits/abc/status"} {
				t.Run(path, func(t *testing.T) {
					calls := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						w.Header().Set("X-RateLimit-Limit", "5000")
						w.Header().Set("X-RateLimit-Remaining", "4000")
						if calls == 1 {
							w.Header().Set(validator, "validator")
							fmt.Fprint(w, `[{"number":1}]`)
							return
						}
						header := "If-None-Match"
						if validator == "Last-Modified" {
							header = "If-Modified-Since"
						}
						if r.Header.Get(header) != "validator" {
							t.Errorf("missing %s", header)
						}
						w.Header().Set("X-RateLimit-Remaining", "3999")
						w.WriteHeader(304)
					}))
					defer server.Close()
					c := New("token", server.URL)
					for i := 0; i < 2; i++ {
						data, status, err := c.doOnce("GET", "/repos/o/r/"+path, nil)
						if err != nil || status != 200 || string(data) != `[{"number":1}]` {
							t.Fatalf("%s %d %v", data, status, err)
						}
					}
					if c.Budget().Remaining != 4000 {
						t.Fatalf("304 lowered quota: %+v", c.Budget())
					}
				})
			}
		})
	}
}

func TestPassReusesOpenListsAcrossClientsAndStages(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls[r.URL.String()]++; fmt.Fprint(w, `[]`) }))
	defer server.Close()
	ctx, pass := BeginPass(context.Background())
	for _, stage := range []string{"scan", "harvest", "automerge", "reopen"} {
		for n := 0; n < 4; n++ {
			c := New("token", server.URL)
			c.SetRequestContext(WithStage(ctx, stage))
			repo := fmt.Sprintf("o/r%d", n)
			if _, err := c.ListOpenIssues(repo); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ListOpenPRs(repo); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(calls) != 8 {
		t.Fatalf("endpoints: %v", calls)
	}
	for path, n := range calls {
		if n != 1 {
			t.Fatalf("%s called %d times", path, n)
		}
	}
	if pass.Calls["scan"] != 8 || len(pass.Calls) != 1 {
		t.Fatalf("stage counts: %v", pass.Calls)
	}
	path := filepath.Join(t.TempDir(), "usage.json")
	pass.Save(path)
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
}

func TestReserveSkipsPollingButAllowsDeliveryAndWrites(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "499")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	c := New("token", server.URL)
	if _, _, err := c.doOnce("GET", "/seed", nil); err != nil {
		t.Fatal(err)
	}
	ctx, pass := BeginPass(context.Background())
	c.SetRequestContext(WithStage(ctx, "scan"))
	if c.PollingAllowed() {
		t.Fatal("polling used reserve")
	}
	if _, _, err := c.doOnce("GET", "/poll", nil); err != vcs.ErrPollingDeferred {
		t.Fatalf("scan: %v", err)
	}
	if _, _, err := c.doOnce("POST", "/write", nil); err != nil {
		t.Fatal(err)
	}
	c.SetRequestContext(WithStage(ctx, "result_application"))
	if _, _, err := c.doOnce("GET", "/delivery", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || pass.Budgets[server.URL].Remaining != 499 {
		t.Fatalf("calls=%d budget=%v", calls, pass.Budgets)
	}
	c.state.Lock()
	c.state.budget.Reset = time.Now().Add(-time.Second)
	c.state.Unlock()
	if !c.PollingAllowed() {
		t.Fatal("polling did not resume after reset")
	}
}

func TestConditionalCacheSurvivesRestartAndPartitionsCredentials(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 2 {
			if r.Header.Get("If-None-Match") != "v1" {
				t.Error("missing persisted validator")
			}
			w.WriteHeader(304)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			t.Error("cache leaked between credentials")
		}
		w.Header().Set("ETag", "v1")
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	root := t.TempDir()
	for i := 0; i < 3; i++ {
		token := "first"
		if i == 2 {
			token = "second"
		}
		c := New(token, server.URL)
		c.UseCacheDir(root)
		if i == 1 {
			c.state.Lock()
			clear(c.state.cache)
			c.state.Unlock()
		}
		data, status, err := c.doOnce("GET", "/read", nil)
		if err != nil || status != 200 || string(data) != "[]" {
			t.Fatalf("%s %d %v", data, status, err)
		}
	}
}

func TestSecondaryBackoffAvoidsRequestsUntilRetryAfter(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "secondary rate limit", 403)
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	c := New("token", server.URL)
	if _, _, err := c.doOnce("GET", "/first", nil); err == nil {
		t.Fatal("rate limit missing")
	}
	if c.PollingAllowed() {
		t.Fatal("secondary backoff did not stop polling")
	}
	if _, _, err := c.doOnce("POST", "/write", nil); err == nil || calls != 1 {
		t.Fatalf("backoff bypassed: calls=%d err=%v", calls, err)
	}
	c.state.Lock()
	c.state.budget.Backoff = time.Now().Add(-time.Second)
	c.state.Unlock()
	if _, _, err := c.doOnce("POST", "/write", nil); err != nil || calls != 2 {
		t.Fatalf("reset failed: calls=%d err=%v", calls, err)
	}
}

func TestExhaustedBudgetDefersUntilReset(t *testing.T) {
	for _, backoff := range []bool{false, true} {
		t.Run(fmt.Sprintf("backoff=%v", backoff), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				fmt.Fprint(w, "[]")
			}))
			defer server.Close()
			c := New("fixture", server.URL)
			until := time.Now().Add(10 * time.Minute)
			c.state.budget = Budget{Limit: 5000, Remaining: 0, Reset: until}
			if backoff {
				c.state.budget = Budget{Remaining: 4000, Backoff: until}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			c.SetRequestContext(ctx)
			_, err := c.ListIssueEvents("o/r", 1)
			var limited *retry.HTTPError
			if !errors.As(err, &limited) || !limited.RateLimited || limited.After < 9*time.Minute || calls != 0 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			// Advance the tracked window without waiting ten minutes.
			c.state.budget.Reset = time.Now().Add(-time.Second)
			c.state.budget.Backoff = time.Now().Add(-time.Second)
			if _, err := c.ListIssueEvents("o/r", 1); err != nil || calls != 1 {
				t.Fatalf("after reset: calls=%d err=%v", calls, err)
			}
		})
	}
}

type timeoutTransport func(*http.Request) (*http.Response, error)

func (f timeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeadlineWithExhaustedBudgetKeepsRateLimitIdentity(t *testing.T) {
	for _, inFlight := range []bool{false, true} {
		t.Run(fmt.Sprintf("inflight=%v", inFlight), func(t *testing.T) {
			c := New("fixture", "https://example.test")
			exhaust := func() {
				c.state.Lock()
				c.state.budget = Budget{Limit: 5000, Remaining: 0, Reset: time.Now().Add(10 * time.Minute)}
				c.state.Unlock()
			}
			if inFlight {
				c.http.Transport = timeoutTransport(func(*http.Request) (*http.Response, error) {
					exhaust()
					return nil, context.DeadlineExceeded
				})
			} else {
				exhaust()
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer cancel()
				c.SetRequestContext(ctx)
			}
			_, err := c.ListIssueEvents("o/r", 1)
			var limited *retry.HTTPError
			if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &limited) || !limited.RateLimited {
				t.Fatalf("lost deadline or rate limit: %v", err)
			}
		})
	}
}

func TestApplicationErrorsWithoutActiveLimitRemainFailures(t *testing.T) {
	for _, budget := range []Budget{
		{},
		{Limit: 5000, Remaining: 1, Reset: time.Now().Add(time.Hour)},
		{Limit: 5000, Remaining: 0, Reset: time.Now().Add(-time.Second)},
	} {
		c := New("fixture", "https://example.test")
		c.state.budget = budget
		for _, original := range []error{context.DeadlineExceeded, os.ErrDeadlineExceeded, errors.New("application failed")} {
			err := c.rateLimitedTimeout(original)
			var limited *retry.HTTPError
			if !errors.Is(err, original) || errors.As(err, &limited) {
				t.Fatalf("budget=%+v changed ordinary failure: %v", budget, err)
			}
		}
	}
	c := New("fixture", "https://example.test")
	c.state.budget = Budget{Remaining: 0, Reset: time.Now().Add(10 * time.Minute)}
	err := c.rateLimitedTimeout(os.ErrDeadlineExceeded)
	var limited *retry.HTTPError
	if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &limited) || !limited.RateLimited {
		t.Fatalf("network timeout lost rate limit: %v", err)
	}
}
