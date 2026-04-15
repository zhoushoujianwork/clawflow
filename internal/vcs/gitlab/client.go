// Package gitlab implements the vcs.Client interface using the GitLab REST API v4.
// Compatible with GitLab 11.11 (self-hosted).
package gitlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Client calls the GitLab REST API v4.
type Client struct {
	token   string
	baseURL string // e.g. https://gitlab.company.com/api/v4
	http    *http.Client
}

// New returns a GitLab client. baseURL should be the GitLab instance root,
// e.g. "https://gitlab.company.com". The /api/v4 prefix is appended automatically.
func New(token, instanceURL string) *Client {
	base := strings.TrimRight(instanceURL, "/") + "/api/v4"
	return &Client{
		token:   token,
		baseURL: base,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// projectID URL-encodes "namespace/repo" for use in API paths.
func projectID(repo string) string {
	return url.PathEscape(repo)
}

func (c *Client) do(method, path string, body url.Values) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		r = strings.NewReader(body.Encode())
	}
	req, err := http.NewRequest(method, c.baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func (c *Client) doJSON(method, path string, body any) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

func (c *Client) ListOpenIssues(repo string) ([]vcs.Issue, error) {
	path := fmt.Sprintf("/projects/%s/issues?state=opened&per_page=100", projectID(repo))
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list issues: HTTP %d: %s", status, data)
	}
	// GitLab 11.11: labels is []string
	var raw []struct {
		IID    int      `json:"iid"`
		Title  string   `json:"title"`
		Body   string   `json:"description"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	issues := make([]vcs.Issue, len(raw))
	for i, r := range raw {
		issues[i] = vcs.Issue{Number: r.IID, Title: r.Title, Body: r.Body, Labels: r.Labels}
	}
	return issues, nil
}

func (c *Client) ListOpenPRs(repo string) ([]vcs.PR, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests?state=opened&per_page=100", projectID(repo))
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list MRs: HTTP %d: %s", status, data)
	}
	var raw []struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		State        string `json:"state"`
		SourceBranch string `json:"source_branch"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	prs := make([]vcs.PR, len(raw))
	for i, r := range raw {
		prs[i] = vcs.PR{Number: r.IID, Title: r.Title, Body: r.Description, State: r.State, HeadBranch: r.SourceBranch}
	}
	return prs, nil
}

func (c *Client) PRExistsForIssue(repo string, issueNumber int) (bool, error) {
	prs, err := c.ListOpenPRs(repo)
	if err != nil {
		return false, err
	}
	needle := fmt.Sprintf("issue-%d", issueNumber)
	fixes := fmt.Sprintf("Fixes #%d", issueNumber)
	for _, pr := range prs {
		if strings.Contains(pr.HeadBranch, needle) || strings.Contains(pr.Body, fixes) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) getIssueLabels(repo string, issueNumber int) ([]string, error) {
	path := fmt.Sprintf("/projects/%s/issues/%d", projectID(repo), issueNumber)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab get issue: HTTP %d: %s", status, data)
	}
	var raw struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw.Labels, nil
}

func (c *Client) AddLabel(repo string, issueNumber int, labels ...string) error {
	current, err := c.getIssueLabels(repo, issueNumber)
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(current))
	for _, l := range current {
		existing[l] = true
	}
	for _, l := range labels {
		existing[l] = true
	}
	all := make([]string, 0, len(existing))
	for l := range existing {
		all = append(all, l)
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", projectID(repo), issueNumber)
	form := url.Values{"labels": {strings.Join(all, ",")}}
	data, status, err := c.do("PUT", path, form)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("gitlab add label: HTTP %d: %s", status, data)
	}
	return nil
}

func (c *Client) RemoveLabel(repo string, issueNumber int, labels ...string) error {
	current, err := c.getIssueLabels(repo, issueNumber)
	if err != nil {
		return err
	}
	remove := make(map[string]bool, len(labels))
	for _, l := range labels {
		remove[l] = true
	}
	var remaining []string
	for _, l := range current {
		if !remove[l] {
			remaining = append(remaining, l)
		}
	}
	path := fmt.Sprintf("/projects/%s/issues/%d", projectID(repo), issueNumber)
	form := url.Values{"labels": {strings.Join(remaining, ",")}}
	data, status, err := c.do("PUT", path, form)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("gitlab remove label: HTTP %d: %s", status, data)
	}
	return nil
}

func (c *Client) ListIssueComments(repo string, issueNumber int) ([]string, error) {
	path := fmt.Sprintf("/projects/%s/issues/%d/notes?per_page=100", projectID(repo), issueNumber)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list comments: HTTP %d: %s", status, data)
	}
	var raw []struct {
		Body   string `json:"body"`
		System bool   `json:"system"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var out []string
	for _, r := range raw {
		if !r.System {
			out = append(out, r.Body)
		}
	}
	return out, nil
}

func (c *Client) PostIssueComment(repo string, issueNumber int, body string) error {
	path := fmt.Sprintf("/projects/%s/issues/%d/notes", projectID(repo), issueNumber)
	form := url.Values{"body": {body}}
	_, status, err := c.do("POST", path, form)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("gitlab post comment: HTTP %d", status)
	}
	return nil
}

func (c *Client) CreateIssue(repo string, title, body string) (vcs.Issue, error) {
	path := fmt.Sprintf("/projects/%s/issues", projectID(repo))
	form := url.Values{"title": {title}, "description": {body}}
	data, status, err := c.do("POST", path, form)
	if err != nil {
		return vcs.Issue{}, err
	}
	if status != 201 {
		return vcs.Issue{}, fmt.Errorf("gitlab create issue: HTTP %d: %s", status, data)
	}
	var raw struct {
		IID   int    `json:"iid"`
		Title string `json:"title"`
		Body  string `json:"description"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.Issue{}, err
	}
	return vcs.Issue{Number: raw.IID, Title: raw.Title, Body: raw.Body}, nil
}

func (c *Client) ListIssues(repo string, state string, labels []string) ([]vcs.Issue, error) {
	// GitLab uses "opened"/"closed" instead of "open"/"closed"
	glState := state
	if state == "open" {
		glState = "opened"
	}
	path := fmt.Sprintf("/projects/%s/issues?state=%s&per_page=100", projectID(repo), glState)
	if len(labels) > 0 {
		path += "&labels=" + strings.Join(labels, ",")
	}
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list issues: HTTP %d: %s", status, data)
	}
	var raw []struct {
		IID    int      `json:"iid"`
		Title  string   `json:"title"`
		Body   string   `json:"description"`
		State  string   `json:"state"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	issues := make([]vcs.Issue, len(raw))
	for i, r := range raw {
		s := r.State
		if s == "opened" {
			s = "open"
		}
		issues[i] = vcs.Issue{Number: r.IID, Title: r.Title, Body: r.Body, State: s, Labels: r.Labels}
	}
	return issues, nil
}

func (c *Client) CloseIssue(repo string, issueNumber int) error {
	path := fmt.Sprintf("/projects/%s/issues/%d", projectID(repo), issueNumber)
	form := url.Values{"state_event": {"close"}}
	data, status, err := c.do("PUT", path, form)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("gitlab close issue: HTTP %d: %s", status, data)
	}
	return nil
}

func (c *Client) ListPRs(repo string, state string) ([]vcs.PR, error) {
	glState := state
	if state == "open" {
		glState = "opened"
	} else if state == "merged" {
		glState = "merged"
	}
	path := fmt.Sprintf("/projects/%s/merge_requests?state=%s&per_page=100", projectID(repo), glState)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab list MRs: HTTP %d: %s", status, data)
	}
	var raw []struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		State        string `json:"state"`
		WebURL       string `json:"web_url"`
		MergedAt     string `json:"merged_at"`
		SourceBranch string `json:"source_branch"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	prs := make([]vcs.PR, len(raw))
	for i, r := range raw {
		s := r.State
		if s == "opened" {
			s = "open"
		}
		prs[i] = vcs.PR{Number: r.IID, Title: r.Title, Body: r.Description, State: s, HeadBranch: r.SourceBranch, MergedAt: r.MergedAt, URL: r.WebURL}
	}
	return prs, nil
}

func (c *Client) CreatePR(repo string, opts vcs.PRCreateOpts) (vcs.PR, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests", projectID(repo))
	form := url.Values{
		"title":         {opts.Title},
		"description":   {opts.Body},
		"source_branch": {opts.Head},
		"target_branch": {opts.Base},
	}
	data, status, err := c.do("POST", path, form)
	if err != nil {
		return vcs.PR{}, err
	}
	if status != 201 {
		return vcs.PR{}, fmt.Errorf("gitlab create MR: HTTP %d: %s", status, data)
	}
	var raw struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		State        string `json:"state"`
		WebURL       string `json:"web_url"`
		SourceBranch string `json:"source_branch"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.PR{}, err
	}
	s := raw.State
	if s == "opened" {
		s = "open"
	}
	return vcs.PR{Number: raw.IID, Title: raw.Title, State: s, HeadBranch: raw.SourceBranch, URL: raw.WebURL}, nil
}

func (c *Client) GetPR(repo string, prNumber int) (vcs.PR, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d", projectID(repo), prNumber)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return vcs.PR{}, err
	}
	if status != 200 {
		return vcs.PR{}, fmt.Errorf("gitlab get MR: HTTP %d: %s", status, data)
	}
	var raw struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		State        string `json:"state"`
		WebURL       string `json:"web_url"`
		MergedAt     string `json:"merged_at"`
		SourceBranch string `json:"source_branch"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.PR{}, err
	}
	s := raw.State
	if s == "opened" {
		s = "open"
	}
	return vcs.PR{Number: raw.IID, Title: raw.Title, Body: raw.Description, State: s, HeadBranch: raw.SourceBranch, MergedAt: raw.MergedAt, URL: raw.WebURL}, nil
}

func (c *Client) PostPRComment(repo string, prNumber int, body string) error {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/notes", projectID(repo), prNumber)
	form := url.Values{"body": {body}}
	_, status, err := c.do("POST", path, form)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("gitlab post MR comment: HTTP %d", status)
	}
	return nil
}

func (c *Client) GetCIStatus(repo string, prNumber int) (vcs.CIStatus, error) {
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/pipelines?per_page=1", projectID(repo), prNumber)
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return vcs.CIStatusNone, err
	}
	if status != 200 {
		return vcs.CIStatusNone, fmt.Errorf("gitlab get MR pipelines: HTTP %d", status)
	}
	var raw []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.CIStatusNone, err
	}
	if len(raw) == 0 {
		return vcs.CIStatusNone, nil
	}
	switch raw[0].Status {
	case "success":
		return vcs.CIStatusSuccess, nil
	case "failed", "canceled":
		return vcs.CIStatusFailure, nil
	default:
		return vcs.CIStatusPending, nil
	}
}

func (c *Client) InitLabels(repo string, labels []vcs.Label) error {
	path := fmt.Sprintf("/projects/%s/labels?per_page=100", projectID(repo))
	data, status, err := c.doJSON("GET", path, nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("gitlab list labels: HTTP %d", status)
	}
	var existing []struct{ Name string `json:"name"` }
	if err := json.Unmarshal(data, &existing); err != nil {
		return err
	}
	has := make(map[string]bool, len(existing))
	for _, l := range existing {
		has[l.Name] = true
	}
	for _, l := range labels {
		if has[l.Name] {
			fmt.Printf("  [skip] %s\n", l.Name)
			continue
		}
		createPath := fmt.Sprintf("/projects/%s/labels", projectID(repo))
		form := url.Values{
			"name":        {l.Name},
			"color":       {"#" + l.Color},
			"description": {l.Desc},
		}
		_, status, err := c.do("POST", createPath, form)
		if err != nil {
			return err
		}
		if status != 201 {
			return fmt.Errorf("gitlab create label %q: HTTP %d", l.Name, status)
		}
		fmt.Printf("  [ok]   %s\n", l.Name)
	}
	return nil
}
