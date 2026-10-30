package gitlab

import (
	"encoding/json"
	"fmt"
)

// ListPRFiles returns changed paths without downloading patch contents.
func (c *Client) ListPRFiles(repo string, prNumber int) ([]string, error) {
	data, status, err := c.doJSON("GET", fmt.Sprintf("/projects/%s/merge_requests/%d/changes", projectID(repo), prNumber), nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("gitlab MR files: HTTP %d", status)
	}
	var raw struct {
		Overflow bool `json:"overflow"`
		Changes  []struct {
			Old string `json:"old_path"`
			New string `json:"new_path"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw.Overflow {
		return nil, fmt.Errorf("gitlab MR files truncated")
	}
	var files []string
	for _, row := range raw.Changes {
		files = append(files, row.New)
		if row.Old != row.New && row.Old != "" {
			files = append(files, row.Old)
		}
	}
	return files, nil
}
