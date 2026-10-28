package pilot

import (
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

type rerunClient struct {
	actionClient
	pr      vcs.PR
	plan    vcs.CIRerunPlan
	posts   []int64
	postErr error
	changed bool
	gets    int
}

func (c *rerunClient) GetPR(string, int) (vcs.PR, error) {
	c.gets++
	p := c.pr
	if c.changed && c.gets > 1 {
		p.HeadSHA = strings.Repeat("b", 40)
	}
	return p, nil
}
func (c *rerunClient) PlanCIRerun(string, int) (vcs.CIRerunPlan, error) { return c.plan, nil }
func (c *rerunClient) RerunFailedJobs(_ string, id int64) error {
	c.posts = append(c.posts, id)
	return c.postErr
}

func TestRerunAction(t *testing.T) {
	for _, mode := range []string{"success", "post_failed", "changed", "unmanaged", "external_only", "gitlab", "dry_run"} {
		t.Run(mode, func(t *testing.T) {
			e, base := testExecutor(t)
			base.issue = vcs.Issue{Number: 1, State: "open", Labels: []string{"agent-implemented"}}
			head := strings.Repeat("a", 40)
			c := &rerunClient{actionClient: *base, pr: vcs.PR{Number: 11, HeadSHA: head, HeadRepo: "o/r", HeadBranch: "fix/issue-1", State: "open", Body: "Fixes #1"}, plan: vcs.CIRerunPlan{HeadSHA: head, RunIDs: []int64{100, 200}, NotRerunnable: []int64{3}}}
			e.Client = func(string) (vcs.Client, error) { return c, nil }
			want := ""
			switch mode {
			case "post_failed":
				c.postErr = errors.New("request failed")
				want = "vcs_error"
			case "changed":
				c.changed = true
				want = "head_changed"
			case "unmanaged":
				c.pr.HeadBranch = "someone-else"
				want = "unmanaged_pr"
			case "external_only":
				c.plan.RunIDs = nil
				want = "no_rerunnable_checks"
			case "gitlab":
				e.Client = func(string) (vcs.Client, error) { return gitlab.New("token", "https://example.invalid"), nil }
				want = "unsupported_platform"
			}
			records, err := e.Apply(actionSet("first", `{"id":"rerun","type":"rerun_ci","pr":11}`), mode == "dry_run")
			if err != nil {
				t.Fatal(err)
			}
			r := records[0]
			if want != "" {
				if r.Outcome != "failed" || r.Reason != want {
					t.Fatalf("%+v", r)
				}
			} else if mode == "dry_run" {
				if r.Outcome != "valid" || c.gets != 0 {
					t.Fatalf("%+v", r)
				}
			} else {
				if r.Outcome != "applied" || r.CIRerun == nil || len(r.CIRerun.RerunIDs) != 2 || len(r.CIRerun.NotRerunnable) != 1 {
					t.Fatalf("%+v", r)
				}
			}
			if mode == "success" || mode == "post_failed" {
				if len(c.posts) != 2 {
					t.Fatalf("did not attempt every selected run: %v", c.posts)
				}
				before := len(c.posts)
				records, err = e.Apply(actionSet("second", `{"id":"rerun2","type":"rerun_ci","pr":11}`), false)
				if err != nil || records[0].Reason != "already_rerun_for_head" || len(c.posts) != before {
					t.Fatalf("%+v %v posts=%v", records, err, c.posts)
				}
			} else if len(c.posts) != 0 {
				t.Fatal(c.posts)
			}
		})
	}
}

func TestRerunActionSchema(t *testing.T) {
	for _, raw := range []string{`{"id":"r","type":"rerun_ci"}`, `{"id":"r","type":"rerun_ci","pr":-1}`, `{"id":"r","type":"rerun_ci","pr":11,"issue":1}`} {
		if _, err := validateAction([]byte(raw)); err == nil {
			t.Fatal(raw)
		}
	}
}
