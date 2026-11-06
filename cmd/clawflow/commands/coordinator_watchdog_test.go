package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// An older controller answers 404 for progress. That must never affect the task,
// unlike a failed heartbeat, which cancels it.
func TestWorkerProgressRejectionDoesNotAffectTask(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	var progressCalls atomic.Int32
	var mu sync.Mutex
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/progress") {
			mu.Lock()
			defer mu.Unlock()
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			progressCalls.Add(1)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	old := runOneOperatorFn
	defer func() { runOneOperatorFn = old }()
	ran := false
	runOneOperatorFn = func(ctx context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		if execution.ProgressFrom(ctx) == nil {
			t.Error("execution context has no progress tracker")
		}
		deadline := time.Now().Add(3 * time.Second)
		for progressCalls.Load() < 3 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if progressCalls.Load() < 3 {
			t.Error("worker stopped reporting after a rejected progress report")
		}
		if ctx.Err() != nil {
			t.Error("rejected progress cancelled the task")
		}
		ran = true
		j.hooks.AfterRun(&jobResult{Status: "success"})
		return true, false, false
	}
	task := coordinator.Task{ID: "task", Repo: "o/r", Issue: 1, Kind: "implement", FencingToken: 7}
	api := &workerAPI{base: server.URL, client: server.Client()}
	err := runWorkerTask(context.Background(), api, workerOptions{RunUntagged: true, Roles: []string{"operator"}}, &task, 5*time.Millisecond, func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) {
		return &runJob{repo: "o/r", sub: &operator.Subject{Number: 1}, op: &operator.Operator{Name: "implement"}}, time.Minute, nil
	})
	if err != nil || !ran {
		t.Fatalf("err=%v ran=%v", err, ran)
	}
	mu.Lock()
	defer mu.Unlock()
	if body["fencing_token"] != float64(7) || body["last_event_age_sec"] != float64(0) || len(body) != 2 {
		t.Fatalf("progress body: %v", body)
	}
}

func TestCoordinatorWatchdogReportsTransitionsAndSurvivesErrors(t *testing.T) {
	finding := coordinator.Finding{TaskID: "t", Rule: coordinator.RuleNoProgress}
	boards := []func() (coordinator.Board, error){
		func() (coordinator.Board, error) { return coordinator.Board{}, errors.New("database unavailable") },
		func() (coordinator.Board, error) {
			return coordinator.Board{Findings: []coordinator.Finding{finding}}, nil
		},
		func() (coordinator.Board, error) {
			return coordinator.Board{Findings: []coordinator.Finding{finding}}, nil
		},
		func() (coordinator.Board, error) { return coordinator.Board{}, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	events, failures, calls := []string{}, 0, 0
	board := func(context.Context) (coordinator.Board, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls >= len(boards) {
			cancel()
			return coordinator.Board{}, nil
		}
		calls++
		return boards[calls-1]()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCoordinatorWatchdog(ctx, board, time.Millisecond, func(event string, f coordinator.Finding) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, event+":"+f.TaskID)
		}, func(error) {
			mu.Lock()
			defer mu.Unlock()
			failures++
		})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchdog did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if failures != 1 || len(events) != 2 || events[0] != "open:t" || events[1] != "resolved:t" {
		t.Fatalf("failures=%d events=%v", failures, events)
	}
}

func TestWatchdogPassFailureReportsElapsedAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	boardErr := context.DeadlineExceeded
	failed := make(chan error, 1)
	go runCoordinatorWatchdog(ctx, func(context.Context) (coordinator.Board, error) {
		return coordinator.Board{}, boardErr
	}, time.Hour, func(string, coordinator.Finding) {}, func(err error) {
		select {
		case failed <- err:
		default:
		}
	})
	select {
	case err := <-failed:
		if !errors.Is(err, boardErr) {
			t.Fatalf("errors.Is lost the original error: %v", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "after ") || !strings.Contains(msg, "timeout "+watchdogPassTimeout.String()) {
			t.Fatalf("error lacks elapsed time or timeout: %q", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fail was not called")
	}
}
