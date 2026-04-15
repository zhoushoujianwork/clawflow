// Package github implements the vcs.Client interface using the GitHub REST API v3.
package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Client calls the GitHub REST API v3.
type Client struct {
	token   string
	baseURL string // default: https://api.github.com
	http    *http.Client
}

// New returns a GitHub client. baseURL is optional (for GHE); pass "" for github.com.
func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{
		token:   token,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(method, path string, body any) ([]byte, int, error) {
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
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues?state=open&per_page=100&filter=all", owner, name)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list issues: HTTP %d: %s", status, data)
	}
	var raw []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		PullRequest *struct{} `json:"pull_request"` // present only on PRs
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var issues []vcs.Issue
	for _, r := range raw {
		if r.PullRequest != nil {
			continue // GitHub returns PRs in /issues — skip them
		}
		issue := vcs.Issue{Number: r.Number, Title: r.Title, Body: r.Body}
		for _, l := range r.Labels {
			issue.Labels = append(issue.Labels, l.Name)
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func (c *Client) ListOpenPRs(repo string) ([]vcs.PR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100", owner, name)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list PRs: HTTP %d: %s", status, data)
	}
	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Head   struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	prs := make([]vcs.PR, len(raw))
	for i, r := range raw {
		prs[i] = vcs.PR{Number: r.Number, Title: r.Title, Body: r.Body, State: r.State, HeadBranch: r.Head.Ref}
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

func (c *Client) AddLabel(repo string, issueNumber int, labels ...string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, name, issueNumber)
	_, status, err := c.do("POST", path, map[string]any{"labels": labels})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("github add label: HTTP %d", status)
	}
	return nil
}

func (c *Client) RemoveLabel(repo string, issueNumber int, labels ...string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	for _, label := range labels {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, name, issueNumber, label)
		_, status, err := c.do("DELETE", path, nil)
		if err != nil {
			return err
		}
		if status != 200 && status != 204 {
			return fmt.Errorf("github remove label %q: HTTP %d", label, status)
		}
	}
	return nil
}

func (c *Client) PostIssueComment(repo string, issueNumber int, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, name, issueNumber)
	_, status, err := c.do("POST", path, map[string]string{"body": body})
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("github post comment: HTTP %d", status)
	}
	return nil
}

func (c *Client) InitLabels(repo string, labels []vcs.Label) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	// fetch existing
	path := fmt.Sprintf("/repos/%s/%s/labels?per_page=100", owner, name)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("github list labels: HTTP %d", status)
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
		createPath := fmt.Sprintf("/repos/%s/%s/labels", owner, name)
		_, status, err := c.do("POST", createPath, map[string]string{
			"name":        l.Name,
			"color":       l.Color,
			"description": l.Desc,
		})
		if err != nil {
			return err
		}
		if status != 201 {
			return fmt.Errorf("github create label %q: HTTP %d", l.Name, status)
		}
		fmt.Printf("  [ok]   %s\n", l.Name)
	}
	return nil
}

func (c *Client) CreateIssue(repo string, title, body string) (vcs.Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return vcs.Issue{}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues", owner, name)
	data, status, err := c.do("POST", path, map[string]string{"title": title, "body": body})
	if err != nil {
		return vcs.Issue{}, err
	}
	if status != 201 {
		return vcs.Issue{}, fmt.Errorf("github create issue: HTTP %d: %s", status, data)
	}
	var raw struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.Issue{}, err
	}
	return vcs.Issue{Number: raw.Number, Title: raw.Title, Body: raw.Body}, nil
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo %q: expected owner/repo", repo)
	}
	return parts[0], parts[1], nil
}
