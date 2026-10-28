package pilot

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestStalledPass(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	h := &snapshot.RunLockInfo{PID: 1, StartedAt: now.Add(-time.Hour)}
	for _, tt := range []struct {
		name   string
		holder *snapshot.RunLockInfo
		last   time.Time
		after  time.Duration
		want   bool
	}{
		{"stalled fetch", h, now.Add(-31 * time.Minute), 0, true},
		{"second precision start log", &snapshot.RunLockInfo{PID: 1, StartedAt: now.Add(-time.Hour).Add(time.Millisecond)}, now.Add(-time.Hour), 0, true},
		{"boundary", h, now.Add(-30 * time.Minute), 0, false},
		{"recent progress", h, now.Add(-time.Minute), 0, false},
		{"dead or unlocked", nil, now.Add(-time.Hour), 0, false},
		{"unknown holder", &snapshot.RunLockInfo{}, now.Add(-time.Hour), 0, false},
		{"no evidence", h, time.Time{}, 0, false},
		{"previous pass", h, now.Add(-2 * time.Hour), 0, false},
		{"custom threshold", h, now.Add(-10 * time.Minute), 5 * time.Minute, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := StalledPass(tt.holder, tt.last, now, tt.after); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestAttentionEventContracts(t *testing.T) {
	for _, tt := range []struct{ line, kind string }{
		{`run/end repo=o/r issue=20 outcome=agent-failed applied_label=agent-failed`, "agent-failed"},
		{`run/end repo=o/r issue=20 outcome=agent-failed applied_label=""`, ""},
		{`run/end repo=o/r issue=15 applied_label=agent-skipped`, "agent-skipped"},
		{`run/circuit_breaker repo=o/r issue=15 label=agent-failed`, "agent-failed"},
		{`run/harvest_end repo=o/r issue=15 verdict=blocked`, "acceptance-blocked"},
		{`run/harvest_end repo=o/r issue=15 verdict=failed`, "acceptance-failed"},
		{`run/harvest_end repo=o/r issue=15 verdict=passed`, ""},
		{`run/acceptance_repair_skip repo=o/r issue=15 reason=budget_exhausted`, "repair-exhausted"},
		{`run/ci_repair_skip repo=o/r pr=16 err="repair exhausted: 3 total attempts"`, "repair-exhausted"},
		{`run/ci_repair_skip repo=o/r pr=16 err="repair cooldown"`, ""},
	} {
		e, ok := pipeline.ParseLine("2026-10-28T12:00:00Z INFO " + tt.line)
		if !ok || attentionEvent(e) != tt.kind {
			t.Errorf("%s: %q want %q", tt.line, attentionEvent(e), tt.kind)
		}
	}
}

func TestAttentionDebounce(t *testing.T) {
	now := time.Now().UTC()
	a := wakeAttention{"o/r", "20", "agent-failed", now}
	state := wakeState{a.key(): {Source: now, Woken: now}}
	if eligibleAttention(a, state, now.Add(time.Hour)) {
		t.Fatal("unchanged event replayed")
	}
	a.At = now.Add(time.Minute)
	if eligibleAttention(a, state, now.Add(time.Minute)) {
		t.Fatal("cooldown ignored")
	}
	if !eligibleAttention(a, state, now.Add(30*time.Minute)) {
		t.Fatal("new event never retried")
	}
	a.Issue = "21"
	if !eligibleAttention(a, state, now.Add(time.Minute)) {
		t.Fatal("different issue suppressed")
	}
	a.Issue = "20"
	a.Kind = "acceptance-failed"
	if !eligibleAttention(a, state, now.Add(time.Minute)) {
		t.Fatal("different kind suppressed")
	}
}

func TestCollectWakeEvidenceAndReplay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, err := project.Create("events")
	if err != nil {
		t.Fatal(err)
	}
	p.Repos = []string{"o/r"}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(clog.LogsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	log := `2026-10-28T11:00:00Z INFO run/end repo=o/r issue=20 applied_label=agent-skipped token=secret
2026-10-28T11:01:00Z INFO run/harvest_end repo=other/r issue=1 verdict=failed
2026-10-28T11:02:00Z INFO run/start only_repo=o/r
`
	if err := os.WriteFile(filepath.Join(clog.LogsDir(), "run.log"), []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	board := pipeline.Snapshot{GeneratedAt: now.Format(time.RFC3339), Repos: []pipeline.RepoBoard{
		{Repo: "o/r", Items: []pipeline.Item{{Issue: 20, Acceptance: &pipeline.AcceptanceInfo{Verdict: "blocked", Summary: "spec gap"}}}},
		{Repo: "other/r"},
	}, GlobalQueue: []pipeline.GlobalQueueEntry{{Repo: "o/r", Number: 20}, {Repo: "other/r", Number: 1}}}
	if err := snapshot.WritePipeline(board); err != nil {
		t.Fatal(err)
	}
	ev, state, err := collectWakeEvidence(p, &config.Config{}, now)
	if err != nil || len(ev.Attention) != 1 || len(ev.Events) != 2 {
		t.Fatalf("%+v %v", ev, err)
	}
	if len(ev.Board.Repos) != 1 || len(ev.Board.GlobalQueue) != 1 || ev.Board.Repos[0].Items[0].Acceptance.Summary != "spec gap" {
		t.Fatalf("board not scoped: %+v", ev.Board)
	}
	if strings.Contains(evidencePrompt(ev), "secret") {
		t.Fatal("secret exposed")
	}
	if err := saveWakeState(p.Name, state, ev, now); err != nil {
		t.Fatal(err)
	}
	ev, _, err = collectWakeEvidence(p, &config.Config{}, now.Add(time.Hour))
	if err != nil || len(ev.Attention) != 0 {
		t.Fatalf("replayed %+v %v", ev, err)
	}
}

// These are contract fixtures based on the issue's recorded 2026-10-27 case
// summaries, not live model evaluations or fabricated provider transcripts.
func TestSupervisorCasePromptContracts(t *testing.T) {
	prompt := chat.BuildPilotContext("pilot", "", "", nil, nil, "en")
	for _, tt := range []struct {
		name     string
		required []string
	}{
		{"offload #20 spec gap", []string{"nonexistent retry chain", "Decided/spec correction", "Release only after evidence resolves"}},
		{"Agent-Host #15 sandbox ps", []string{"sandbox denial of", "environment limitation", "Python 3.9/SQLite WAL", "authorized verification path"}},
		{"stalled git fetch", []string{"stalled git fetch", "root-cause issue", "Do not", "restart_service is outside this slice"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.required {
				if !strings.Contains(prompt, s) {
					t.Errorf("missing %q", s)
				}
			}
		})
	}
}

func seedAttention(t *testing.T, name string) {
	t.Helper()
	if err := project.AddRepo(name, "o/r"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(clog.LogsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	line := time.Now().Add(-time.Second).UTC().Format(time.RFC3339) + " INFO run/end repo=o/r issue=20 applied_label=agent-skipped\n"
	if err := os.WriteFile(filepath.Join(clog.LogsDir(), "run.log"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleNoChangeDoesNotConsumeCooldown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Create("quiet"); err != nil {
		t.Fatal(err)
	}
	if err := project.SetAutomation("quiet", true, 1); err != nil {
		t.Fatal(err)
	}
	n, err := Schedule(context.Background(), time.Second, time.Time{})
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	p, err := project.Get("quiet")
	if err != nil {
		t.Fatal(err)
	}
	if p.Automation.LastWokenAt != "" {
		t.Fatal("quiet tick consumed cooldown")
	}
	if _, err := os.Stat(wakeStatePath(p.Name)); !os.IsNotExist(err) {
		t.Fatal("quiet tick wrote wake state")
	}
}

func TestScheduleAttentionDeliveryAndCostLimitRetry(t *testing.T) {
	for _, capped := range []bool{false, true} {
		t.Run(fmt.Sprint(capped), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			cfg := &config.Config{}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			if _, err := project.Create("delivery"); err != nil {
				t.Fatal(err)
			}
			if err := project.SetAutomation("delivery", true, 1); err != nil {
				t.Fatal(err)
			}
			seedAttention(t, "delivery")
			original := runWakeOffload
			t.Cleanup(func() { runWakeOffload = original })
			calls := 0
			runWakeOffload = func(_ context.Context, prompt, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
				calls++
				if !strings.Contains(prompt, `"kind":"agent-skipped"`) {
					t.Error("attention missing from prompt")
				}
				if capped && calls == 1 {
					return "", operator.ErrCostLimit
				}
				pos := strings.LastIndex(prompt, "Wake run_id: ")
				id := strings.TrimSpace(prompt[pos+len("Wake run_id: "):])
				return "```pilot-actions\n{\"run_id\":\"" + id + "\",\"actions\":[]}\n```", nil
			}
			if _, err := Schedule(context.Background(), time.Second, time.Time{}); err != nil {
				t.Fatal(err)
			}
			if capped {
				p, _ := project.Get("delivery")
				if p.Automation.LastWokenAt != "" {
					t.Fatal("cost limit consumed cooldown")
				}
				if _, err := Schedule(context.Background(), time.Second, time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			// Bypass the project cooldown: persisted per-event deduplication must still
			// suppress the replay, even after another process reloads the project.
			if err := project.SetLastWokenAt("delivery", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := Schedule(context.Background(), time.Second, time.Time{}); err != nil {
				t.Fatal(err)
			}
			want := 1
			if capped {
				want = 2
			}
			if calls != want {
				t.Fatalf("calls=%d want=%d", calls, want)
			}
		})
	}
}

func TestWakeCollectionRetainsUnconsumedEvents(t *testing.T) {
	for _, stampDelay := range []time.Duration{0, time.Second} {
		t.Run(stampDelay.String(), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			p, err := project.Create("boundary")
			if err != nil {
				t.Fatal(err)
			}
			p.Repos = []string{"o/r"}
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			if err := os.MkdirAll(clog.LogsDir(), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(clog.LogsDir(), "run.log")
			first := now.Format(time.RFC3339) + " INFO run/end repo=o/r issue=20 applied_label=agent-skipped\n"
			if err := os.WriteFile(path, []byte(first), 0600); err != nil {
				t.Fatal(err)
			}
			ev, state, err := collectWakeEvidence(p, &config.Config{}, now)
			if err != nil || len(ev.Attention) != 1 {
				t.Fatalf("first: %+v %v", ev, err)
			}
			// MarkWoken may happen in this second or the next. A later append was
			// not part of the collected snapshot and must not be acknowledged by it.
			p.Automation.LastWokenAt = now.Add(stampDelay).Format(time.RFC3339)
			second := now.Format(time.RFC3339) + " INFO run/harvest_end repo=o/r issue=21 verdict=failed\n"
			if err := os.WriteFile(path, []byte(first+second), 0600); err != nil {
				t.Fatal(err)
			}
			if err := saveWakeState(p.Name, state, ev, now); err != nil {
				t.Fatal(err)
			}
			ev, state, err = collectWakeEvidence(p, &config.Config{}, now.Add(time.Hour))
			if err != nil || len(ev.Attention) != 1 || ev.Attention[0].Issue != "21" {
				t.Fatalf("unconsumed failure lost: %+v %v", ev, err)
			}
			found := false
			for _, event := range ev.Events {
				if event.Fields["issue"] == "21" {
					found = true
				}
			}
			if !found {
				t.Fatal("pending failure missing from evidence")
			}
			if err := saveWakeState(p.Name, state, ev, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			ev, _, err = collectWakeEvidence(p, &config.Config{}, now.Add(2*time.Hour))
			if err != nil || len(ev.Attention) != 0 {
				t.Fatalf("consumed event replayed: %+v %v", ev, err)
			}
		})
	}
}

func TestBoardWakeTriggers(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		name, kind string
		item       pipeline.Item
		created    time.Time
	}{
		{"head-2", "missing-decided", pipeline.Item{Issue: 3, State: "open", InQueue: true, QueuePosition: 3, HasDecided: pipeline.DecidedSection("## Plan"), UpdatedAt: old}, now.Add(-time.Hour)},
		{"unqueued", "unqueued-issue", pipeline.Item{Issue: 4, State: "open", UpdatedAt: old}, now.Add(-time.Hour)},
		{"stuck PR", "stuck-pr", pipeline.Item{Issue: 5, State: "open", InQueue: true, PR: &pipeline.PRInfo{State: "open", UpdatedAt: old}}, now.Add(-time.Hour)},
		{"digest", "daily-digest", pipeline.Item{Issue: 6, State: "closed"}, now.Add(-25 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			p, err := project.Create("board")
			if err != nil {
				t.Fatal(err)
			}
			p.Repos = []string{"o/r"}
			p.CreatedAt = tc.created.Format(time.RFC3339)
			p.Automation.LastWokenAt = now.Add(-time.Minute).Format(time.RFC3339)
			board := pipeline.Snapshot{GeneratedAt: now.Format(time.RFC3339), Repos: []pipeline.RepoBoard{{Repo: "o/r", Items: []pipeline.Item{tc.item}}}}
			if tc.kind == "unqueued-issue" {
				board.Repos[0].Items = nil
				board.Repos[0].UnqueuedIssues = []pipeline.Item{tc.item}
			}
			board.Repos = append(board.Repos, pipeline.RepoBoard{Repo: "other/r", UnqueuedIssues: []pipeline.Item{{Issue: 99, State: "open"}}})
			if err := snapshot.WritePipeline(board); err != nil {
				t.Fatal(err)
			}
			ev, state, err := collectWakeEvidence(p, &config.Config{}, now)
			if err != nil || len(ev.Attention) != 1 || ev.Attention[0].Kind != tc.kind {
				t.Fatalf("missing trigger: %+v, %v", ev.Attention, err)
			}
			if err := saveWakeState(p.Name, state, ev, now); err != nil {
				t.Fatal(err)
			}
			// A new snapshot timestamp alone must never generate another wake,
			// even after a process reload and a full day's cooldown.
			board.GeneratedAt = now.Add(26 * time.Hour).Format(time.RFC3339)
			if err := snapshot.WritePipeline(board); err != nil {
				t.Fatal(err)
			}
			ev, _, err = collectWakeEvidence(p, &config.Config{}, now.Add(26*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range ev.Attention {
				if a.Kind == tc.kind {
					t.Fatalf("unchanged condition replayed: %+v", a)
				}
			}
			if tc.kind == "daily-digest" {
				board.Repos[0].Items[0].Title = "new delivery"
				if err := snapshot.WritePipeline(board); err != nil {
					t.Fatal(err)
				}
				ev, _, err = collectWakeEvidence(p, &config.Config{}, now.Add(26*time.Hour))
				if err != nil || len(ev.Attention) != 1 || ev.Attention[0].Kind != "daily-digest" {
					t.Fatalf("changed digest lost: %+v %v", ev, err)
				}
			}
		})
	}
}

func TestBoardWakeNegativeConditions(t *testing.T) {
	now := time.Now().UTC()
	board := pipeline.Snapshot{Repos: []pipeline.RepoBoard{{Repo: "o/r", Items: []pipeline.Item{
		{Issue: 1, State: "closed"},
		{Issue: 2, State: "open", InQueue: true, QueuePosition: 4, HasDecided: pipeline.DecidedSection("")},
		{Issue: 3, State: "open", InQueue: true, QueuePosition: 3, HasDecided: pipeline.DecidedSection("## Decided\nship")},
		{Issue: 4, State: "open", InQueue: true, QueuePosition: 3}, // Unknown on legacy snapshots.
		{Issue: 5, State: "open", InQueue: true, PR: &pipeline.PRInfo{State: "open", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}},
		{Issue: 6, State: "open", InQueue: true, PR: &pipeline.PRInfo{State: "closed", UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339)}},
		{Issue: 7, State: "open", InQueue: true, PR: &pipeline.PRInfo{State: "open"}},
	}}}}
	if got := boardAttention(board, wakeState{}, now.Add(-time.Hour), now); len(got) != 0 {
		t.Fatalf("unexpected: %+v", got)
	}
	board.Repos[0].Items = []pipeline.Item{{Issue: 8, State: "open"}}
	board.Repos[0].Stale = true
	if got := boardAttention(board, wakeState{}, now, now); len(got) != 0 {
		t.Fatalf("stale evidence: %+v", got)
	}
}
