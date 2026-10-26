package github

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// allPages fails the entire read on a later-page failure, never returning a
// successful truncated history. Paths already include per_page=100.
func (c *Client) allPages(path string) ([]byte, error) {
	out := []json.RawMessage{}
	for page := 1; page <= 10000; page++ {
		data, status, err := c.do("GET", path+"&page="+strconv.Itoa(page), nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("github history page %d: HTTP %d", page, status)
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if len(rows) < 100 {
			return json.Marshal(out)
		}
	}
	return nil, fmt.Errorf("github history pagination limit exceeded")
}

func (c *Client) ListIssueEvents(repo string, number int) ([]vcs.IssueEvent, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	data, err := c.allPages(fmt.Sprintf("/repos/%s/%s/issues/%d/events?per_page=100", owner, name, number))
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID        int64  `json:"id"`
		Event     string `json:"event"`
		CreatedAt string `json:"created_at"`
		Actor     struct {
			Login string `json:"login"`
		} `json:"actor"`
		Label struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := []vcs.IssueEvent{}
	for _, r := range raw {
		switch r.Event {
		case "reopened", "closed", "labeled", "unlabeled":
			out = append(out, vcs.IssueEvent{ID: "github-" + strconv.FormatInt(r.ID, 10), Type: r.Event, CreatedAt: r.CreatedAt, Actor: r.Actor.Login, Label: r.Label.Name})
		}
	}
	return out, nil
}
