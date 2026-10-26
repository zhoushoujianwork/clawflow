package commands

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func implementationForTest(t *testing.T) *operator.Operator {
	t.Helper()
	data, err := os.ReadFile("../../../skills/implement/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	op, err := operator.Parse(data, "test")
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestAutonomousEligibility(t *testing.T) {
	op := implementationForTest(t)
	for _, tc := range []struct {
		name     string
		labels   []string
		children int
		want     bool
	}{
		{"unlabeled", nil, 0, true}, {"evaluated", []string{"agent-evaluated"}, 0, true},
		{"question", []string{"question"}, 0, false}, {"parent", nil, 1, false},
		{"tracking", []string{"tracking"}, 0, false},
		{"implemented", []string{"agent-implemented"}, 0, false}, {"failed", []string{"agent-failed"}, 0, false},
		{"skipped", []string{"agent-skipped"}, 0, false}, {"running", []string{"agent-running"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := &operator.Subject{State: "open", Labels: tc.labels, SubTotal: tc.children}
			effective := effectiveOperator(op, sub)
			got := effective != nil && operator.Matches(sub, effective)
			if got != tc.want {
				t.Fatalf("matched=%t, want %t", got, tc.want)
			}
			if effective != nil && len(effective.Trigger.LabelsConsumed) != 0 {
				t.Fatal("autonomous implementation consumes obsolete approval label")
			}
			if len(op.Trigger.LabelsRequired) != 0 {
				t.Fatal("registry operator mutated")
			}
		})
	}
	for _, name := range []string{"classify", "evaluate-bug", "evaluate-feat", "decompose"} {
		if effectiveOperator(&operator.Operator{Name: name}, &operator.Subject{}) != nil {
			t.Fatalf("%s was not suppressed", name)
		}
	}
	question := &operator.Operator{Name: "reply-question", Trigger: operator.Trigger{LabelsRequired: []string{"question"}}}
	sub := &operator.Subject{State: "open", Labels: []string{"question"}}
	if !operator.Matches(sub, effectiveOperator(question, sub)) {
		t.Fatal("question reply suppressed")
	}
}

func TestAutonomousDefaultDispatch(t *testing.T) {
	reg := operator.NewRegistry()
	if err := reg.LoadEmbedded(os.DirFS("../../../skills"), "."); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		labels []string
		want   string
	}{
		{"default leaf", nil, "implement"},
		{"evaluated leaf", []string{"bug", "agent-evaluated"}, "implement"},
		{"question", []string{"question"}, "reply-question"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := &operator.Subject{State: "open", Labels: tc.labels}
			var best *operator.Operator
			for _, original := range reg.All() {
				op := effectiveOperator(original, sub)
				if op != nil && operator.Matches(sub, op) && (best == nil || operatorPriority(op.Name) < operatorPriority(best.Name)) {
					best = op
				}
			}
			if best == nil || best.Name != tc.want {
				t.Fatalf("selected %+v, want %s", best, tc.want)
			}
		})
	}
}

func TestAutonomousCustomConditions(t *testing.T) {
	op := implementationForTest(t)
	op.Trigger.LabelsRequired = []string{"custom"}
	op.Trigger.LabelsRequiredAny = []string{"one", "two"}
	sub := &operator.Subject{State: "open", Labels: []string{"custom", "one"}}
	if !operator.Matches(sub, effectiveOperator(op, sub)) {
		t.Fatal("custom matching issue rejected")
	}
	sub.Labels = []string{"custom"}
	if operator.Matches(sub, effectiveOperator(op, sub)) {
		t.Fatal("required_any lost")
	}
	sub.Labels = []string{"one"}
	if operator.Matches(sub, effectiveOperator(op, sub)) {
		t.Fatal("custom requirement lost")
	}
	sub.Labels = []string{"custom", "one"}
	op.Trigger.Target = "pr"
	if operator.Matches(sub, effectiveOperator(op, sub)) {
		t.Fatal("custom target lost")
	}
}

func TestCustomEvaluatorModelRole(t *testing.T) {
	if got := roleForOperator("evaluate-custom"); got != config.RoleEval {
		t.Fatalf("custom evaluator role = %q", got)
	}
}

type autonomousRecheckFake struct {
	vcs.Client
	labels           []string
	structuralChecks int
}

func (f *autonomousRecheckFake) GetIssueLabels(string, int) ([]string, error) { return f.labels, nil }
func (f *autonomousRecheckFake) ListSubIssues(string, int) ([]vcs.Issue, error) {
	f.structuralChecks++
	return []vcs.Issue{{Number: 99}}, nil // stop before workdir/provider execution
}

func TestAutonomousFreshRecheck(t *testing.T) {
	for _, tc := range []struct {
		name             string
		labels           []string
		reachesStructure bool
	}{
		{"unlabeled", nil, true}, {"evaluated", []string{"agent-evaluated"}, true},
		{"question added", []string{"question"}, false}, {"failure added", []string{"agent-failed"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withRunLog(t)
			fake := &autonomousRecheckFake{labels: tc.labels}
			sub := &operator.Subject{Number: 42, State: "open"}
			op := effectiveOperator(implementationForTest(t), sub)
			fired, limited := runOneOperator(context.Background(), &runJob{op: op, sub: sub, repo: "o/r", client: fake}, time.Second)
			if fired || limited {
				t.Fatal("structural skip should not fire")
			}
			if (fake.structuralChecks > 0) != tc.reachesStructure {
				t.Fatalf("structural checks=%d", fake.structuralChecks)
			}
		})
	}
}

func TestRepoCIRequiredCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {AutoMerge: true}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"on", "off"} {
		cmd := newRepoSetCmd()
		cmd.SetArgs([]string{"o/r", "--ci-required", required})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		got, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		r := got.Repos["o/r"]
		if r.CIRequired != (required == "on") || !r.AutoMerge || r.Enabled {
			t.Fatalf("unexpected repo: %+v", r)
		}
	}
	for _, args := range [][]string{{"--workflow", "legacy"}, {"--auto-approve", "on"}, {"--ci-required", "true"}, {"--ci-required="}} {
		cmd := newRepoSetCmd()
		cmd.SetArgs(append([]string{"o/r"}, args...))
		if err := cmd.Execute(); err == nil {
			t.Fatalf("retired or invalid flags accepted: %v", args)
		}
	}
}

type ciErrorFake struct{ *sweepFake }

func (f ciErrorFake) GetCIStatus(string, int) (vcs.CIStatus, error) {
	return vcs.CIStatusNone, vcs.ErrNotSupported
}

func TestRequiredCIOnlySuccessMerges(t *testing.T) {
	for _, status := range []vcs.CIStatus{vcs.CIStatusSuccess, vcs.CIStatusNone, vcs.CIStatusFailure, vcs.CIStatusPending} {
		t.Run(string(status), func(t *testing.T) {
			fake := &sweepFake{ciStatus: status, openPRs: []vcs.PR{{Number: 1, HeadBranch: "fix/issue-1"}}}
			got := attemptAutoMerge(autoMergeAttempt{client: fake, repo: "o/r", repoCfg: config.Repo{CIRequired: true}, prNum: 1, issueNum: 1, ciPoll: true, quiet: true})
			want := status == vcs.CIStatusSuccess
			if got != want || (len(fake.merged) > 0) != want {
				t.Fatalf("merged=%v, want %v", got, want)
			}
		})
	}
	fake := &sweepFake{}
	if attemptAutoMerge(autoMergeAttempt{client: ciErrorFake{fake}, repoCfg: config.Repo{CIRequired: true}, ciPoll: true, quiet: true}) || len(fake.merged) > 0 {
		t.Fatal("CI lookup error permitted merge")
	}
}
