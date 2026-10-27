package github_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func fastWriteback(t *testing.T) {
	t.Helper()
	old := retry.DefaultConfig
	retry.DefaultConfig = retry.Config{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	t.Cleanup(func() { retry.DefaultConfig = old })
}

func TestWritebackLabelsRetryTransientOnly(t *testing.T) {
	fastWriteback(t)
	for _, status := range []int{0, 503, 429, 403, 422, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := newTestClient(t, map[string]http.HandlerFunc{
				"POST /repos/o/r/issues/1/labels": func(w http.ResponseWriter, r *http.Request) {
					calls++
					if calls > 1 {
						jsonResp(w, 200, []string{"done"})
						return
					}
					if status == 0 {
						conn, _, _ := w.(http.Hijacker).Hijack()
						conn.Close()
						return
					}
					if status == 403 {
						w.Header().Set("Retry-After", "0")
						http.Error(w, "secondary rate limit", status)
						return
					}
					http.Error(w, "error", status)
				},
			})
			err := c.AddLabel("o/r", 1, "done")
			transient := status == 0 || status == 503 || status == 429 || status == 403
			if transient && (err != nil || calls != 2) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if !transient && (err == nil || calls != 1) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestWritebackCommentLostResponseDeduplicates(t *testing.T) {
	fastWriteback(t)
	posts := 0
	body := ""
	c := newTestClient(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/issues/1/comments": func(w http.ResponseWriter, r *http.Request) {
			posts++
			var input struct{ Body string }
			json.NewDecoder(r.Body).Decode(&input)
			body = input.Body
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		},
		"GET /repos/o/r/issues/1/comments": func(w http.ResponseWriter, r *http.Request) { jsonResp(w, 200, []map[string]string{{"body": body}}) },
	})
	if err := c.PostPRComment("o/r", 1, "done"); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || !strings.Contains(body, "clawflow:run-id=") {
		t.Fatalf("posts=%d body=%s", posts, body)
	}
}

func TestWritebackCommentValidationNotRetried(t *testing.T) {
	fastWriteback(t)
	posts := 0
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/issues/1/comments":  func(w http.ResponseWriter, r *http.Request) { jsonResp(w, 200, []string{}) },
		"POST /repos/o/r/issues/1/comments": func(w http.ResponseWriter, r *http.Request) { posts++; w.WriteHeader(422) },
	})
	if err := c.PostIssueCommentIdempotent("o/r", 1, "done", "run"); err == nil || posts != 1 {
		t.Fatalf("posts=%d err=%v", posts, err)
	}
}

func TestWritebackPRLostResponseReconcilesHeadAndBase(t *testing.T) {
	fastWriteback(t)
	posts := 0
	pr := map[string]any{"number": 2, "state": "open", "head": map[string]any{"ref": "fix", "repo": map[string]string{"full_name": "o/r"}}, "base": map[string]string{"ref": "main"}}
	c := newTestClient(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/pulls": func(w http.ResponseWriter, r *http.Request) {
			posts++
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		},
		"GET /repos/o/r/pulls": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []any{map[string]any{"number": 3, "head": map[string]string{"ref": "fix"}}, pr})
		},
		"GET /repos/o/r/pulls/3": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, map[string]any{"number": 3, "base": map[string]string{"ref": "main"}, "head": map[string]any{"ref": "fix", "repo": map[string]string{"full_name": "foreign/r"}}})
		},
		"GET /repos/o/r/pulls/2": func(w http.ResponseWriter, r *http.Request) { jsonResp(w, 200, pr) },
	})
	result, err := c.CreatePR("o/r", vcs.PRCreateOpts{Head: "fix", Base: "main"})
	if err != nil || result.Number != 2 || posts != 1 {
		t.Fatalf("pr=%+v posts=%d err=%v", result, posts, err)
	}
}

func TestWritebackRemoveAbsentAndCloseRetry(t *testing.T) {
	fastWriteback(t)
	closes := 0
	c := newTestClient(t, map[string]http.HandlerFunc{
		"DELETE /repos/o/r/issues/1/labels/done": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) },
		"PATCH /repos/o/r/issues/1": func(w http.ResponseWriter, r *http.Request) {
			closes++
			if closes == 1 {
				w.WriteHeader(503)
				return
			}
			jsonResp(w, 200, map[string]string{"state": "closed"})
		},
	})
	if err := c.RemoveLabel("o/r", 1, "done"); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseIssue("o/r", 1); err != nil || closes != 2 {
		t.Fatalf("closes=%d err=%v", closes, err)
	}
}

func TestCommentReconciliationValidationNotRetried(t *testing.T) {
	fastWriteback(t)
	gets := 0
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/issues/1/comments": func(w http.ResponseWriter, r *http.Request) { gets++; w.WriteHeader(403) },
	})
	if err := c.PostIssueCommentIdempotent("o/r", 1, "done", "stable"); err == nil || gets != 1 {
		t.Fatalf("gets=%d err=%v", gets, err)
	}
}
