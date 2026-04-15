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
