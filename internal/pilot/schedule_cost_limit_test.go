package pilot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// TestClassifyWakeStatus_CostLimit is the core regression guard for issue #320:
// a 402 billing cap reaching pilot must be recorded as "cost-limit", not as a
// generic "failed". 18 wakes in one day were misfiled as "failed" — which
// counted them toward the consecutive-failure alert ("check credentials" on
// perfectly good credentials) and, through the pre-wake MarkWoken stamp, burned
// a full cooldown per 4-second failure.
func TestClassifyWakeStatus_CostLimit(t *testing.T) {
	// The sentinel path is what production actually hits: RunOffload's
	// provider-exhaustion branch returns ("" , ErrCostLimit-wrapped), so the
	// 402 text is nowhere in output.
	sentinel := fmt.Errorf("%w: all 1 provider(s) failed", operator.ErrCostLimit)
	if got := classifyWakeStatus(sentinel, ""); got != "cost-limit" {
		t.Errorf("classifyWakeStatus(ErrCostLimit, empty output) = %q, want cost-limit — "+
			"the exhaustion path discards output, so only errors.Is can catch it", got)
	}

	// The text path covers the pre-#308 shape still present in historical
	// meta.json: a bare "claude: exit status 1" plus the 402 line in output.
	textCases := []struct {
		name   string
		output string
	}{
		{"chinese 402 from proxy", "API Error: 402 已达到每日费用限制 ($500)"},
		{"chinese 402 higher cap", "API Error: 402 已达到每日费用限制 ($700)"},
		{"english 402", "API Error: 402 Payment Required"},
	}
	for _, tc := range textCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWakeStatus(errors.New("claude: exit status 1"), tc.output); got != "failed" {
				t.Errorf("classifyWakeStatus(exit status 1, %q) = %q, want failed", tc.output, got)
			}
		})
	}
}

// TestClassifyWakeStatus_OtherStatuses verifies cost-limit detection did not
// swallow the statuses that were already working: auth errors keep their own
// bucket (issue #204) and everything else stays "failed".
func TestClassifyWakeStatus_OtherStatuses(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		output string
		want   string
	}{
		{"nil err", nil, "", ""},
		{"auth protocol", operator.ErrAuthError, "API Error: 403 Request not allowed", "auth-error"},
		{"timeout", errors.New("signal: killed"), "", "failed"},
		{"generic", errors.New("claude: exit status 1"), "", "failed"},
		{"rate limit is not a cost limit", errors.New("claude: exit status 1"), "You've hit your limit", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWakeStatus(tc.err, tc.output); got != tc.want {
				t.Errorf("classifyWakeStatus(%v, %q) = %q, want %q", tc.err, tc.output, got, tc.want)
			}
		})
	}
}

// TestClassifyWakeStatus_BudgetCapped guards the wake-side half of the
// --max-budget-usd fix: a session that hit its own budget ceiling must be
// recorded as "budget-capped", distinct from both "cost-limit" (an
// account-level provider refusal) and "failed" (a genuine error) — it must
// never count toward the consecutive-failure alert, since the run did real,
// paid-for work and stopped exactly as configured.
func TestClassifyWakeStatus_BudgetCapped(t *testing.T) {
	sentinel := fmt.Errorf("%w: Reached maximum budget ($15.00)", operator.ErrBudgetExceeded)
	if got := classifyWakeStatus(sentinel, "PILOT-RESULT: partial work done"); got != "budget-capped" {
		t.Errorf("classifyWakeStatus(ErrBudgetExceeded, ...) = %q, want budget-capped", got)
	}
	if got := classifyWakeStatus(sentinel, ""); got == "cost-limit" {
		t.Error("ErrBudgetExceeded must not be classified as cost-limit — they have different causes and different dashboard semantics")
	}
}

// TestRestoreCooldownAfterCostLimit verifies the cooldown rollback: after a
// capped wake, LastWokenAt must be back to its pre-wake value so the project is
// eligible again on the very next tick. Without this a 4-second $0 failure
// silenced the project for its whole cooldown — 60 min typical, 120 min on some
// projects (issue #320).
func TestRestoreCooldownAfterCostLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const name = "cooldown-rollback-test"
	if _, err := project.Create(name); err != nil {
		t.Fatalf("create project: %v", err)
	}

	const prev = "2026-09-11T12:00:00Z"
	if err := project.SetLastWokenAt(name, prev); err != nil {
		t.Fatalf("seed LastWokenAt: %v", err)
	}

	// Schedule stamps before the wake; the wake then dies on a 402.
	if err := project.MarkWoken(name); err != nil {
		t.Fatalf("MarkWoken: %v", err)
	}
	stamped, err := project.Get(name)
	if err != nil {
		t.Fatalf("get after MarkWoken: %v", err)
	}
	if stamped.Automation.LastWokenAt == prev {
		t.Fatal("MarkWoken did not advance LastWokenAt — test cannot prove the rollback")
	}

	restoreCooldownAfterCostLimit(name, prev)

	got, err := project.Get(name)
	if err != nil {
		t.Fatalf("get after rollback: %v", err)
	}
	if got.Automation.LastWokenAt != prev {
		t.Errorf("LastWokenAt = %q after cost-limit rollback, want %q — a capped wake must not consume the cooldown",
			got.Automation.LastWokenAt, prev)
	}
}

// TestSetLastWokenAtPreservesSkipReason pins the reason SetLastWokenAt exists
// separately from MarkWoken: the rollback must touch only LastWokenAt.
func TestSetLastWokenAtPreservesSkipReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const name = "skip-reason-test"
	if _, err := project.Create(name); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := project.MarkSkipped(name, "bound to other-host"); err != nil {
		t.Fatalf("MarkSkipped: %v", err)
	}
	if err := project.SetLastWokenAt(name, "2026-09-11T12:00:00Z"); err != nil {
		t.Fatalf("SetLastWokenAt: %v", err)
	}
	got, err := project.Get(name)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Automation.LastSkipReason != "bound to other-host" {
		t.Errorf("LastSkipReason = %q, want it untouched by SetLastWokenAt", got.Automation.LastSkipReason)
	}
	if got.Automation.LastWokenAt != "2026-09-11T12:00:00Z" {
		t.Errorf("LastWokenAt = %q, want the explicit timestamp", got.Automation.LastWokenAt)
	}
}

// TestCostLimitSalvageFromEvents covers the "keep the work already paid for"
// half of issue #320. Two capped wakes cost $17.52 (41 and 65 turns) and were
// recorded with an empty Result / Duties / Summary, because RunOffload's
// exhaustion path returns "" and the doc write-back was gated on err == nil.
// The teed events.jsonl is the only surviving copy of the partial transcript,
// so the recovery has to read it back and re-run the extractors against plain
// text.
func TestCostLimitSalvageFromEvents(t *testing.T) {
	dir := t.TempDir()
	// Two assistant turns in the stream-json shape claude emits: a finished
	// context.md block, then a PILOT-RESULT line, then a 402 result event.
	events := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Patrol done.\n\n` + "```context.md" + `\n# Overview\n\nUpdated by pilot.\n` + "```" + `\n"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PILOT-RESULT: 1 action — filed #42\n"}]}}
{"type":"result","subtype":"success","is_error":true,"api_error_status":402,"num_turns":41,"total_cost_usd":8.43635,"result":"API Error: 402 已达到每日费用限制 ($500)"}
`
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, []byte(events), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	salvaged := chat.CollectAssistantText(string(raw))
	if salvaged == "" {
		t.Fatal("CollectAssistantText returned empty — nothing to salvage from a paid-for wake")
	}
	if got := extractResult(salvaged); got != "PILOT-RESULT: 1 action — filed #42" {
		t.Errorf("extractResult(salvaged) = %q, want the PILOT-RESULT line", got)
	}
	// extractContextMD must handle the flattened text the salvage produces;
	// chat.ExtractLastContextMD alone expects raw stream-json and finds nothing.
	block := extractContextMD(salvaged)
	if block == "" {
		t.Fatal("extractContextMD found no block in salvaged plain text — the write-back would be skipped")
	}
	if !strings.Contains(block, "Updated by pilot.") {
		t.Errorf("salvaged context.md = %q, want the block body", block)
	}
}

// TestExtractContextMDBothShapes pins extractContextMD accepting both the
// plain-text form (operator.RunOffload's return value / salvaged transcript) and
// the raw stream-json form, since the two paths reach the same write-back.
func TestExtractContextMDBothShapes(t *testing.T) {
	plain := "prose\n\n```context.md\n# From plain text\n```\n"
	if got := extractContextMD(plain); !strings.Contains(got, "From plain text") {
		t.Errorf("extractContextMD(plain) = %q, want the block body", got)
	}

	streamJSON := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` +
		"```context.md" + `\n# From stream json\n` + "```" + `\n"}]}}` + "\n"
	if got := extractContextMD(streamJSON); !strings.Contains(got, "From stream json") {
		t.Errorf("extractContextMD(streamJSON) = %q, want the block body", got)
	}

	if got := extractContextMD("PILOT-RESULT: no-action — backlog coherent"); got != "" {
		t.Errorf("extractContextMD(no block) = %q, want empty", got)
	}
}

// The default Pilot dollar cap is unsupported by direct Offload. Admission must
// fail before any provider process instead of silently dropping the cap.
func TestScheduleRejectsUnsupportedBudgetBeforeLaunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubOffloadFailure(t, "cost_limit", "PILOT-RESULT: should never launch")
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	dir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("settings:\n  language: en\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Create("budget-rejected"); err != nil {
		t.Fatal(err)
	}
	if err := project.SetAutomation("budget-rejected", true, 60); err != nil {
		t.Fatal(err)
	}
	seedAttention(t, "budget-rejected")
	woken, err := Schedule(context.Background(), 30*time.Second, time.Time{})
	if err != nil || woken != 0 {
		t.Fatalf("woken=%d err=%v", woken, err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("unsupported cap launched Offload")
	}
}

// TestScheduleSkipsWhenBudgetInsufficient is the regression guard for issue
// #325: `clawflow run`'s self-watchdog bounds the WHOLE process, but Pilot
// wakes run sequentially inside Phase 4 with no awareness of how much of
// that budget is left. A wake started with less time remaining than its own
// perWakeTimeout is guaranteed to be killed mid-flight by the watchdog,
// orphaning a claude subprocess that keeps writing to the VCS with nothing
// ever recorded in meta.json. Schedule must refuse to start it.
//
// No claude stub is needed here — the check runs before wake() is ever
// invoked, so a real production incident (pop project, 2026-09-12) never
// reaches the point that produced the orphan.
func TestScheduleSkipsWhenBudgetInsufficient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".clawflow", "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "repos.yaml"), []byte("repos: {}\n"), 0o644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "config.yaml"), []byte("settings:\n  language: en\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	names := []string{"budget-a", "budget-b"}
	for _, n := range names {
		if _, err := project.Create(n); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
		if err := project.SetAutomation(n, true, 60); err != nil {
			t.Fatalf("enable automation %s: %v", n, err)
		}
	}

	// The watchdog is about to fire in 1 second, but each wake claims it
	// needs a full hour — nowhere near enough runway to start one.
	deadline := time.Now().Add(1 * time.Second)
	woken, err := Schedule(context.Background(), time.Hour, deadline)
	if err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}
	if woken != 0 {
		t.Errorf("woken = %d, want 0 — no wake should start when remaining budget < perWakeTimeout", woken)
	}

	for _, n := range names {
		if entries, rerr := os.ReadDir(filepath.Join(home, ".clawflow", "data", "pilot-runs", n)); rerr == nil && len(entries) > 0 {
			t.Errorf("project %s: %d pilot-run dir(s) found, want 0 — wake must never have been attempted", n, len(entries))
		}
	}
}

// TestScheduleProceedsWhenBudgetSufficient is the flip side: a generous
// deadline must not trip the new guard and block wakes that would have
// succeeded before issue #325's fix.
func TestScheduleProceedsWhenBudgetSufficient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubOffloadFailure(t, "cost_limit", "PILOT-RESULT: refused") // fast, deterministic subprocess; outcome doesn't matter here

	if err := os.MkdirAll(filepath.Join(home, ".clawflow", "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "repos.yaml"), []byte("repos: {}\n"), 0o644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "config.yaml"), []byte("settings:\n  language: en\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	const name = "budget-sufficient"
	if _, err := project.Create(name); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	if err := project.SetAutomation(name, true, 60); err != nil {
		t.Fatalf("enable automation %s: %v", name, err)
	}

	seedAttention(t, name)
	// Plenty of runway relative to the wake's own timeout.
	deadline := time.Now().Add(time.Hour)
	if _, err := Schedule(context.Background(), 30*time.Second, deadline); err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(home, ".clawflow", "data", "pilot-runs", name))
	if err != nil || len(entries) == 0 {
		t.Errorf("project %s: expected a pilot-run dir (wake attempted), got err=%v entries=%d", name, err, len(entries))
	}
}
