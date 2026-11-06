package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// parentScanVCS serves a fake GitHub for one repo. open lists the issues
// returned by the open-issue listing; children maps a parent number to the
// sub-issues returned by its sub_issues endpoint (which, like GitHub, also
// returns closed children). The listing deliberately carries no
// sub_issues_summary, exactly like the real ListOpenIssues request path
// the scanner relies on.
func parentScanVCS(t *testing.T, open []map[string]any, children map[int][]map[string]any) {
	t.Helper()
	t.Setenv("GH_TOKEN", "pending-parent-test-only")
	t.Setenv("CLAWFLOW_PILOT_BUDGET_PATH", "")
	original := http.DefaultTransport
	http.DefaultTransport = reopenedLifecycleTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "vcs.invalid" {
			return nil, fmt.Errorf("unexpected external request: %s", r.URL.Host)
		}
		var body any = []any{}
		var n int
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			body = map[string]int{"id": 1}
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/issues":
			body = open
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/sub_issues"):
			if _, err := fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/%d/sub_issues", &n); err != nil {
				return nil, err
			}
			body = children[n]
			if body == nil {
				body = []any{}
			}
		case r.Method == "GET" && (r.URL.Path == "/repos/o/r/pulls" || strings.HasSuffix(r.URL.Path, "/events")):
		default:
			return nil, fmt.Errorf("unexpected fake VCS call: %s %s", r.Method, r.URL.Path)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func pendingImplementIssues(pending []snapshot.PendingEntry) map[int]bool {
	out := map[int]bool{}
	for _, p := range pending {
		if p.Operator == "implement" {
			out[p.IssueNumber] = true
		}
	}
	return out
}

// A parent must not appear as a pending implement entry: the dispatch path
// never schedules it, so listing it made `pending` permanently non-empty
// with no job ever created. The decision must not depend on whether the
// open-issue listing reports a sub-issue summary.
func TestPendingExcludesParentIssues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		children []map[string]any
	}{
		{"open child", []map[string]any{targetedIssue(2)}},
		{"all children closed", []map[string]any{func() map[string]any { c := targetedIssue(2); c["state"] = "closed"; return c }()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withRunLog(t)
			parentScanVCS(t,
				[]map[string]any{targetedIssue(1), targetedIssue(3)},
				map[int][]map[string]any{1: tc.children})
			reg := operator.NewRegistry()
			if err := reg.LoadEmbedded(os.DirFS("../../../skills"), "."); err != nil {
				t.Fatal(err)
			}
			rc := config.Repo{Enabled: true, Platform: "github", BaseURL: "https://vcs.invalid", LocalPath: t.TempDir(), BaseBranch: "main"}
			var captured []snapshot.IssueEntry
			pending, jobs, err := scanRepoOnce(context.Background(), reg, "o/r", rc, true, 0, &captured)
			if err != nil {
				t.Fatal(err)
			}
			got := pendingImplementIssues(pending)
			if got[1] {
				t.Fatalf("parent #1 is pending for implement: %+v", pending)
			}
			if !got[3] {
				t.Fatalf("leaf #3 missing from pending: %+v", pending)
			}
			// Pending and dispatch must agree issue by issue.
			dispatched := map[int]bool{}
			for _, j := range jobs {
				if j.op.Name == "implement" {
					dispatched[j.sub.Number] = true
				}
			}
			if dispatched[1] || !dispatched[3] {
				t.Fatalf("dispatched implement = %v, want only #3", dispatched)
			}
		})
	}
}

// Only the structural parent gate may change: an issue the listing returns
// without children keeps its existing pending behavior, including labels
// that already excluded it.
func TestPendingLeafRulesUnchangedWithoutChildren(t *testing.T) {
	withRunLog(t)
	parentScanVCS(t,
		[]map[string]any{targetedIssue(1), targetedIssue(2, "tracking"), targetedIssue(3, "agent-implemented")},
		nil)
	reg := operator.NewRegistry()
	if err := reg.LoadEmbedded(os.DirFS("../../../skills"), "."); err != nil {
		t.Fatal(err)
	}
	rc := config.Repo{Enabled: true, Platform: "github", BaseURL: "https://vcs.invalid", LocalPath: t.TempDir(), BaseBranch: "main"}
	var captured []snapshot.IssueEntry
	pending, _, err := scanRepoOnce(context.Background(), reg, "o/r", rc, true, 0, &captured)
	if err != nil {
		t.Fatal(err)
	}
	if got := pendingImplementIssues(pending); len(got) != 1 || !got[1] {
		t.Fatalf("pending implement = %v, want only #1", got)
	}
}
