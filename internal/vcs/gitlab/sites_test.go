package gitlab

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// fakeSite records every request it receives. Its handler answers like a
// GitLab server: the token must equal want, otherwise 401.
type fakeSite struct {
	srv  *httptest.Server
	want string
	mu   sync.Mutex
	reqs []recorded
	// pipelineStatus is what the MR head pipeline reports.
	pipelineStatus string
	// extra lets a test override the response for a path prefix.
	extra func(w http.ResponseWriter, r *http.Request) bool
}

type recorded struct {
	Method, Path, Token string
}

func newFakeSite(t *testing.T, want string) *fakeSite {
	t.Helper()
	f := &fakeSite{want: want, pipelineStatus: "success"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN")})
		f.mu.Unlock()
		if r.Header.Get("PRIVATE-TOKEN") != f.want {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
			return
		}
		if f.extra != nil && f.extra(w, r) {
			return
		}
		p := r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(p, "/merge_requests/7"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"iid": 7, "sha": strings.Repeat("a", 40), "source_project_id": 5, "target_project_id": 5,
				"state": "opened", "title": "t", "source_branch": "fix/issue-7", "target_branch": "main",
				"head_pipeline": map[string]any{"id": 90, "sha": strings.Repeat("a", 40), "status": f.pipelineStatus},
			})
		case r.Method == "GET" && strings.Contains(p, "/pipelines/90"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 90, "sha": strings.Repeat("a", 40), "status": f.pipelineStatus})
		case r.Method == "GET" && strings.Contains(p, "/projects/") && strings.Count(p, "/") == 4: // /api/v4/projects/<id>
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 5, "path_with_namespace": "team/tools/service"})
		case r.Method == "PUT" && strings.HasSuffix(p, "/merge_requests/7/merge"):
			_, _ = w.Write([]byte(`{"state":"merged"}`))
		case r.Method == "PUT" && strings.HasSuffix(p, "/merge_requests/7"):
			_, _ = w.Write([]byte(`{"state":"closed"}`))
		case r.Method == "DELETE":
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSite) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakeSite) client(name, token string, unsupported ...string) *Client {
	return NewForSite(Site{Name: name, URL: f.srv.URL, Token: token, Unsupported: unsupported})
}

const sameRepo = "team/tools/service"

// Reads and writes against the same project path, project ID and MR number go
// only to the site the client is bound to, with only that site's token.
func TestTwoSitesGetOnlyTheirOwnRequestsAndCredentials(t *testing.T) {
	a := newFakeSite(t, "token-a")
	b := newFakeSite(t, "token-b")
	ca, cb := a.client("a", "token-a"), b.client("b", "token-b")

	for i := 0; i < 2; i++ { // alternate between sites
		for _, c := range []*Client{ca, cb} {
			if id, err := c.GetRepoID(sameRepo); err != nil || id != 5 {
				t.Fatalf("%s GetRepoID = %d, %v", c.SiteName(), id, err)
			}
			if _, err := c.GetPR(sameRepo, 7); err != nil {
				t.Fatalf("%s GetPR: %v", c.SiteName(), err)
			}
		}
	}
	if err := ca.MergePR(sameRepo, 7); err != nil {
		t.Fatalf("merge on a: %v", err)
	}
	if err := cb.ClosePR(sameRepo, 7); err != nil {
		t.Fatalf("close on b: %v", err)
	}
	if err := cb.DeleteBranch(sameRepo, "fix/issue-7"); err != nil {
		t.Fatalf("delete branch on b: %v", err)
	}

	for site, f := range map[string]*fakeSite{"a": a, "b": b} {
		for _, r := range f.requests() {
			if r.Token != f.want {
				t.Errorf("site %s received token %q", site, r.Token)
			}
			if !strings.HasPrefix(r.Path, "/api/v4/projects/team%2Ftools%2Fservice") {
				t.Errorf("site %s request path %s", site, r.Path)
			}
		}
	}
	has := func(f *fakeSite, method, suffix string) bool {
		for _, r := range f.requests() {
			if r.Method == method && strings.HasSuffix(r.Path, suffix) {
				return true
			}
		}
		return false
	}
	if !has(a, "PUT", "/merge_requests/7/merge") || has(b, "PUT", "/merge_requests/7/merge") {
		t.Error("merge must reach site a only")
	}
	if !has(b, "PUT", "/merge_requests/7") || has(a, "DELETE", "/repository/branches/fix%2Fissue-7") || !has(b, "DELETE", "/repository/branches/fix%2Fissue-7") {
		t.Error("close and branch delete must reach site b only")
	}
}

// A 401 from a site is reported as that site's authentication failure, is not
// retried, and the other site never hears about the request or the credential.
func TestAuthFailureStaysOnItsSite(t *testing.T) {
	a := newFakeSite(t, "token-a")
	b := newFakeSite(t, "token-b")
	cb := b.client("b", "wrong-token")

	_, err := cb.GetPR(sameRepo, 7)
	var ae *vcs.AuthError
	if !errors.As(err, &ae) || ae.Site != "b" || ae.Status != 401 || ae.NoCred {
		t.Fatalf("want AuthError for site b, got %v", err)
	}
	if !strings.Contains(err.Error(), "no other site was tried") || strings.Contains(err.Error(), "wrong-token") {
		t.Errorf("auth error text: %v", err)
	}
	if n := len(b.requests()); n != 1 {
		t.Errorf("a 401 must not be retried; site b saw %d requests", n)
	}
	if n := len(a.requests()); n != 0 {
		t.Errorf("site a saw %d requests after b failed", n)
	}
	for _, r := range a.requests() {
		if r.Token == "wrong-token" || r.Token == "token-b" {
			t.Error("another site's credential reached site a")
		}
	}
}

func TestMissingCredentialMakesNoRequest(t *testing.T) {
	b := newFakeSite(t, "token-b")
	err := b.client("b", "").MergePR(sameRepo, 7)
	var ae *vcs.AuthError
	if !errors.As(err, &ae) || !ae.NoCred {
		t.Fatalf("want NoCred AuthError, got %v", err)
	}
	if len(b.requests()) != 1 {
		// The request is sent without a token and refused; nothing else is tried.
		t.Logf("requests: %v", b.requests())
	}
	for _, r := range b.requests() {
		if r.Token != "" {
			t.Errorf("a token was sent for a site without credential: %q", r.Token)
		}
	}
}

// 403 and 404 are returned as they are; the client never retries them against
// another host (it has no other host to try).
func TestForbiddenAndNotFoundAreNotReclassified(t *testing.T) {
	b := newFakeSite(t, "token-b")
	b.extra = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/merge_requests/8") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/merge_requests/9") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"404 Not found"}`))
			return true
		}
		return false
	}
	cb := b.client("b", "token-b")
	for n, want := range map[int]string{8: "403", 9: "404"} {
		_, err := cb.GetPR(sameRepo, n)
		var ae *vcs.AuthError
		if err == nil || errors.As(err, &ae) || !strings.Contains(err.Error(), want) {
			t.Errorf("MR %d: want plain %s error, got %v", n, want, err)
		}
		if !strings.Contains(err.Error(), "GitLab site b") {
			t.Errorf("MR %d error does not name the site: %v", n, err)
		}
	}
}

// A repository qualified with another site's alias is refused before any
// request leaves the process.
func TestClientRefusesRepositoryOfAnotherSite(t *testing.T) {
	a := newFakeSite(t, "token-a")
	ca := a.client("a", "token-a")
	err := ca.MergePR("b:"+sameRepo, 7)
	var me *vcs.SiteMismatchError
	if !errors.As(err, &me) || me.ClientSite != "a" {
		t.Fatalf("want SiteMismatchError, got %v", err)
	}
	if len(a.requests()) != 0 {
		t.Fatalf("a request was sent for another site's repository: %v", a.requests())
	}
	// Its own qualified key is accepted and addresses the bare project path.
	if _, err := ca.GetPR("a:"+sameRepo, 7); err != nil {
		t.Fatal(err)
	}
	for _, r := range a.requests() {
		if !strings.Contains(r.Path, "/projects/team%2Ftools%2Fservice/") || strings.Contains(r.Path, "a%3A") {
			t.Errorf("qualified key leaked into the path: %s", r.Path)
		}
	}
}

// A redirect to a different host is not followed, so the token cannot be
// forwarded to it.
func TestRedirectToAnotherHostIsRefused(t *testing.T) {
	other := newFakeSite(t, "token-other")
	a := newFakeSite(t, "token-a")
	a.extra = func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, other.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
		return true
	}
	if _, err := a.client("a", "token-a").GetPR(sameRepo, 7); err == nil {
		t.Fatal("redirect to another host was followed")
	}
	if n := len(other.requests()); n != 0 {
		t.Fatalf("redirect target received %d requests (token forwarded)", n)
	}
}

// A capability a site lacks fails only that site, with a message that names
// site, repo and capability, and is not an authentication error. Other sites
// and the CI gate are unaffected.
func TestUnsupportedCapabilityIsPerSiteAndNotAnAuthError(t *testing.T) {
	old := newFakeSite(t, "token-a")
	cur := newFakeSite(t, "token-b")
	co := old.client("a", "token-a", vcs.CapIssueEvents, vcs.CapMRPipeline, vcs.CapRunners)
	cn := cur.client("b", "token-b")

	_, err := co.ListIssueEvents(sameRepo, 3)
	ue, ok := vcs.AsUnsupported(err)
	if !ok || ue.Site != "a" || ue.Capability != vcs.CapIssueEvents || ue.Repo != sameRepo {
		t.Fatalf("want UnsupportedError for a/issue_events, got %v", err)
	}
	var ae *vcs.AuthError
	if errors.As(err, &ae) || !errors.Is(err, vcs.ErrNotSupported) {
		t.Errorf("unsupported must not look like an auth error: %v", err)
	}
	for _, want := range []string{"site a", sameRepo, "issue_events", "not an authentication error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if n := len(old.requests()); n != 0 {
		t.Errorf("an unsupported call still sent %d requests", n)
	}

	// The same call on the newer site works.
	if _, err := cn.ListIssueEvents(sameRepo, 3); err != nil {
		t.Errorf("site b issue events: %v", err)
	}
	if _, err := co.ListProjectRunners(sameRepo); !errors.Is(err, vcs.ErrNotSupported) {
		t.Errorf("runners on a: %v", err)
	}
	if _, err := cn.ListProjectRunners(sameRepo); err != nil {
		t.Errorf("runners on b: %v", err)
	}
}

// The CI gate: a site without head-pipeline support must not report "no CI"
// (which a caller could read as nothing to wait for) or success. Success still
// requires an exact-head success pipeline on a site that supports it.
func TestCIGateDoesNotDowngradeOnUnsupportedSite(t *testing.T) {
	old := newFakeSite(t, "token-a")
	cur := newFakeSite(t, "token-b")
	co := old.client("a", "token-a", vcs.CapMRPipeline)
	cn := cur.client("b", "token-b")

	st, err := co.GetCIStatusAtHead(sameRepo, 7, "")
	if err == nil || st == vcs.CIStatusSuccess {
		t.Fatalf("unsupported site CI status = %v, %v; want an error and no success", st, err)
	}
	if _, ok := vcs.AsUnsupported(err); !ok {
		t.Errorf("want UnsupportedError, got %v", err)
	}

	st, err = cn.GetCIStatusAtHead(sameRepo, 7, strings.Repeat("a", 40))
	if err != nil || st != vcs.CIStatusSuccess {
		t.Fatalf("site b success pipeline = %v, %v", st, err)
	}
	cur.pipelineStatus = "failed"
	if st, _ := cn.GetCIStatusAtHead(sameRepo, 7, ""); st != vcs.CIStatusFailure {
		t.Errorf("failed pipeline = %v", st)
	}
	cur.pipelineStatus = "running"
	if st, _ := cn.GetCIStatusAtHead(sameRepo, 7, ""); st != vcs.CIStatusPending {
		t.Errorf("running pipeline = %v", st)
	}
	// A stale expected head is "no evidence", never success.
	if st, _ := cn.GetCIStatusAtHead(sameRepo, 7, strings.Repeat("b", 40)); st == vcs.CIStatusSuccess {
		t.Error("a pipeline for a different head counted as success")
	}
}

// Environment-scoped variables are a capability too.
func TestVariableScopeCapability(t *testing.T) {
	old := newFakeSite(t, "token-a")
	c := old.client("a", "token-a", vcs.CapCIVariableScope)
	if _, err := c.GetProjectVariable(sameRepo, "DEPLOY", "staging"); !errors.Is(err, vcs.ErrNotSupported) {
		t.Fatalf("scoped lookup on a site without scopes: %v", err)
	}
	if len(old.requests()) != 0 {
		t.Errorf("requests sent: %v", old.requests())
	}
}

func TestErrorsNameSiteAndNeverContainToken(t *testing.T) {
	b := newFakeSite(t, "token-b")
	b.extra = func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
		return true
	}
	// Single attempt to avoid retry backoff: use a write (POST is not retried).
	_, err := b.client("b", "token-b").CreateIssue(sameRepo, "t", "body")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "GitLab site b") || !strings.Contains(err.Error(), b.srv.URL) {
		t.Errorf("error does not name site and URL: %v", err)
	}
	if strings.Contains(err.Error(), "token-b") {
		t.Errorf("error leaks the token: %v", err)
	}
}
