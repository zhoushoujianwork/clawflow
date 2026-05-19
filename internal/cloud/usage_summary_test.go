package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBuildUsageSummary pins down the pure aggregation function: a
// hand-rolled slice of UsageRecord rows lands in the right buckets and
// the daily trend has zeros for empty days.
func TestBuildUsageSummary(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	mkRec := func(repo, op string, endedAt time.Time, cost float64, in, out int64, model string) *UsageRecord {
		return &UsageRecord{
			RunID:        "r-" + op + "-" + endedAt.Format("0102"),
			Repo:         repo,
			Operator:     op,
			TotalCostUSD: cost,
			InputTokens:  in,
			OutputTokens: out,
			EndedAt:      endedAt,
			ModelUsage: map[string]ModelUsage{
				model: {CostUSD: cost, InputTokens: in, OutputTokens: out},
			},
		}
	}

	records := []*UsageRecord{
		mkRec("acme/widgets", "evaluate-bug", now.AddDate(0, 0, -2), 0.10, 100, 50, "claude-opus-4-7"),
		mkRec("acme/widgets", "evaluate-bug", now.AddDate(0, 0, -2), 0.05, 80, 40, "claude-opus-4-7"),
		mkRec("acme/gadgets", "implement", now.AddDate(0, 0, -1), 0.30, 500, 200, "claude-haiku-4-5-20251001"),
		// Chat row — synthetic "chat" operator bucket.
		{
			SessionID:    "s-1",
			Repo:         "acme/widgets",
			TotalCostUSD: 0.02,
			InputTokens:  60,
			OutputTokens: 20,
			EndedAt:      now.AddDate(0, 0, -1),
			ModelUsage:   map[string]ModelUsage{"claude-opus-4-7": {CostUSD: 0.02, InputTokens: 60, OutputTokens: 20}},
		},
		// Outside the 30-day window: contributes to all-time totals
		// but not to the period.
		mkRec("acme/widgets", "evaluate-bug", now.AddDate(0, 0, -60), 1.00, 1000, 500, "claude-opus-4-7"),
	}

	sum := BuildUsageSummary(records, now)

	// All-time totals: 5 records.
	if sum.Totals.Runs != 5 {
		t.Errorf("Totals.Runs = %d, want 5", sum.Totals.Runs)
	}
	wantCost := 0.10 + 0.05 + 0.30 + 0.02 + 1.00
	if absDiff(sum.Totals.TotalCostUSD, wantCost) > 1e-9 {
		t.Errorf("Totals.TotalCostUSD = %v, want %v", sum.Totals.TotalCostUSD, wantCost)
	}
	if sum.ByOperator["evaluate-bug"].Runs != 3 {
		t.Errorf("ByOperator[evaluate-bug].Runs = %d, want 3", sum.ByOperator["evaluate-bug"].Runs)
	}
	if sum.ByOperator["chat"].Runs != 1 {
		t.Errorf("ByOperator[chat].Runs = %d, want 1", sum.ByOperator["chat"].Runs)
	}
	if sum.ByRepo["acme/widgets"].Runs != 4 {
		t.Errorf("ByRepo[acme/widgets].Runs = %d, want 4", sum.ByRepo["acme/widgets"].Runs)
	}

	// One period (current 30d). Outside-window row dropped from period.
	if len(sum.Periods) != 1 {
		t.Fatalf("len(Periods) = %d, want 1", len(sum.Periods))
	}
	p := sum.Periods[0]
	if p.Totals.Runs != 4 {
		t.Errorf("period.Totals.Runs = %d, want 4 (60-day-old row excluded)", p.Totals.Runs)
	}
	if len(p.DailyTrend) != 30 {
		t.Errorf("daily trend length = %d, want 30", len(p.DailyTrend))
	}

	// The two -2-day rows go to the same bucket.
	twoDaysAgo := now.AddDate(0, 0, -2).Format("2006-01-02")
	var foundDay *DailyPoint
	for i := range p.DailyTrend {
		if p.DailyTrend[i].Date == twoDaysAgo {
			foundDay = &p.DailyTrend[i]
			break
		}
	}
	if foundDay == nil {
		t.Fatalf("day %s not found in trend", twoDaysAgo)
	}
	if foundDay.Runs != 2 {
		t.Errorf("day %s Runs = %d, want 2", twoDaysAgo, foundDay.Runs)
	}
}

// TestUsageSummaryEndpoint exercises GET /api/cloud/usage/summary end
// to end: an authenticated user gets the aggregation; another user's
// rows do NOT appear (multi-user isolation); 401 without auth.
func TestUsageSummaryEndpoint(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	// Seed two users' worth of chat usage rows. user_id strictly filters.
	for _, u := range []struct {
		userID string
		cost   float64
	}{{"u-alice", 0.10}, {"u-bob", 0.20}} {
		if err := store.AddChatUsage(AddChatUsageInput{
			SessionID: "s-" + u.userID,
			UserID:    u.userID,
			Repo:      "acme/widgets",
			Usage: &Usage{
				TotalCostUSD: u.cost,
				InputTokens:  100,
				OutputTokens: 50,
			},
			EndedAt: now.AddDate(0, 0, -1),
		}); err != nil {
			t.Fatalf("AddChatUsage(%s): %v", u.userID, err)
		}
	}

	// One orphaned run_usage row (user_id=='') — must appear for any
	// authenticated user under the "single-user self-host" fallback.
	store.RunUsage["r-orphan"] = &UsageRecord{
		RunID:        "r-orphan",
		Repo:         "acme/gadgets",
		Operator:     "implement",
		TotalCostUSD: 0.99,
		InputTokens:  1000,
		OutputTokens: 200,
		EndedAt:      now.AddDate(0, 0, -2),
	}

	srv := httptest.NewServer(NewServerWithAuth(store, nil, &stubAuth{userID: "u-alice"}))
	defer srv.Close()

	// Authenticated request.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/cloud/usage/summary", nil)
	req.Header.Set("Authorization", "Bearer test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got UsageSummary
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Alice sees: her chat row (0.10) + orphan run row (0.99) = 2 rows, $1.09
	if got.Totals.Runs != 2 {
		t.Errorf("Alice Totals.Runs = %d, want 2 (her chat + orphan run)", got.Totals.Runs)
	}
	if absDiff(got.Totals.TotalCostUSD, 1.09) > 1e-9 {
		t.Errorf("Alice Totals.TotalCostUSD = %v, want 1.09", got.Totals.TotalCostUSD)
	}
	// Bob's $0.20 row must NOT have leaked into Alice's view.
	if absDiff(got.Totals.TotalCostUSD, 1.29) <= 1e-9 {
		t.Error("Alice saw Bob's $0.20 chat usage — multi-user isolation broken")
	}
}

// stubAuth is the in-package fake AuthHandler used by the summary
// endpoint test. RequireUser injects a constant user into the context;
// UserFromContext pulls it back out. RequireMachine / RegisterRoutes
// are no-ops because this test only hits user-auth routes.
type stubAuth struct {
	userID string
}

type stubAuthCtxKey struct{}

func (a *stubAuth) RegisterRoutes(_ *http.ServeMux) {}

func (a *stubAuth) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), stubAuthCtxKey{}, &User{ID: a.userID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *stubAuth) RequireMachine(next http.Handler) http.Handler { return next }

func (a *stubAuth) UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(stubAuthCtxKey{}).(*User)
	return u
}

func (a *stubAuth) TokenFromContext(_ context.Context) *APIToken { return nil }

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
