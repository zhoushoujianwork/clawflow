package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func applyCoordinatorEffect(t *coordinator.Task, j *runJob, r *coordinatorReport, intent coordinator.EffectIntent) (coordinator.EffectResult, error) {
	var e jobEffect
	if err := json.Unmarshal(intent.Payload, &e); err != nil {
		return coordinator.EffectResult{}, err
	}
	out := coordinator.EffectResult{Reference: e.RunID}
	if j.client == nil {
		return out, fmt.Errorf("cannot apply coordinator effect %q: missing VCS client", e.Kind)
	}
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
		pr, lookupErr := findCoordinatorPR(j.client, e)
		if lookupErr != nil {
			return out, lookupErr
		}
		if pr.Number == 0 {
			pr, err = j.client.CreatePR(e.Repo, vcs.PRCreateOpts{Head: e.Head, Base: e.Base, Title: e.Title, Body: e.Body + "\n\n<!-- clawflow:run-id=" + e.RunID + " -->"})
			if err != nil {
				// A competing create (422 on GitHub, 409 on GitLab) or a
				// lost response may have delivered the PR. Reconcile before
				// charging an application failure; never issue another create.
				var lookupErr error
				pr, lookupErr = findCoordinatorPR(j.client, e)
				if lookupErr != nil || pr.Number == 0 {
					return out, err
				}
			}
		}
		// Adoption preserves the existing title/body, including human edits.
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

// findCoordinatorPR prefers an open same-repository PR. Closed PRs are only
// recovered for this exact intent, so replay cannot duplicate a completed write.
func findCoordinatorPR(client vcs.Client, e jobEffect) (vcs.PR, error) {
	var prs []vcs.PR
	var err error
	if lister, ok := client.(vcs.PRHeadLister); ok {
		prs, err = lister.ListPRsForHead(e.Repo, e.Head)
	} else {
		prs, err = client.ListPRs(e.Repo, "all")
	}
	if err != nil {
		return vcs.PR{}, err
	}
	var closed vcs.PR
	for _, pr := range prs {
		if pr.HeadBranch != e.Head {
			continue
		}
		// GitLab list responses may omit source project and target branch.
		// Resolve details before deciding identity; never adopt a fork MR.
		if pr.HeadRepo == "" || pr.BaseBranch == "" {
			pr, err = client.GetPR(e.Repo, pr.Number)
			if err != nil {
				return vcs.PR{}, err
			}
		}
		if pr.HeadRepo != e.Repo || pr.BaseBranch != e.Base || pr.HeadBranch != e.Head {
			continue
		}
		if pr.State == "open" {
			return pr, nil
		}
		if strings.Contains(pr.Body, "<!-- clawflow:run-id="+e.RunID+" -->") {
			closed = pr
		}
	}
	return closed, nil
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
			pr, err := coordinatorResultPR(current, j)
			if err != nil {
				return err
			}
			r.PRNumber, r.PRURL, r.Branch = pr.Number, pr.URL, pr.HeadBranch
		}
		r.Effects = nil
		if failed && retainedInterruption(&current, r) && coordinator.RecoveryExhausted(&current) {
			// The store ends this task instead of reserving it, so say why here.
			if err := announceRecoveryExhausted(j, &current, r); err != nil {
				return err
			}
		}
		return s.apply(ctx, &current, j, r)
	})
}

// recoveryExhaustedRunID is the stable marker that makes the issue comment
// idempotent across replays, restarts and the sweep below.
func recoveryExhaustedRunID(taskID string) string { return "recovery-exhausted-" + taskID }

// announceRecoveryExhausted makes the end of a task visible on its issue and in
// its run record: the recovery budget is spent, so the task failed and its slot
// is free. Only controller-known facts go into the comment; worker-supplied
// error text stays out of it.
func announceRecoveryExhausted(j *runJob, t *coordinator.Task, r *coordinatorReport) error {
	previous := strings.TrimSpace(r.Error)
	r.Error = fmt.Sprintf("recovery attempts exhausted (%d of %d used); task failed and its slot was released. The worker keeps the worktree and session as evidence.", t.Attempt+1, coordinator.MaxInfrastructureAttempts)
	if previous != "" && !strings.Contains(previous, "recovery attempts exhausted") {
		r.Error += " Last interruption: " + sanitizeReviewerText(previous, 500)
	}
	number := t.Issue
	if number <= 0 {
		number = t.PR
	}
	if number <= 0 {
		return nil
	}
	if j.client == nil {
		return fmt.Errorf("cannot announce exhausted recovery for %s#%d: missing VCS client", t.Repo, number)
	}
	body := fmt.Sprintf("恢复次数已用尽（%d/%d）：%s 任务 %s 在 worker %s 上多次中断，已结束为 failed，仓库名额已释放。worker 上保留原 worktree 和 session 作为证据，不会自动创建新任务。\n\nRecovery attempts exhausted (%d of %d): the %s task %s was interrupted repeatedly on worker %s. It is now failed and the repository slot is released. The worker keeps the worktree and session as evidence, and no new task is created.",
		t.Attempt+1, coordinator.MaxInfrastructureAttempts, t.Kind, t.ID, t.LeaseOwner,
		t.Attempt+1, coordinator.MaxInfrastructureAttempts, t.Kind, t.ID, t.LeaseOwner)
	err := j.client.PostIssueCommentIdempotent(t.Repo, number, body, recoveryExhaustedRunID(t.ID))
	if errors.Is(err, vcs.ErrNotSupported) {
		err = j.client.PostIssueComment(t.Repo, number, body)
	}
	return err
}

// coordinatorResultPR restores repair identity from the persisted job context.
func coordinatorResultPR(current coordinator.Task, j *runJob) (vcs.PR, error) {
	if j.repair != nil && j.repair.PR.Number == current.PR {
		return j.repair.PR, nil
	}
	if j.client == nil {
		return vcs.PR{}, fmt.Errorf("cannot restore PR %s#%d: missing repair PR and VCS client", current.Repo, current.PR)
	}
	return j.client.GetPR(current.Repo, current.PR)
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
		err = retryRateLimited(taskCtx, func() error {
			err := json.Unmarshal(t.Result, &r)
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
			return err
		})
		cancel()
		var limited *retry.HTTPError
		if errors.As(err, &limited) && limited.RateLimited {
			coordLog().Warn("coordinator/result-apply-deferred", "task", t.ID, "retry_after", limited.After)
			continue
		}
		if err != nil && ctx.Err() == nil {
			message := sanitizeReviewerText(err.Error(), 4000)
			coordLog().Warn("coordinator/result-apply-failed", "task", t.ID, "err", message)
			if recordErr := store.RecordApplicationFailure(ctx, t.ID, t.FencingToken, message); recordErr != nil && first == nil {
				first = recordErr
			}
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

// retryRateLimited leaves durable results pending if the caller cannot wait for
// reset. Only genuine application failures consume the three-attempt ledger.
func retryRateLimited(ctx context.Context, apply func() error) error {
	for {
		err := apply()
		var limited *retry.HTTPError
		if !errors.As(err, &limited) || !limited.RateLimited {
			return err
		}
		wait := limited.After
		if wait <= 0 {
			wait = time.Minute
		}
		if deadline, ok := ctx.Deadline(); ok && time.Now().Add(wait).After(deadline) {
			return err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
	}
}

// endInactiveInterruptions finishes retained interruptions whose recovery found
// the issue closed or owner-skipped. The store changes only the task row, so
// the worker's journal, worktree and session evidence stay in place, no task is
// created and nothing is written to the VCS. The reason goes to the log.
func endInactiveInterruptions(ctx context.Context, store interface {
	EndInactiveInterruptions(context.Context) ([]coordinator.Task, error)
}) error {
	ended, err := store.EndInactiveInterruptions(ctx)
	for _, t := range ended {
		coordLog().Info("coordinator/interruption_ended", "task", t.ID, "repo", t.Repo, "issue", t.Issue, "kind", t.Kind, "worker", t.LeaseOwner, "epoch", t.FencingToken,
			"reason", "issue is closed or owner-skipped; retained worktree and session are kept as evidence, the repository slot is released")
	}
	return err
}

// endExhaustedInterruptions fails retained interruptions that FinalizeApplication
// kept before the recovery budget check existed. Each is announced on its issue
// first, then ended; the worker's journal, worktree and session stay in place
// and no task is created.
func (s *coordinatorResults) endExhaustedInterruptions(ctx context.Context, store interface {
	EndExhaustedInterruptions(context.Context, func(coordinator.Task) error) ([]coordinator.Task, error)
}) error {
	ended, err := store.EndExhaustedInterruptions(ctx, func(t coordinator.Task) error {
		var r coordinatorReport
		if err := json.Unmarshal(t.Result, &r); err != nil {
			return err
		}
		j, err := s.loadJob(ctx, &t)
		if err != nil {
			return err
		}
		if err := announceRecoveryExhausted(j, &t, &r); err != nil {
			return err
		}
		return s.apply(ctx, &t, j, &r)
	})
	for _, t := range ended {
		coordLog().Info("coordinator/recovery_exhausted", "task", t.ID, "repo", t.Repo, "issue", t.Issue, "kind", t.Kind, "worker", t.LeaseOwner, "epoch", t.FencingToken,
			"reason", "recovery attempts exhausted; task failed and its slot released; retained worktree and session are kept as evidence")
	}
	return err
}
