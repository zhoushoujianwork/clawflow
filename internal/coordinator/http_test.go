package coordinator

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The fake checks API identity and fencing independently of PostgreSQL availability.
type apiStore struct {
	WorkerStore
	mu     sync.Mutex
	worker Worker
	audits []Audit
	calls  int
}

func (s *apiStore) VerifyToken(_ context.Context, secret string) (*Token, error) {
	if secret != "valid" {
		return nil, ErrToken
	}
	return &Token{Name: "worker"}, nil
}
func (s *apiStore) RegisterWorker(_ context.Context, w Worker) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.worker = w
	s.calls++
	if w.MaxParallel < 1 {
		return ErrInvalid
	}
	return nil
}
func (s *apiStore) Claim(_ context.Context, w string) (*Task, error) {
	if w != "worker" {
		return nil, errors.New("wrong identity")
	}
	return &Task{ID: "task", FencingToken: 7, LeaseOwner: w}, nil
}
func (s *apiStore) Heartbeat(_ context.Context, id, w string, f int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if id != "task" || w != "worker" || f != 7 {
		return ErrStaleLease
	}
	return nil
}
func (s *apiStore) Complete(c context.Context, id, w string, f int64, r json.RawMessage) error {
	return s.Heartbeat(c, id, w, f)
}
func (s *apiStore) Fail(c context.Context, id, w string, f int64, r json.RawMessage) error {
	return s.Heartbeat(c, id, w, f)
}
func (s *apiStore) AppendAudit(_ context.Context, w, id, a string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, Audit{Worker: w, Task: id, Action: a})
	return nil
}
func request(t *testing.T, s *httptest.Server, path, auth, body string) (int, []byte) {
	t.Helper()
	r, err := http.NewRequest("POST", s.URL+"/worker/v1/"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	resp, err := s.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}
func TestWorkerHTTPS(t *testing.T) {
	store := &apiStore{}
	server := httptest.NewTLSServer(NewHandler(store))
	defer server.Close()
	cases := []struct {
		path, auth, body string
		status           int
	}{
		{"register", "Bearer valid", `{"tags":["host-test"],"max_parallel":2}`, 200},
		{"claim", "Bearer valid", "{}", 200},
		{"tasks/task/heartbeat", "Bearer valid", `{"fencing_token":7}`, 200},
		{"tasks/task/complete", "Bearer valid", `{"fencing_token":7,"result":{"output":"done"}}`, 200},
		{"tasks/task/fail", "Bearer valid", `{"fencing_token":7,"result":{}}`, 200},
		{"tasks/task/release", "Bearer valid", `{"fencing_token":7}`, 200},
		{"tasks/task/release", "Bearer valid", `{"fencing_token":6}`, 409},
		{"register", "", "{}", 401}, {"register", "Basic valid", "{}", 401},
		{"register", "Bearer unknown", "{}", 401}, {"register", "Bearer revoked", "{}", 401},
		{"register", "Bearer valid extra", "{}", 401},
		{"tasks/task/heartbeat", "Bearer valid", `{"fencing_token":6}`, 409},
		{"tasks/task/complete", "Bearer valid", `{"fencing_token":6}`, 409},
		{"tasks/task/fail", "Bearer valid", `{"fencing_token":6}`, 409},
		{"register", "Bearer valid", `{"id":"victim"}`, 400},
		{"register", "Bearer valid", `{"max_parallel":0}`, 400},
		{"claim", "Bearer valid", "{} {}", 400},
		{"claim", "Bearer valid", "{", 400},
		{"register", "Bearer valid", `{"model":"` + strings.Repeat("x", MaxRequestBytes) + `"}`, 413},
		{"claim", "Bearer valid", "{}" + strings.Repeat(" ", MaxRequestBytes), 413},
		{"missing", "Bearer valid", "{}", 404},
	}
	for _, tc := range cases {
		status, body := request(t, server, tc.path, tc.auth, tc.body)
		if status != tc.status {
			t.Fatalf("%s: %d want %d: %s", tc.path, status, tc.status, body)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.audits) != len(cases) {
		t.Fatalf("audits=%d", len(store.audits))
	}
	for _, a := range store.audits {
		if strings.Contains(a.Action, "valid") {
			t.Fatal("secret in audit")
		}
	}
}

func TestCertificateReuse(t *testing.T) {
	dir := t.TempDir()
	cert, pin, err := LoadCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pin) != 64 {
		t.Fatalf("pin %q", pin)
	}
	second, pin2, err := LoadCertificate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pin != pin2 || string(cert.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatal("certificate changed")
	}
	info, err := os.Stat(filepath.Join(dir, "coordinator.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	server := httptest.NewUnstartedServer(NewHandler(&apiStore{}))
	server.TLS = NewServer("", &apiStore{}, cert).TLSConfig
	server.StartTLS()
	defer server.Close()
	if status, _ := request(t, server, "register", "Bearer valid", "{}"); status != 200 {
		t.Fatal(status)
	}
	if server.TLS.MinVersion != tls.VersionTLS12 {
		t.Fatal("TLS minimum")
	}
	if err := os.WriteFile(filepath.Join(dir, "coordinator.pem"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadCertificate(dir); err == nil {
		t.Fatal("corrupt certificate silently replaced")
	}
}

func TestWorkerHTTPSPostgres(t *testing.T) {
	store, db, _ := setup(t)
	secret, err := store.CreateToken(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store))
	defer server.Close()
	for _, auth := range []string{"", "Bearer unknown"} {
		if status, _ := request(t, server, "claim", auth, "{}"); status != 401 {
			t.Fatal(status)
		}
	}
	if status, _ := request(t, server, "register", "Bearer "+secret, "{}"); status != 200 {
		t.Fatal(status)
	}
	task := enqueue(t, store, 1)
	if status, _ := request(t, server, "claim", "Bearer "+secret, "{}"); status != 200 {
		t.Fatal(status)
	}
	for _, action := range []string{"heartbeat", "complete", "fail"} {
		if status, _ := request(t, server, "tasks/"+task.ID+"/"+action, "Bearer "+secret, `{"fencing_token":0}`); status != 409 {
			t.Fatal(action, status)
		}
	}
	if status, _ := request(t, server, "tasks/"+task.ID+"/heartbeat", "Bearer "+secret, `{"fencing_token":1}`); status != 200 {
		t.Fatal(status)
	}
	if status, _ := request(t, server, "tasks/"+task.ID+"/complete", "Bearer "+secret, `{"fencing_token":1,"result":{"ok":true}}`); status != 200 {
		t.Fatal(status)
	}
	task = enqueue(t, store, 2)
	request(t, server, "claim", "Bearer "+secret, "{}")
	if status, _ := request(t, server, "tasks/"+task.ID+"/fail", "Bearer "+secret, `{"fencing_token":1}`); status != 200 {
		t.Fatal(status)
	}
	tokens, err := store.ListTokens(ctx)
	if err != nil || tokens[0].LastSeen == nil {
		t.Fatal("last_seen", err)
	}
	if err := store.RevokeToken(ctx, "worker"); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(t, server, "claim", "Bearer "+secret, "{}"); status != 401 {
		t.Fatal(status)
	}
	var count int64
	if err := db.DB().Model(&Audit{}).Where("action LIKE 'http:%'").Count(&count).Error; err != nil || count != 12 {
		t.Fatal("audit", count, err)
	}
}

func TestCertificateConcurrentFirstStart(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	pins := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, pin, err := LoadCertificate(dir)
			if err != nil {
				t.Error(err)
				return
			}
			pins <- pin
		}()
	}
	wg.Wait()
	close(pins)
	first := ""
	for pin := range pins {
		if first == "" {
			first = pin
		}
		if pin != first {
			t.Fatal("concurrent starts advertised different pins")
		}
	}
}

func TestWorkerHTTPGuards(t *testing.T) {
	store := &apiStore{}
	server := httptest.NewTLSServer(NewHandler(store))
	defer server.Close()
	for _, method := range []string{"GET", "DELETE"} {
		r, _ := http.NewRequest(method, server.URL+"/worker/v1/claim", nil)
		r.Header.Set("Authorization", "Bearer valid")
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 405 {
			t.Fatal(resp.StatusCode)
		}
	}
	r, _ := http.NewRequest("POST", server.URL+"/worker/v1/claim", strings.NewReader("{}"))
	r.Header.Add("Authorization", "Bearer valid")
	r.Header.Add("Authorization", "Bearer valid")
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	if status, _ := request(t, server, "register", "Bearer valid", "{}"); status != 200 {
		t.Fatal(status)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.worker.ID != "worker" || !store.worker.RunUntagged || store.worker.MaxParallel != 1 {
		t.Fatalf("defaults: %+v", store.worker)
	}
}

func TestWorkerReportAllowsBoundedDevelopmentMetadata(t *testing.T) {
	s := httptest.NewServer(NewHandler(&apiStore{}))
	defer s.Close()
	body := `{"fencing_token":7,"result":{"development_events":"` + strings.Repeat("x", MaxRequestBytes) + `"}}`
	status, _ := request(t, s, "tasks/task/complete", "Bearer valid", body)
	if status != 200 {
		t.Fatalf("capped development metadata rejected: %d", status)
	}
	status, _ = request(t, s, "tasks/task/heartbeat", "Bearer valid", body)
	if status != 413 {
		t.Fatalf("heartbeat accepted oversized body: %d", status)
	}
}

type inspectAPIStore struct {
	apiStore
	task Task
}

func (s *inspectAPIStore) Task(context.Context, string) (*Task, error) { return &s.task, nil }

func TestWorkerHTTPInspectReceiptFencesAndRedacts(t *testing.T) {
	for _, scenario := range []string{"missing", "committed", "other_owner", "old_epoch"} {
		t.Run(scenario, func(t *testing.T) {
			store := &inspectAPIStore{task: Task{ID: "task", State: "leased", LeaseOwner: "worker", FencingToken: 7, Payload: json.RawMessage(`{"secret":"private payload"}`)}}
			if scenario == "committed" {
				store.task.Result = json.RawMessage(`{"secret":"private report"}`)
			}
			if scenario == "other_owner" {
				store.task.LeaseOwner = "another"
			}
			fence := `{"fencing_token":7}`
			if scenario == "old_epoch" {
				fence = `{"fencing_token":6}`
			}
			server := httptest.NewTLSServer(NewHandler(store))
			defer server.Close()
			status, body := request(t, server, "tasks/task/inspect", "Bearer valid", fence)
			if scenario == "other_owner" || scenario == "old_epoch" {
				if status != 409 {
					t.Fatal(status, string(body))
				}
				return
			}
			if status != 200 || strings.Contains(string(body), "secret") || strings.Contains(string(body), "private") {
				t.Fatal(status, string(body))
			}
			var receipt struct {
				State     string `json:"state"`
				Committed bool   `json:"report_committed"`
			}
			if err := json.Unmarshal(body, &receipt); err != nil || receipt.State != "leased" || receipt.Committed != (scenario == "committed") {
				t.Fatal(receipt, err)
			}
			status, _ = request(t, server, "tasks/task/inspect", "Bearer unknown", fence)
			if status != 401 {
				t.Fatal("unauthenticated inspection", status)
			}
		})
	}
}

func TestWorkerHTTPInspectPostgres(t *testing.T) {
	store, _, _ := setup(t)
	secret, err := store.CreateToken(ctx, "worker")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store))
	defer server.Close()
	request(t, server, "register", "Bearer "+secret, `{}`)
	task := enqueue(t, store, 1)
	request(t, server, "claim", "Bearer "+secret, `{}`)
	check := func(committed bool) {
		status, body := request(t, server, "tasks/"+task.ID+"/inspect", "Bearer "+secret, `{"fencing_token":1}`)
		var value struct {
			Committed bool `json:"report_committed"`
		}
		if status != 200 || json.Unmarshal(body, &value) != nil || value.Committed != committed {
			t.Fatal(status, string(body))
		}
	}
	check(false)
	status, body := request(t, server, "tasks/"+task.ID+"/complete", "Bearer "+secret, `{"fencing_token":1,"result":{"ok":true}}`)
	if status != 200 {
		t.Fatal(status, string(body))
	}
	check(true)
}

func (s *apiStore) Release(c context.Context, id, w string, f int64, reason string) error {
	return s.Heartbeat(c, id, w, f)
}
