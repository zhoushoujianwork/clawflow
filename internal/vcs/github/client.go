// Package github implements the vcs.Client interface using the GitHub REST API v3.
package github

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
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		CreatedAt   string    `json:"created_at"`
		UpdatedAt   string    `json:"updated_at"`
		ClosedAt    string    `json:"closed_at"`
		PullRequest *struct{} `json:"pull_request"`
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
		issue := vcs.Issue{Number: r.Number, Title: r.Title, Body: r.Body, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt}
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
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		Head      struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	prs := make([]vcs.PR, len(raw))
	for i, r := range raw {
		prs[i] = vcs.PR{Number: r.Number, Title: r.Title, Body: r.Body, State: r.State, HeadBranch: r.Head.Ref, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
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

func (c *Client) GetIssueLabels(repo string, issueNumber int) ([]string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, name, issueNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github get issue labels: HTTP %d: %s", status, data)
	}
	var raw []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	labels := make([]string, len(raw))
	for i, l := range raw {
		labels[i] = l.Name
	}
	return labels, nil
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

func (c *Client) ListIssueComments(repo string, issueNumber int) ([]string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, name, issueNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list comments: HTTP %d: %s", status, data)
	}
	var raw []struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = r.Body
	}
	return out, nil
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

func (c *Client) ListIssueCommentsDetail(repo string, issueNumber int) ([]vcs.IssueComment, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, name, issueNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list comments: HTTP %d: %s", status, data)
	}
	var raw []struct {
		ID        int64  `json:"id"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]vcs.IssueComment, len(raw))
	for i, r := range raw {
		out[i] = vcs.IssueComment{ID: r.ID, Author: r.User.Login, Body: r.Body, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

func (c *Client) DeleteIssueComment(repo string, _ int, commentID int64) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, name, commentID)
	_, status, err := c.do("DELETE", path, nil)
	if err != nil {
		return err
	}
	if status != 204 {
		return fmt.Errorf("github delete comment: HTTP %d", status)
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
	var existing []struct {
		Name string `json:"name"`
	}
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
		ID     int64  `json:"id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.Issue{}, err
	}
	return vcs.Issue{ID: raw.ID, Number: raw.Number, Title: raw.Title, Body: raw.Body}, nil
}

func (c *Client) UpdateIssue(repo string, issueNumber int, update vcs.IssueUpdate) (vcs.Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return vcs.Issue{}, err
	}
	body := make(map[string]string)
	if update.Title != nil {
		body["title"] = *update.Title
	}
	if update.Body != nil {
		body["body"] = *update.Body
	}
	if len(body) == 0 {
		return vcs.Issue{}, fmt.Errorf("github update issue: no fields to update")
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, issueNumber)
	data, status, err := c.do("PATCH", path, body)
	if err != nil {
		return vcs.Issue{}, err
	}
	if status != 200 {
		return vcs.Issue{}, fmt.Errorf("github update issue: HTTP %d: %s", status, data)
	}
	var raw struct {
		ID     int64  `json:"id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.Issue{}, err
	}
	return vcs.Issue{ID: raw.ID, Number: raw.Number, Title: raw.Title, Body: raw.Body, State: raw.State}, nil
}

func (c *Client) ListIssues(repo string, state string, labels []string) ([]vcs.Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues?state=%s&per_page=100&filter=all", owner, name, state)
	if len(labels) > 0 {
		path += "&labels=" + strings.Join(labels, ",")
	}
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list issues: HTTP %d: %s", status, data)
	}
	var raw []struct {
		ID          int64     `json:"id"`
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		State       string    `json:"state"`
		CreatedAt   string    `json:"created_at"`
		UpdatedAt   string    `json:"updated_at"`
		ClosedAt    string    `json:"closed_at"`
		PullRequest *struct{} `json:"pull_request"`
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
			continue
		}
		issue := vcs.Issue{ID: r.ID, Number: r.Number, Title: r.Title, Body: r.Body, State: r.State, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt}
		for _, l := range r.Labels {
			issue.Labels = append(issue.Labels, l.Name)
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func (c *Client) CloseIssue(repo string, issueNumber int) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, issueNumber)
	_, status, err := c.do("PATCH", path, map[string]string{"state": "closed"})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("github close issue: HTTP %d", status)
	}
	return nil
}

func (c *Client) ListPRs(repo string, state string) ([]vcs.PR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&per_page=100", owner, name, state)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list PRs: HTTP %d: %s", status, data)
	}
	var raw []struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		HTMLURL   string `json:"html_url"`
		MergedAt  string `json:"merged_at"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
		Head      struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	prs := make([]vcs.PR, len(raw))
	for i, r := range raw {
		s := r.State
		if r.MergedAt != "" {
			s = "merged"
		}
		prs[i] = vcs.PR{Number: r.Number, Title: r.Title, Body: r.Body, State: s, HeadBranch: r.Head.Ref, MergedAt: r.MergedAt, URL: r.HTMLURL, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	}
	return prs, nil
}

func (c *Client) CreatePR(repo string, opts vcs.PRCreateOpts) (vcs.PR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return vcs.PR{}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls", owner, name)
	data, status, err := c.do("POST", path, map[string]string{
		"title": opts.Title,
		"body":  opts.Body,
		"head":  opts.Head,
		"base":  opts.Base,
	})
	if err != nil {
		return vcs.PR{}, err
	}
	if status != 201 {
		return vcs.PR{}, fmt.Errorf("github create PR: HTTP %d: %s", status, data)
	}
	var raw struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		State   string `json:"state"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.PR{}, err
	}
	return vcs.PR{Number: raw.Number, Title: raw.Title, State: raw.State, HeadBranch: raw.Head.Ref, URL: raw.HTMLURL}, nil
}

func (c *Client) GetPR(repo string, prNumber int) (vcs.PR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return vcs.PR{}, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, prNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return vcs.PR{}, err
	}
	if status != 200 {
		return vcs.PR{}, fmt.Errorf("github get PR: HTTP %d: %s", status, data)
	}
	var raw struct {
		Number   int    `json:"number"`
		Title    string `json:"title"`
		Body     string `json:"body"`
		State    string `json:"state"`
		HTMLURL  string `json:"html_url"`
		MergedAt string `json:"merged_at"`
		Head     struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.PR{}, err
	}
	s := raw.State
	if raw.MergedAt != "" {
		s = "merged"
	}
	return vcs.PR{Number: raw.Number, Title: raw.Title, Body: raw.Body, State: s, HeadBranch: raw.Head.Ref, MergedAt: raw.MergedAt, URL: raw.HTMLURL}, nil
}

func (c *Client) PostPRComment(repo string, prNumber int, body string) error {
	// GitHub PR comments use the issues endpoint
	return c.PostIssueComment(repo, prNumber, body)
}

func (c *Client) GetCIStatus(repo string, prNumber int) (vcs.CIStatus, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return vcs.CIStatusNone, err
	}
	// Get the PR head SHA first
	pr, err := c.GetPR(repo, prNumber)
	if err != nil {
		return vcs.CIStatusNone, err
	}
	// Get check runs for the head branch
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", owner, name, pr.HeadBranch)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return vcs.CIStatusNone, err
	}
	if status != 200 {
		return vcs.CIStatusNone, fmt.Errorf("github get check runs: HTTP %d", status)
	}
	var raw struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.CIStatusNone, err
	}
	if raw.TotalCount == 0 {
		return vcs.CIStatusNone, nil
	}
	for _, r := range raw.CheckRuns {
		if r.Status != "completed" {
			return vcs.CIStatusPending, nil
		}
		if r.Conclusion != "success" && r.Conclusion != "skipped" && r.Conclusion != "neutral" {
			return vcs.CIStatusFailure, nil
		}
	}
	return vcs.CIStatusSuccess, nil
}

// ListIssuesByBodyKeyword returns all open issues whose body contains keyword.
// It fetches all open issues and filters client-side (GitHub search API has
// indexing delays that make it unreliable for freshly-created issues).
func (c *Client) ListIssuesByBodyKeyword(repo string, keyword string) ([]vcs.Issue, error) {
	issues, err := c.ListOpenIssues(repo)
	if err != nil {
		return nil, err
	}
	var out []vcs.Issue
	for _, i := range issues {
		if strings.Contains(i.Body, keyword) {
			out = append(out, i)
		}
	}
	return out, nil
}

// SearchIssues runs GitHub's /search/issues against the given repo,
// querying title + body. state ∈ {open, closed, all}; "" defaults to
// all. limit caps results (clamped to [1, 100], the per-page max).
//
// We exclude PRs from results — the search API conflates issues and
// PRs, but the Issue type carries no PR concept and downstream code
// (operators, PM) only wants real issues for triage context.
//
// Note: GitHub's search index lags issue creation by ~1-5 minutes,
// so brand-new issues may not appear immediately. Acceptable for
// the historical-context use case (operators searching past issues
// to inform a current evaluation); not suitable for "did the PM
// just file a duplicate of itself one second ago" — which we avoid
// with cooldowns and skip-touched-issue rules instead.
func (c *Client) SearchIssues(repo, query, state string, limit int) ([]vcs.Issue, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := query + " repo:" + repo + " is:issue"
	switch state {
	case "open":
		q += " is:open"
	case "closed":
		q += " is:closed"
	case "", "all":
		// no state filter
	default:
		return nil, fmt.Errorf("invalid state %q (want open|closed|all)", state)
	}
	path := fmt.Sprintf("/search/issues?q=%s&per_page=%d", url.QueryEscape(q), limit)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github search issues: HTTP %d: %s", status, data)
	}
	var raw struct {
		Items []struct {
			Number      int       `json:"number"`
			Title       string    `json:"title"`
			Body        string    `json:"body"`
			State       string    `json:"state"`
			CreatedAt   string    `json:"created_at"`
			UpdatedAt   string    `json:"updated_at"`
			PullRequest *struct{} `json:"pull_request"`
			Labels      []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var issues []vcs.Issue
	for _, r := range raw.Items {
		if r.PullRequest != nil {
			continue
		}
		iss := vcs.Issue{Number: r.Number, Title: r.Title, Body: r.Body, State: r.State, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
		for _, l := range r.Labels {
			iss.Labels = append(iss.Labels, l.Name)
		}
		issues = append(issues, iss)
	}
	return issues, nil
}

func (c *Client) MergePR(repo string, prNumber int) error {
	_, err := c.MergePRDetailed(repo, prNumber)
	return err
}

// MergePRDetailed merges a PR and returns the resulting merge commit SHA.
// GitHub's PUT /pulls/:n/merge returns { sha, merged, message } on 200.
func (c *Client) MergePRDetailed(repo string, prNumber int) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, name, prNumber)
	data, status, err := c.do("PUT", path, map[string]string{"merge_method": "merge"})
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("github merge PR: HTTP %d: %s", status, data)
	}
	var raw struct {
		SHA    string `json:"sha"`
		Merged bool   `json:"merged"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	return raw.SHA, nil
}

// DeleteBranch removes a branch via DELETE /repos/:owner/:name/git/refs/heads/:branch.
// Treats 404/422 as success (already gone) for idempotency.
func (c *Client) DeleteBranch(repo string, branch string) error {
	if branch == "" {
		return fmt.Errorf("github delete branch: empty branch name")
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, name, branch)
	data, status, err := c.do("DELETE", path, nil)
	if err != nil {
		return err
	}
	if status == 204 || status == 404 || status == 422 {
		return nil
	}
	return fmt.Errorf("github delete branch: HTTP %d: %s", status, data)
}

// PRReview is a single review on a pull request, as returned by
// GET /repos/:owner/:repo/pulls/:number/reviews.
type PRReview struct {
	ID          int64  `json:"id"`
	User        string `json:"user"`         // login
	State       string `json:"state"`        // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	SubmittedAt string `json:"submitted_at"` // RFC3339, empty for PENDING
}

// ListPRReviews fetches all review events on a PR. The caller is responsible
// for state-folding (latest-per-reviewer, dismissed handling, etc.).
func (c *Client) ListPRReviews(repo string, prNumber int) ([]PRReview, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", owner, name, prNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list PR reviews: HTTP %d: %s", status, data)
	}
	var raw []struct {
		ID   int64 `json:"id"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		State       string `json:"state"`
		SubmittedAt string `json:"submitted_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]PRReview, len(raw))
	for i, r := range raw {
		out[i] = PRReview{ID: r.ID, User: r.User.Login, State: r.State, SubmittedAt: r.SubmittedAt}
	}
	return out, nil
}

func (c *Client) GetPRMergeability(repo string, prNumber int) (vcs.MergeStatus, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return vcs.MergeStatusUnknown, err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, prNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return vcs.MergeStatusUnknown, err
	}
	if status != 200 {
		return vcs.MergeStatusUnknown, fmt.Errorf("github get PR: HTTP %d", status)
	}
	var raw struct {
		Mergeable      *bool  `json:"mergeable"`
		MergeableState string `json:"mergeable_state"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.MergeStatusUnknown, err
	}
	if raw.Mergeable == nil {
		return vcs.MergeStatusPending, nil
	}
	if !*raw.Mergeable {
		return vcs.MergeStatusConflict, nil
	}
	return vcs.MergeStatusClean, nil
}

func (c *Client) AddSubIssue(repo string, parentNumber int, subIssueID int64) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/sub_issues", owner, name, parentNumber)
	_, status, err := c.do("POST", path, map[string]any{"sub_issue_id": subIssueID})
	if err != nil {
		return err
	}
	if status != 200 && status != 201 {
		return fmt.Errorf("github add sub-issue: HTTP %d", status)
	}
	return nil
}

func (c *Client) ListSubIssues(repo string, issueNumber int) ([]vcs.Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/sub_issues", owner, name, issueNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list sub-issues: HTTP %d", status)
	}
	var raw []struct {
		ID     int64  `json:"id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	issues := make([]vcs.Issue, len(raw))
	for i, r := range raw {
		labels := make([]string, len(r.Labels))
		for j, l := range r.Labels {
			labels[j] = l.Name
		}
		issues[i] = vcs.Issue{ID: r.ID, Number: r.Number, Title: r.Title, Body: r.Body, State: r.State, Labels: labels}
	}
	return issues, nil
}

func (c *Client) GetParentIssue(repo string, issueNumber int) (*vcs.Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/parent", owner, name, issueNumber)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, nil // no parent
	}
	if status != 200 {
		return nil, fmt.Errorf("github get parent issue: HTTP %d", status)
	}
	var raw struct {
		ID     int64  `json:"id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &vcs.Issue{ID: raw.ID, Number: raw.Number, Title: raw.Title, State: raw.State}, nil
}

// GetIssue fetches a single issue by number.
func (c *Client) GetIssue(repo string, number int) (*vcs.Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, number)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, nil
	}
	if status != 200 {
		return nil, fmt.Errorf("github get issue: HTTP %d: %s", status, data)
	}
	var raw struct {
		ID     int64  `json:"id"`
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &vcs.Issue{ID: raw.ID, Number: raw.Number, Title: raw.Title, State: raw.State}, nil
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo %q: expected owner/repo", repo)
	}
	return parts[0], parts[1], nil
}
