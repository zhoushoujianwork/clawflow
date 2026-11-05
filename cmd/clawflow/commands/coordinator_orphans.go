package commands

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// orphanCause reports why no automatic flow will advance an open PR, or "".
// acceptanceVerdict returns the latest stored verdict ("" when unknown);
// budgetErr is the repair ledger check for the PR. Read-only: it only reads.
// orphanUnstructuredExhausted is the verdict token for an invalid report whose
// automatic fresh reviews are used up.
const orphanUnstructuredExhausted = "unstructured_exhausted"

func orphanCause(pr vcs.PR, acceptanceRequired bool, acceptanceVerdict func() string, budgetErr error) string {
	issue := pipelinePRIssue(pr)
	if !pipelineBranchIssue.MatchString(pr.HeadBranch) {
		if issue == 0 {
			return coordinator.OrphanIssueUnlinked
		}
		return coordinator.OrphanBranchUnmanaged
	}
	if budgetErr != nil {
		return coordinator.OrphanRepairExhausted
	}
	if acceptanceRequired && acceptanceVerdict() == orphanUnstructuredExhausted {
		return coordinator.OrphanAcceptanceUnstructured
	}
	if acceptanceRequired && acceptanceVerdict() == "blocked" {
		return coordinator.OrphanAcceptanceBlocked
	}
	return ""
}

// newOrphanedPRProvider lists open PRs of enabled repos that no automatic flow
// will advance. A cause's start is the first snapshot that saw it; a restart
// only delays reporting. A repo whose PRs cannot be listed is skipped.
func newOrphanedPRProvider() func(context.Context) ([]coordinator.OrphanedPR, error) {
	var mu sync.Mutex
	first := map[string]time.Time{}
	return func(ctx context.Context) ([]coordinator.OrphanedPR, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		seen := map[string]time.Time{}
		var out []coordinator.OrphanedPR
		mu.Lock()
		defer mu.Unlock()
		for repo, rc := range cfg.Repos {
			if !rc.Enabled || ctx.Err() != nil {
				continue
			}
			client, err := newVCSClient(rc)
			if err != nil {
				continue
			}
			prs, err := client.ListOpenPRs(repo)
			if err != nil {
				continue
			}
			for _, pr := range prs {
				pr := pr
				verdict := func() string {
					info, _ := pipelineAcceptance(ctx, client, repo, rc, pr, now)
					if info == nil {
						return ""
					}
					if info.Unstructured && info.UnstructuredAttempts > info.MaxUnstructuredRetries {
						return orphanUnstructuredExhausted
					}
					return info.Verdict
				}
				budget := checkRepairBudget(snapshot.DataDir(), repo, pr.Number, now)
				if budget != nil && !isTerminalBudget(budget) {
					budget = nil // cooldown and backoff are temporary waits
				}
				cause := orphanCause(pr, rc.AcceptanceRequired, verdict, budget)
				if cause == "" {
					continue
				}
				k := repo + "!" + strconv.Itoa(pr.Number) + "/" + cause
				since, ok := first[k]
				if !ok {
					since = now
				}
				seen[k] = since
				out = append(out, coordinator.OrphanedPR{Repo: repo, PR: pr.Number, Issue: pipelinePRIssue(pr), Cause: cause, Since: since})
			}
		}
		first = seen
		return out, nil
	}
}

// isTerminalBudget is true when the repair ledger will never allow another
// attempt (quota used or infrastructure budget exhausted/locked).
func isTerminalBudget(err error) bool {
	return errors.Is(err, errRepairExhausted) || errors.Is(err, errRepairBlocked)
}
