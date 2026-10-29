package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type workerAcceptanceKey struct{}

var enqueueWorkerAcceptanceFn = enqueueWorkerAcceptance

func enqueueWorkerAcceptance(ctx context.Context, client vcs.Client, repo string, rc config.Repo, pr vcs.PR, timeout time.Duration) error {
	if err := coordinatorPRAllowedFn(ctx, repo, pr.Number); err != nil {
		return err
	}
	if err := issueWorkAllowed(client, repo, extractClosesIssue(pr.Body)); err != nil {
		return err
	}
	c, _, _, _, err := acceptanceContract(ctx, client, repo, pr.Number)
	if err != nil {
		return err
	}
	db, err := requireStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	s := coordinator.New(db.DB())
	issue := extractClosesIssue(pr.Body)
	dev, err := acceptanceDevelopmentWorker(ctx, s, rc, repo, issue, pr, c)
	if err != nil {
		return err
	}
	subject, err := client.GetIssue(repo, issue)
	if err != nil {
		return err
	}
	if subject == nil {
		return coordinator.ErrInvalid
	}
	contractJSON, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := s.CancelSupersededAcceptance(ctx, repo, pr.Number, contractJSON); err != nil {
		return err
	}
	active, err := s.Active(ctx)
	if err != nil {
		return err
	}
	count := 0
	for _, task := range active {
		if task.Repo == repo && task.Kind == "acceptance" {
			if task.PR == pr.Number {
				return nil
			}
			count++
		}
	}
	if count >= rc.AcceptanceParallelLimit() {
		return errHarvestBusy
	}
	t, p := coordinatorTask(&runJob{repo: repo, repoCfg: rc, op: &operator.Operator{Name: "acceptance"}, sub: &operator.Subject{Number: issue, Labels: subject.Labels}}, timeout)
	t.PR = pr.Number
	p.Acceptance, p.DevelopmentWorker, p.Role = &c, dev.LeaseOwner, "acceptance"
	p.AcceptanceWorker = rc.AcceptanceWorker
	if p.AcceptanceWorker != "" && p.AcceptanceWorker != "any" && p.AcceptanceWorker != "different" {
		return coordinator.ErrInvalid
	}
	_, err = s.EnqueuePrepared(ctx, t, func(t *coordinator.Task) error {
		p.TaskID = t.ID
		t.Payload, err = json.Marshal(p)
		return err
	})
	return err
}

// Workers use the coordinator's contract; local policy and provenance never replace it.
func workerAcceptanceContract(ctx context.Context, client vcs.Client, repo string, prNumber int, p *coordinatorPayload) (acceptance.Contract, config.Repo, execution.Config, vcs.PR, error) {
	c := *p.Acceptance
	rc, ec, err := acceptanceConfiguration(repo)
	var pr vcs.PR
	if err != nil {
		return c, rc, ec, pr, err
	}
	rc.CIRequired, rc.TestCommand, rc.AutoMerge = p.RepoPolicy.CIRequired, p.RepoPolicy.TestCommand, false
	platform := rc.Platform
	if platform == "" {
		platform = "github"
	}
	if platform != p.Platform || rc.BaseURL != p.BaseURL || c.Repository != repo || c.PRNumber != prNumber {
		return c, rc, ec, pr, coordinator.ErrInvalid
	}
	pr, err = client.GetPR(repo, prNumber)
	if err != nil {
		return c, rc, ec, pr, err
	}
	if pr.HeadSHA != c.HeadSHA || pr.BaseSHA != c.BaseSHA || extractClosesIssue(pr.Body) != p.Issue {
		return c, rc, ec, pr, errors.New("acceptance PR changed")
	}
	issue, err := client.GetIssue(repo, p.Issue)
	if err != nil {
		return c, rc, ec, pr, err
	}
	if issue == nil || issue.State != "open" || issue.Title+"\n\n"+issue.Body != c.Requirements {
		return c, rc, ec, pr, errors.New("acceptance requirements changed")
	}
	return c, rc, ec, pr, c.Validate()
}

func runWorkerAcceptanceTask(ctx, parent context.Context, api *workerAPI, o workerOptions, t *coordinator.Task, stop chan struct{}, done chan bool) error {
	report := coordinatorReport{jobResult: jobResult{Status: "failed"}}
	var p coordinatorPayload
	err := json.Unmarshal(t.Payload, &p)
	if err == nil && (!slices.Contains(o.Roles, "acceptance") || p.Acceptance == nil || p.Kind != "acceptance" || p.TaskID != t.ID || p.Repo != t.Repo || p.Issue != t.Issue || p.Acceptance.PRNumber != t.PR || p.TimeoutSec <= 0 || p.TimeoutSec > 86400) {
		err = coordinator.ErrInvalid
	}
	if err == nil {
		var rc config.Repo
		rc, _, err = acceptanceConfiguration(t.Repo)
		if err == nil {
			var client vcs.Client
			client, err = acceptanceClientFactory(rc)
			if err == nil {
				runCtx, cancel := context.WithTimeout(context.WithValue(ctx, workerAcceptanceKey{}, &p), time.Duration(p.TimeoutSec)*time.Second)
				if contextual, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
					contextual.SetRequestContext(runCtx)
				}
				var r acceptance.Record
				r, err = runAcceptance(runCtx, client, t.Repo, t.PR)
				cancel()
				if r.AttemptID != "" && r.Completed && !r.Cancelled {
					report.Acceptance = &r
					report.Status = "success"
				}
			}
		}
	}
	if err != nil {
		report.Error = "worker acceptance did not pass or could not execute"
	}
	close(stop)
	lost := <-done
	if lost || parent.Err() != nil {
		return nil
	}
	action := "fail"
	if report.Acceptance != nil {
		action = "complete"
	}
	reportCtx, cancel := context.WithCancel(parent)
	defer cancel()
	return api.post(reportCtx, "tasks/"+url.PathEscape(t.ID)+"/"+action, map[string]any{"fencing_token": t.FencingToken, "result": report}, nil)
}

func (s *coordinatorResults) finishAcceptance(ctx context.Context, t *coordinator.Task, worker string, fence int64, raw json.RawMessage, r *coordinatorReport, failed bool) error {
	var p coordinatorPayload
	if json.Unmarshal(t.Payload, &p) != nil || p.Acceptance == nil || p.TaskID != t.ID || len(r.Effects) != 0 || r.DevelopmentEvents != "" {
		return coordinator.ErrInvalid
	}
	// Routing policy never relaxes the independence required for uploaded evidence.
	if (p.AcceptanceWorker != "any" && p.AcceptanceWorker != "different") || worker == p.DevelopmentWorker || p.DevelopmentWorker == "" {
		return coordinator.ErrInvalid
	}
	if r.Acceptance == nil {
		if !failed {
			return coordinator.ErrInvalid
		}
		now := time.Now().UTC()
		digest, _ := p.Acceptance.Digest()
		r.Acceptance = &acceptance.Record{Version: acceptance.Version, Contract: *p.Acceptance, ContractDigest: digest, AttemptID: execution.NewSessionKey(), Role: "acceptance", Verdict: acceptance.Blocked, Report: "Worker acceptance setup or execution failed", Completed: true, StartedAt: now, CompletedAt: now}
	} else {
		// Decode nested evidence with the store's exact schema decoder.
		var envelope struct {
			Acceptance json.RawMessage `json:"acceptance"`
		}
		if json.Unmarshal(raw, &envelope) != nil || acceptance.DecodeUpload(envelope.Acceptance, r.Acceptance) != nil {
			return coordinator.ErrInvalid
		}
	}
	rc, _, err := acceptanceConfiguration(t.Repo)
	if err != nil {
		return err
	}
	client, err := acceptanceClientFactory(rc)
	if err != nil {
		return err
	}
	if contextual, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
		contextual.SetRequestContext(ctx)
	}
	c, _, _, _, err := acceptanceContract(ctx, client, t.Repo, t.PR)
	if err != nil {
		return err
	}
	pinned, _ := p.Acceptance.Digest()
	current, _ := c.Digest()
	if pinned != current || acceptance.ValidateUpload(c, *r.Acceptance) != nil {
		return coordinator.ErrInvalid
	}
	unlock, err := acceptanceLock(t.Repo, t.PR)
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok := s.tasks.(coordinator.EffectStore); ok {
		r.Effects = []jobEffect{{Kind: "pr-comment", Repo: t.Repo, Number: t.PR, Body: acceptanceReportBody(*r.Acceptance)}}
		sealed, err := json.Marshal(r)
		if err != nil {
			return err
		}
		return s.tasks.AcceptResult(ctx, t.ID, worker, fence, sealed)
	}
	if err := s.tasks.AcceptResult(ctx, t.ID, worker, fence, raw); err != nil {
		return err
	}
	store, err := acceptance.OpenStore(acceptanceStorePath(t.Repo, t.PR))
	if err != nil {
		return err
	}
	defer store.Close()
	if r.Acceptance.Plan.Text != "" {
		if err := store.SavePlan(c, r.Acceptance.Plan); err != nil {
			return err
		}
	}
	if err := store.SaveRecord(*r.Acceptance); err != nil {
		return err
	}
	if err := s.tasks.FinishResult(ctx, t.ID, worker, fence, false); err != nil {
		return err
	}
	// The normal coordinator sweep consumes this record for repair or conditional merge.
	return publishAcceptanceReport(ctx, client, *r.Acceptance)
}

// Resolve worker ownership only at a head justified by the existing provenance check.
type acceptanceDevelopmentLookup interface {
	LatestDevelopment(context.Context, string, int, int, string) (*coordinator.Task, error)
}

func acceptanceDevelopmentWorker(ctx context.Context, s acceptanceDevelopmentLookup, rc config.Repo, repo string, issue int, pr vcs.PR, c acceptance.Contract) (*coordinator.Task, error) {
	dev, err := s.LatestDevelopment(ctx, repo, issue, pr.Number, c.HeadSHA)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return dev, err
	}
	identity, head, err := acceptanceInheritedDevelopmentHead(ctx, rc, repo, issue, pr)
	if err != nil {
		return nil, err
	}
	if identity != c.Development {
		return nil, errors.New("inherited development identity changed")
	}
	return s.LatestDevelopment(ctx, repo, issue, pr.Number, head)
}
