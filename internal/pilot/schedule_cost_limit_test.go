package pilot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// The sentinel path is what production actually hits: RunClaude's
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
			if got := classifyWakeStatus(errors.New("claude: exit status 1"), tc.output); got != "cost-limit" {
				t.Errorf("classifyWakeStatus(exit status 1, %q) = %q, want cost-limit", tc.output, got)
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
		{"auth 403", errors.New("claude: exit status 1"), "API Error: 403 Request not allowed", "auth-error"},
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
// recorded with an empty Result / Duties / Summary, because RunClaude's
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
// plain-text form (operator.RunClaude's return value / salvaged transcript) and
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

// TestScheduleAbortsOnCostLimit is the end-to-end guard for the cascade half of
// issue #320: on 2026-09-11 a single account cap took three projects down in
// six seconds (09:18:16 / :24 / :30), each burning its own full cooldown. With
// the fix Schedule stops after the first capped wake, and no project involved
// has its cooldown consumed.
//
// Drives the real Schedule against a stubbed `claude` that always answers 402,
// which is also what makes this a runtime check and not just a unit assertion.
func TestScheduleAbortsOnCostLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stub402Claude(t)

	if err := os.MkdirAll(filepath.Join(home, ".clawflow", "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "repos.yaml"), []byte("repos: {}\n"), 0o644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "config.yaml"), []byte("settings:\n  language: en\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	names := []string{"abort-a", "abort-b", "abort-c"}
	for _, n := range names {
		if _, err := project.Create(n); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
		if err := project.SetAutomation(n, true, 60); err != nil {
			t.Fatalf("enable automation %s: %v", n, err)
		}
	}

	woken, err := Schedule(context.Background(), 30*time.Second, time.Time{})
	if err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}
	if woken != 0 {
		t.Errorf("woken = %d, want 0 — every wake was capped, none did any work", woken)
	}

	// Exactly one wake should have been attempted; the abort must stop the
	// fan-out before the other two projects are touched.
	attempted := 0
	for _, n := range names {
		if entries, rerr := os.ReadDir(filepath.Join(home, ".clawflow", "data", "pilot-runs", n)); rerr == nil {
			attempted += len(entries)
		}
	}
	if attempted != 1 {
		t.Errorf("%d wake(s) attempted, want 1 — a cap is account-level, the pass must abort after the first", attempted)
	}

	// No project may have its cooldown consumed: the one that hit the cap gets
	// rolled back, the other two were never stamped.
	for _, n := range names {
		p, gerr := project.Get(n)
		if gerr != nil {
			t.Fatalf("get %s: %v", n, gerr)
		}
		if p.Automation.LastWokenAt != "" {
			t.Errorf("%s: LastWokenAt = %q, want empty — a capped pass must not burn any cooldown",
				n, p.Automation.LastWokenAt)
		}
		if rem := p.CooldownRemaining(time.Now()); rem > 0 {
			t.Errorf("%s: CooldownRemaining = %s, want 0 — must be eligible again on the next tick", n, rem)
		}
	}
}

// stub402Claude puts a fake `claude` at the front of PATH that answers every
// invocation with a 402 billing cap, mirroring the proxy's Chinese phrasing
// from the incident. Two assistant turns precede the refusal so the salvage
// path has a finished context.md block and a PILOT-RESULT line to recover.
func stub402Claude(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub is POSIX-only")
	}
	bin := t.TempDir()
	// Quoted heredoc delimiters keep "$500" from being shell-expanded.
	script := `#!/bin/sh
cat <<'STDOUT_EOF'
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Patrol done.\n\n` +
		"```context.md" + `\n# Overview\n\nSalvaged.\n` + "```" + `\n"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PILOT-RESULT: 1 action — filed #42\n"}]}}
{"type":"result","subtype":"success","is_error":true,"api_error_status":402,"num_turns":41,"total_cost_usd":8.43635,"result":"API Error: 402 已达到每日费用限制 ($500)"}
STDOUT_EOF
echo 'API Error: 402 已达到每日费用限制 ($500)' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	stub402Claude(t) // fast, deterministic subprocess; outcome doesn't matter here

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
