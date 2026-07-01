package gitlab

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// GitLab CI/CD read helpers: runners, pipelines, jobs and job logs (traces).
//
// These are GitLab-specific — GitHub Actions has no equivalent object model —
// so they live on the concrete *gitlab.Client rather than the platform-agnostic
// vcs.Client interface. The `clawflow ci` commands call them directly after
// asserting the target repo is a GitLab project.

// Runner is a CI/CD runner registered to (or shared with) a project.
type Runner struct {
	ID          int64    `json:"id"`
	Description string   `json:"description"`
	Name        string   `json:"name"`
	Active      bool     `json:"active"`
	Paused      bool     `json:"paused"`
	IsShared    bool     `json:"is_shared"`
	RunnerType  string   `json:"runner_type"`
	Online      bool     `json:"online"`
	Status      string   `json:"status"`
	IPAddress   string   `json:"ip_address"`
	TagList     []string `json:"tag_list"`
}

// Pipeline is a single CI/CD pipeline run.
type Pipeline struct {
	ID        int64  `json:"id"`
	IID       int64  `json:"iid"`
	Ref       string `json:"ref"`
	SHA       string `json:"sha"`
	Status    string `json:"status"`
	Source    string `json:"source"`
	WebURL    string `json:"web_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Duration  int    `json:"duration"`
}

// Job is a single job within a pipeline.
type Job struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Stage      string  `json:"stage"`
	Status     string  `json:"status"`
	Ref        string  `json:"ref"`
	WebURL     string  `json:"web_url"`
	CreatedAt  string  `json:"created_at"`
	StartedAt  string  `json:"started_at"`
	FinishedAt string  `json:"finished_at"`
	Duration   float64 `json:"duration"`
	Runner     *Runner `json:"runner"`
}

// PipelineListOpts filters ListPipelines. Empty fields are omitted.
type PipelineListOpts struct {
	Ref    string // branch/tag name
	Status string // e.g. "success", "failed", "running", "pending"
	Limit  int    // caps results, clamped to [1, 100]
}

// ListProjectRunners returns the runners available to the project
// (project-specific plus shared/group runners).
func (c *Client) ListProjectRunners(repo string) ([]Runner, error) {
	path := fmt.Sprintf("/projects/%s/runners?per_page=100", projectID(repo))
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list runners: HTTP %d: %s", status, data)
	}
	var runners []Runner
	if err := json.Unmarshal(data, &runners); err != nil {
		return nil, err
	}
	return runners, nil
}

// ListPipelines returns pipelines for the project, newest first, filtered by opts.
func (c *Client) ListPipelines(repo string, opts PipelineListOpts) ([]Pipeline, error) {
	limit := opts.Limit
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := url.Values{}
	q.Set("per_page", fmt.Sprintf("%d", limit))
	q.Set("order_by", "id")
	q.Set("sort", "desc")
	if opts.Ref != "" {
		q.Set("ref", opts.Ref)
	}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	path := fmt.Sprintf("/projects/%s/pipelines?%s", projectID(repo), q.Encode())
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list pipelines: HTTP %d: %s", status, data)
	}
	var pipelines []Pipeline
	if err := json.Unmarshal(data, &pipelines); err != nil {
		return nil, err
	}
	return pipelines, nil
}

// GetPipeline returns a single pipeline by its project-wide id.
func (c *Client) GetPipeline(repo string, pipelineID int64) (Pipeline, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d", projectID(repo), pipelineID)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return Pipeline{}, err
	}
	if status != 200 {
		return Pipeline{}, fmt.Errorf("gitlab get pipeline %d: HTTP %d: %s", pipelineID, status, data)
	}
	var p Pipeline
	if err := json.Unmarshal(data, &p); err != nil {
		return Pipeline{}, err
	}
	return p, nil
}

// ListPipelineJobs returns the jobs of a pipeline.
func (c *Client) ListPipelineJobs(repo string, pipelineID int64) ([]Job, error) {
	path := fmt.Sprintf("/projects/%s/pipelines/%d/jobs?per_page=100", projectID(repo), pipelineID)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list pipeline jobs: HTTP %d: %s", status, data)
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

// GetJobLog returns the raw trace (console log) of a job. The trace endpoint
// returns plain text, not JSON. An empty trace (job not yet started) yields "".
func (c *Client) GetJobLog(repo string, jobID int64) (string, error) {
	path := fmt.Sprintf("/projects/%s/jobs/%d/trace", projectID(repo), jobID)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return "", err
	}
	if status == 404 {
		return "", fmt.Errorf("gitlab job %d not found (or has no trace)", jobID)
	}
	if status != 200 {
		return "", fmt.Errorf("gitlab get job trace: HTTP %d: %s", status, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}
