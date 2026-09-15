package commands

import (
	"encoding/json"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// TestPRViewOutput_JSONIncludesMergeable guards the --json shape added for
// issue #334: mergeable must be present alongside the embedded vcs.PR
// fields, and must be omitted (not printed as "") when the mergeability
// check failed.
func TestPRViewOutput_JSONIncludesMergeable(t *testing.T) {
	out := prViewOutput{
		PR: vcs.PR{
			Number:     7,
			Title:      "fix: thing",
			HeadBranch: "fix/issue-7",
			State:      "open",
		},
		Mergeable: string(vcs.MergeStatusConflict),
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["mergeable"] != "conflict" {
		t.Errorf("expected mergeable=conflict in JSON, got %v (full: %s)", got["mergeable"], data)
	}
	if got["number"] != float64(7) {
		t.Errorf("expected embedded PR fields to surface, got %v", got["number"])
	}

	// mergeability lookup failed: field must be omitted, not printed as "".
	empty := prViewOutput{PR: vcs.PR{Number: 8}}
	data2, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got2 map[string]any
	if err := json.Unmarshal(data2, &got2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got2["mergeable"]; ok {
		t.Errorf("expected mergeable to be omitted when empty, got %s", data2)
	}
}
