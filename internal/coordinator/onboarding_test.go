package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInvitationIsSingleUseAndWorkersStartPaused(t *testing.T) {
	s, db, _ := setup(t)
	offer, err := s.Invite(ctx, "lan-worker")
	if err != nil {
		t.Fatal(err)
	}
	if offer.ExpiresAt.Before(time.Now()) || len(offer.Secret) != 64 {
		t.Fatal("invalid offer")
	}
	if _, err := s.VerifyToken(ctx, offer.Secret); !errors.Is(err, ErrToken) {
		t.Fatal("invitation granted worker access")
	}
	reply, err := s.Exchange(ctx, offer.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if reply["token"] == offer.Secret {
		t.Fatal("credential not rotated")
	}
	if _, err := s.Exchange(ctx, offer.Secret); !errors.Is(err, ErrToken) {
		t.Fatal("invitation replay accepted")
	}
	token, err := s.VerifyToken(ctx, reply["token"])
	if err != nil || !token.HasScope(ScopeWorker) {
		t.Fatal(token, err)
	}
	register(t, s, "lan-worker", 2, "project-a")
	task := enqueue(t, s, 17, "project-a")
	if _, err := s.Claim(ctx, "lan-worker"); !errors.Is(err, ErrNoTask) {
		t.Fatal("registration cleared pause", err)
	}
	board, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !board.Workers[0].Paused || board.Workers[0].MaxParallel != 2 {
		t.Fatal(board.Workers)
	}
	if err := s.SetWorkerPaused(ctx, "lan-worker", false); err != nil {
		t.Fatal(err)
	}
	leased := claim(t, s, "lan-worker")
	if leased.ID != task.ID {
		t.Fatal(leased)
	}
	if err := s.SetWorkerPaused(ctx, "lan-worker", true); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveWorker(ctx, "lan-worker"); err == nil {
		t.Fatal("removed worker with active task")
	}
	if err := s.Heartbeat(ctx, task.ID, "lan-worker", leased.FencingToken); err != nil {
		t.Fatal("drain interrupted active task", err)
	}
	if err := s.Complete(ctx, task.ID, "lan-worker", leased.FencingToken, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveWorker(ctx, "lan-worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyToken(ctx, reply["token"]); !errors.Is(err, ErrToken) {
		t.Fatal("removed credential still authenticates")
	}
	var audits []Audit
	if err := db.DB().Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(audits)
	if strings.Contains(string(data), offer.Secret) || strings.Contains(string(data), reply["token"]) {
		t.Fatal("secret in audit")
	}
}

func TestInvitationExpiryAndConcurrentExchange(t *testing.T) {
	s, db, _ := setup(t)
	offer, err := s.Invite(ctx, "expired")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DB().Model(&Invitation{}).Where("name = ?", "expired").Update("expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exchange(ctx, offer.Secret); !errors.Is(err, ErrToken) {
		t.Fatal("expired invitation accepted")
	}
	offer, err = s.Invite(ctx, "concurrent")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Exchange(ctx, offer.Secret); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrToken) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("%d successful exchanges", success)
	}
}

func TestJoinHTTPRejectsMalformedAndReplayedInput(t *testing.T) {
	s, _, _ := setup(t)
	offer, err := s.Invite(ctx, "http-worker")
	if err != nil {
		t.Fatal(err)
	}
	h := JoinHandler(s)
	for _, tc := range []struct {
		method, body string
		status       int
	}{
		{"GET", "", 405}, {"POST", `{"unknown":1}`, 400}, {"POST", `{"secret":"short"}`, 400},
		{"POST", `{"secret":"` + offer.Secret + `"} {}`, 400},
		{"POST", `{"secret":"` + offer.Secret + `"}`, 200},
		{"POST", `{"secret":"` + offer.Secret + `"}`, 401},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, "/worker/v1/join", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.method, w.Code)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("credential response cacheable")
		}
	}
}

type dashboardTestService struct{ adminTestService }

func (s *dashboardTestService) Dashboard(_ context.Context) (Dashboard, error) {
	s.calls++
	return Dashboard{SchemaVersion: 1}, nil
}
func (s *dashboardTestService) DashboardAction(_ context.Context, _ DashboardRequest) (any, error) {
	s.calls++
	return map[string]bool{"ok": true}, nil
}

func TestDashboardAuthorizationAndMutationAllowlist(t *testing.T) {
	service := &dashboardTestService{}
	h := NewPlatformHandler(&adminTestStore{}, service)
	for _, tc := range []struct {
		secret, method, path, body string
		status                     int
	}{
		{"worker", "GET", "/admin/v1/dashboard", "", 403},
		{"", "GET", "/admin/v1/dashboard", "", 401},
		{"admin", "GET", "/admin/v1/dashboard", "", 200},
		{"admin", "POST", "/admin/v1/dashboard/action", `{"action":"invite","name":"mac"}`, 200},
		{"worker", "POST", "/admin/v1/dashboard/action", `{"action":"invite","name":"mac"}`, 403},
		{"admin", "POST", "/admin/v1/dashboard/action", `{"action":"merge","name":"mac"}`, 400},
		{"admin", "POST", "/admin/v1/dashboard/action", `{"action":"resume","name":"mac","scope":"admin"}`, 400},
		{"admin", "GET", "/admin/v1/dashboard/action", "", 405},
	} {
		before := service.calls
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.secret)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
		if tc.status != 200 && before != service.calls {
			t.Fatal("rejected request reached service")
		}
	}
}
