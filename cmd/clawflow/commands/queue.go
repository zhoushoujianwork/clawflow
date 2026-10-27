package commands

import (
	"errors"
	"fmt"
	"os"

	"github.com/zhoushoujianwork/clawflow/internal/queue"
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

// importQueueIssue appends the open sub-issues of a legacy queue issue to the
// global rank file in their current order, skipping keys already ranked. It
// returns the keys that were added.
func importQueueIssue(client queueSubIssueLister, repo string, queueIssue int) ([]queue.Key, error) {
	items, err := readQueueItems(client, repo, queueIssue)
	if err != nil {
		return nil, err
	}
	var added []queue.Key
	_, err = queue.UpdateRank(func(order []queue.Key) ([]queue.Key, error) {
		have := queue.Positions(order)
		added = added[:0]
		for _, it := range items {
			if it.State != "open" {
				continue
			}
			k := queue.Key{Repo: repo, Number: it.Number}
			if _, dup := have[k]; dup {
				continue
			}
			have[k] = len(order)
			order = append(order, k)
			added = append(added, k)
		}
		return order, nil
	}, nil)
	if err != nil {
		return nil, err
	}
	return added, nil
}

// migrateQueueIssue performs the one-time import of a repo's legacy
// queue_issue into the rank file and emits the one-time deprecation notice.
// It returns false when the import failed transiently; the caller must then
// hold back implement for this pass because the old ordering is not yet
// preserved. The state is updated in place; the caller persists it.
func migrateQueueIssue(client queueSubIssueLister, repo string, queueIssue int, st *queue.RepoState) bool {
	if queueIssue <= 0 {
		return true
	}
	if !st.Imported {
		added, err := importQueueIssue(client, repo, queueIssue)
		switch {
		case err == nil:
		case errors.Is(err, vcs.ErrNotSupported):
			// Platform without sub-issues: nothing to preserve.
			runLog.Warn("queue/import", "repo", repo, "queue", queueIssue, "err", err.Error(), "result", "unsupported")
		default:
			runLog.Warn("queue/import", "repo", repo, "queue", queueIssue, "err", err.Error(), "result", "retry")
			return false
		}
		st.Imported = true
		runLog.Info("queue/import", "repo", repo, "queue", queueIssue, "added", len(added))
	}
	if !st.DeprecationShown {
		st.DeprecationShown = true
		msg := fmt.Sprintf("queue_issue (#%d) is deprecated and ignored for dispatch; its sub-issues were imported into the unified queue (clawflow queue list). You may close the queue issue.", queueIssue)
		fmt.Fprintf(os.Stderr, "[%s] ⚠ %s\n", repo, msg)
		runLog.Warn("queue/deprecated", "repo", repo, "queue", queueIssue, "detail", msg)
	}
	return true
}
