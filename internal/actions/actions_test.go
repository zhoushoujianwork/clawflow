package actions

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func requestFixture() Request {
	return Request{
		Contract: acceptance.Contract{Version: 1, TaskID: "task", Generation: 1, Repository: "o/r", PRNumber: 7, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), IntegrationSHA: strings.Repeat("c", 40), PolicyDigest: "policy", Requirements: "tests", Development: acceptance.Identity{SessionID: "dev", RunID: "run"}},
		Policy:   Policy{Version: 1, RepositoryID: 1, WorkflowID: 2, WorkflowPath: ".github/workflows/ci.yml", WorkflowDigest: strings.Repeat("d", 64), Jobs: []RequiredJob{{Name: "Test", Steps: []string{"Unit tests"}}}},
	}
}

func TestRequestImmutableIdentity(t *testing.T) {
	r := requestFixture()
	key, err := r.Key()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.Contract.HeadSHA = strings.Repeat("e", 40) },
		func(r *Request) { r.Contract.BaseSHA = strings.Repeat("e", 40) },
		func(r *Request) { r.Contract.IntegrationSHA = strings.Repeat("e", 40) },
		func(r *Request) { r.Contract.Generation++ },
		func(r *Request) { r.Contract.PolicyDigest = "different" },
		func(r *Request) { r.Policy.WorkflowDigest = strings.Repeat("e", 64) },
		func(r *Request) { r.Policy.WorkflowID++ },
	} {
		next := requestFixture()
		change(&next)
		got, err := next.Key()
		if err != nil || got == key {
			t.Fatalf("identity did not change: %s %v", got, err)
		}
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.Policy.Version = 0 },
		func(r *Request) { r.Policy.WorkflowDigest = "" },
		func(r *Request) { r.Policy.Jobs = nil },
		func(r *Request) { r.Policy.Jobs[0].Steps = nil },
		func(r *Request) { r.Policy.Jobs = append(r.Policy.Jobs, r.Policy.Jobs[0]) },
		func(r *Request) { r.Contract.HeadSHA = "main" },
		func(r *Request) { r.Contract.Repository = "o/r/../../other" },
		func(r *Request) { r.Contract.IntegrationSHA = "" },
	} {
		next := requestFixture()
		change(&next)
		if _, err := next.Key(); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}

type observeFunc func(context.Context, Request, Observation) (Observation, error)

func (f observeFunc) ObserveActions(ctx context.Context, r Request, o Observation) (Observation, error) {
	return f(ctx, r, o)
}

func TestLedgerRestartRerunAndErrors(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	r := requestFixture()
	key, _ := r.Key()
	calls := 0
	provider := observeFunc(func(_ context.Context, _ Request, previous Observation) (Observation, error) {
		calls++
		if calls == 1 {
			if previous.RunID != 0 {
				t.Fatal(previous)
			}
			return Observation{RunID: 10, Attempt: 1, State: "waiting_external"}, nil
		}
		if previous.RunID != 10 || previous.Key != key {
			t.Fatalf("lost restart identity: %+v", previous)
		}
		if calls == 2 {
			return Observation{RunID: 10, Attempt: 1, State: "passed"}, nil
		}
		if calls == 3 {
			return Observation{}, errors.New("429 secret response body")
		}
		return Observation{RunID: 10, Attempt: 2, State: "failed"}, nil
	})
	for _, want := range []string{"waiting_external", "passed", "infra_error", "failed", "failed"} {
		// A new Ledger value simulates process reattachment; no in-memory state.
		o, err := (Ledger{DB: db.DB()}).Reconcile(ctx, provider, r)
		if err != nil || o.State != want || o.RunID != 10 || strings.Contains(o.Reason, "secret") {
			t.Fatalf("%+v %v", o, err)
		}
		pending, err := (Ledger{DB: db.DB()}).Pending(ctx)
		waiting := want == "waiting_external" || want == "infra_error"
		if err != nil || (len(pending) == 1) != waiting {
			t.Fatalf("restart polling %s: %v %v", want, pending, err)
		}
	}
	var count int64
	if err := db.DB().Model(&record{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("one durable record per attempt: %d %v", count, err)
	}
	// Changed contract inputs must not reuse a successful or failed attachment.
	r.Contract.BaseSHA = strings.Repeat("e", 40)
	_, err := (Ledger{DB: db.DB()}).Reconcile(ctx, observeFunc(func(_ context.Context, _ Request, previous Observation) (Observation, error) {
		if previous.RunID != 0 {
			t.Fatal("stale attachment reused")
		}
		return Observation{State: "waiting_external"}, nil
	}), r)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLedgerRetainsFirstPartiallyObservedRunOnError(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	r := requestFixture()
	key, _ := r.Key()
	o, err := (Ledger{DB: db.DB()}).Reconcile(ctx, observeFunc(func(context.Context, Request, Observation) (Observation, error) {
		return Observation{RunID: 22, Attempt: 2}, errors.New("rate limited after discovery")
	}), r)
	if err != nil || o.RunID != 22 || o.State != "infra_error" {
		t.Fatalf("%+v %v", o, err)
	}
	loaded, err := (Ledger{DB: db.DB()}).Load(ctx, key)
	if err != nil || loaded.RunID != 22 || loaded.Attempt != 2 {
		t.Fatalf("first discovered identity lost on restart: %+v %v", loaded, err)
	}
}
