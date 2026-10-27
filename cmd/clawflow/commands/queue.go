package commands

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
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
// queue_issue into the rank file, makes sure the repo is scoped "ranked" (so
// work that was never listed stays unpicked) and emits the one-time
// deprecation notice. scopeExplicit is true when the repo config already has
// a queue_scope; otherwise setRankedScope persists queue_scope: ranked. It
// returns false when a step failed; the caller must then hold back implement
// for this pass (fail closed) and the step is retried next pass. The state is
// updated in place; the caller persists it.
func migrateQueueIssue(client queueSubIssueLister, repo string, queueIssue int, scopeExplicit bool, setRankedScope func() error, st *queue.RepoState) bool {
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
	if !scopeExplicit {
		if err := setRankedScope(); err != nil {
			runLog.Warn("queue/scope", "repo", repo, "err", err.Error(), "result", "retry")
			return false
		}
		runLog.Info("queue/scope", "repo", repo, "scope", queue.ScopeRanked, "reason", "queue_issue_migration")
	}
	if !st.DeprecationShown {
		st.DeprecationShown = true
		msg := fmt.Sprintf("queue_issue (#%d) is deprecated and ignored for dispatch; its sub-issues were imported into the unified queue and queue_scope is ranked (clawflow queue list). You may close the queue issue.", queueIssue)
		fmt.Fprintf(os.Stderr, "[%s] ⚠ %s\n", repo, msg)
		runLog.Warn("queue/deprecated", "repo", repo, "queue", queueIssue, "detail", msg)
	}
	return true
}

// persistRepoQueueScope writes queue_scope for one repo through the normal
// config save path under the config sync lock, never overriding a value that
// is already set.
func persistRepoQueueScope(repo, scope string) error {
	var err error
	if !snapshot.WithConfigSyncLock(time.Minute, func() {
		var cfg *config.Config
		if cfg, err = config.Load(); err != nil {
			return
		}
		if _, ok := cfg.Repos[repo]; !ok {
			err = fmt.Errorf("repo %q not in config", repo)
			return
		}
		if cfg.Repos[repo].QueueScope != "" {
			return
		}
		config.TouchRepo(cfg, repo, func(r config.Repo) config.Repo { r.QueueScope = scope; return r })
		err = cfg.Save()
	}) {
		return errors.New("config sync lock busy")
	}
	return err
}
