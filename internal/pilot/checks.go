package pilot

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// ScheduleDefinition deliberately excludes runtime state and ownership.
type ScheduleDefinition struct {
	Cron           string `json:"cron"`
	Timezone       string `json:"timezone"`
	Prompt         string `json:"prompt"`
	Ops            int    `json:"ops"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Enabled        bool   `json:"enabled"`
}

func (d ScheduleDefinition) check(id, owner string) store.ScheduledCheck {
	return store.ScheduledCheck{ID: id, Owner: owner, Cron: d.Cron, Timezone: d.Timezone, Prompt: d.Prompt, Ops: d.Ops, TimeoutSeconds: d.TimeoutSeconds, Enabled: d.Enabled}
}

// TickChecks shares the conversation invocation and audit path. The caller owns
// the store until all launched invocations finish.
func TickChecks(ctx context.Context, s *store.Store, now time.Time) error {
	claimCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	runs, err := s.ClaimScheduledChecks(claimCtx, now)
	cancel()
	if err != nil {
		return err
	}
	for _, run := range runs {
		go runScheduledCheck(ctx, s, run, Ask)
	}
	return nil
}

func runScheduledCheck(ctx context.Context, s *store.Store, r store.ScheduledCheckRun, invoke func(context.Context, string, int, time.Duration) (StewardResult, error)) {
	j := r.Check
	result, err := invoke(ctx, j.Prompt, j.Ops, time.Duration(j.TimeoutSeconds)*time.Second)
	status, summary := "success", result.Answer
	if err != nil {
		status = "failed"
		summary = err.Error()
	}
	actions, _ := json.Marshal(result.Actions)
	usage, _ := json.Marshal(result.Usage)
	saveCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if saveErr := s.FinishScheduledCheck(saveCtx, r.ID, status, summary, actions, usage); saveErr != nil {
		// No prompt, result or credentials in scheduler diagnostics.
		fmt.Printf("[scheduled checks] unable to record run %s\n", r.ID)
	}
}

func (e ActionExecutor) executeSchedule(a Action) error {
	ctx := e.Context
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := store.EnsureReady(ctx, e.Config)
	if err != nil {
		return err
	}
	defer s.Close()
	if a.Type == "schedule_remove" {
		return s.RemoveScheduledCheck(ctx, a.ScheduleID)
	}
	if a.Type == "schedule_add" {
		j := a.Schedule.check(a.ScheduleID, e.Actor)
		return s.SaveScheduledCheck(ctx, &j, true)
	}
	old, err := s.ScheduledCheck(ctx, a.ScheduleID)
	if err != nil {
		return err
	}
	j := a.Schedule.check(old.ID, old.Owner)
	return s.SaveScheduledCheck(ctx, &j, false)
}

// OpenCheckStore keeps database setup identical for CLI, web and steward actions.
func OpenCheckStore(ctx context.Context) (*store.Store, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return store.EnsureReady(ctx, cfg)
}
