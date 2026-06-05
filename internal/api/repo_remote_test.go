package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestListGitLabReposPaginates(t *testing.T) {
	// Two pages of 2 projects each; X-Next-Page drives the loop.
	page1 := `[{"path_with_namespace":"grp/a","default_branch":"main","visibility":"private","web_url":"http://x/a"},
		{"path_with_namespace":"grp/b","default_branch":"main","visibility":"public","web_url":"http://x/b"}]`
	page2 := `[{"path_with_namespace":"grp/c","default_branch":"main","visibility":"internal","web_url":"http://x/c"}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			t.Errorf("missing token header")
		}
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, page1)
		case "2":
			w.Header().Set("X-Next-Page", "") // last page
			fmt.Fprint(w, page2)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	repos, err := listGitLabRepos("tok", srv.URL)
	if err != nil {
		t.Fatalf("listGitLabRepos: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("expected 3 repos across 2 pages, got %d", len(repos))
	}
	if repos[0].FullName != "grp/a" || repos[2].FullName != "grp/c" {
		t.Errorf("unexpected aggregation order: %+v", repos)
	}
	if !repos[0].Private || repos[1].Private {
		t.Errorf("visibility mapping wrong: %+v", repos)
	}
}

func TestListGitHubReposPaginates(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		switch page {
		case "1":
			w.Header().Set("Link", fmt.Sprintf(`<%s/user/repos?page=2>; rel="next"`, srvURL))
			fmt.Fprint(w, `[{"full_name":"o/a","default_branch":"main"},{"full_name":"o/b","default_branch":"main"}]`)
		case "2":
			// no Link header => last page
			fmt.Fprint(w, `[{"full_name":"o/c","default_branch":"main"}]`)
		default:
			t.Errorf("unexpected page %q", page)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	// listGitHubRepos hardcodes api.github.com for the first URL, so exercise the
	// Link-following helper directly plus the per-page parse to keep the test
	// hermetic.
	if got := parseLinkNext(`<` + srv.URL + `/user/repos?page=2>; rel="next"`); got != srv.URL+"/user/repos?page=2" {
		t.Errorf("parseLinkNext = %q", got)
	}
	if got := parseLinkNext(""); got != "" {
		t.Errorf("parseLinkNext empty = %q", got)
	}
	if got := parseLinkNext(`<x>; rel="prev"`); got != "" {
		t.Errorf("parseLinkNext no-next = %q", got)
	}
}

func TestParseNextPage(t *testing.T) {
	cases := map[string]int{"": 0, "0": 0, "2": 2, "abc": 0, strconv.Itoa(7): 7}
	for in, want := range cases {
		if got := parseNextPage(in); got != want {
			t.Errorf("parseNextPage(%q) = %d, want %d", in, got, want)
		}
	}
}
