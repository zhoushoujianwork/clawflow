package gitlab

import (
	"encoding/json"
	"fmt"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

var _ vcs.ConditionalMerger = (*Client)(nil)

// MergePRAtHead merges only the accepted source head, never scheduling a later merge.
func (c *Client) MergePRAtHead(repo string, prNumber int, expectedHead string) (string, error) {
	if !canonicalMergeSHA(expectedHead) {
		return "", fmt.Errorf("gitlab merge MR: invalid expected head SHA")
	}
	path := fmt.Sprintf("/projects/%s/merge_requests/%d/merge", projectID(repo), prNumber)
	data, status, err := c.doJSON("PUT", path, map[string]any{
		"sha": expectedHead, "merge_when_pipeline_succeeds": false,
	})
	if err != nil {
		return "", err
	}
	// As on GitHub, conflicts and non-mergeable responses are errors, not success.
	if status != 200 {
		return "", fmt.Errorf("gitlab merge MR: HTTP %d: %s", status, data)
	}
	var raw struct {
		State                     string `json:"state"`
		MergeCommitSHA            string `json:"merge_commit_sha"`
		SquashCommitSHA           string `json:"squash_commit_sha"`
		MergeWhenPipelineSucceeds bool   `json:"merge_when_pipeline_succeeds"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	if raw.State != "merged" || raw.MergeWhenPipelineSucceeds {
		return "", fmt.Errorf("gitlab merge MR: response did not confirm a merge commit")
	}
	sha := raw.MergeCommitSHA
	if sha == "" {
		sha = raw.SquashCommitSHA
	}
	if !canonicalMergeSHA(sha) {
		return "", fmt.Errorf("gitlab merge MR: invalid resulting merge SHA")
	}
	return sha, nil
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
