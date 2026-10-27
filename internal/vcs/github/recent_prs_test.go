package github

import (
	"fmt"
	"net/http"
	"testing"
)

func TestListRecentPRsKeepsSHAsAndMergedState(t *testing.T) {
	c := ciTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("state") != "closed" || q.Get("sort") != "updated" || q.Get("direction") != "desc" || q.Get("per_page") != "100" {
				t.Errorf("query = %v", q)
			}
			fmt.Fprint(w, `[
			 {"number":5,"title":"merged","state":"closed","merged_at":"2026-10-27T10:00:00Z","draft":false,"merge_commit_sha":"mc5","html_url":"u5","head":{"ref":"fix/issue-1","sha":"h5"},"base":{"ref":"main"}},
			 {"number":4,"title":"abandoned","state":"closed","merged_at":null,"draft":true,"head":{"ref":"x","sha":"h4"},"base":{"ref":"main"}}
			]`)
		},
	})
	prs, err := c.ListRecentPRs("o/r", "closed", 0)
	if err != nil || len(prs) != 2 {
		t.Fatalf("%v %v", prs, err)
	}
	if p := prs[0]; p.State != "merged" || !p.IsMerged() || p.HeadSHA != "h5" || p.MergeCommitSHA != "mc5" || p.BaseBranch != "main" || p.URL != "u5" || p.HeadBranch != "fix/issue-1" {
		t.Fatalf("merged PR = %+v", p)
	}
	if p := prs[1]; p.State != "closed" || p.IsMerged() || !p.Draft {
		t.Fatalf("closed PR = %+v", p)
	}
	if _, err := c.ListRecentPRs("o/r", "merged", 10); err == nil {
		t.Fatal("unsupported state must error")
	}
}
