package commands

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// publishAcceptanceReport runs under the existing PR lock. Worker results are
// published only by the coordinator after fencing, never by an executing worker.
func publishAcceptanceReport(ctx context.Context, client vcs.Client, r acceptance.Record) error {
	if r.Contract.EvidenceSchema != 1 || !r.Completed {
		return nil
	}
	if _, worker := ctx.Value(workerAcceptanceKey{}).(*coordinatorPayload); worker {
		return nil
	}
	publisher, ok := client.(vcs.AcceptanceCommentPublisher)
	if !ok {
		return errors.New("provider cannot update acceptance reports")
	}
	c, _, _, _, err := acceptanceContract(ctx, client, r.Contract.Repository, r.Contract.PRNumber)
	if err != nil {
		return err
	}
	d, _ := c.Digest()
	if d != r.ContractDigest {
		return errors.New("stale acceptance publication contract")
	}
	store, err := acceptance.OpenStore(acceptanceStorePath(c.Repository, c.PRNumber))
	if err != nil {
		return err
	}
	defer store.Close()
	latest, err := store.LatestRecord(c)
	if err != nil {
		return err
	}
	if latest.AttemptID != r.AttemptID {
		return errors.New("superseded acceptance publication")
	}
	return publisher.PublishAcceptanceComment(c.Repository, c.PRNumber, vcs.AcceptanceComment{Attempt: r.AttemptID, Contract: d, Head: c.HeadSHA, Started: r.StartedAt, Completed: r.CompletedAt, Body: acceptanceReportBody(r)})
}

func reconcileAcceptanceReport(ctx context.Context, client vcs.Client, r acceptance.Record) error {
	if r.Contract.EvidenceSchema != 1 || !r.Completed {
		return nil
	}
	unlock, err := acceptanceLock(r.Contract.Repository, r.Contract.PRNumber)
	if err != nil {
		return err
	}
	defer unlock()
	return publishAcceptanceReport(ctx, client, r)
}

func acceptanceReportBody(r acceptance.Record) string {
	clean := func(s string) string {
		return strings.NewReplacer("\n", " ", "\r", " ", "\\", "\\\\", "`", "\\`", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_").Replace(html.EscapeString(sanitizeReviewerText(s, 4000)))
	}
	var b strings.Builder
	verdict := string(r.Verdict)
	owner, next := r.NextOwner, r.NextAction
	if acceptance.IsInfrastructure(r) {
		verdict, owner, next = "infrastructure_error", "runner", "Retry under the existing infrastructure retry policy; do not repair code."
	}
	fmt.Fprintf(&b, "## Independent acceptance: %s\n\nCommit: `%s`\nAttempt: `%s`\n\n%s\n", verdict, clean(r.Contract.HeadSHA), clean(r.AttemptID), clean(r.Report))
	for _, criterion := range r.Plan.Criteria {
		for _, check := range r.Checks {
			if check.ID != criterion.ID {
				continue
			}
			mark := " "
			if check.Status == acceptance.Passed {
				mark = "x"
			}
			fmt.Fprintf(&b, "\n- [%s] %s — %s: %s\n", mark, clean(criterion.ID), check.Status, clean(criterion.Requirement))
			for _, e := range check.Evidence {
				fmt.Fprintf(&b, "  - %s / %s: %s; reference: %s; commit: `%s`\n", e.Type, e.Result, clean(e.Check), clean(e.Reference), clean(e.Commit))
			}
			if check.Missing != "" {
				fmt.Fprintf(&b, "  - Missing proof: %s\n", clean(string(check.Missing)))
			}
		}
	}
	for _, f := range acceptance.DeduplicateFindings(r.Findings) {
		fmt.Fprintf(&b, "\nFinding (%s): %s — %s\n", clean(f.Kind), clean(f.Summary), clean(string(f.Evidence)))
	}
	fmt.Fprintf(&b, "\nNext action owner: %s\nNext action: %s\n\nPrior attempts remain in the private acceptance audit store. CI and merge gates remain required.\n", clean(owner), clean(next))
	return b.String()
}
