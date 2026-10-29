package commands

import (
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// issueWorkAllowed checks owner opt-out immediately before starting PR work.
// It is deliberately not part of result ingestion or acceptance contracts.
func issueWorkAllowed(client vcs.Client, repo string, issue int) error {
	labels, err := client.GetIssueLabels(repo, issue)
	if err != nil {
		return err
	}
	if hasLabel(labels, "clawflow-skip") {
		return fmt.Errorf("issue excluded by clawflow-skip")
	}
	return nil
}
