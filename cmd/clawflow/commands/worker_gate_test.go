package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func blockedGate() *workerGate {
	return &workerGate{name: "w", cache: newReadinessCache(func(context.Context) execution.Readiness {
		return execution.Readiness{State: execution.StateBlocked, Reason: execution.ReasonMissing, Summary: "missing"}
	})}
}

func TestBlockedWorkerNeverClaimsButStaysRegistered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	var claims, registers atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/worker/v1/claim":
			claims.Add(1)
		case "/worker/v1/register":
			registers.Add(1)
		}
		_, _ = io.WriteString(w, `{"task":null}`)
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, client: server.Client()}
	if err := runWorker(ctx, api, workerOptions{MaxParallel: 1, Gate: blockedGate()}, time.Second, 10*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if claims.Load() != 0 || registers.Load() < 2 {
		t.Fatalf("claims=%d registers=%d", claims.Load(), registers.Load())
	}
}

// Shutdown while a keep-visible re-registration is in flight must end the
// worker cleanly, not surface the cancelled request as a coordinator failure.
func TestBlockedWorkerShutdownDuringReregistrationIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var registers atomic.Int32
	inflight := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker/v1/register" && registers.Add(1) > 1 {
			_, _ = io.Copy(io.Discard, r.Body) // reading the body lets the server notice a closed connection
			close(inflight)
			<-r.Context().Done() // hang until the worker cancels the request
			return
		}
		_, _ = io.WriteString(w, `{"task":null}`)
	}))
	defer server.Close()
	go func() {
		<-inflight
		cancel()
	}()
	api := &workerAPI{base: server.URL, client: server.Client()}
	if err := runWorker(ctx, api, workerOptions{MaxParallel: 1, Gate: blockedGate()}, time.Second, 10*time.Millisecond, nil); err != nil {
		t.Fatalf("shutdown during re-registration returned %v", err)
	}
}

func TestHealthyGateStillClaims(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var claims atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker/v1/claim" {
			claims.Add(1)
			cancel()
		}
		_, _ = io.WriteString(w, `{"task":null}`)
	}))
	defer server.Close()
	gate := &workerGate{name: "w", cache: newReadinessCache(func(context.Context) execution.Readiness {
		return execution.Readiness{State: execution.StateReady}
	})}
	api := &workerAPI{base: server.URL, client: server.Client()}
	if err := runWorker(ctx, api, workerOptions{MaxParallel: 1, Gate: gate}, time.Second, 10*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if claims.Load() != 1 {
		t.Fatalf("claims=%d", claims.Load())
	}
}

func TestClaimedTaskIsReleasedWithoutResultWhenOffloadLost(t *testing.T) {
	var paths []string
	var bodies []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(b))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, client: server.Client()}
	var built atomic.Bool
	task := &coordinator.Task{ID: "t1", FencingToken: 5, Kind: "implement", Operator: "implement", Tags: []string{"github"}, Payload: json.RawMessage(`{}`)}
	err := runWorkerTask(context.Background(), api, workerOptions{Tags: []string{"github"}, Roles: []string{"operator"}, Gate: blockedGate()}, task, time.Hour,
		func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) {
			built.Store(true)
			return &runJob{}, time.Minute, nil
		})
	if err != nil || built.Load() {
		t.Fatalf("err=%v built=%v", err, built.Load())
	}
	if len(paths) != 1 || paths[0] != "/worker/v1/tasks/t1/release" {
		t.Fatalf("paths=%v", paths)
	}
	// No result is reported, so no comment, label or failure can be produced.
	if bodies[0] != `{"fencing_token":5}`+"\n" && bodies[0] != `{"fencing_token":5}` {
		t.Fatalf("body=%q", bodies[0])
	}
}

func TestDurableGateIsRebuiltAndReleaseSurvivesRestart(t *testing.T) {
	for _, status := range []int{200, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			o := workerOptions{StateDir: t.TempDir(), Executable: "/fixture/clawflow", Roles: []string{"operator"}, Durable: true}
			t.Setenv("CLAWFLOW_STATE_DIR", o.StateDir)
			until := time.Now().Add(time.Hour)
			task := &coordinator.Task{ID: "task", State: "leased", LeaseOwner: "worker", FencingToken: 4, LeaseExpiresAt: &until, Payload: json.RawMessage(`{"durable_execution":true}`)}
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/worker/v1/tasks/task/release" {
					t.Error("unexpected task execution", r.URL.Path)
				}
				calls.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			token := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(token, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			o.Coordinator = server.URL
			o.TokenFile = token
			o.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(server.Certificate().Raw))
			o.Gate = blockedGate()
			path := filepath.Join(workerJournalRoot(o), task.ID, "journal.json")
			j := &workerJournal{Version: 1, Task: *task, Options: o, Executable: o.Executable, State: "prepared"}
			if err := writeWorkerJournal(path, j); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			if strings.Contains(string(raw), `"Gate"`) {
				t.Fatal("process-local gate persisted")
			}
			old := workerGateFactory
			defer func() { workerGateFactory = old }()
			workerGateFactory = func(string, []string) *workerGate { return blockedGate() }
			command := newWorkerExecuteCmd()
			command.SetArgs([]string{"--journal", path})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			released, err := readWorkerJournal(path)
			if err != nil || released.State != "released" || released.Action != "release" || calls.Load() != 1 {
				t.Fatal("release not retained", released, calls.Load(), err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "execution.json")); !os.IsNotExist(err) {
				t.Fatal("unavailable runtime started execution", err)
			}
			// Simulate losing the HTTP reply before persisting the local receipt.
			released.State = "pending_release"
			if err := writeWorkerJournal(path, released); err != nil {
				t.Fatal(err)
			}
			command = newWorkerExecuteCmd()
			command.SetArgs([]string{"--journal", path})
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			final, _ := readWorkerJournal(path)
			if final.State != "released" || calls.Load() != 2 {
				t.Fatal("release replay failed", final, calls.Load())
			}
			for _, mode := range []string{"resume", "checkpoint"} {
				final.State = "prepared"
				final.Resume = mode == "resume"
				if mode == "checkpoint" {
					if err := os.WriteFile(filepath.Join(filepath.Dir(path), "execution.json"), []byte(`{}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := writeWorkerJournal(path, final); err != nil {
					t.Fatal(err)
				}
				command = newWorkerExecuteCmd()
				command.SetArgs([]string{"--journal", path})
				if err := command.Execute(); err == nil {
					t.Fatal("retained session released without runtime", mode)
				}
				retained, _ := readWorkerJournal(path)
				if retained.State != "prepared" || calls.Load() != 2 {
					t.Fatal("retained session changed", mode, retained, calls.Load())
				}
			}
		})
	}
}
