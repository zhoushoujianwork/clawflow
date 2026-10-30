package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

func TestWorkerReconcileExpiredUnstartedClaim(t *testing.T) {
	for _, scenario := range []string{"new", "prepared", "acknowledged_retry", "checkpoint", "running", "resume", "executor_lock", "session_lock", "stale_owner", "lost_response"} {
		t.Run(scenario, func(t *testing.T) {
			o, task, path := workerClaimFixture(t)
			until := time.Now().Add(-time.Minute)
			task.LeaseExpiresAt = &until
			j, _ := readWorkerJournal(path)
			switch scenario {
			case "new", "stale_owner", "lost_response":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "prepared", "checkpoint", "resume":
				j.Task = *task
				j.State = "prepared"
				j.Report = nil
				j.Resume = scenario == "resume"
				if err := writeWorkerJournal(path, j); err != nil {
					t.Fatal(err)
				}
				if scenario == "checkpoint" {
					if err := os.WriteFile(filepath.Join(filepath.Dir(path), "execution.json"), []byte(`{"session":"retained"}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "running":
				j.State = "running"
				if err := writeWorkerJournal(path, j); err != nil {
					t.Fatal(err)
				}
			case "executor_lock", "session_lock":
				lockPath := path + ".lock"
				if scenario == "session_lock" {
					lockPath = filepath.Join(filepath.Dir(path), "execution.json.lock")
				}
				lock, err := filelock.Try(lockPath, map[string]any{"PID": os.Getpid()})
				if err != nil {
					t.Fatal(err)
				}
				defer filelock.Release(lock)
			}
			before, _ := os.ReadFile(path)
			reports := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/inspect") {
					if scenario == "stale_owner" {
						w.WriteHeader(409)
						return
					}
					_, _ = w.Write([]byte(`{"state":"leased","report_committed":false}`))
					return
				}
				reports++
				var report struct {
					FencingToken int64 `json:"fencing_token"`
					Result       struct {
						Status    string `json:"status"`
						InfraKind string `json:"infra_kind"`
					} `json:"result"`
				}
				if err := json.NewDecoder(r.Body).Decode(&report); err != nil || report.FencingToken != task.FencingToken || report.Result.Status != "failed" || report.Result.InfraKind != "launch" {
					t.Error("invalid failure report", report, err)
				}
				if scenario == "lost_response" && reports == 1 {
					w.WriteHeader(503)
				}
			}))
			defer server.Close()
			api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
			err := reconcileWorkerClaim(context.Background(), api, o, task)
			blocked := scenario == "checkpoint" || scenario == "running" || scenario == "resume" || strings.HasSuffix(scenario, "lock") || scenario == "stale_owner"
			if blocked {
				after, _ := os.ReadFile(path)
				if err == nil || reports != 0 || string(before) != string(after) {
					t.Fatal("unsafe claim changed", err, reports)
				}
				return
			}
			if scenario == "lost_response" {
				if err == nil {
					t.Fatal("missing upload failure")
				}
				pending, _ := readWorkerJournal(path)
				if pending.State != "pending_sync" {
					t.Fatal(pending)
				}
				err = reconcileWorkerClaim(context.Background(), api, o, task)
			}
			if err != nil {
				t.Fatal(err)
			}
			current, err := readWorkerJournal(path)
			if err != nil || current.State != "acknowledged" || current.Task.FencingToken != task.FencingToken {
				t.Fatal(current, err)
			}
			if scenario == "acknowledged_retry" {
				archived, err := readWorkerJournal(filepath.Join(workerJournalRoot(o), "history", task.ID, "epoch-2", "journal.json"))
				if err != nil || archived.State != "acknowledged" {
					t.Fatal("lost archive", err)
				}
			}
		})
	}
}

func TestWorkerReconcileExactAcknowledgedReport(t *testing.T) {
	for _, scenario := range []string{"missing", "committed", "superseded", "wrong_report_epoch", "lost_response", "executor_lock", "session_lock"} {
		t.Run(scenario, func(t *testing.T) {
			o, _, path := workerClaimFixture(t)
			j, _ := readWorkerJournal(path)
			raw := json.RawMessage(`{"fencing_token":2,"result":{"status":"failed","infra_kind":"launch","error":"retained evidence"}}`)
			if scenario == "wrong_report_epoch" {
				raw = json.RawMessage(`{"fencing_token":1,"result":{"status":"success"}}`)
			}
			j.Report = raw
			if err := writeWorkerJournal(path, j); err != nil {
				t.Fatal(err)
			}
			if scenario == "executor_lock" || scenario == "session_lock" {
				lockPath := path + ".lock"
				if scenario == "session_lock" {
					lockPath = filepath.Join(filepath.Dir(path), "execution.json.lock")
				}
				lock, err := filelock.Try(lockPath, map[string]any{"PID": os.Getpid()})
				if err != nil {
					t.Fatal(err)
				}
				defer filelock.Release(lock)
			}
			uploads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/inspect") {
					if scenario == "superseded" {
						w.WriteHeader(409)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"state": "leased", "report_committed": scenario == "committed"})
					return
				}
				uploads++
				body, _ := io.ReadAll(r.Body)
				if string(body) != string(raw) {
					t.Error("report changed", string(body))
				}
				if scenario == "lost_response" {
					w.WriteHeader(503)
				}
			}))
			defer server.Close()
			api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
			err := reconcileWorkerReceipt(context.Background(), api, o, path)
			if (scenario == "wrong_report_epoch" || scenario == "lost_response") != (err != nil) {
				t.Fatal("unexpected error", err)
			}
			expected := 0
			if scenario == "missing" || scenario == "lost_response" {
				expected = 1
			}
			if uploads != expected {
				t.Fatal("unexpected replay", uploads)
			}
			current, _ := readWorkerJournal(path)
			if string(current.Report) != string(raw) {
				t.Fatal("report discarded")
			}
			if scenario == "lost_response" && current.State != "pending_sync" {
				t.Fatal("retry not retained", current)
			}
		})
	}
}

func TestWorkerReconcileIsolatesCorruptClaimAndJournal(t *testing.T) {
	o, _, path := workerClaimFixture(t)
	healthy, _ := readWorkerJournal(path)
	healthy.Task.ID = "healthy"
	healthy.State = "running"
	healthyPath := filepath.Join(workerJournalRoot(o), "healthy", "journal.json")
	if err := writeWorkerJournal(healthyPath, healthy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken journal"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(workerJournalRoot(o), "claims", "broken.json")
	if err := writeDurableFile(bad, []byte("broken claim"), 0600); err != nil {
		t.Fatal(err)
	}
	good, _, err := prepareWorkerClaim(o)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"task":null}`)) }))
	defer server.Close()
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	if err := recoverWorkerClaims(context.Background(), api, o); err != nil {
		t.Fatal(err)
	}
	if err := recoverWorkerExecutors(o); err != nil {
		t.Fatal(err)
	}
	recovered, err := readWorkerJournal(healthyPath)
	if err != nil || recovered.State != "needs_recovery" {
		t.Fatal("healthy journal blocked", recovered, err)
	}
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatal("healthy claim blocked", err)
	}
	if raw, _ := os.ReadFile(bad); string(raw) != "broken claim" {
		t.Fatal("corrupt evidence overwritten")
	}
	diagnostics, _ := filepath.Glob(filepath.Join(workerJournalRoot(o), "recovery", "*.json"))
	if len(diagnostics) != 2 {
		t.Fatal("missing per-item diagnostics", diagnostics)
	}
	if err := writeDurableFile(filepath.Join(workerJournalRoot(o), "recovery", "broken.json"), []byte("broken diagnostic"), 0600); err != nil {
		t.Fatal(err)
	}
	commands := newWorkerRecoveryCommands()
	var out bytes.Buffer
	commands[1].SetOut(&out)
	commands[1].SetArgs([]string{"--root", workerJournalRoot(o)})
	if err := commands[1].Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "recovery_blocked") || !strings.Contains(out.String(), "broken.json") {
		t.Fatal("blocked status not exposed", out.String())
	}
}

func TestDurableWorkerKeepsPollingAfterBlockedClaim(t *testing.T) {
	o, task, path := workerClaimFixture(t)
	j, _ := readWorkerJournal(path)
	j.State = "needs_recovery"
	if err := writeWorkerJournal(path, j); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			if claims.Add(1) > 3 {
				cancel()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"task": task})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	if err := runDurableWorker(ctx, api, o, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if claims.Load() < 4 {
		t.Fatal("manager stopped after blocked claim", claims.Load())
	}
	retained, _ := readWorkerJournal(path)
	if retained.State != "needs_recovery" {
		t.Fatal("execution replaced", retained)
	}
}

func TestWorkerReconcileCachesOnlyVerifiedUnchangedReceipts(t *testing.T) {
	o, _, path := workerClaimFixture(t)
	o.receiptCache = make(map[string][32]byte)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"state":"done","report_committed":true}`))
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	for i := 0; i < 2; i++ {
		if err := reconcileWorkerReceipt(context.Background(), api, o, path); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("unchanged receipt inspected repeatedly", calls)
	}
	j, _ := readWorkerJournal(path)
	j.Task.FencingToken++
	if err := writeWorkerJournal(path, j); err != nil {
		t.Fatal(err)
	}
	if err := reconcileWorkerReceipt(context.Background(), api, o, path); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("changed epoch skipped", calls)
	}
	o.receiptCache = make(map[string][32]byte)
	if err := reconcileWorkerReceipt(context.Background(), api, o, path); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("new manager skipped controller reconciliation", calls)
	}
}
