package commands

import (
	"errors"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestOrphanCause(t *testing.T) {
	none := func() string { return "" }
	blocked := func() string { return "blocked" }
	managed := vcs.PR{Number: 1, HeadBranch: "feat/issue-7"}
	cases := []struct {
		name string
		pr   vcs.PR
		req  bool
		v    func() string
		bud  error
		want string
	}{
		{"healthy", managed, true, none, nil, ""},
		{"acceptance blocked", managed, true, blocked, nil, coordinator.OrphanAcceptanceBlocked},
		{"acceptance not required", managed, false, blocked, nil, ""},
		{"repair exhausted", managed, false, none, errRepairExhausted, coordinator.OrphanRepairExhausted},
		{"repair locked", managed, false, none, errRepairBlocked, coordinator.OrphanRepairExhausted},
		{"unmanaged branch with issue", vcs.PR{Number: 2, HeadBranch: "topic", Body: "Closes #7"}, false, none, nil, coordinator.OrphanBranchUnmanaged},
		{"WIP title", vcs.PR{Number: 4, HeadBranch: "topic", Title: "WIP: x"}, true, blocked, nil, ""},
		{"bracket draft title", vcs.PR{Number: 4, HeadBranch: "feat/issue-7", Title: "[DRAFT] x"}, true, blocked, nil, ""},
		{"platform draft", vcs.PR{Number: 5, HeadBranch: "feat/issue-7", Draft: true}, true, blocked, nil, ""},
		{"blocked auto-repairable", managed, true, func() string { return "" }, nil, ""},
		{"blocked unrepairable", managed, true, blocked, nil, coordinator.OrphanAcceptanceBlocked},
		{"unstructured retries left", managed, true, func() string { return "" }, nil, ""},
		{"unstructured exhausted", managed, true, func() string { return orphanUnstructuredExhausted }, nil, coordinator.OrphanAcceptanceUnstructured},
		{"unlinked", vcs.PR{Number: 3, HeadBranch: "topic"}, false, none, nil, coordinator.OrphanIssueUnlinked},
	}
	for _, c := range cases {
		if got := orphanCause(c.pr, c.req, c.v, c.bud); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if isTerminalBudget(errors.New("x")) || !isTerminalBudget(errRepairExhausted) {
		t.Fatal("isTerminalBudget")
	}
}

func TestOrphanCauseUnstructuredExhausted(t *testing.T) {
	pr := vcs.PR{Number: 1, HeadBranch: "feat/issue-7"}
	got := orphanCause(pr, true, func() string { return orphanUnstructuredExhausted }, nil)
	if got != coordinator.OrphanAcceptanceUnstructured {
		t.Fatalf("got %q", got)
	}
}

func TestOrphanVerdict(t *testing.T) {
	cases := []struct {
		name string
		info *pipeline.AcceptanceInfo
		want string
	}{
		{"nil", nil, ""},
		{"blocked repairable", &pipeline.AcceptanceInfo{Verdict: "blocked", BlockedRepairable: true}, ""},
		{"blocked unrepairable", &pipeline.AcceptanceInfo{Verdict: "blocked"}, "blocked"},
		{"unstructured retries left", &pipeline.AcceptanceInfo{Verdict: "failed", Unstructured: true, UnstructuredAttempts: 1, MaxUnstructuredRetries: 2}, ""},
		{"unstructured exhausted", &pipeline.AcceptanceInfo{Verdict: "failed", Unstructured: true, UnstructuredAttempts: 3, MaxUnstructuredRetries: 2}, orphanUnstructuredExhausted},
	}
	for _, c := range cases {
		if got := orphanVerdict(c.info); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
