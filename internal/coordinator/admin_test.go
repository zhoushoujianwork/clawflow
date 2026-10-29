package coordinator

import (
	"context"
	"errors"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
)

type adminTestStore struct{ apiStore }

func (s *adminTestStore) VerifyToken(_ context.Context, secret string) (*Token, error) {
	switch secret {
	case "admin":
		return &Token{Name: "admin", Scope: ScopeAdmin}, nil
	case "worker":
		return &Token{Name: "worker", Scope: ScopeWorker}, nil
	case "legacy":
		return &Token{Name: "legacy"}, nil
	}
	return nil, ErrToken
}

type adminTestService struct{ calls int }

func (s *adminTestService) Repos(_ context.Context, r RepoRequest) (any, error) {
	s.calls++
	return map[string]string{"action": r.Action}, nil
}
func (s *adminTestService) Status(context.Context) (PlatformStatus, error) {
	s.calls++
	return PlatformStatus{Version: "test-version"}, nil
}

func TestAdminAuthenticationAndAudit(t *testing.T) {
	store, service := &adminTestStore{}, &adminTestService{}
	h := NewPlatformHandler(store, service)
	for _, tc := range []struct {
		token, method, path, body string
		status                    int
	}{
		{"", "GET", "/admin/v1/repos", "", 401},
		{"worker", "GET", "/admin/v1/repos", "", 403},
		{"legacy", "GET", "/admin/v1/status", "", 403},
		{"admin", "GET", "/admin/v1/repos", "", 200},
		{"admin", "GET", "/admin/v1/status", "", 200},
		{"admin", "POST", "/admin/v1/repos", `{"action":"add","repo":"o/r"}`, 200},
		{"admin", "POST", "/admin/v1/repos", `{"action":"enable","repo":"o/r"}`, 200},
		{"admin", "POST", "/admin/v1/repos", `{"action":"disable","repo":"o/r"}`, 200},
		{"admin", "POST", "/admin/v1/repos", `{"action":"set","repo":"o/r","options":{"ci-required":"on"}}`, 200},
		{"admin", "POST", "/admin/v1/repos", `{"action":"remove","repo":"o/r"}`, 400},
		{"admin", "POST", "/admin/v1/repos", `{"action":"add","repo":"o/r","unknown":1}`, 400},
		{"admin", "POST", "/admin/v1/repos", `{} {}`, 400},
		{"admin", "DELETE", "/admin/v1/repos", "", 405},
		{"admin", "POST", "/worker/v1/register", `{}`, 401},
	} {
		before := service.calls
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
		if tc.status != 200 && service.calls != before {
			t.Fatalf("rejected request reached admin service: %+v", tc)
		}
		if tc.path == "/admin/v1/status" && tc.status == 200 && !strings.Contains(w.Body.String(), "test-version") {
			t.Fatal(w.Body.String())
		}
	}
	for _, action := range []string{"add", "enable", "disable", "set"} {
		found := false
		for _, a := range store.audits {
			if a.Action == "admin:repo-"+action+":200" && a.Worker == "admin" && a.Task == "o/r" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing audit for %s", action)
		}
	}
}

func TestTokenScopesPersist(t *testing.T) {
	s, db, _ := setup(t)
	secrets := map[string]string{}
	for _, scope := range []string{ScopeWorker, ScopeAdmin} {
		secret, err := s.CreateToken(ctx, scope, scope)
		if err != nil {
			t.Fatal(err)
		}
		secrets[scope] = secret
		token, err := s.VerifyToken(ctx, secret)
		if err != nil || token == nil || !token.HasScope(scope) {
			t.Fatalf("scope %s: %v %v", scope, token, err)
		}
	}
	tokens, err := s.ListTokens(ctx)
	if err != nil || len(tokens) != 2 {
		t.Fatalf("list %v %v", tokens, err)
	}
	for _, token := range tokens {
		if token.Scope != token.Name {
			t.Fatalf("missing scope: %+v", token)
		}
	}
	if _, err := s.CreateToken(ctx, "invalid", "root"); err == nil {
		t.Fatal("invalid scope accepted")
	}
	if err := db.DB().Exec("ALTER TABLE coordinator_tokens DROP COLUMN scope").Error; err != nil {
		t.Fatal(err)
	}
	// Upgrade a pre-scope schema and preserve existing worker access.
	if err := db.DB().Exec("DELETE FROM schema_migrations WHERE name = ?", "coordinator_token_scope").Error; err != nil {
		t.Fatal(err)
	}
	// The migration harness itself is covered by store tests; exercise its registered upgrade.
	if applied, err := db.Migrate(ctx, store.Migrations); err != nil || len(applied) != 1 || applied[0] != 5 {
		t.Fatalf("token scope upgrade: applied=%v err=%v", applied, err)
	}
	for _, secret := range secrets {
		token, err := s.VerifyToken(ctx, secret)
		if err != nil || token == nil || !token.HasScope(ScopeWorker) || token.HasScope(ScopeAdmin) {
			t.Fatalf("legacy token upgrade: %+v %v", token, err)
		}
	}
	token, err := s.VerifyToken(ctx, "invalid")
	if err == nil || token != nil {
		t.Fatal("invalid secret accepted")
	}
}

func TestRepoAuditTarget(t *testing.T) {
	for input, want := range map[string]string{
		"o/r": "o/r", "https://user:secret@github.com/o/r.git?token=secret": "o/r", "git@github.com:o/r.git": "o/r", "./secret": "", "/tmp/secret": "", "o/../secret": "",
	} {
		if got := repoAuditTarget(input); got != want {
			t.Fatalf("target %q: %q", input, got)
		}
	}
}

type failingAdminAudit struct{ adminTestStore }

func (*failingAdminAudit) AppendAudit(context.Context, string, string, string) error {
	return errors.New("audit unavailable")
}
func TestAdminWriteRequiresAudit(t *testing.T) {
	service := &adminTestService{}
	h := NewPlatformHandler(&failingAdminAudit{}, service)
	r := httptest.NewRequest("POST", "/admin/v1/repos", strings.NewReader(`{"action":"enable","repo":"o/r"}`))
	r.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 500 || service.calls != 0 {
		t.Fatalf("unaudited write: %d calls=%d", w.Code, service.calls)
	}
}
