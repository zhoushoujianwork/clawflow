package coordinator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestRepoAuditTargetNamesPlatformSiteAndFullPath(t *testing.T) {
	for in, want := range map[string]string{
		"acme/service":                        "acme/service",
		"team/tools/service":                  "team/tools/service",
		"b:team/tools/service":                "gitlab site=b team/tools/service",
		"https://github.com/acme/service.git": "acme/service",
		"git@github.com:acme/service.git":     "acme/service",
		// Deployment prefix and port stay visible; the prefix is part of the path
		// only because the syntactic fallback cannot know the site root.
		"https://gitlab-b.example.com:8443/gitlab/team/tools/service":    "gitlab host=gitlab-b.example.com:8443 gitlab/team/tools/service",
		"ssh://git@ssh.gitlab-b.example.com:2222/team/tools/service.git": "gitlab host=ssh.gitlab-b.example.com:2222 team/tools/service",
		"git@gitlab-a.example.com:team/tools/service.git":                "gitlab host=gitlab-a.example.com team/tools/service",
	} {
		if got := repoAuditTarget(in); got != want {
			t.Errorf("repoAuditTarget(%q) = %q, want %q", in, got, want)
		}
	}
	// Credentials and queries never reach the audit text.
	got := repoAuditTarget("https://user:secret@gitlab-b.example.com/team/tools/service?private_token=abc")
	if got != "gitlab host=gitlab-b.example.com team/tools/service" {
		t.Errorf("credential URL target = %q", got)
	}
	for _, bad := range []string{"", "x", "../a/b", "b:../x/y", "bad alias:a/b", "a/b c", "https://", "ssh://git@/a/b", "https://gitlab.example.com/a/../b", strings.Repeat("a", 2000)} {
		if got := repoAuditTarget(bad); got != "" {
			t.Errorf("repoAuditTarget(%q) = %q, want empty", bad, got)
		}
	}
}

type auditStore struct {
	WorkerStore
	mu   sync.Mutex
	rows []string // "task|action"
}

func (s *auditStore) VerifyToken(context.Context, string) (*Token, error) {
	return &Token{Name: "admin-1", Scope: ScopeAdmin}, nil
}

func (s *auditStore) AppendAudit(_ context.Context, _, task, action string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, task+"|"+action)
	return nil
}

type auditAdmin struct {
	AdminService
	resolved string
}

func (a auditAdmin) Repos(context.Context, RepoRequest) (any, error) {
	return map[string]bool{"ok": true}, nil
}

func (a auditAdmin) AuditRepo(string) string { return a.resolved }

func postRepo(t *testing.T, h http.Handler, repo string) {
	t.Helper()
	body, _ := json.Marshal(RepoRequest{Action: "enable", Repo: repo})
	req := httptest.NewRequest("POST", "/admin/v1/repos", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// Every repo write records its attempt and result with the platform, site and
// full path, for a site-qualified key and for an ssh:// address alike.
func TestRepoAdminAuditRecordsSiteAndFullPath(t *testing.T) {
	for _, repo := range []string{"b:team/tools/service", "ssh://git@ssh.gitlab-b.example.com:2222/team/tools/service.git"} {
		store := &auditStore{}
		postRepo(t, NewPlatformHandler(store, auditAdmin{}), repo)
		if len(store.rows) != 2 {
			t.Fatalf("%s: audit rows %v", repo, store.rows)
		}
		for _, row := range store.rows {
			task, _, _ := strings.Cut(row, "|")
			if task == "" || !strings.HasPrefix(task, "gitlab ") || !strings.HasSuffix(task, "team/tools/service") {
				t.Errorf("%s: audit target %q lacks platform/site/full path", repo, task)
			}
		}
	}
}

// When the host resolves the argument, the audit carries the resolved site and
// URL (including a deployment prefix) instead of the syntactic guess.
func TestRepoAdminAuditPrefersHostResolution(t *testing.T) {
	store := &auditStore{}
	want := "gitlab site=b https://gitlab-b.example.com:8443/gitlab team/tools/service"
	postRepo(t, NewPlatformHandler(store, auditAdmin{resolved: want}), "https://gitlab-b.example.com:8443/gitlab/team/tools/service")
	for _, row := range store.rows {
		if task, _, _ := strings.Cut(row, "|"); task != want {
			t.Errorf("audit target %q, want %q", task, want)
		}
	}
	// An unresolved argument falls back to the syntactic target, never to "".
	store = &auditStore{}
	postRepo(t, NewPlatformHandler(store, auditAdmin{}), "acme/service")
	if task, _, _ := strings.Cut(store.rows[0], "|"); task != "acme/service" {
		t.Errorf("github audit target %q", task)
	}
}
