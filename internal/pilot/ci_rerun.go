package pilot

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/repairledger"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

var rerunClosingReference = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+#(\d+)\b`)
var rerunHead = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

func (e ActionExecutor) rerunCI(a Action, now time.Time) (*vcs.CIRerunPlan, error) {
	c, err := e.Client(a.Repo)
	if err != nil {
		return nil, err
	}
	provider, ok := c.(vcs.CIRerunProvider)
	if !ok {
		return nil, errors.New("unsupported_platform")
	}
	pr, err := c.GetPR(a.Repo, a.PR)
	if err != nil {
		return nil, err
	}
	if pr.State != "open" || pr.Draft || pr.IsMerged() || !strings.EqualFold(pr.HeadRepo, a.Repo) || !rerunHead.MatchString(pr.HeadSHA) {
		return nil, errors.New("unmanaged_pr")
	}
	managed := false
	for _, match := range rerunClosingReference.FindAllStringSubmatch(pr.Body, -1) {
		number, _ := strconv.Atoi(match[1])
		if !vcs.ManagedIssueBranch(pr.HeadBranch, number) {
			continue
		}
		issue, err := c.GetIssue(a.Repo, number)
		if err != nil {
			return nil, err
		}
		if issue != nil && issue.State == "open" && issue.SubIssuesTotal == 0 && issue.HasLabel("agent-implemented") {
			if issue.HasLabel("agent-running") {
				return nil, errors.New("issue_running")
			}
			managed = true
		}
	}
	if !managed {
		return nil, errors.New("unmanaged_pr")
	}
	// Check the durable claim before querying CI: a rerun may now be queued or green.
	_, path := repairledger.Path(snapshot.DataDir(), a.Repo, a.PR)
	ledger, err := repairledger.Load(path)
	if err != nil {
		return nil, err
	}
	for _, attempt := range ledger.Attempts {
		if attempt.Head == pr.HeadSHA && attempt.Reason == "rerun_ci" {
			return nil, errors.New("already_rerun_for_head")
		}
	}
	plan, err := provider.PlanCIRerun(a.Repo, a.PR)
	if err != nil {
		return &plan, err
	}
	if plan.HeadSHA != pr.HeadSHA {
		return &plan, errors.New("head_changed")
	}
	if len(plan.RunIDs) == 0 {
		return &plan, errors.New("no_rerunnable_checks")
	}
	current, err := c.GetPR(a.Repo, a.PR)
	if err != nil {
		return &plan, err
	}
	if current.HeadSHA != pr.HeadSHA || current.State != "open" || current.Draft || current.IsMerged() {
		return &plan, errors.New("head_changed")
	}
	if err := repairledger.ReserveRerun(snapshot.DataDir(), a.Repo, pr, now); err != nil {
		if errors.Is(err, repairledger.ErrBlocked) {
			return &plan, errors.New("infrastructure_budget_exhausted")
		}
		if errors.Is(err, repairledger.ErrWait) {
			return &plan, errors.New("repair_waiting")
		}
		return &plan, err
	}
	var postErrors error
	for _, id := range plan.RunIDs {
		if err := provider.RerunFailedJobs(a.Repo, id); err != nil {
			postErrors = errors.Join(postErrors, err)
			continue
		}
		plan.RerunIDs = append(plan.RerunIDs, id)
	}
	return &plan, postErrors
}
