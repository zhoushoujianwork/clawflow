package github

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// PlanCIRerun uses only checks selected by the existing CI aggregate. Workflow
// runs outside those suites never become rerun candidates.
func (c *Client) PlanCIRerun(repo string, prNumber int) (vcs.CIRerunPlan, error) {
	var plan vcs.CIRerunPlan
	evidence, err := c.GetCIEvidence(repo, prNumber)
	if err != nil {
		return plan, err
	}
	plan.HeadSHA = evidence.HeadSHA
	suites := map[int64]bool{}
	for _, check := range evidence.Checks {
		if check.Status == "completed" && check.Conclusion != nil && *check.Conclusion != "success" && check.App.Slug == "github-actions" && check.CheckSuite.ID > 0 {
			suites[check.CheckSuite.ID] = true
		}
	}
	matched := map[int64]bool{}
	if len(suites) > 0 {
		owner, name, err := splitRepo(repo)
		if err != nil {
			return plan, err
		}
		total := -1
		seen := map[int64]bool{}
		for page := 1; page <= 100; page++ {
			path := fmt.Sprintf("/repos/%s/%s/actions/runs?head_sha=%s&per_page=100&page=%d", owner, name, url.QueryEscape(plan.HeadSHA), page)
			data, status, err := c.do("GET", path, nil)
			if err != nil {
				return plan, err
			}
			if status != 200 {
				return plan, fmt.Errorf("github list workflow runs: HTTP %d", status)
			}
			var raw struct {
				Total *int `json:"total_count"`
				Runs  []struct {
					ID         int64  `json:"id"`
					Head       string `json:"head_sha"`
					Suite      int64  `json:"check_suite_id"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
				} `json:"workflow_runs"`
			}
			if err := json.Unmarshal(data, &raw); err != nil {
				return plan, err
			}
			if raw.Total == nil || *raw.Total < 0 || *raw.Total > 10000 || raw.Runs == nil {
				return plan, fmt.Errorf("invalid workflow pagination")
			}
			if total < 0 {
				total = *raw.Total
			}
			expected := min(100, total-len(seen))
			if total != *raw.Total || len(raw.Runs) != expected {
				return plan, fmt.Errorf("inconsistent workflow pagination")
			}
			for _, run := range raw.Runs {
				if run.ID <= 0 || seen[run.ID] || run.Head != plan.HeadSHA {
					return plan, fmt.Errorf("invalid workflow identity or revision")
				}
				seen[run.ID] = true
				if suites[run.Suite] && run.Status == "completed" && (run.Conclusion == "failure" || run.Conclusion == "timed_out" || run.Conclusion == "cancelled") {
					plan.RunIDs = append(plan.RunIDs, run.ID)
					matched[run.Suite] = true
				}
			}
			if len(seen) == total {
				break
			}
		}
	}
	for _, check := range evidence.Checks {
		if check.Conclusion != nil && *check.Conclusion == "success" {
			continue
		}
		if check.Status != "completed" || check.App.Slug != "github-actions" || !matched[check.CheckSuite.ID] {
			plan.NotRerunnable = append(plan.NotRerunnable, check.ID)
		}
	}
	return plan, nil
}

func (c *Client) RerunFailedJobs(repo string, runID int64) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if runID <= 0 {
		return fmt.Errorf("invalid workflow run ID")
	}
	_, status, err := c.do("POST", fmt.Sprintf("/repos/%s/%s/actions/runs/%d/rerun-failed-jobs", owner, name, runID), nil)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("github rerun failed jobs: HTTP %d", status)
	}
	return nil
}
