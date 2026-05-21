package commands

import (
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
