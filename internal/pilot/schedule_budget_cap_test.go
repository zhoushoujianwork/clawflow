package pilot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// TestWakeSucceedsOnBudgetCap is the end-to-end guard for the wake-budget fix:
// two outlier wakes ($34/119 turns, $20/114 turns) ran unbounded until the
// wall-clock --timeout SIGKILLed them, discarding the entire (already
// paid-for) session with an empty PILOT-RESULT. With Settings.PilotMaxBudgetUSD
// set, claude itself stops at the cap and returns a well-formed result, so
// the wake must be recorded as "budget-capped" (not "failed"), keep the
// partial output, honour the context.md write-back, and — unlike a cost-limit
// cap — consume its cooldown normally, since nothing here indicates the
// project or credentials are unhealthy.
//
// Drives the real wake() against a stubbed `claude` that always answers with
// subtype "error_max_budget_usd", mirroring what the live CLI does when
// --max-budget-usd is hit.
func TestWakeSucceedsOnBudgetCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubBudgetCappedClaude(t)

	if err := os.MkdirAll(filepath.Join(home, ".clawflow", "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "repos.yaml"), []byte("repos: {}\n"), 0o644); err != nil {
		t.Fatalf("write repos.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "config.yaml"),
		[]byte("settings:\n  language: en\n  pilot_max_budget_usd: 8\n"), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	const name = "budget-cap-test"
	if _, err := project.Create(name); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := project.SetAutomation(name, true, 60); err != nil {
		t.Fatalf("enable automation: %v", err)
	}

	woken, err := Schedule(context.Background(), 30*time.Second)
	if err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}
	// A budget-capped wake still "worked" — it did paid work and produced a
	// result — so unlike the cost-limit abort path it must count as woken.
	if woken != 1 {
		t.Errorf("woken = %d, want 1 — a budget cap is a clean stop, not a failed wake", woken)
	}

	p, gerr := project.Get(name)
	if gerr != nil {
		t.Fatalf("get project: %v", gerr)
	}
	if p.Automation.LastWokenAt == "" {
		t.Error("LastWokenAt is empty — a budget-capped wake must consume its cooldown normally (it is not a cost-limit-style no-op)")
	}

	entries, rerr := os.ReadDir(filepath.Join(home, ".clawflow", "data", "pilot-runs", name))
	if rerr != nil || len(entries) != 1 {
		t.Fatalf("pilot-runs dir: %v, %d entries, want 1", rerr, len(entries))
	}
	runDir := filepath.Join(home, ".clawflow", "data", "pilot-runs", name, entries[0].Name())

	metaPath := filepath.Join(runDir, "meta.json")
	metaBytes, rerr := os.ReadFile(metaPath)
	if rerr != nil {
		t.Fatalf("read meta.json: %v", rerr)
	}
	var meta snapshot.PilotRunMeta
	if perr := json.Unmarshal(metaBytes, &meta); perr != nil {
		t.Fatalf("unmarshal meta.json: %v", perr)
	}
	if meta.Status != "budget-capped" {
		t.Errorf("meta.Status = %q, want budget-capped", meta.Status)
	}
	if meta.Result == "" {
		t.Error("meta.Result is empty — the PILOT-RESULT line the stub emitted before the cap should have survived")
	}

	// context.md write-back must still have happened — a budget cap stops the
	// work that would follow a fenced block, it doesn't invalidate a block the
	// model already finished writing (same reasoning as the cost-limit path).
	contextMD, cerr := project.ReadContext(name)
	if cerr != nil {
		t.Fatalf("read context.md: %v", cerr)
	}
	if contextMD == "" {
		t.Error("context.md is empty — the budget-capped wake's context.md block should have been written back")
	}
}

// stubBudgetCappedClaude puts a fake `claude` at the front of PATH that
// always answers with a stream-json result carrying subtype
// "error_max_budget_usd", mirroring the live CLI's exit when --max-budget-usd
// is reached. One assistant turn precedes the cap so the wake has a
// PILOT-RESULT line and a finished context.md block to recover.
func stubBudgetCappedClaude(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub is POSIX-only")
	}
	bin := t.TempDir()
	script := `#!/bin/sh
cat <<'STDOUT_EOF'
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Patrol done.\n\n` +
		"```context.md" + `\n# Overview\n\nUpdated before the budget cap.\n` + "```" + `\n"}]}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PILOT-RESULT: 1 action — filed #42\n"}]}}
{"type":"result","subtype":"error_max_budget_usd","is_error":true,"num_turns":5,"total_cost_usd":8.0,"errors":["Reached maximum budget ($8.00)"]}
STDOUT_EOF
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
