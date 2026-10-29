package github

import (
	"encoding/json"
	"fmt"
)

// ListPRFiles returns changed paths without downloading patch contents.
func (c *Client) ListPRFiles(repo string, prNumber int) ([]string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	var files []string
	for page := 1; page <= 30; page++ {
		data, status, err := c.do("GET", fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, name, prNumber, page), nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("github PR files: HTTP %d", status)
		}
		var rows []struct {
			Filename string `json:"filename"`
			Previous string `json:"previous_filename"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, err
		}
		for _, row := range rows {
			files = append(files, row.Filename)
			if row.Previous != "" {
				files = append(files, row.Previous)
			}
		}
		if len(rows) < 100 {
			return files, nil
		}
	}
	return nil, fmt.Errorf("github PR file limit reached")
}
