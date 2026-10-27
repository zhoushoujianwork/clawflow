package commands

import (
	"errors"
	"fmt"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// The unified cross-repo queue lives in internal/queue (pure policy) and
// queue_unified.go (the per-pass wiring). This file keeps the legacy
// queue_issue sub-issue reader, now used only for the one-time import.

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// queueSubIssueLister is the slice of vcs.Client the queue read needs.
type queueSubIssueLister interface {
	ListSubIssues(repo string, issueNumber int) ([]vcs.Issue, error)
}

// readQueueItems lists the queue issue's direct sub-issues. Any failure,
// including a platform without sub-issues, is returned so the caller fails
// closed.
func readQueueItems(client queueSubIssueLister, repo string, queueIssue int) ([]vcs.Issue, error) {
	items, err := client.ListSubIssues(repo, queueIssue)
	if err != nil {
		if errors.Is(err, vcs.ErrNotSupported) {
			return nil, fmt.Errorf("sub-issues not supported on this platform: %w", err)
		}
		return nil, err
	}
	return items, nil
}
