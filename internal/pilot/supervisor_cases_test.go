package pilot

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
)

// Replay reference plans against normalized incident evidence supplied by #407
// and #410. These fixtures are not captured model responses: this test covers
// evidence-to-prompt delivery and executable plan contracts, not model quality.
func TestSupervisorRecordedCaseActionPlans(t *testing.T) {
	var cases []struct {
		Name, Source, Repo, Evidence string
		Issue                        int
		Duties                       []int
		Output                       ActionSet
	}
	data, err := os.ReadFile("testdata/supervisor_cases_2026-10-27.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	expected := map[string][]string{
		"offload-20-spec-gap":      {"edit_spec", "comment", "note"},
		"agent-host-15-sandbox-ps": {"ask_human", "note"},
		"stalled-git-fetch":        {"note", "comment", "note", "note"},
	}
	covered := map[int]bool{}
	if len(cases) != len(expected) {
		t.Fatal("missing recorded cases")
	}
	for _, f := range cases {
		t.Run(f.Name, func(t *testing.T) {
			if f.Source == "" || f.Evidence == "" {
				t.Fatal("missing incident provenance/evidence")
			}
			for _, d := range f.Duties {
				covered[d] = true
			}
			evidence := wakeEvidence{Board: pipeline.Snapshot{Repos: []pipeline.RepoBoard{{Repo: f.Repo, Items: []pipeline.Item{{Issue: f.Issue, Acceptance: &pipeline.AcceptanceInfo{Verdict: "blocked", Summary: f.Evidence}}}}}}}
			prompt := chat.BuildPilotContext(f.Name, f.Evidence, "", nil, nil, "zh") + evidencePrompt(evidence)
			if !strings.Contains(prompt, f.Evidence) || !strings.Contains(prompt, f.Repo) {
				t.Fatal("incident not delivered to supervisor")
			}
			// Replay the nonempty structured proposal through the production parser,
			// validator and executor with an isolated fake VCS, never live providers.
			encoded, err := json.Marshal(f.Output)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := ParseActions("```pilot-actions\n" + string(encoded) + "\n```")
			if err != nil {
				t.Fatal(err)
			}
			var types []string
			var bodies strings.Builder
			for _, raw := range plan.Actions {
				a, err := validateAction(raw)
				if err != nil {
					t.Fatal(err)
				}
				if a.Repo != f.Repo {
					t.Fatal("cross-repository proposal")
				}
				types = append(types, a.Type)
				bodies.WriteString(a.Body)
				switch a.Type {
				case "release", "restart_service", "override_merge", "queue_add":
					t.Fatalf("unsafe proposal %s", a.Type)
				}
			}
			if !reflect.DeepEqual(types, expected[f.Name]) {
				t.Fatalf("plan=%v want=%v", types, expected[f.Name])
			}
			executor, client := testExecutor(t)
			executor.Repos = []string{f.Repo}
			executor.DefaultRepo = f.Repo
			client.issue.Number = f.Issue
			records, err := executor.Apply(plan, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != len(types) {
				t.Fatal("missing action records")
			}
			for _, r := range records {
				if r.Outcome != "applied" {
					t.Fatalf("plan not executable: %+v", r)
				}
			}
			switch f.Name {
			case "offload-20-spec-gap":
				for _, s := range []string{"Decided", "spec_gap", "retry chain", "验收标准"} {
					if !strings.Contains(client.issue.Body, s) {
						t.Fatalf("spec correction missing %q", s)
					}
				}
				if len(client.calls) != 2 || client.calls[0] != "edit" || !strings.HasPrefix(client.calls[1], "comment:") {
					t.Fatalf("wrong spec effects: %v", client.calls)
				}
			case "agent-host-15-sandbox-ps":
				if len(client.calls) != 0 {
					t.Fatalf("unresolved sandbox failure mutated VCS: %v", client.calls)
				}
				for _, s := range []string{"environment_sandbox", "Python 3.9", "SQLite WAL", "授权"} {
					if !strings.Contains(records[0].Action.Body, s) {
						t.Fatalf("verification question missing %q", s)
					}
				}
			case "stalled-git-fetch":
				if len(client.calls) != 1 || !strings.HasPrefix(client.calls[0], "comment:") || records[1].Action.Issue != 403 {
					t.Fatal("must link existing root cause without recovery mutation")
				}
				for _, s := range []string{"45 分钟", "transient_infrastructure", "发布 main SHA", "不安装", "职责 8"} {
					if !strings.Contains(bodies.String(), s) {
						t.Fatalf("diagnosis/report missing %q", s)
					}
				}
			}
			// Persisted idempotency must prevent a fixture replay from repeating effects.
			count := len(client.calls)
			replay, err := executor.Apply(plan, false)
			if err != nil || len(client.calls) != count {
				t.Fatalf("replay repeated effects: %v", err)
			}
			for _, r := range replay {
				if r.Outcome != "already_applied" {
					t.Fatalf("non-idempotent plan: %+v", r)
				}
			}
		})
	}
	for _, d := range []int{1, 2, 3, 4, 5, 7, 8} {
		if !covered[d] {
			t.Errorf("duty %d has no fixture", d)
		}
	}
}
