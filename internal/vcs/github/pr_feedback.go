package github

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type feedbackUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

// ListPRFeedback returns reviews, inline review comments and conversation
// comments of a PR. A failure on any page fails the whole read.
func (c *Client) ListPRFeedback(repo string, prNumber int) ([]vcs.PRFeedback, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	var out []vcs.PRFeedback

	data, err := c.allPages(fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100", owner, name, prNumber))
	if err != nil {
		return nil, err
	}
	var reviews []struct {
		ID          int64        `json:"id"`
		User        feedbackUser `json:"user"`
		Body        string       `json:"body"`
		State       string       `json:"state"`
		CommitID    string       `json:"commit_id"`
		SubmittedAt string       `json:"submitted_at"`
	}
	if err := json.Unmarshal(data, &reviews); err != nil {
		return nil, err
	}
	for _, r := range reviews {
		out = append(out, vcs.PRFeedback{ID: "review:" + strconv.FormatInt(r.ID, 10), Kind: "review", Author: r.User.Login, AuthorType: r.User.Type, Body: r.Body, State: r.State, CommitID: r.CommitID, CreatedAt: r.SubmittedAt})
	}

	data, err = c.allPages(fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", owner, name, prNumber))
	if err != nil {
		return nil, err
	}
	var inline []struct {
		ID           int64        `json:"id"`
		User         feedbackUser `json:"user"`
		Body         string       `json:"body"`
		Path         string       `json:"path"`
		Line         int          `json:"line"`
		OriginalLine int          `json:"original_line"`
		CommitID     string       `json:"commit_id"`
		CreatedAt    string       `json:"created_at"`
	}
	if err := json.Unmarshal(data, &inline); err != nil {
		return nil, err
	}
	for _, r := range inline {
		line := r.Line
		if line == 0 {
			line = r.OriginalLine
		}
		out = append(out, vcs.PRFeedback{ID: "inline:" + strconv.FormatInt(r.ID, 10), Kind: "inline", Author: r.User.Login, AuthorType: r.User.Type, Body: r.Body, Path: r.Path, Line: line, CommitID: r.CommitID, CreatedAt: r.CreatedAt})
	}

	data, err = c.allPages(fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100", owner, name, prNumber))
	if err != nil {
		return nil, err
	}
	var convo []struct {
		ID        int64        `json:"id"`
		User      feedbackUser `json:"user"`
		Body      string       `json:"body"`
		CreatedAt string       `json:"created_at"`
	}
	if err := json.Unmarshal(data, &convo); err != nil {
		return nil, err
	}
	for _, r := range convo {
		out = append(out, vcs.PRFeedback{ID: "comment:" + strconv.FormatInt(r.ID, 10), Kind: "comment", Author: r.User.Login, AuthorType: r.User.Type, Body: r.Body, CreatedAt: r.CreatedAt})
	}
	return out, nil
}
