package acceptance

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// planningContract deliberately excludes all implementation and PR inputs.
func planningContract(c Contract) Contract {
	return Contract{Version: Version, EvidenceSchema: c.EvidenceSchema, Repository: c.Repository, TaskID: c.TaskID, Requirements: c.Requirements, PolicyDigest: c.PolicyDigest}
}

func validatePreparedPlan(c Contract, p Plan) error {
	c = planningContract(c)
	if !nonempty(c.Repository, c.TaskID, c.Requirements, c.PolicyDigest) || p.ContractDigest != digest(c) || p.PlanningPolicyDigest != c.PolicyDigest || p.RequirementsDigest != digest(c.Requirements) || p.Version != Version || !nonempty(p.AttemptID, p.Text) || p.Digest != digest(p.Text) || p.StartedAt.IsZero() || p.CompletedAt.Before(p.StartedAt) {
		return errors.New("invalid requirements acceptance plan")
	}
	if p.PlannerIsolation != ToolsDisabled && p.PlannerIsolation != IsolatedReadonly {
		return errors.New("invalid planner isolation mode")
	}
	if c.EvidenceSchema == 1 {
		if err := validateCriteria(c, p); err != nil {
			return err
		}
	}
	return p.Planner.validate(Identity{}, PlannerPolicy)
}

// RequirementsPlan creates the same isolated plan before implementation or review.
// An optional private PlanStore permits reuse across PR heads and attempts.
func (runner Runner) RequirementsPlan(ctx context.Context, c Contract, scratch string) (Plan, error) {
	usageContract := c
	c = planningContract(c)
	if runner.PlanPolicy != "" {
		c.PolicyDigest = runner.PlanPolicy
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if runner.SavedPlan != nil {
		return *runner.SavedPlan, validatePreparedPlan(c, *runner.SavedPlan)
	}
	name := digest(c) + ".requirements-plan"
	if runner.PlanStore != nil {
		// A PID-owned local lock prevents duplicate model calls for one key.
		// Contention fails closed; the scheduler can retry after the owner exits.
		lockKey := "acceptance-plan-" + digest(c)
		if err := snapshot.AcquireLock(lockKey, 1, "acceptance-plan"); err != nil {
			return Plan{}, err
		}
		defer snapshot.ReleaseLock(lockKey, 1)
		var saved Plan
		err := runner.PlanStore.read(name, &saved)
		if err == nil {
			return saved, validatePreparedPlan(c, saved)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Plan{}, err
		}
	}
	var err error
	scratch, err = directory(scratch)
	if err != nil {
		return Plan{}, err
	}
	if runner.PlanStore != nil && (containsPath(scratch, runner.PlanStore.path) || containsPath(runner.PlanStore.path, scratch)) {
		return Plan{}, errors.New("planner scratch and store must be separate")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		return Plan{}, errors.New("planner scratch must be empty")
	}
	execute := runner.Execute
	if execute == nil {
		execute = execution.Execute
	}
	planInstruction := ""
	if c.EvidenceSchema == 1 {
		planInstruction = criteriaInstruction
	}
	plan := Plan{PlanningPolicyDigest: c.PolicyDigest, RequirementsDigest: digest(c.Requirements), PlannerIsolation: ToolsDisabled, Version: Version, ContractDigest: digest(c), AttemptID: randomID(), StartedAt: time.Now().UTC()}
	// Only requirements are sent in phase one: no source locations, Git inputs,
	// developer output, contract identity, or development session identifiers.
	planned, err := runner.executeRecorded(ctx, usageContract, "acceptance-plan", execute, "Create an independent acceptance test plan for these requirements. Return the plan as nonempty text. Do not inspect source or use tools.\n\n"+planInstruction+c.Requirements,
		execution.Options{Workspace: scratch, Role: "acceptance", PermissionMode: "default", ToolsDisabled: true})
	if err != nil && execution.IsKind(err, "unsupported_tools") && planned.ErrorKind == "unsupported_tools" {
		if err = ctx.Err(); err != nil {
			return Plan{}, err
		}
		entries, readErr := os.ReadDir(scratch)
		if readErr != nil || len(entries) != 0 {
			return Plan{}, errors.New("planner scratch must remain empty before isolation fallback")
		}
		plan.PlannerIsolation = IsolatedReadonly
		planned, err = runner.executeRecorded(ctx, usageContract, "acceptance-plan", execute, "Create an independent acceptance test plan for these requirements. Return the plan as nonempty text. Do not read files outside your working directory or any repository. Do not inspect source.\n\n"+planInstruction+c.Requirements,
			execution.Options{Workspace: scratch, Role: "acceptance", PermissionMode: "default"})
	}
	if err != nil {
		return Plan{}, err
	}
	if err = ctx.Err(); err != nil {
		return Plan{}, err
	}
	plan.Planner = native(planned)
	plan.Text = planned.Text
	if c.EvidenceSchema == 1 {
		if err = decodeStrict([]byte(unfenceReport(plan.Text)), &plan.Criteria); err != nil {
			return Plan{}, err
		}
	}
	plan.Digest = digest(plan.Text)
	plan.CompletedAt = time.Now().UTC()
	if err = validatePreparedPlan(c, plan); err != nil {
		return Plan{}, err
	}

	if runner.PlanStore != nil {
		if err := runner.PlanStore.write(name, plan); err != nil {
			// Another writer may have published the identical requirements key.
			var saved Plan
			if readErr := runner.PlanStore.read(name, &saved); readErr != nil {
				return Plan{}, err
			}
			return saved, validatePreparedPlan(c, saved)
		}
	}
	return plan, nil
}
