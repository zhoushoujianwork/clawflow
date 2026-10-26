package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type historyTransport func(*http.Request) (*http.Response, error)

func (f historyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHistoryPaginationAndErrors(t *testing.T) {
	for _, kind := range []string{"comments", "events", "prs"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%v", kind, fail), func(t *testing.T) {
				calls := 0
				client := New("fake", "https://example.invalid")
				client.http = &http.Client{Transport: historyTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					page := r.URL.Query().Get("page")
					status := 200
					rows := []map[string]any{}
					if strings.Contains(r.URL.Path, "resource_label_events") {
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("[]")), Header: make(http.Header)}, nil
					}
					count := 100
					if page == "2" {
						count = 1
						if fail {
							status = 403
						}
					}
					if page != "1" && page != "2" {
						t.Fatalf("unexpected page %q", page)
					}
					for i := 0; i < count; i++ {
						id := i + 1
						if page == "2" {
							id = 101
						}
						rows = append(rows, map[string]any{"id": id, "body": strings.Repeat("full body", 30), "description": "Fixes #9", "number": id, "iid": id, "title": "delivery", "state": "closed", "event": "reopened", "created_at": "2026-01-03T00:00:00Z", "closed_at": "2026-01-02T00:00:00Z", "actor": map[string]string{"login": "alice"}, "user": map[string]string{"login": "alice", "username": "alice"}, "head": map[string]string{"ref": "fix/issue-9"}, "source_branch": "fix/issue-9"})
						if strings.Contains(r.URL.Path, "resource_state_events") {
							rows[len(rows)-1]["state"] = "reopened"
						}
					}
					data, _ := json.Marshal(rows)
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
				})}
				var length int
				var err error
				switch kind {
				case "comments":
					var rowsLen int
					rows, e := client.ListIssueCommentsDetail("o/r", 9)
					rowsLen = len(rows)
					length, err = rowsLen, e
					if e == nil && (rows[100].Author != "alice" || len(rows[100].Body) < 200) {
						t.Fatal("lost comment metadata/body")
					}
				case "events":
					rows, e := client.ListIssueEvents("o/r", 9)
					length, err = len(rows), e
				case "prs":
					rows, e := client.ListPRs("o/r", "all")
					length, err = len(rows), e
					if e == nil && rows[100].ClosedAt == "" {
						t.Fatal("lost closed time")
					}
				}
				if fail {
					if err == nil {
						t.Fatal("second-page error hidden")
					}
				} else if err != nil || length != 101 {
					t.Fatalf("len=%d err=%v", length, err)
				}
				if calls < 2 {
					t.Fatal("did not fetch second page")
				}
			})
		}
	}
}

func TestHistoryEventsUnsupported(t *testing.T) {
	client := New("fake", "https://example.invalid")
	client.http = &http.Client{Transport: historyTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	if _, err := client.ListIssueEvents("o/r", 9); err == nil {
		t.Fatal("unsupported events treated as complete")
	}
}

func TestHistoryEventNormalization(t *testing.T) {
	client := New("fake", "https://example.invalid")
	client.http = &http.Client{Transport: historyTransport(func(r *http.Request) (*http.Response, error) {
		data := `[{"created_at": "2026-01-01T00:00:00Z", "actor": {"login": "alice"}, "id": 1, "event": "closed", "label": {"name": "agent-implemented"}}, {"created_at": "2026-01-01T00:00:00Z", "actor": {"login": "alice"}, "id": 2, "event": "reopened", "label": {"name": "agent-implemented"}}, {"created_at": "2026-01-01T00:00:00Z", "actor": {"login": "alice"}, "id": 3, "event": "labeled", "label": {"name": "agent-implemented"}}, {"created_at": "2026-01-01T00:00:00Z", "actor": {"login": "alice"}, "id": 4, "event": "unlabeled", "label": {"name": "agent-implemented"}}]`
		if strings.Contains(r.URL.Path, "resource_label_events") {
			data = `[]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(data)), Header: make(http.Header)}, nil
	})}
	events, err := client.ListIssueEvents("o/r", 9)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range events {
		if e.ID == "" || e.Actor != "alice" || e.CreatedAt == "" {
			t.Fatalf("lost metadata: %+v", e)
		}
		got[e.Type] = true
		if (e.Type == "labeled" || e.Type == "unlabeled") && e.Label != "agent-implemented" {
			t.Fatalf("lost label: %+v", e)
		}
	}
	for _, kind := range []string{"closed", "reopened", "labeled", "unlabeled"} {
		if !got[kind] {
			t.Errorf("missing %s: %+v", kind, events)
		}
	}
}
