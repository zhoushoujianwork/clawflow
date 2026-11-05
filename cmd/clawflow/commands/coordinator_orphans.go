package commands

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/repairledger"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// orphanCause reports why no automatic flow will advance an open PR, or "".
// acceptanceVerdict returns the latest stored verdict ("" when unknown);
// budgetErr is the repair ledger check for the PR. Read-only: it only reads.
// orphanUnstructuredExhausted is the verdict token for an invalid report whose
// automatic fresh reviews are used up.
const orphanUnstructuredExhausted = "unstructured_exhausted"

func orphanCause(pr vcs.PR, acceptanceRequired bool, acceptanceVerdict func() string, budgetErr error, cancelledExhausted ...bool) string {
	// Intentional drafts (platform flag or WIP/DRAFT title) are not abandoned.
	if pr.Draft || isDraftPR(pr.Title) {
		return ""
	}
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
	if len(cancelledExhausted) > 0 && cancelledExhausted[0] {
		return coordinator.OrphanCICancelled
	}
	if acceptanceRequired && acceptanceVerdict() == orphanUnstructuredExhausted {
		return coordinator.OrphanAcceptanceUnstructured
	}
	if acceptanceRequired && acceptanceVerdict() == "blocked" {
		return coordinator.OrphanAcceptanceBlocked
	}
	return ""
}

// orphanVerdict maps acceptance evidence to the verdict token orphanCause uses.
// Verdicts that repair or a fresh same-head review will still advance map to "".
func orphanVerdict(info *pipeline.AcceptanceInfo) string {
	if info == nil {
		return ""
	}
	if info.Unstructured {
		if info.UnstructuredAttempts > info.MaxUnstructuredRetries {
			return orphanUnstructuredExhausted
		}
		return ""
	}
	if info.BlockedRepairable {
		return ""
	}
	return info.Verdict
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
					return orphanVerdict(info)
				}
				budget := checkRepairBudget(snapshot.DataDir(), repo, pr.Number, now)
				if budget != nil && !isTerminalBudget(budget) {
					budget = nil // cooldown and backoff are temporary waits
				}
				cancelled := false
				if rc.CIRequired {
					if st, err := repairCIStatus(client, repo, pr); err == nil && st == vcs.CIStatusCancelled {
						cancelled = repairledger.CancelRerunsExhausted(snapshot.DataDir(), repo, pr)
					}
				}
				cause := orphanCause(pr, rc.AcceptanceRequired, verdict, budget, cancelled)
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
