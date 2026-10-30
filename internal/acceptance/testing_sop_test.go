package acceptance

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestDevelopmentAndReviewSharePrimaryTestingSOP(t *testing.T) {
	for _, mode := range []string{"full-suite", "ci-verified", "structured", "structured-ci"} {
		for _, state := range []string{"primary", "fallback", "no-project", "no-document", "empty-document"} {
			t.Run(mode+"/"+state, func(t *testing.T) {
				r, scratch, workspace := setupRunner(t)
				want := ""
				if state != "no-project" {
					for _, name := range []string{"alpha", "zebra"} {
						if _, err := project.Create(name); err != nil {
							t.Fatal(err)
						}
						if err := project.AddRepo(name, "owner/repo"); err != nil {
							t.Fatal(err)
						}
						for path, text := range map[string]string{
							project.TestingPath(name):    "fixture-sop-" + name,
							project.ContextPath(name):    "developer-answer-do-not-share",
							project.DeploymentPath(name): "deployment-do-not-share",
						} {
							if err := os.WriteFile(path, []byte(text), 0600); err != nil {
								t.Fatal(err)
							}
						}
					}
					primary := "zebra"
					if state == "fallback" {
						primary = "alpha"
					} else {
						cfg := &config.Config{Repos: map[string]config.Repo{"owner/repo": {PrimaryProject: primary}}}
						if err := cfg.Save(); err != nil {
							t.Fatal(err)
						}
					}
					want = "fixture-sop-" + primary
					if state == "no-document" {
						if err := os.Remove(project.TestingPath(primary)); err != nil {
							t.Fatal(err)
						}
						want = ""
					} else if state == "empty-document" {
						if err := os.WriteFile(project.TestingPath(primary), []byte(" \n"), 0600); err != nil {
							t.Fatal(err)
						}
						want = ""
					}
				}
				development := operator.BuildSystemPrompt(&operator.Operator{Name: "implement", Prompt: "task"}, "owner/repo", "")
				assertSOP := func(prompt string) {
					t.Helper()
					if want != "" && !strings.Contains(prompt, want) {
						t.Fatal("missing primary SOP")
					}
					for _, marker := range []string{"fixture-sop-alpha", "fixture-sop-zebra"} {
						if marker != want && strings.Contains(prompt, marker) {
							t.Fatal("received another project's SOP")
						}
					}
				}
				assertSOP(development)
				sharedHeader := project.TestingHeaderForRepo("owner/repo")
				if want != "" {
					_, section, ok := strings.Cut(sharedHeader, "\n\n")
					if !ok || !strings.Contains(development, strings.TrimSuffix(section, "---\n\n")) {
						t.Fatal("development and review SOP sections differ")
					}
				}
				if want == "" && project.TestingHeaderForRepo("owner/repo") != "" {
					t.Fatal("missing SOP changed legacy review input")
				}
				c := contract()
				fixture := structuredRecord()
				if strings.HasPrefix(mode, "structured") {
					c = fixture.Contract
				}
				if mode == "ci-verified" || mode == "structured-ci" {
					r.CIRequired = true
					r.CI = func() (vcs.CIEvidence, error) {
						return vcs.CIEvidence{HeadSHA: c.HeadSHA, Status: vcs.CIStatusSuccess, CheckIDs: []int64{1}}, nil
					}
				}
				base := r.Execute
				plans, reviews := 0, 0
				r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
					if opts.ToolsDisabled {
						plans++
						if strings.Contains(prompt, "fixture-sop-") || strings.Contains(prompt, "developer-answer") || strings.Contains(prompt, "deployment-do-not-share") {
							t.Fatal("planner received machine or development context")
						}
					} else {
						reviews++
						assertSOP(prompt)
						if !strings.Contains(prompt, "Authoritative exact-head Web render status: blocked") || !strings.Contains(prompt, "Do not launch Chromium") {
							t.Fatal("shared SOP displaced exact-head render policy")
						}
						if strings.Contains(prompt, "developer-answer") || strings.Contains(prompt, "deployment-do-not-share") {
							t.Fatal("review received development context")
						}
					}
					if !strings.HasPrefix(mode, "structured") {
						return base(ctx, prompt, opts)
					}
					if opts.ToolsDisabled {
						result := successful("planner", "plan-run")
						result.Text = fixture.Plan.Text
						return result, nil
					}
					result := reviewerResult("reviewer", "review-run")
					body, err := json.Marshal(map[string]any{"status": "passed", "summary": "verified", "evidence": fixture.Evidence, "findings": fixture.Findings, "checks": fixture.Checks, "next_owner": fixture.NextOwner, "next_action": fixture.NextAction})
					result.Text = string(body)
					return result, err
				}
				record, err := r.Run(context.Background(), c, scratch, workspace)
				if err != nil || record.Verdict != Passed || plans != 1 || reviews != 1 {
					t.Fatalf("review failed: verdict=%s plans=%d reviews=%d err=%v", record.Verdict, plans, reviews, err)
				}
				if err := Evaluate(c, record); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
