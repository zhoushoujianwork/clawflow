package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
