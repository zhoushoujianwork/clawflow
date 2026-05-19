package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// TestDeterministicSkip pins down which (operator × repo-config) combinations
// are considered "configuration-level unrunnable" — the answer drives both
// pending-list filtering and firstMatch execution gating, and a regression
// here would resurrect the old bug where an `implement` op with no
// local_path piled up forever in the dashboard's Pending list.
func TestDeterministicSkip(t *testing.T) {
	implement := &operator.Operator{Name: "implement"}
	evalBug := &operator.Operator{Name: "evaluate-bug"}

	cases := []struct {
		name string
		op   *operator.Operator
		repo config.Repo
		want bool
	}{
		{
			name: "implement with empty local_path is skipped",
			op:   implement,
			repo: config.Repo{LocalPath: ""},
			want: true,
		},
		{
			name: "implement with local_path runs",
			op:   implement,
			repo: config.Repo{LocalPath: "/tmp/clone"},
			want: false,
		},
		{
			name: "evaluate-bug never deterministically skipped (no local_path needed)",
			op:   evalBug,
			repo: config.Repo{LocalPath: ""},
			want: false,
		},
		{
			name: "nil operator is safely false (defensive)",
			op:   nil,
			repo: config.Repo{},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deterministicSkip(tc.op, tc.repo); got != tc.want {
				t.Errorf("deterministicSkip = %v, want %v", got, tc.want)
			}
			// Reason must be non-empty exactly when skip is true. This
			// keeps the debug log readable and prevents future contributors
			// from forgetting to add a reason when they add a new skip.
			reason := deterministicSkipReason(tc.op, tc.repo)
			if tc.want && reason == "" {
				t.Errorf("expected non-empty reason when skip=true")
			}
			if !tc.want && reason != "" {
				t.Errorf("expected empty reason when skip=false, got %q", reason)
			}
		})
	}
}

// TestExtractCloudUsage covers the worker-side helper: real events.jsonl
// with a result event → populated cloud.Usage; missing file or absent
// result event → nil (lets FinishRun proceed with no Usage attached).
func TestExtractCloudUsage(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		dir := t.TempDir()
		eventsLine := `{"type":"result","duration_ms":4321,"num_turns":3,"total_cost_usd":0.0876,"usage":{"input_tokens":500,"output_tokens":120,"cache_read_input_tokens":2000,"cache_creation_input_tokens":10},"modelUsage":{"claude-opus-4-7":{"inputTokens":500,"outputTokens":120,"cacheReadInputTokens":2000,"cacheCreationInputTokens":10,"costUSD":0.0876}}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(eventsLine), 0o644); err != nil {
			t.Fatalf("write events.jsonl: %v", err)
		}
		u := ExtractCloudUsage(dir)
		if u == nil {
			t.Fatal("ExtractCloudUsage returned nil for a real result event")
		}
		if u.TotalCostUSD != 0.0876 || u.InputTokens != 500 || u.OutputTokens != 120 {
			t.Errorf("scalar fields mismatched: %+v", u)
		}
		if u.NumTurns != 3 || u.DurationMs != 4321 {
			t.Errorf("turns/duration mismatched: %+v", u)
		}
		m, ok := u.ModelUsage["claude-opus-4-7"]
		if !ok {
			t.Fatalf("model breakdown missing: %+v", u.ModelUsage)
		}
		if m.CostUSD != 0.0876 || m.InputTokens != 500 {
			t.Errorf("model usage round-trip mismatch: %+v", m)
		}
	})

	t.Run("empty runDir returns nil", func(t *testing.T) {
		if u := ExtractCloudUsage(""); u != nil {
			t.Errorf("empty runDir → want nil, got %+v", u)
		}
	})

	t.Run("missing events.jsonl returns nil (no error)", func(t *testing.T) {
		// Use a non-existent directory; helper should swallow ENOENT.
		if u := ExtractCloudUsage(filepath.Join(t.TempDir(), "nope")); u != nil {
			t.Errorf("missing file → want nil, got %+v", u)
		}
	})

	t.Run("no result event returns nil", func(t *testing.T) {
		dir := t.TempDir()
		// Lines exist but none are terminal result events.
		other := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(other), 0o644); err != nil {
			t.Fatalf("write events.jsonl: %v", err)
		}
		if u := ExtractCloudUsage(dir); u != nil {
			t.Errorf("no result event → want nil, got %+v", u)
		}
	})
}
