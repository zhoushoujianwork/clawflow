package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- Test auth stub ----

// testAuth is a minimal AuthHandler for unit-testing worker token enforcement.
// It holds a flat map of plaintext-token → APIToken so tests can verify that
// machine A's token is rejected when the request body claims machine B.
type testAuth struct {
	tokens map[string]*APIToken // plaintext → token row
	users  map[string]*User     // user_id → User
}

type testAuthUserKey struct{}
type testAuthTokenKey struct{}

func (a *testAuth) RegisterRoutes(_ *http.ServeMux) {}

func (a *testAuth) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, tok := a.resolve(r)
		if tok == nil {
			writeCloudError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		u := a.users[tok.UserID]
		if u == nil {
			writeCloudError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		ctx := context.WithValue(r.Context(), testAuthUserKey{}, u)
		ctx = context.WithValue(ctx, testAuthTokenKey{}, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *testAuth) RequireMachine(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, tok := a.resolve(r)
		if tok == nil || tok.Kind != APITokenKindMachine {
			writeCloudError(w, http.StatusUnauthorized, "machine credential required")
			return
		}
		u := a.users[tok.UserID]
		ctx := context.WithValue(r.Context(), testAuthUserKey{}, u)
		ctx = context.WithValue(ctx, testAuthTokenKey{}, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *testAuth) UserFromContext(ctx context.Context) *User {
	u, _ := ctx.Value(testAuthUserKey{}).(*User)
	return u
}

func (a *testAuth) TokenFromContext(ctx context.Context) *APIToken {
	tok, _ := ctx.Value(testAuthTokenKey{}).(*APIToken)
	return tok
}

func (a *testAuth) resolve(r *http.Request) (*User, *APIToken) {
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, "Bearer ") {
		return nil, nil
	}
	plain := strings.TrimSpace(strings.TrimPrefix(authz, "Bearer "))
	tok := a.tokens[plain]
	if tok == nil {
		return nil, nil
	}
	u := a.users[tok.UserID]
	return u, tok
}

// newTestAuthWithMachines registers two machines (A and B) plus one personal
// token and returns the auth, the store, and each machine's token plaintext.
func newTestAuthWithMachines(t *testing.T) (ta *testAuth, store *MemoryStore, tokenA, tokenB, personalToken string, machineA, machineB RegisterWorkerResponse) {
	t.Helper()
	store = NewMemoryStore()

	// Seed one user for token attribution (testAuth resolves users from its own map).
	alice := &User{ID: "u1", Login: "alice"}
	ta = &testAuth{
		tokens: make(map[string]*APIToken),
		users:  map[string]*User{"u1": alice},
	}

	// Register machine A.
	var err error
	machineA, err = store.RegisterWorker(RegisterWorkerRequest{Hostname: "machine-a"})
	if err != nil {
		t.Fatalf("register machine-a: %v", err)
	}
	tokenA = "wtok-machine-a"
	ta.tokens[tokenA] = &APIToken{
		ID: "tok-a", UserID: "u1", Kind: APITokenKindMachine, MachineID: machineA.MachineID,
	}

	// Register machine B.
	machineB, err = store.RegisterWorker(RegisterWorkerRequest{Hostname: "machine-b"})
	if err != nil {
		t.Fatalf("register machine-b: %v", err)
	}
	tokenB = "wtok-machine-b"
	ta.tokens[tokenB] = &APIToken{
		ID: "tok-b", UserID: "u1", Kind: APITokenKindMachine, MachineID: machineB.MachineID,
	}

	// Personal token (kind=personal — should be rejected by all worker routes).
	personalToken = "pat-personal"
	ta.tokens[personalToken] = &APIToken{
		ID: "tok-p", UserID: "u1", Kind: APITokenKindPersonal,
	}

	return
}

func TestMemoryStoreLeaseHonorsMachineBinding(t *testing.T) {
	store := NewMemoryStore()
	a, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "b"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.EnqueueJob(JobSpec{
		DedupeKey: "github:o/r:issue:1:evaluate-bug",
		Repo:      "o/r",
		Platform:  "github",
		Operator:  "evaluate-bug",
		Target:    "issue",
		Number:    1,
	}, a.MachineID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Lease(LeaseRequest{MachineID: b.MachineID, WorkerID: b.WorkerID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("machine b leased bound job: %#v", got)
	}
	got, err = store.Lease(LeaseRequest{MachineID: a.MachineID, WorkerID: a.WorkerID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.JobID != rec.Spec.JobID || got.RunID == "" {
		t.Fatalf("machine a lease = %#v, want job %s with run id", got, rec.Spec.JobID)
	}
}

func TestMemoryStoreDedupeReturnsExistingPendingJob(t *testing.T) {
	store := NewMemoryStore()
	spec := JobSpec{
		DedupeKey: "github:o/r:issue:2:evaluate-bug",
		Repo:      "o/r",
		Operator:  "evaluate-bug",
		Target:    "issue",
		Number:    2,
	}
	first, err := store.EnqueueJob(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueJob(spec, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Spec.JobID != second.Spec.JobID {
		t.Fatalf("dedupe created new job %s, want %s", second.Spec.JobID, first.Spec.JobID)
	}
}

func TestMemoryStoreLeaseExpiryReturnsJobToPending(t *testing.T) {
	store := NewMemoryStore()
	reg, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host"})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.EnqueueJob(JobSpec{
		Repo:     "o/r",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   3,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lease(LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID}, time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	got, err := store.Lease(LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.JobID != rec.Spec.JobID {
		t.Fatalf("expired job was not leased again: %#v", got)
	}
	if job := store.GetJob(rec.Spec.JobID); job.AttemptCount != 2 {
		t.Fatalf("attempt count = %d, want 2", job.AttemptCount)
	}
}

func TestServerWorkerLifecycle(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()
	client, err := NewClient(Config{BaseURL: srv.URL, AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := client.RegisterWorker(t.Context(), RegisterWorkerRequest{Hostname: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Heartbeat(t.Context(), HeartbeatRequest{
		MachineID: reg.MachineID,
		WorkerID:  reg.WorkerID,
		Status:    WorkerStatusOnline,
		Capacity:  1,
	}); err != nil {
		t.Fatal(err)
	}
	store.EnqueueJob(JobSpec{
		Repo:     "o/r",
		Platform: "github",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   4,
	}, reg.MachineID)
	lease, err := client.Lease(t.Context(), LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID, Capacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Job == nil || lease.Job.RunID == "" {
		t.Fatalf("lease = %#v", lease)
	}
	if err := client.AppendRunEvents(t.Context(), lease.Job.RunID, RunEventsRequest{Events: []RunEvent{{Message: "started"}}}); err != nil {
		t.Fatal(err)
	}
	if err := client.FinishRun(t.Context(), lease.Job.RunID, FinishRunRequest{Status: JobStatusSucceeded, Outcome: "agent-evaluated"}); err != nil {
		t.Fatal(err)
	}
	run := store.GetRun(lease.Job.RunID)
	if run == nil || run.Status != JobStatusSucceeded || len(run.Events) != 1 {
		t.Fatalf("run = %#v", run)
	}
}

func TestServerDevJobEndpoint(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"job": map[string]any{
			"repo":     "o/r",
			"operator": "evaluate-bug",
			"number":   5,
		},
	})
	resp, err := http.Post(srv.URL+"/api/cloud/dev/jobs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// ---- Security regression tests (issue #185) ----

// machineReq is a helper that builds an authenticated POST request using the
// supplied Bearer token and JSON body.
func machineReq(t *testing.T, srvURL, path, token string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srvURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// TestMachineTokenEnforcesHeartbeatOwnership verifies that machine A's token
// cannot send a heartbeat claiming to be machine B.
func TestMachineTokenEnforcesHeartbeatOwnership(t *testing.T) {
	ta, store, tokenA, tokenB, personalToken, machA, machB := newTestAuthWithMachines(t)
	srv := httptest.NewServer(NewServerWithAuth(store, nil, ta))
	defer srv.Close()

	hbBodyA := HeartbeatRequest{
		MachineID: machA.MachineID, WorkerID: machA.WorkerID,
		Status: WorkerStatusOnline, Capacity: 1,
	}
	hbBodyB := HeartbeatRequest{
		MachineID: machB.MachineID, WorkerID: machB.WorkerID,
		Status: WorkerStatusOnline, Capacity: 1,
	}

	// Token A → heartbeat as machine A: should succeed (200).
	resp := machineReq(t, srv.URL, "/api/worker/heartbeat", tokenA, hbBodyA)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("token-A heartbeating as machine-A: got %d, want 200", resp.StatusCode)
	}

	// Token A → heartbeat as machine B: must be 403.
	resp = machineReq(t, srv.URL, "/api/worker/heartbeat", tokenA, hbBodyB)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("token-A heartbeating as machine-B: got %d, want 403", resp.StatusCode)
	}

	// Token B → heartbeat as machine B: should succeed (200).
	resp = machineReq(t, srv.URL, "/api/worker/heartbeat", tokenB, hbBodyB)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("token-B heartbeating as machine-B: got %d, want 200", resp.StatusCode)
	}

	// Personal token → must be rejected by RequireMachine (401).
	resp = machineReq(t, srv.URL, "/api/worker/heartbeat", personalToken, hbBodyA)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("personal token on heartbeat: got %d, want 401", resp.StatusCode)
	}

	// No token → 401.
	resp = machineReq(t, srv.URL, "/api/worker/heartbeat", "", hbBodyA)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token on heartbeat: got %d, want 401", resp.StatusCode)
	}
}

// TestMachineTokenEnforcesLeaseOwnership verifies that machine A's token
// cannot lease as machine B.
func TestMachineTokenEnforcesLeaseOwnership(t *testing.T) {
	ta, store, tokenA, tokenB, personalToken, machA, machB := newTestAuthWithMachines(t)
	srv := httptest.NewServer(NewServerWithAuth(store, nil, ta))
	defer srv.Close()

	// Enqueue two jobs, one bound to each machine.
	store.EnqueueJob(JobSpec{
		Repo: "o/r", Platform: "github",
		Operator: "evaluate-bug", Target: "issue", Number: 10,
	}, machA.MachineID)
	store.EnqueueJob(JobSpec{
		Repo: "o/r", Platform: "github",
		Operator: "evaluate-bug", Target: "issue", Number: 11,
	}, machB.MachineID)

	leaseA := LeaseRequest{MachineID: machA.MachineID, WorkerID: machA.WorkerID, Capacity: 1}
	leaseB := LeaseRequest{MachineID: machB.MachineID, WorkerID: machB.WorkerID, Capacity: 1}

	// Token A → lease as machine A: should succeed.
	resp := machineReq(t, srv.URL, "/api/worker/lease", tokenA, leaseA)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("token-A leasing as machine-A: got %d, want 200", resp.StatusCode)
	}

	// Token A → lease claiming machine B: must be 403.
	resp = machineReq(t, srv.URL, "/api/worker/lease", tokenA, leaseB)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("token-A leasing as machine-B: got %d, want 403", resp.StatusCode)
	}

	// Token B → lease as machine B: should succeed.
	resp = machineReq(t, srv.URL, "/api/worker/lease", tokenB, leaseB)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("token-B leasing as machine-B: got %d, want 200", resp.StatusCode)
	}

	// Personal token on lease → 401.
	resp = machineReq(t, srv.URL, "/api/worker/lease", personalToken, leaseA)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("personal token on lease: got %d, want 401", resp.StatusCode)
	}
}

// TestMachineTokenEnforcesRunOwnership verifies that machine A's token cannot
// post events or finish a run that was leased by machine B.
func TestMachineTokenEnforcesRunOwnership(t *testing.T) {
	ta, store, tokenA, tokenB, _, _, machB := newTestAuthWithMachines(t)
	srv := httptest.NewServer(NewServerWithAuth(store, nil, ta))
	defer srv.Close()

	// Machine B leases a job directly via the store (bypass HTTP so we don't
	// need to deal with HTTP lease ownership check for this setup step).
	store.EnqueueJob(JobSpec{
		Repo: "o/r", Platform: "github",
		Operator: "evaluate-bug", Target: "issue", Number: 20,
	}, machB.MachineID)
	jobSpec, err := store.Lease(LeaseRequest{
		MachineID: machB.MachineID, WorkerID: machB.WorkerID,
	}, time.Minute)
	if err != nil || jobSpec == nil {
		t.Fatalf("store lease for machine-B: %v / %v", err, jobSpec)
	}
	runID := jobSpec.RunID

	eventsPath := "/api/worker/runs/" + runID + "/events"
	finishPath := "/api/worker/runs/" + runID + "/finish"

	// Machine A trying to append events to machine B's run → 403.
	resp := machineReq(t, srv.URL, eventsPath, tokenA,
		RunEventsRequest{Events: []RunEvent{{Message: "hijack"}}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("token-A events on machine-B's run: got %d, want 403", resp.StatusCode)
	}

	// Machine A trying to finish machine B's run → 403.
	resp = machineReq(t, srv.URL, finishPath, tokenA,
		FinishRunRequest{Status: JobStatusSucceeded})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("token-A finish on machine-B's run: got %d, want 403", resp.StatusCode)
	}

	// Machine B posting events for its own run → 204.
	resp = machineReq(t, srv.URL, eventsPath, tokenB,
		RunEventsRequest{Events: []RunEvent{{Message: "progress"}}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("token-B events on machine-B's run: got %d, want 204", resp.StatusCode)
	}

	// Machine B finishing its own run → 204.
	resp = machineReq(t, srv.URL, finishPath, tokenB,
		FinishRunRequest{Status: JobStatusSucceeded, Outcome: "agent-evaluated"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("token-B finish on machine-B's run: got %d, want 204", resp.StatusCode)
	}
}

// TestDevJobsDisabledInProdMode verifies that /api/cloud/dev/jobs returns 404
// when the server is started with a real auth handler (production mode).
func TestDevJobsDisabledInProdMode(t *testing.T) {
	ta, store, tokenA, _, _, _, _ := newTestAuthWithMachines(t)
	srv := httptest.NewServer(NewServerWithAuth(store, nil, ta))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"job": map[string]any{
			"repo": "o/r", "operator": "evaluate-bug", "number": 99,
		},
	})
	// Even with a valid machine token the endpoint should not exist.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/cloud/dev/jobs",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenA)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("dev/jobs in prod mode: got %d, want 404", resp.StatusCode)
	}
}
