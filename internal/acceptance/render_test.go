package acceptance

import (
	"context"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"gopkg.in/yaml.v3"
	"os"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestRenderReviewEvidence(t *testing.T) {
	for _, tc := range []struct{ name, head, checkHead, nameCheck, app, state, conclusion, want string }{
		{"success", "head", "head", "Web render", "github-actions", "completed", "success", "passed"},
		{"failure", "head", "head", "Web render", "github-actions", "completed", "failure", "failed"},
		{"timeout", "head", "head", "Web render", "github-actions", "completed", "timed_out", "failed"},
		{"missing", "head", "head", "Test", "github-actions", "completed", "success", "blocked"},
		{"stale observation", "old", "head", "Web render", "github-actions", "completed", "success", "blocked"},
		{"stale check", "head", "old", "Web render", "github-actions", "completed", "success", "blocked"},
		{"pending", "head", "head", "Web render", "github-actions", "in_progress", "", "blocked"},
		{"skipped", "head", "head", "Web render", "github-actions", "completed", "skipped", "blocked"},
		{"cancelled", "head", "head", "Web render", "github-actions", "completed", "cancelled", "blocked"},
		{"untrusted app", "head", "head", "Web render", "other", "completed", "success", "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := vcs.CICheck{ID: 123, Name: tc.nameCheck, HeadSHA: tc.checkHead, Status: tc.state, Conclusion: &tc.conclusion}
			check.App.Slug = tc.app
			ci := vcs.CIEvidence{HeadSHA: tc.head, Checks: []vcs.CICheck{check}}
			got := renderReviewInstruction("head", ci)
			for _, want := range []string{"Authoritative exact-head Web render status: " + tc.want, "Do not launch Chromium", "1440px/390px", "grouping and interaction", "integration execution proof", "never produce infrastructure_error", "web/scripts/check-*-render.mjs"} {
				if !strings.Contains(got, want) {
					t.Fatalf("missing %q: %s", want, got)
				}
			}
			ci.Checks = append(ci.Checks, check)
			if !strings.Contains(renderReviewInstruction("head", ci), "status: blocked") {
				t.Fatal("ambiguous checks accepted")
			}
		})
	}
}

// Pin the evidence mapping to the actual PR workflow, not just prompt wording.
func TestRenderWorkflow(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			PullRequest any `yaml:"pull_request"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Name   string `yaml:"name"`
			RunsOn string `yaml:"runs-on"`
			Steps  []struct {
				With             map[string]string `yaml:"with"`
				WorkingDirectory string            `yaml:"working-directory"`
				Run              string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["web-render"]
	if workflow.On.PullRequest == nil || job.Name != "Web render" || job.RunsOn != "ubuntu-latest" {
		t.Fatal("missing dedicated PR render job")
	}
	if len(job.Steps) == 0 || job.Steps[0].With["ref"] != "${{ github.event.pull_request.head.sha }}" {
		t.Fatal("render job does not check out exact PR head")
	}
	if job.Steps[len(job.Steps)-1].WorkingDirectory != "web" {
		t.Fatal("render fixtures require the web working directory")
	}
	var commands string
	for _, step := range job.Steps {
		commands += step.Run + "\n"
	}
	for _, want := range []string{"--frozen-lockfile", "pnpm --dir web build", "install --with-deps chromium", "for fixture in scripts/check-*-render.mjs", `node "$fixture"`} {
		if !strings.Contains(commands, want) {
			t.Fatalf("render workflow missing %q", want)
		}
	}
}

func TestRunnerIncludesRenderEvidenceWithoutRequiredCI(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprint(required), func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			r.CIRequired = required
			conclusion := "success"
			check := vcs.CICheck{ID: 123, Name: "Web render", HeadSHA: "head", Status: "completed", Conclusion: &conclusion}
			check.App.Slug = "github-actions"
			r.CI = func() (vcs.CIEvidence, error) {
				// Another job is pending: render proof remains usable independently.
				return vcs.CIEvidence{HeadSHA: "head", Status: vcs.CIStatusPending, CheckIDs: []int64{123}, Checks: []vcs.CICheck{check}}, nil
			}
			base := r.Execute
			seen := false
			r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
				if opts.Workspace == workspace {
					seen = true
					if !strings.Contains(prompt, "Authoritative exact-head Web render status: passed") || !strings.Contains(prompt, `"id":123`) {
						t.Fatal("reviewer missing independent render execution proof")
					}
				}
				return base(ctx, prompt, opts)
			}
			if _, err := r.Run(context.Background(), contract(), scratch, workspace); err != nil {
				t.Fatal(err)
			}
			if !seen {
				t.Fatal("reviewer did not execute")
			}
		})
	}
}
