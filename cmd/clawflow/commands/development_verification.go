package commands

import (
	"context"
	"errors"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Only absence of a completed identity is recoverable by a fresh evaluation.
// Corrupt/ambiguous evidence, an active developer, policy failures and transport
// errors remain blocked; they must not be hidden by creating a newer record.
func developmentVerificationNeeded(ctx context.Context, client vcs.Client, repo string, pr vcs.PR) bool {
	_, _, _, current, err := acceptanceContract(ctx, client, repo, pr.Number)
	return current.HeadSHA == pr.HeadSHA && current.HeadBranch == pr.HeadBranch &&
		(errors.Is(err, errDevelopmentHeadMissing) || errors.Is(err, errDevelopmentHistoryMissing))
}

func validateDevelopmentVerificationActivity(eventsPath string) error {
	active, err := repairToolActivity(eventsPath)
	if err != nil || !active {
		return errors.New("development verification requires retained native tool activity, not only a text claim")
	}
	return nil
}
