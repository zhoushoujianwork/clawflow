package gitlab

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func (c *Client) allPages(path string) ([]byte, error) {
	out := []json.RawMessage{}
	for page := 1; page <= 10000; page++ {
		data, status, err := c.doJSON("GET", path+"&page="+strconv.Itoa(page), nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("gitlab history page %d: HTTP %d (lifecycle endpoints may be unsupported on older GitLab)", page, status)
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
	return nil, fmt.Errorf("gitlab history pagination limit exceeded")
}

func (c *Client) ListIssueEvents(repo string, number int) ([]vcs.IssueEvent, error) {
	out := []vcs.IssueEvent{}
	for _, kind := range []string{"state", "label"} {
		path := fmt.Sprintf("/projects/%s/issues/%d/resource_%s_events?per_page=100", projectID(repo), number, kind)
		data, err := c.allPages(path)
		if err != nil {
			return nil, err
		}
		var raw []struct {
			ID        int64  `json:"id"`
			State     string `json:"state"`
			Action    string `json:"action"`
			CreatedAt string `json:"created_at"`
			User      struct {
				Username string `json:"username"`
			} `json:"user"`
			Label struct {
				Name string `json:"name"`
			} `json:"label"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		for _, r := range raw {
			typ := r.State
			if kind == "label" {
				switch r.Action {
				case "add":
					typ = "labeled"
				case "remove":
					typ = "unlabeled"
				default:
					continue
				}
			}
			if typ != "closed" && typ != "reopened" && typ != "labeled" && typ != "unlabeled" {
				continue
			}
			out = append(out, vcs.IssueEvent{ID: "gitlab-" + kind + "-" + strconv.FormatInt(r.ID, 10), Type: typ, CreatedAt: r.CreatedAt, Actor: r.User.Username, Label: r.Label.Name})
		}
	}
	return out, nil
}
