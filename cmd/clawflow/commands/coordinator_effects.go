package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func applyCoordinatorEffect(t *coordinator.Task, j *runJob, r *coordinatorReport, intent coordinator.EffectIntent) (coordinator.EffectResult, error) {
	var e jobEffect
	if err := json.Unmarshal(intent.Payload, &e); err != nil {
		return coordinator.EffectResult{}, err
	}
	out := coordinator.EffectResult{Reference: e.RunID}
	var err error
	switch e.Kind {
	case "issue-comment":
		err = j.client.PostIssueCommentIdempotent(e.Repo, e.Number, e.Body, e.RunID)
	case "pr-comment":
		if j.repoCfg.Platform == "gitlab" {
			if c, ok := j.client.(interface {
				PostPRCommentIdempotent(string, int, string, string) error
			}); ok {
				err = c.PostPRCommentIdempotent(e.Repo, e.Number, e.Body, e.RunID)
			} else {
				err = fmt.Errorf("VCS client lacks idempotent PR comments")
			}
		} else {
			err = j.client.PostIssueCommentIdempotent(e.Repo, e.Number, e.Body, e.RunID)
		}
	case "add-label":
		err = j.client.AddLabel(e.Repo, e.Number, e.Labels...)
	case "remove-label":
		err = j.client.RemoveLabel(e.Repo, e.Number, e.Labels...)
	case "close-issue":
		err = j.client.CloseIssue(e.Repo, e.Number)
	case "pr-create":
		// Include closed PRs: an external close between a successful write and
		// recovery must not create a second logical PR for this intent.
		prs, listErr := j.client.ListPRs(e.Repo, "all")
		if listErr != nil {
			return out, listErr
		}
		var pr vcs.PR
		for _, p := range prs {
			if p.HeadBranch == e.Head && p.HeadRepo == e.Repo && p.BaseBranch == e.Base && (p.State == "open" || strings.Contains(p.Body, "<!-- clawflow:run-id="+e.RunID+" -->")) {
				pr = p
				break
			}
		}
		if pr.Number == 0 {
			pr, err = j.client.CreatePR(e.Repo, vcs.PRCreateOpts{Head: e.Head, Base: e.Base, Title: e.Title, Body: e.Body + "\n\n<!-- clawflow:run-id=" + e.RunID + " -->"})
			if err != nil {
				return out, err
			}
		}
		pr, err = j.client.GetPR(e.Repo, pr.Number)
		if err != nil {
			return out, err
		}
		out.PR = pr.Number
		out.Reference = strconv.Itoa(pr.Number)
		out.HeadMismatch = pr.HeadSHA != r.HeadSHA || pr.HeadRepo != e.Repo || pr.HeadBranch != e.Head || pr.BaseBranch != e.Base
		r.PRNumber, r.PRURL, r.Branch = pr.Number, pr.URL, pr.HeadBranch
	default:
		err = coordinator.ErrInvalid
	}
	return out, err
}

func (s *coordinatorResults) reconcile(ctx context.Context, store coordinator.EffectStore, t *coordinator.Task, j *runJob, r *coordinatorReport, failed bool) error {
	if err := store.ApplyEffects(ctx, t.ID, t.FencingToken, func(e coordinator.EffectIntent) (coordinator.EffectResult, error) {
		result, err := applyCoordinatorEffect(t, j, r, e)
		if err == nil && result.HeadMismatch {
			// Persist the visible run before making the task terminal, so a
			// crash cannot strand the PR identity outside run history.
			r.Status = "head-mismatch"
			r.Effects = nil
			err = s.apply(ctx, t, j, r)
		}
		return result, err
	}); err != nil {
		return err
	}
	current, err := s.tasks.Task(ctx, t.ID)
	if err != nil {
		return err
	}
	if current.State == "head-mismatch" {
		return nil
	}
	return store.FinalizeApplication(ctx, t.ID, t.FencingToken, failed, func(current coordinator.Task) error {
		// Restore the PR identity even when all intents were applied before restart.
		if current.PR > 0 && r.PRNumber == 0 {
			pr, err := j.client.GetPR(current.Repo, current.PR)
			if err != nil {
				return err
			}
			r.PRNumber, r.PRURL, r.Branch = pr.Number, pr.URL, pr.HeadBranch
		}
		r.Effects = nil
		return s.apply(ctx, &current, j, r)
	})
}

func (s *coordinatorResults) recoverResults(ctx context.Context, store coordinator.EffectStore) error {
	tasks, err := store.PendingResults(ctx)
	if err != nil {
		return err
	}
	var first error
	for i := range tasks {
		t := &tasks[i]
		taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)

		var r coordinatorReport
		err = json.Unmarshal(t.Result, &r)
		if err == nil && t.Kind == "acceptance" {
			err = s.recoverAcceptance(taskCtx, store, t, &r)
		} else if err == nil {
			var j *runJob
			j, err = s.loadJob(taskCtx, t)
			if err == nil {
				failed := r.TerminalState == "failed"
				if r.TerminalState == "" {
					failed = r.Status != "success" && r.Status != "skipped"
				}
				err = s.reconcile(taskCtx, store, t, j, &r, failed)
			}
		}
		cancel()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

var coordinatorPRAllowedFn = coordinatorPRAllowed

func coordinatorPRAllowed(ctx context.Context, repo string, pr int) error {
	db, err := requireStore(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	blocked, err := coordinator.New(db.DB()).PRBlocked(ctx, repo, pr)
	if err != nil {
		return err
	}
	if blocked {
		return fmt.Errorf("coordinator PR head mismatch requires a fresh attempt")
	}
	return nil
}

// Recovery keeps the existing independent acceptance contract and publisher.
// Its stable acceptance marker makes publication replay safe.
func (s *coordinatorResults) recoverAcceptance(ctx context.Context, durable coordinator.EffectStore, t *coordinator.Task, r *coordinatorReport) error {
	if r.Acceptance == nil {
		return coordinator.ErrInvalid
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
	unlock, err := acceptanceLock(t.Repo, t.PR)
	if err != nil {
		return err
	}
	defer unlock()
	current, _, _, _, err := acceptanceContract(ctx, client, t.Repo, t.PR)
	if err != nil {
		return err
	}
	digest, _ := current.Digest()
	if digest != r.Acceptance.ContractDigest {
		return durable.Cancel(ctx, t.ID)
	}
	saveAndPublish := func() error {
		records, err := acceptance.OpenStore(acceptanceStorePath(t.Repo, t.PR))
		if err != nil {
			return err
		}
		if r.Acceptance.Plan.Text != "" {
			err = records.SavePlan(current, r.Acceptance.Plan)
		}
		if err == nil {
			err = records.SaveRecord(*r.Acceptance)
		}
		records.Close()
		if err != nil {
			return err
		}
		return publishAcceptanceReport(ctx, client, *r.Acceptance)
	}
	if len(r.Effects) > 0 {
		if err := durable.ApplyEffects(ctx, t.ID, t.FencingToken, func(coordinator.EffectIntent) (coordinator.EffectResult, error) {
			return coordinator.EffectResult{Reference: r.Acceptance.AttemptID}, saveAndPublish()
		}); err != nil {
			return err
		}
		return durable.FinalizeApplication(ctx, t.ID, t.FencingToken, false, func(coordinator.Task) error { return nil })
	}
	// Reports sealed before the outbox migration retain their existing marker.
	return durable.FinalizeApplication(ctx, t.ID, t.FencingToken, false, func(coordinator.Task) error { return saveAndPublish() })
}
