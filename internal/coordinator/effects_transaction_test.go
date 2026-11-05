package coordinator

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func sealForApplication(t *testing.T, s *Store, worker string, issue int, report json.RawMessage) *Task {
	t.Helper()
	register(t, s, worker, 8)
	enqueue(t, s, issue)
	task := claim(t, s, worker)
	if err := s.AcceptResult(ctx, task.ID, worker, task.FencingToken, report); err != nil {
		t.Fatal(err)
	}
	return task
}

func runMeta(repo string) []byte {
	return []byte(`{"repo":"` + repo + `","operator":"implement","status":"success","started_at":"2026-11-05T00:00:00Z","profile":"p","backend":"b","machine":"m","model":"model"}`)
}

func commentEffectReport(issue int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"status":  "success",
		"effects": []EffectRequest{{Kind: "issue-comment", Repo: "owner/repo", Number: issue, Body: "hello"}},
	})
	return raw
}

func TestFinalizeApplicationAllowsSecondConnectionWrite(t *testing.T) {
	s, db, _ := setup(t)
	task := sealForApplication(t, s, "w", 1, json.RawMessage(`{"status":"success"}`))
	start := time.Now()
	err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error {
		_, err := db.UpsertRun(ctx, filepath.Join(t.TempDir(), "run"), "run", runMeta("owner/repo"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("nested write took %v", elapsed)
	}
}

func TestApplyEffectsHeadMismatchAllowsSecondConnectionWrite(t *testing.T) {
	s, db, _ := setup(t)
	task := sealForApplication(t, s, "w", 1, effectReport("pr-create"))
	start := time.Now()
	err := s.ApplyEffects(ctx, task.ID, task.FencingToken, func(EffectIntent) (EffectResult, error) {
		_, err := db.UpsertRun(ctx, filepath.Join(t.TempDir(), "run"), "run", runMeta("owner/repo"))
		return EffectResult{PR: 7, HeadMismatch: true}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("head-mismatch nested write took %v", elapsed)
	}
	current, err := s.Task(ctx, task.ID)
	if err != nil || current.State != "head-mismatch" || current.PR != 7 {
		t.Fatalf("task=%+v err=%v", current, err)
	}
}

func TestEnqueuePreparationAndLeaseLimitsAllowSecondConnectionWrites(t *testing.T) {
	s, db, _ := setup(t)
	var preparedID string
	start := time.Now()
	_, err := s.EnqueuePrepared(ctx, Task{Repo: "owner/repo", Issue: 1, Kind: "implement", Operator: "implement"}, func(task *Task) error {
		preparedID = task.ID
		_, err := db.UpsertRun(ctx, filepath.Join(t.TempDir(), "prepare"), "run", runMeta("prepare/repo"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if preparedID == "" || time.Since(start) >= time.Second {
		t.Fatalf("preparation blocked or missing id: %q %v", preparedID, time.Since(start))
	}
	register(t, s, "w", 1)
	s.LeaseLimits = func() (int, map[string]int, error) {
		_, err := db.UpsertRun(ctx, filepath.Join(t.TempDir(), "limits"), "run", runMeta("limits/repo"))
		return 4, map[string]int{"owner/repo": 1}, err
	}
	if _, err := s.Claim(ctx, "w"); err != nil {
		t.Fatal(err)
	}
}

func TestBoardDeferredReadDoesNotBlockWriter(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task := claim(t, s, "w")
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- deferredReadTransaction(s.db.WithContext(ctx), func(*gorm.DB) error {
			close(started)
			time.Sleep(2 * time.Second)
			return nil
		})
	}()
	<-started
	start := time.Now()
	if err := s.Heartbeat(ctx, task.ID, "w", task.FencingToken); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("board read blocked writer for %v", elapsed)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestApplicationCallbacksDoNotBlockOtherWriters(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Store, *Task, func()) error
	}{
		{"apply", func(s *Store, task *Task, block func()) error {
			return s.ApplyEffects(ctx, task.ID, task.FencingToken, func(EffectIntent) (EffectResult, error) {
				block()
				return EffectResult{}, nil
			})
		}},
		{"finalize", func(s *Store, task *Task, block func()) error {
			return s.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error {
				block()
				return nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := setup(t)
			report := json.RawMessage(`{"status":"success"}`)
			if tc.name == "apply" {
				report = effectReport("issue-comment")
			}
			application := sealForApplication(t, s, "app", 1, report)
			enqueue(t, s, 2)
			heartbeat := claim(t, s, "app")
			enqueue(t, s, 3)
			logTask := claim(t, s, "app")
			enqueue(t, s, 4)
			register(t, s, "claimer", 1)

			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- tc.run(s, application, func() {
					close(started)
					time.Sleep(2 * time.Second)
				})
			}()
			<-started

			assertPrompt := func(name string, fn func() error) {
				t.Helper()
				start := time.Now()
				if err := fn(); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if elapsed := time.Since(start); elapsed >= time.Second {
					t.Fatalf("%s blocked for %v", name, elapsed)
				}
			}
			assertPrompt("heartbeat", func() error { return s.Heartbeat(ctx, heartbeat.ID, "app", heartbeat.FencingToken) })
			assertPrompt("claim", func() error { _, err := s.Claim(ctx, "claimer"); return err })
			assertPrompt("log append", func() error {
				_, err := s.AppendLog(ctx, logTask.ID, "app", page(logTask.FencingToken, 0, 1, logEvent(1, "message", "live")))
				return err
			})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCancelDuringApplyStopsRemainingIntents(t *testing.T) {
	s, _, _ := setup(t)
	report := json.RawMessage(`{"status":"success","effects":[{"kind":"issue-comment","repo":"owner/repo","number":1,"body":"one"},{"kind":"add-label","repo":"owner/repo","number":1,"labels":["done"]}]}`)
	task := sealForApplication(t, s, "w", 1, report)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- s.ApplyEffects(ctx, task.ID, task.FencingToken, func(EffectIntent) (EffectResult, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return EffectResult{}, nil
		})
	}()
	<-started
	if err := s.Cancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrStaleLease) {
		t.Fatalf("apply error=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCancelDuringFinalizeDoesNotCompleteTask(t *testing.T) {
	s, _, _ := setup(t)
	task := sealForApplication(t, s, "w", 1, json.RawMessage(`{"status":"success"}`))
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	if err := s.Cancel(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrStaleLease) {
		t.Fatalf("finalize error=%v", err)
	}
	current, _ := s.Task(ctx, task.ID)
	if current.State != "cancelled" {
		t.Fatalf("state=%s", current.State)
	}
}

func TestFinalizeCrashReplayKeepsOneRun(t *testing.T) {
	s, db, _ := setup(t)
	task := sealForApplication(t, s, "w", 1, json.RawMessage(`{"status":"success"}`))
	dir := filepath.Join(t.TempDir(), "run")
	crash := errors.New("simulated crash")
	err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error {
		if _, err := db.UpsertRun(ctx, dir, "run", runMeta("owner/repo")); err != nil {
			return err
		}
		return crash
	})
	if !errors.Is(err, crash) {
		t.Fatalf("first finalize=%v", err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, false, func(Task) error {
		_, err := db.UpsertRun(ctx, dir, "run", runMeta("owner/repo"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.DB().Table("runs").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("runs=%d err=%v", count, err)
	}
}

func TestApplicationKeyedMutexSerializesSameTaskOnly(t *testing.T) {
	s, _, _ := setup(t)
	report := json.RawMessage(`{"status":"success","effects":[{"kind":"issue-comment","repo":"owner/repo","number":1,"body":"one"},{"kind":"add-label","repo":"owner/repo","number":1,"labels":["done"]}]}`)
	task := sealForApplication(t, s, "w", 1, report)
	counts := map[string]int{}
	var mu sync.Mutex
	apply := func(intent EffectIntent) (EffectResult, error) {
		mu.Lock()
		counts[intent.ID]++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return EffectResult{}, nil
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			errs <- s.ApplyEffects(ctx, task.ID, task.FencingToken, apply)
		}()
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for id, count := range counts {
		if count != 1 {
			t.Fatalf("intent %s applied %d times", id, count)
		}
	}
	if len(counts) != 2 {
		t.Fatalf("intent counts=%v", counts)
	}

	other := sealForApplication(t, s, "w", 2, commentEffectReport(2))
	third := sealForApplication(t, s, "w", 3, commentEffectReport(3))
	started := make(chan string, 2)
	release := make(chan struct{})
	for _, current := range []*Task{other, third} {
		go func(task *Task) {
			errs <- s.ApplyEffects(ctx, task.ID, task.FencingToken, func(EffectIntent) (EffectResult, error) {
				started <- task.ID
				<-release
				return EffectResult{}, nil
			})
		}(current)
	}
	seen := map[string]bool{<-started: true, <-started: true}
	close(release)
	if !seen[other.ID] || !seen[third.ID] {
		t.Fatalf("different tasks did not run together: %v", seen)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
