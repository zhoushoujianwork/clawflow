package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func acceptancePlanStorePath(repo string) string {
	return filepath.Join(snapshot.DataDir(), "acceptance-plans", acceptanceHash(repo))
}

// Planning uses the same pinned profile, sanitized environment and native API as review.
func acceptanceExecutor(ec execution.Config) acceptance.ExecuteFunc {
	return func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		timeout := 30 * time.Minute
		if deadline, ok := ctx.Deadline(); ok {
			timeout = time.Until(deadline)
		}
		if timeout <= 0 {
			return execution.Result{}, context.DeadlineExceeded
		}
		return acceptanceNativeRun(ctx, ec, execution.Request{Version: 1, RequestID: execution.NewSessionKey(), Resume: opts.Resume, SessionKey: opts.SessionKey, Workspace: opts.Workspace, Prompt: prompt, Instructions: opts.Instructions, PermissionMode: opts.PermissionMode, ToolsDisabled: opts.ToolsDisabled, AllowedTools: opts.AllowedTools, DisallowedTools: opts.DisallowedTools, TimeoutMS: max(1, timeout.Milliseconds())}, acceptanceReviewerEnv(os.Environ()), opts.Events, nil)
	}
}

// prepareImplementationPlan runs before creating the implementation checkout.
// Only acceptance-enrolled repositories incur planning work.
func prepareImplementationPlan(ctx context.Context, j *runJob) (*acceptance.Plan, error) {
	if j.op.Name != "implement" && j.op.Name != "repair-ci" {
		return nil, nil
	}
	if !j.repoCfg.AcceptanceRequired {
		return nil, nil
	}
	if err := issueWorkAllowed(j.client, j.repo, j.sub.Number); err != nil {
		return nil, err
	}
	rc, ec, err := acceptanceConfiguration(j.repo)
	if err != nil {
		return nil, err
	}
	policy := acceptancePlanningPolicy(j.repo, rc, ec)
	c := acceptance.Contract{Version: acceptance.Version, EvidenceSchema: acceptanceEvidenceSchema, Repository: j.repo, TaskID: j.repo + ":issue:" + strconv.Itoa(j.sub.Number), Requirements: j.sub.Title + "\n\n" + j.sub.Body, PolicyDigest: policy, PRNumber: j.sub.Number}
	if len(c.Requirements) > execution.MaxRequestBytes/2 {
		return nil, errors.New("acceptance requirements exceed size limit")
	}
	if j.acceptancePlan != nil {
		c.PolicyDigest = j.acceptancePlan.PlanningPolicyDigest
	}
	store, err := acceptance.OpenStore(acceptancePlanStorePath(j.repo))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	scratch, err := os.MkdirTemp("", "clawflow-acceptance-plan-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	plan, err := (acceptance.Runner{PlanStore: store, SavedPlan: j.acceptancePlan, Execute: acceptanceExecutor(ec)}).RequirementsPlan(ctx, c, scratch)
	return &plan, err
}

func implementationChecklist(plan *acceptance.Plan) string {
	if plan == nil {
		return ""
	}
	return "\n## Acceptance checklist\n\nThe saved independent acceptance criteria follow. They are derived only from approved requirements and cannot expand scope. Before opening or updating the PR, self-check every criterion and state in the PR body how each criterion is covered, including checks run and limitations. Your self-check is not acceptance evidence; a separate fresh reviewer will judge independently.\n\n" + plan.Text + "\n"
}

// Planning policy excludes checkout paths and PR revision/base identities because
// the isolated planner receives requirements only. Changes to execution policy
// or the criteria protocol still invalidate the cache.
func acceptancePlanningPolicy(repo string, rc config.Repo, ec execution.Config) string {
	return acceptanceHash(struct {
		Version, EvidenceSchema                                  int
		Repository, Platform, BaseURL, Command, Profile, Sandbox string
	}{1, acceptanceEvidenceSchema, repo, rc.Platform, rc.BaseURL, ec.Command, ec.Profile, ec.CodexSandbox})
}
