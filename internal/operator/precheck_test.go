package operator

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestImplementPrecheck(t *testing.T) {
	for _, outcome := range []string{"proceed", "already-done", "blocked-dependency", "waiting", "error", "invalid", "missing-evidence"} {
		t.Run(outcome, func(t *testing.T) {
			dir := t.TempDir()
			for _, args := range [][]string{{"init"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base"}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git: %s: %v", out, err)
				}
			}
			calls, triages := 0, 0
			v := newFakeVCS()
			body, label, err := Run(context.Background(), &Operator{Name: "implement"}, &Subject{Number: 498, Body: "requirements"}, v, RunOptions{
				Workdir: dir, Comments: []string{"owner update"},
				PrecheckFunc: func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
					triages++
					if !opts.ToolsDisabled || opts.Role != "light" || opts.Timeout > time.Minute || !strings.Contains(prompt, "owner update") || !strings.Contains(prompt, "requirements") || !strings.Contains(prompt, "git log") {
						t.Fatalf("unsafe or incomplete precheck: %+v %s", opts, prompt)
					}
					if outcome == "error" {
						return execution.Result{}, errors.New("provider failure")
					}
					if outcome == "invalid" {
						return execution.Result{Text: "not JSON"}, nil
					}
					if outcome == "missing-evidence" {
						return execution.Result{Text: `{"outcome":"waiting"}`}, nil
					}
					return execution.Result{Text: `{"outcome":"` + outcome + `","evidence":"explicit source evidence"}`}, nil
				},
				RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
					calls++
					return "implemented\n<!-- clawflow:outcome=agent-implemented -->", nil
				},
			})
			skip := outcome == "already-done" || outcome == "blocked-dependency" || outcome == "waiting"
			want := "agent-implemented"
			if skip {
				want = "agent-skipped"
			}
			if err != nil || label != want || triages != 1 || (calls == 0) != skip || len(v.comments) != 1 || len(v.labels[498]) != 1 {
				t.Fatalf("body=%s label=%s err=%v calls=%d triages=%d comments=%v labels=%v", body, label, err, calls, triages, v.comments, v.labels)
			}
			if skip && !strings.Contains(body, "explicit source evidence") {
				t.Fatal("missing evidence")
			}
		})
	}
}
