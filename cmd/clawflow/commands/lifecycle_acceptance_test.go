package commands

import (
	"context"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type lifecycleFake struct {
	*sweepFake
	labels []string
	events []string
}

func (f *lifecycleFake) PostIssueComment(repo string, issue int, body string) error {
	f.events = append(f.events, "comment")
	return f.sweepFake.PostIssueComment(repo, issue, body)
}

func (f *lifecycleFake) PostIssueCommentIdempotent(repo string, issue int, body, runID string) error {
	return f.PostIssueComment(repo, issue, body)
}

func (f *lifecycleFake) AddLabel(_ string, _ int, labels ...string) error {
	for _, label := range labels {
		f.events = append(f.events, "label:"+label)
	}
	f.labels = append(f.labels, labels...)
	return nil
}

func (f *lifecycleFake) RemoveLabel(_ string, _ int, labels ...string) error {
	for _, label := range labels {
		f.events = append(f.events, "remove:"+label)
	}
	return nil
}

func (f *lifecycleFake) GetCIStatus(repo string, pr int) (vcs.CIStatus, error) {
	f.events = append(f.events, "ci")
	return f.sweepFake.GetCIStatus(repo, pr)
}

func (f *lifecycleFake) MergePR(repo string, pr int) error {
	f.events = append(f.events, "merge")
	return f.sweepFake.MergePR(repo, pr)
}

func TestAutonomousLifecycleAcceptance(t *testing.T) {
	reg := operator.NewRegistry()
	if err := reg.LoadEmbedded(os.DirFS("../../../skills"), "."); err != nil {
		t.Fatal(err)
	}
	for _, status := range []vcs.CIStatus{vcs.CIStatusSuccess, vcs.CIStatusFailure, vcs.CIStatusNone} {
		t.Run(string(status), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			sub := &operator.Subject{Number: 7, State: "open", Title: "Implement this feature"}
			var op *operator.Operator
			for _, candidate := range reg.All() {
				candidate = effectiveOperator(candidate, sub)
				if candidate != nil && operator.Matches(sub, candidate) && (op == nil || operatorPriority(candidate.Name) < operatorPriority(op.Name)) {
					op = candidate
				}
			}
			if op == nil || op.Name != "implement" {
				t.Fatal("unlabeled issue did not route directly to implementation")
			}
			fake := &lifecycleFake{sweepFake: &sweepFake{ciStatus: status, openPRs: []vcs.PR{{Number: 10, HeadBranch: "fix/issue-7"}}}}
			calls := 0
			output, outcome, err := operator.Run(context.Background(), op, sub, fake, operator.RunOptions{
				Repo: "o/r", Workdir: t.TempDir(), Timeout: time.Second,
				RunFunc: func(_ context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, role string, systemPrompt ...string) (string, error) {
					calls++
					if !strings.Contains(prompt, "Implement this feature") {
						t.Fatal("provider did not receive issue context")
					}
					return "Implemented and tested. PR: https://github.com/o/r/pull/10\n<!-- clawflow:outcome=agent-implemented -->", nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || outcome != "agent-implemented" {
				t.Fatalf("provider calls=%d outcome=%q", calls, outcome)
			}
			if len(fake.comments) != 1 || !strings.Contains(fake.comments[0], "Implemented and tested") || strings.Contains(fake.comments[0], "clawflow:outcome") {
				t.Fatalf("writeback comments: %v", fake.comments)
			}
			if !reflect.DeepEqual(fake.labels, []string{"agent-implemented"}) {
				t.Fatalf("writeback labels: %v", fake.labels)
			}
			runPostAutomation(&runJob{repo: "o/r", sub: sub, op: op, client: fake, repoCfg: config.Repo{AutoMerge: true, CIRequired: true}}, outcome, output, "test")
			want := []string{"comment", "label:agent-implemented", "ci"}
			if status == vcs.CIStatusSuccess {
				want = append(want, "merge")
			} else {
				want = append(want, "comment")
			}
			if !reflect.DeepEqual(fake.events, want) {
				t.Fatalf("lifecycle events=%v want=%v", fake.events, want)
			}
			if (len(fake.merged) == 1) != (status == vcs.CIStatusSuccess) {
				t.Fatalf("CI %s merged PRs: %v", status, fake.merged)
			}
			if len(fake.merged) == 1 && fake.merged[0] != 10 {
				t.Fatalf("wrong PR merged: %v", fake.merged)
			}
		})
	}
}
