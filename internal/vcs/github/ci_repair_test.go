package github

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type repairTransport func(*http.Request) (*http.Response, error)

func (f repairTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetPRRepairIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, body, repo, sha string
		draft                 bool
	}{
		{"fork draft", `{"number":11,"state":"open","draft":true,"head":{"ref":"fix/issue-9","sha":"abc123","repo":{"full_name":"fork/r"}}}`, "fork/r", "abc123", true},
		{"missing identity", `{"number":11,"state":"open","head":{"ref":"fix/issue-9","repo":null}}`, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New("test", "")
			c.http = &http.Client{Transport: repairTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.Path != "/repos/o/r/pulls/11" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			pr, err := c.GetPR("o/r", 11)
			if err != nil {
				t.Fatal(err)
			}
			if pr.Draft != tc.draft || pr.HeadRepo != tc.repo || pr.HeadSHA != tc.sha || pr.HeadBranch != "fix/issue-9" {
				t.Fatalf("identity lost: %+v", pr)
			}
		})
	}
}

func TestConflictMergeability(t *testing.T) {
	for _, tc := range []struct {
		body string
		want vcs.MergeStatus
	}{
		{`{"mergeable":false,"mergeable_state":"dirty"}`, vcs.MergeStatusConflict},
		{`{"mergeable":null,"mergeable_state":"dirty"}`, vcs.MergeStatusConflict},
		{`{"mergeable":null,"mergeable_state":"unknown"}`, vcs.MergeStatusPending},
		{`{"mergeable":true,"mergeable_state":"clean"}`, vcs.MergeStatusClean},
		{`{"mergeable":true,"mergeable_state":"blocked"}`, vcs.MergeStatusClean},
	} {
		c := New("test", "")
		c.http = &http.Client{Transport: repairTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}
		got, err := c.GetPRMergeability("o/r", 11)
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %s, %v", tc.body, got, err)
		}
	}
}
