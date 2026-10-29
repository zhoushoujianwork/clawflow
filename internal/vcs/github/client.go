// Package github implements the vcs.Client interface using the GitHub REST API v3.
package github

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/githubauth"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Client calls the GitHub REST API v3.
type Client struct {
	identity   string
	login      string
	cacheRoot  string
	repository *atomic.Pointer[string]
	authorized bool
	token      string
	baseURL    string // default: https://api.github.com
	http       *http.Client
	state      *requestState
	ctx        context.Context // optional; bounds every request (see SetRequestContext)
}

// SetRequestContext binds ctx to every subsequent request so the caller's
// deadline / cancellation can abort in-flight HTTP calls. Without it a slow or
// rate-limited GitHub endpoint is only bounded by the 30s client timeout, and
// the scan phase issues one request per open issue (ListSubIssues) — N
// sequential 30s calls amplify into hours of "silent" hang after run/reconcile
// (issue #221). The run-scan budget is propagated through here so the poll
// phase respects the run timeout and can be cancelled.
func (c *Client) SetRequestContext(ctx context.Context) { c.ctx = ctx }

func (c *Client) reqContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// New returns a GitHub client. baseURL is optional (for GHE); pass "" for github.com.
func New(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &Client{
		token:      token,
		repository: &atomic.Pointer[string]{},
		state:      &requestState{cache: map[string]cachedResponse{}},
		baseURL:    strings.TrimRight(baseURL, "/"),
		http:       &http.Client{Timeout: 30 * time.Second},
	}
}

// SetRepository supplies scope for endpoints without a repository in their path.
func (c *Client) SetRepository(repo string) {
	if c.repository == nil {
		c.repository = &atomic.Pointer[string]{}
	}
	c.repository.Store(&repo)
}

func (c *Client) authenticated(path string) (*Client, error) {
	if c.authorized {
		return c, nil
	}
	repo := ""
	if c.repository != nil {
		if value := c.repository.Load(); value != nil {
			repo = *value
		}
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "repos" {
		repo = parts[1] + "/" + parts[2]
		if c.repository != nil {
			c.repository.CompareAndSwap(nil, &repo)
		}
	}
	if repo == "" {
		return c, nil
	}
	id, err := githubauth.Resolve(c.reqContext(), repo, c.token, c.baseURL)
	if err != nil {
		return nil, err
	}
	clone := *c
	clone.token = id.Token
	clone.login = id.Login
	clone.identity = id.Name
	clone.authorized = true
	if id.Name != "personal" {
		clone.state = accountState(id.Name, c.baseURL+"\x00"+c.cacheRoot)
		if c.cacheRoot != "" {
			clone.UseCacheDir(c.cacheRoot)
		}
	}
	return &clone, nil
}

// doOnce sends a single HTTP request and returns the response body and status.
func (c *Client) doOnce(method, path string, body any) ([]byte, int, error) {
	selected, authErr := c.authenticated(path)
	if authErr != nil {
		return nil, 0, authErr
	}
	c = selected
	if b := c.Budget(); time.Now().Before(b.Backoff) {
		return nil, http.StatusTooManyRequests, &retry.HTTPError{Status: 429, RateLimited: true, After: time.Until(b.Backoff)}
	}
	stage, _ := c.reqContext().Value(stageKey{}).(string)
	if method == http.MethodGet && (stage == "scan" || stage == "harvest" || stage == "automerge" || stage == "board" || stage == "snapshot") && !c.PollingAllowed() {
		return nil, 0, vcs.ErrPollingDeferred
	}

	pass, _ := c.reqContext().Value(passKey{}).(*Pass)
	key := fmt.Sprintf("%x:%s", sha256.Sum256([]byte(c.baseURL+"\x00"+c.accountIdentity())), path)
	if pass != nil && method == "GET" && openList(path) {
		pass.Lock()
		defer pass.Unlock()
		if v, ok := pass.responses[key]; ok {
			return v.data, v.status, v.err
		}
	}
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
	}
	var r io.Reader
	if bodyBytes != nil {
		r = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(c.reqContext(), method, c.baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	entry := cachedResponse{}
	if method == "GET" {
		entry = c.cached(path)
		if entry.ETag != "" {
			req.Header.Set("If-None-Match", entry.ETag)
		} else if entry.Modified != "" {
			req.Header.Set("If-Modified-Since", entry.Modified)
		}
	}
	if pass != nil {
		locked := method == "GET" && openList(path)
		if !locked {
			pass.Lock()
		}
		stage, _ := c.reqContext().Value(stageKey{}).(string)
		if stage == "" {
			stage = "other"
		}
		pass.Calls[stage]++
		if !locked {
			pass.Unlock()
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	status := resp.StatusCode
	if err == nil {
		data, status, err = c.observe(path, method, resp, data)
		if status == http.StatusNotModified && entry.Body != nil {
			data, status = entry.Body, http.StatusOK
		}
	}
	if pass != nil {
		locked := method == "GET" && openList(path)
		if !locked {
			pass.Lock()
		}
		pass.Budgets[c.budgetKey()] = c.Budget()
		if locked && err == nil && status == 200 {
			pass.responses[key] = passResponse{data, status, err}
		}
		if !locked {
			pass.Unlock()
		}
	}
	return data, status, err
}

// isRetryableStatus reports whether an HTTP status code is worth retrying.
// 429 (rate limit) and transient 5xx (except 501 Not Implemented) are
// retried. 4xx client errors and 501 are permanent failures.
func isRetryableStatus(status int) bool {
	return status == 429 || (status >= 500 && status != 501)
}

// do sends a request with automatic exponential-backoff retry for transient
// network errors and retryable HTTP status codes (429, 5xx). POST requests
// are NOT retried here because they are not idempotent; callers that need
// retry semantics on POST must handle idempotency themselves (see
// PostIssueCommentIdempotent).
func (c *Client) do(method, path string, body any) ([]byte, int, error) {
	// Retry idempotent methods and additive label POSTs automatically.
	ctx, cancel := context.WithTimeout(c.reqContext(), 60*time.Second)
	defer cancel()
	bounded := *c
	bounded.ctx = ctx
	c = &bounded
	if method == "POST" && !strings.HasSuffix(path, "/labels") {
		return c.doOnce(method, path, body)
	}
	cfg := retry.DefaultConfig
	var (
		data   []byte
		status int
	)
	err := retry.Do(c.reqContext(), cfg, func() error {
		var e error
		data, status, e = c.doOnce(method, path, body)
		if e != nil {
			if errors.Is(e, vcs.ErrPollingDeferred) {
				return retry.Permanent(e)
			}
			return e // network error — retryable
		}
		if isRetryableStatus(status) {
			return fmt.Errorf("github %s %s: HTTP %d (transient)", method, path, status)
		}
		return nil
	})
	return data, status, err
}

// runIDMarker is the HTML comment appended to every result comment so a
// retry can detect a previously delivered comment and skip re-posting.
// Format: <!-- clawflow:run-id=<runID> -->
func runIDMarker(runID string) string {
	if runID == "" {
		return ""
	}
	return "\n\n<!-- clawflow:run-id=" + runID + " -->"
}

// PostIssueCommentIdempotent posts a comment only if no existing comment
// already contains the <!-- clawflow:run-id=<runID> --> marker. This makes
// the POST safe to retry: if the first attempt succeeded but the HTTP
// response was lost (context deadline exceeded after GitHub wrote the
// comment), a retry will detect the duplicate and return nil without
// posting again. When runID is empty the method falls back to a plain POST
// without dedup.
func (c *Client) PostIssueCommentIdempotent(repo string, issueNumber int, body, runID string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, name, issueNumber)
	return c.postComment(path, body, runID, runID != "")
}

func (c *Client) postComment(path, body, runID string, checkFirst bool) error {
	if runID == "" {
		runID = fmt.Sprintf("write-%x", randomCommentID())
	}
	marker := "<!-- clawflow:run-id=" + runID + " -->"
	fullBody := body + "\n\n" + marker
	ctx, cancel := context.WithTimeout(c.reqContext(), 60*time.Second)
	defer cancel()
	bounded := *c
	bounded.ctx = ctx
	c = &bounded
	attempt := 0
	return retry.Do(ctx, retry.DefaultConfig, func() error {
		attempt++
		if checkFirst || attempt > 1 {
			data, err := c.allPages(path + "?per_page=100")
			if err != nil {
				return retry.RequestError(err)
			}
			var comments []struct {
				Body string `json:"body"`
			}
			if err := json.Unmarshal(data, &comments); err != nil {
				return retry.Permanent(err)
			}
			for _, comment := range comments {
				if strings.Contains(comment.Body, marker) {
					return nil
				}
			}
		}
		_, status, err := c.doOnce("POST", path, map[string]string{"body": fullBody})
		if err != nil {
			return err
		}
		if status != 201 {
			return retry.Permanent(fmt.Errorf("post comment: HTTP %d", status))
		}
		return nil
	})
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
		// This endpoint is queried with state=open, so every returned issue is
		// open. Set State explicitly — the list table prints i.State and would
		// otherwise show a blank STATE column on the default --state open path
		// (issue #274).
		issue := vcs.Issue{Number: r.Number, State: "open", Title: r.Title, Body: r.Body, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt}
		for _, l := range r.Labels {
			issue.Labels = append(issue.Labels, l.Name)
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

func (c *Client) ListOpenPRs(repo string) ([]vcs.PR, error) { return c.ListPRs(repo, "open") }

func (c *Client) PRExistsForIssue(repo string, issueNumber int) (bool, error) {
	prs, err := c.ListOpenPRs(repo)
	if err != nil {
		return false, err
	}
	for _, pr := range prs {
		if vcs.PRReferencesIssue(pr, issueNumber) {
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
		if status != 200 && status != 204 && status != 404 {
			return fmt.Errorf("github remove label %q: HTTP %d", label, status)
		}
	}
	return nil
}

func (c *Client) ListIssueComments(repo string, issueNumber int) ([]string, error) {
	comments, err := c.ListIssueCommentsDetail(repo, issueNumber)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(comments))
	for i, comment := range comments {
		out[i] = comment.Body
	}
	return out, nil
}

func (c *Client) PostIssueComment(repo string, issueNumber int, body string) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, name, issueNumber)
	return c.postComment(path, body, "", false)
}

func (c *Client) ListIssueCommentsDetail(repo string, issueNumber int) ([]vcs.IssueComment, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, name, issueNumber)
	data, err := c.allPages(path)
	if err != nil {
		return nil, err
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
		SubIssuesSummary *struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
		} `json:"sub_issues_summary"`
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
		if r.SubIssuesSummary != nil {
			issue.SubIssuesTotal = r.SubIssuesSummary.Total
			issue.SubIssuesCompleted = r.SubIssuesSummary.Completed
		}
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

// ListPRs lists PRs for repo filtered by state, where state is one of
// "open"/"closed"/"merged"/"all". GitHub's REST API (GET /pulls) only
// accepts state=open|closed|all — "merged" is not a real API state, it's
// a derived property (closed + merged_at != nil). Passing "merged"
// straight through silently falls back to the API's default ("open"),
// so we translate it here: request state=closed and filter the results
// by MergedAt client-side. See issue #330.
func (c *Client) ListPRs(repo string, state string) ([]vcs.PR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	apiState := state
	if apiState == "merged" {
		apiState = "closed"
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&per_page=100", owner, name, apiState)
	data, err := c.allPages(path)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		HTMLURL   string `json:"html_url"`
		ClosedAt  string `json:"closed_at"`
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
	prs := make([]vcs.PR, 0, len(raw))
	for _, r := range raw {
		if state == "merged" && r.MergedAt == "" {
			continue
		}
		s := r.State
		if r.MergedAt != "" {
			s = "merged"
		}
		prs = append(prs, vcs.PR{Number: r.Number, Title: r.Title, Body: r.Body, State: s, HeadBranch: r.Head.Ref, MergedAt: r.MergedAt, ClosedAt: r.ClosedAt, URL: r.HTMLURL, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return prs, nil
}

// ListRecentPRs returns one page (at most limit, capped at 100) of pull
// requests in state "open" or "closed", most recently updated first. Unlike
// ListPRs it never walks the whole history, and it keeps the head and merge
// commit SHAs the list response already carries. Merged PRs get State "merged".
func (c *Client) ListRecentPRs(repo, state string, limit int) ([]vcs.PR, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if state != "open" && state != "closed" {
		return nil, fmt.Errorf("github list recent PRs: unsupported state %q", state)
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&sort=updated&direction=desc&per_page=%d", owner, name, state, limit)
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github list recent PRs: HTTP %d", status)
	}
	var raw []struct {
		Number         int    `json:"number"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		State          string `json:"state"`
		HTMLURL        string `json:"html_url"`
		ClosedAt       string `json:"closed_at"`
		MergedAt       string `json:"merged_at"`
		CreatedAt      string `json:"created_at"`
		UpdatedAt      string `json:"updated_at"`
		Draft          bool   `json:"draft"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Head           struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	prs := make([]vcs.PR, 0, len(raw))
	for _, r := range raw {
		s := r.State
		if r.MergedAt != "" {
			s = "merged"
		}
		prs = append(prs, vcs.PR{Number: r.Number, Title: r.Title, Body: r.Body, State: s, Draft: r.Draft,
			HeadBranch: r.Head.Ref, HeadSHA: r.Head.SHA, BaseBranch: r.Base.Ref, MergeCommitSHA: r.MergeCommitSHA,
			MergedAt: r.MergedAt, ClosedAt: r.ClosedAt, URL: r.HTMLURL, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt})
	}
	return prs, nil
}

func (c *Client) createPROnce(repo string, opts vcs.PRCreateOpts) (vcs.PR, error) {
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
		return vcs.PR{}, retry.Permanent(fmt.Errorf("github create PR: HTTP %d: %s", status, data))
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
		MergeCommitSHA string `json:"merge_commit_sha"`
		Base           struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		Number   int    `json:"number"`
		Title    string `json:"title"`
		Body     string `json:"body"`
		State    string `json:"state"`
		HTMLURL  string `json:"html_url"`
		ClosedAt string `json:"closed_at"`
		MergedAt string `json:"merged_at"`
		Draft    bool   `json:"draft"`
		Head     struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return vcs.PR{}, err
	}
	s := raw.State
	if raw.MergedAt != "" {
		s = "merged"
	}
	return vcs.PR{Number: raw.Number, Title: raw.Title, Body: raw.Body, State: s, HeadBranch: raw.Head.Ref, HeadSHA: raw.Head.SHA, HeadRepo: raw.Head.Repo.FullName, Draft: raw.Draft, MergedAt: raw.MergedAt, ClosedAt: raw.ClosedAt, URL: raw.HTMLURL, BaseBranch: raw.Base.Ref, BaseSHA: raw.Base.SHA, MergeCommitSHA: raw.MergeCommitSHA}, nil
}

func (c *Client) PostPRComment(repo string, prNumber int, body string) error {
	// GitHub PR comments use the issues endpoint
	return c.PostIssueComment(repo, prNumber, body)
}

// GetCIStatus aggregates check runs for one captured PR revision. It does not
// cover commit-status contexts, required check names, or workflow-run identity;
// callers must not treat this observation as an atomic merge authorization.
func (c *Client) GetCIStatus(repo string, prNumber int) (vcs.CIStatus, error) {
	return c.getCIStatus(repo, prNumber, nil)
}

func (c *Client) GetCIEvidence(repo string, prNumber int) (vcs.CIEvidence, error) {
	var evidence vcs.CIEvidence
	status, err := c.getCIStatus(repo, prNumber, &evidence)
	evidence.Status = status
	return evidence, err
}

func (c *Client) getCIStatus(repo string, prNumber int, evidence *vcs.CIEvidence) (vcs.CIStatus, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return vcs.CIStatusNone, err
	}
	pr, err := c.GetPR(repo, prNumber)
	if err != nil {
		return vcs.CIStatusNone, err
	}
	if strings.TrimSpace(pr.HeadSHA) == "" {
		return vcs.CIStatusNone, fmt.Errorf("github get check runs: missing PR head SHA")
	}
	if evidence != nil {
		evidence.HeadSHA = pr.HeadSHA
	}
	// Bound pagination even if the API reports an unexpectedly large count.
	const pageSize, maxPages = 100, 100
	total := -1
	seen := make(map[int64]bool)
	pending, failed := false, false
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=%d&filter=latest&page=%d", owner, name, url.PathEscape(pr.HeadSHA), pageSize, page)
		data, status, err := c.do("GET", path, nil)
		if err != nil {
			return vcs.CIStatusNone, err
		}
		if status != 200 {
			return vcs.CIStatusNone, fmt.Errorf("github get check runs: HTTP %d", status)
		}
		var raw struct {
			TotalCount *int          `json:"total_count"`
			CheckRuns  []vcs.CICheck `json:"check_runs"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return vcs.CIStatusNone, err
		}
		if raw.TotalCount == nil || *raw.TotalCount < 0 || *raw.TotalCount > pageSize*maxPages || raw.CheckRuns == nil {
			return vcs.CIStatusNone, fmt.Errorf("github get check runs: missing or invalid pagination data")
		}
		if total == -1 {
			total = *raw.TotalCount
		}
		if total != *raw.TotalCount {
			return vcs.CIStatusNone, fmt.Errorf("github get check runs: count changed during pagination")
		}
		expected := total - len(seen)
		if expected > pageSize {
			expected = pageSize
		}
		if len(raw.CheckRuns) != expected {
			return vcs.CIStatusNone, fmt.Errorf("github get check runs: incomplete or inconsistent page %d", page)
		}
		for _, run := range raw.CheckRuns {
			if run.ID <= 0 || seen[run.ID] || run.HeadSHA != pr.HeadSHA {
				return vcs.CIStatusNone, fmt.Errorf("github get check runs: invalid identity or revision on page %d", page)
			}
			seen[run.ID] = true
			if evidence != nil {
				evidence.CheckIDs = append(evidence.CheckIDs, run.ID)
				evidence.Checks = append(evidence.Checks, run)
			}
			switch run.Status {
			case "completed":
				if run.Conclusion == nil || *run.Conclusion == "" {
					return vcs.CIStatusNone, fmt.Errorf("github get check runs: completed check missing conclusion")
				}
				failed = failed || *run.Conclusion != "success"
			case "queued", "in_progress", "waiting", "requested", "pending":
				if run.Conclusion != nil && *run.Conclusion != "" {
					return vcs.CIStatusNone, fmt.Errorf("github get check runs: unfinished check has conclusion")
				}
				pending = true
			default:
				return vcs.CIStatusNone, fmt.Errorf("github get check runs: unknown status %q", run.Status)
			}
		}
		if len(seen) == total {
			break
		}
	}
	if total == 0 {
		return vcs.CIStatusNone, nil
	}
	if failed {
		return vcs.CIStatusFailure, nil
	}
	if pending {
		return vcs.CIStatusPending, nil
	}
	current, err := c.GetPR(repo, prNumber)
	if err != nil {
		return vcs.CIStatusNone, err
	}
	if current.HeadSHA != pr.HeadSHA {
		return vcs.CIStatusPending, nil
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
	selected, authErr := c.authenticated("/repos/" + repo + "/")
	if authErr != nil {
		return nil, authErr
	}
	c = selected
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

// ClosePR closes a PR without merging via PATCH /repos/:owner/:name/pulls/:n
// with {"state":"closed"}. Note the path is /pulls/ (not /issues/), though
// GitHub also accepts the issues endpoint for the shared number space.
func (c *Client) ClosePR(repo string, prNumber int) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, prNumber)
	data, status, err := c.do("PATCH", path, map[string]string{"state": "closed"})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("github close PR: HTTP %d: %s", status, data)
	}
	return nil
}

// MergePRDetailed merges a PR and returns the resulting merge commit SHA.
// GitHub's PUT /pulls/:n/merge returns { sha, merged, message } on 200.
func (c *Client) MergePRDetailed(repo string, prNumber int) (string, error) {
	return c.mergePR(repo, prNumber, "")
}

var _ vcs.ConditionalMerger = (*Client)(nil)

// MergePRAtHead merges only the specified immutable head and validates the result.
func (c *Client) MergePRAtHead(repo string, prNumber int, expectedHead string) (string, error) {
	if !canonicalMergeSHA(expectedHead) {
		return "", fmt.Errorf("github merge PR: invalid expected head SHA")
	}
	return c.mergePR(repo, prNumber, expectedHead)
}

func canonicalMergeSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, ch := range sha {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

func (c *Client) mergePR(repo string, prNumber int, expectedHead string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, name, prNumber)
	body := map[string]string{"merge_method": "merge"}
	if expectedHead != "" {
		body["sha"] = expectedHead
	}
	data, status, err := c.do("PUT", path, body)
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
	if !raw.Merged || strings.TrimSpace(raw.SHA) == "" {
		return "", fmt.Errorf("github merge PR: response did not confirm a merge commit")
	}
	if expectedHead != "" && !canonicalMergeSHA(raw.SHA) {
		return "", fmt.Errorf("github merge PR: invalid resulting merge SHA")
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
	if raw.MergeableState == "dirty" {
		return vcs.MergeStatusConflict, nil
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
	// Read every page: GitHub returns 30 sub-issues per page by default, and a
	// truncated list silently drops queue items and parent/leaf structure.
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/sub_issues?per_page=100", owner, name, issueNumber)
	data, err := c.allPages(path)
	if err != nil {
		return nil, fmt.Errorf("github list sub-issues: %w", err)
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

// GetRepoID returns GitHub's immutable numeric repository ID. The
// owner/name pair is NOT stable — deleting a repo and recreating it under
// the same name mints a new ID — so callers use this to detect recreation
// and segregate local run history (issue #272).
func (c *Client) GetRepoID(repo string) (int64, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return 0, err
	}
	data, status, err := c.do("GET", fmt.Sprintf("/repos/%s/%s", owner, name), nil)
	if err != nil {
		return 0, err
	}
	if status != 200 {
		return 0, fmt.Errorf("github get repo: HTTP %d: %s", status, data)
	}
	var raw struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, err
	}
	return raw.ID, nil
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
		ID               int64  `json:"id"`
		Number           int    `json:"number"`
		Title            string `json:"title"`
		Body             string `json:"body"`
		State            string `json:"state"`
		HTMLURL          string `json:"html_url"`
		CreatedAt        string `json:"created_at"`
		UpdatedAt        string `json:"updated_at"`
		ClosedAt         string `json:"closed_at"`
		SubIssuesSummary struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
		} `json:"sub_issues_summary"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	labels := make([]string, len(raw.Labels))
	for i, l := range raw.Labels {
		labels[i] = l.Name
	}
	return &vcs.Issue{
		ID:        raw.ID,
		Number:    raw.Number,
		Title:     raw.Title,
		Body:      raw.Body,
		State:     raw.State,
		Labels:    labels,
		URL:       raw.HTMLURL,
		CreatedAt: raw.CreatedAt,
		UpdatedAt: raw.UpdatedAt, ClosedAt: raw.ClosedAt, SubIssuesTotal: raw.SubIssuesSummary.Total, SubIssuesCompleted: raw.SubIssuesSummary.Completed,
	}, nil
}

// UploadAttachment is not supported on GitHub: the REST API v3 has no public
// endpoint for uploading issue/comment attachments (the web UI uses an
// undocumented session-authenticated upload flow that does not accept PAT /
// Bearer tokens). Callers should fall back to pasting images in the web UI.
func (c *Client) UploadAttachment(repo string, filePath string) (string, error) {
	return "", vcs.ErrNotSupported
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo %q: expected owner/repo", repo)
	}
	return parts[0], parts[1], nil
}

// CreatePR reconciles an ambiguous response before retrying a non-idempotent POST.
func (c *Client) CreatePR(repo string, opts vcs.PRCreateOpts) (vcs.PR, error) {
	ctx, cancel := context.WithTimeout(c.reqContext(), 60*time.Second)
	defer cancel()
	bounded := *c
	bounded.ctx = ctx
	c = &bounded
	headBranch := opts.Head
	headOwner, branch, qualified := strings.Cut(opts.Head, ":")
	if qualified {
		headBranch = branch
	}
	var result vcs.PR
	attempt := 0
	err := retry.Do(ctx, retry.DefaultConfig, func() error {
		attempt++
		if attempt > 1 {
			prs, err := c.ListOpenPRs(repo)
			if err != nil {
				return retry.RequestError(err)
			}
			for _, pr := range prs {
				if pr.HeadBranch != headBranch {
					continue
				}
				current, err := c.GetPR(repo, pr.Number)
				if err != nil {
					return retry.RequestError(err)
				}
				sameSource := current.HeadRepo == repo
				if qualified {
					sameSource = strings.HasPrefix(current.HeadRepo, headOwner+"/")
				}
				if sameSource && current.BaseBranch == opts.Base {
					result = current
					return nil
				}
			}
		}
		var err error
		result, err = c.createPROnce(repo, opts)
		return err
	})
	return result, err
}

func randomCommentID() []byte { id := make([]byte, 16); _, _ = rand.Read(id); return id }

// PublishAcceptanceComment updates one authenticated-actor-owned PR comment.
func (c *Client) PublishAcceptanceComment(repo string, pr int, report vcs.AcceptanceComment) error {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return err
	}
	selected, err := c.authenticated("/repos/" + repo + "/")
	if err != nil {
		return err
	}
	c = selected
	actor := struct {
		Login string `json:"login"`
	}{Login: c.login}
	if c.identity == "personal" || c.identity == "" {
		data, _, err := c.do("GET", "/user", nil)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &actor); err != nil {
			return err
		}
	}
	if actor.Login == "" || actor.Login == "[bot]" {
		return fmt.Errorf("github: authenticated actor unavailable")
	}
	return vcs.UpsertAcceptanceComment(report, actor.Login,
		func() ([]vcs.IssueComment, error) { return c.ListIssueCommentsDetail(repo, pr) },
		func(body string) error {
			_, _, e := c.do("POST", fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, name, pr), map[string]string{"body": body})
			return e
		},
		func(id int64, body string) error {
			_, _, e := c.do("PATCH", fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, name, id), map[string]string{"body": body})
			return e
		})
}
