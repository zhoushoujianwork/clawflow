package commands

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordination"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/repairledger"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type coordinatorPayload struct {
	Coordination *coordination.Snapshot `json:"coordination,omitempty"`

	Title             string               `json:"title,omitempty"`
	Testing           testingRequirements  `json:"testing,omitempty"`
	AcceptancePlan    *acceptance.Plan     `json:"acceptance_plan,omitempty"`
	Acceptance        *acceptance.Contract `json:"acceptance,omitempty"`
	AcceptanceWorker  string               `json:"acceptance_worker,omitempty"`
	DevelopmentWorker string               `json:"development_worker,omitempty"`
	TaskID            string               `json:"task_id"`
	Kind              string               `json:"kind"`
	Operator          string               `json:"operator"`
	SkillDigest       string               `json:"skill_digest"`
	Repo              string               `json:"repo"`
	Platform          string               `json:"platform"`
	BaseURL           string               `json:"base_url"`
	Issue             int                  `json:"issue"`
	Role              string               `json:"role"`
	Language          string               `json:"language"`
	TimeoutSec        int64                `json:"timeout_sec"`
	Tags              []string             `json:"tags"`
	RepoPolicy        struct {
		AcceptanceSetupCommand string `json:"acceptance_setup_command,omitempty"`
		BaseBranch             string `json:"base_branch"`
		TestCommand            string `json:"test_command"`
		AcceptanceRequired     bool   `json:"acceptance_required"`
		CIRequired             bool   `json:"ci_required"`
	} `json:"repo_policy"`
	ReopenEventID string             `json:"reopen_event_id"`
	Repair        *coordinatorRepair `json:"repair,omitempty"`
	Scan          struct {
		Labels     []string  `json:"labels"`
		SubTotal   int       `json:"sub_total"`
		ObservedAt time.Time `json:"observed_at"`
	} `json:"scan"`
}

type coordinatorRepair struct {
	PR         int                 `json:"pr"`
	HeadSHA    string              `json:"head_sha"`
	HeadBranch string              `json:"head_branch"`
	HeadRepo   string              `json:"head_repo"`
	BaseSHA    string              `json:"base_sha"`
	BaseBranch string              `json:"base_branch"`
	Attempt    int                 `json:"attempt"`
	Reason     string              `json:"reason"`
	Acceptance *acceptanceFindings `json:"acceptance,omitempty"`
}

// skillDigest excludes machine-local paths while covering executable semantics.
func skillDigest(op *operator.Operator) string {
	copy := *op
	copy.Source = ""
	b, _ := json.Marshal(copy)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func coordinatorTask(j *runJob, timeout time.Duration) (coordinator.Task, coordinatorPayload) {
	kind := "reply"
	if j.op.Name == "acceptance" {
		kind = "acceptance"
	}
	if j.op.Name == "implement" {
		kind = "implement"
	}
	if j.repair != nil {
		kind = "repair"
	}
	platform := j.repoCfg.Platform
	if platform == "" {
		platform = "github"
	}
	tags := append([]string{}, j.repoCfg.WorkerTags...)
	if override, ok := j.repoCfg.WorkerTagsByKind[kind]; ok {
		tags = append([]string{}, override...)
		// Host affinity applies to every task kind, including legacy pins.
		for _, tag := range j.repoCfg.WorkerTags {
			if strings.HasPrefix(tag, "host-") {
				tags = append(tags, tag)
			}
		}
	}
	// Platform metadata does not imply a worker tag; routing constraints are explicit.
	if j.repoCfg.BoundMachine != "" {
		tags = append(tags, config.HostTag(j.repoCfg.BoundMachine))
	}
	priority := j.repoCfg.Priority * 3
	for _, label := range j.sub.Labels {
		if tag, ok := strings.CutPrefix(label, "worker:"); ok && tag != "" {
			tags = append(tags, tag)
		}
		switch label {
		case "priority:high":
			priority--
		case "priority:low":
			priority++
		}
	}
	if j.coordination != nil {
		tags = append(tags, coordination.WorkerTag)
	}
	sort.Strings(tags)
	unique := tags[:0]
	for _, tag := range tags {
		if len(unique) == 0 || unique[len(unique)-1] != tag {
			unique = append(unique, tag)
		}
	}
	p := coordinatorPayload{Testing: declaredTesting(j.repo, j.repoCfg), Kind: kind, Operator: j.op.Name, SkillDigest: skillDigest(j.op), Repo: j.repo, Platform: platform, BaseURL: j.repoCfg.BaseURL, Issue: j.sub.Number, Role: roleForOperator(j.op.Name), Language: j.language, TimeoutSec: int64(timeout / time.Second), Tags: unique}
	p.Coordination = j.coordination
	p.Title = j.sub.Title
	p.RepoPolicy.AcceptanceSetupCommand = j.repoCfg.AcceptanceSetupCommand
	p.RepoPolicy.BaseBranch, p.RepoPolicy.TestCommand, p.RepoPolicy.CIRequired = j.repoCfg.BaseBranch, j.repoCfg.TestCommand, j.repoCfg.CIRequired
	p.AcceptancePlan = j.acceptancePlan
	p.RepoPolicy.AcceptanceRequired = j.repoCfg.AcceptanceRequired
	p.Scan.Labels, p.Scan.SubTotal, p.Scan.ObservedAt = append([]string{}, j.sub.Labels...), j.sub.SubTotal, time.Now().UTC()
	if j.reopen != nil {
		p.ReopenEventID = j.reopen.Event.ID
	}
	t := coordinator.Task{Kind: kind, Repo: j.repo, Issue: j.sub.Number, Operator: j.op.Name, Tags: unique, Priority: priority}
	if j.repair != nil {
		r := j.repair
		t.PR = r.PR.Number
		p.Repair = &coordinatorRepair{PR: r.PR.Number, HeadSHA: r.PR.HeadSHA, HeadBranch: r.PR.HeadBranch, HeadRepo: r.PR.HeadRepo, BaseSHA: r.PR.BaseSHA, BaseBranch: r.PR.BaseBranch, Attempt: r.Attempt, Reason: r.reason(), Acceptance: r.Acceptance}
	}
	return t, p
}

func enqueueCoordinatorJobs(ctx context.Context, jobs []*runJob, timeout time.Duration) ([]firedKey, []firedKey, error) {
	db, err := requireStore(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	return dispatchCoordinatorJobs(ctx, coordinator.New(db.DB()), jobs, timeout)
}

type coordinatorQueue interface {
	EnqueuePrepared(context.Context, coordinator.Task, func(*coordinator.Task) error) (*coordinator.Task, error)
	ExpireLeases(context.Context, time.Time) ([]coordinator.Task, error)
}

func dispatchCoordinatorJobs(ctx context.Context, s coordinatorQueue, jobs []*runJob, timeout time.Duration) ([]firedKey, []firedKey, error) {
	if _, err := s.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		return nil, nil, err
	}
	rank, err := queue.LoadRank()
	if err != nil {
		return nil, nil, err
	}
	positions := queue.Positions(rank)
	var fired, failed []firedKey
	for _, j := range jobs {
		if j.harvest != nil {
			failed = append(failed, j.harvest(ctx)...)
			continue
		}
		// Use the same queue dependency syntax, including cross-repository keys.
		blocked := false
		deps, _ := queue.ParseDependsOn(j.sub.Body, j.repo)
		for _, dep := range deps {
			client := j.client
			if dep.Repo != j.repo {
				var err error
				client, _, _, err = newVCSClientForRepo(dep.Repo)
				if err != nil {
					blocked = true
					break
				}
			}
			issue, err := client.GetIssue(dep.Repo, dep.Number)
			if err != nil || issue == nil || issue.State != "closed" {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		if source, ok := s.(coordinationTasks); ok {
			allowed, checkErr := coordinateDispatch(ctx, j, source)
			if checkErr != nil {
				runLog.Warn("run/coordination_wait", "repo", j.repo, "issue", j.sub.Number, "err", checkErr.Error())
			}
			if !allowed || checkErr != nil {
				continue
			}
		}
		plan, err := prepareImplementationPlan(ctx, j)
		if err != nil {
			return fired, failed, err
		}
		j.acceptancePlan = plan
		t, p := coordinatorTask(j, timeout)
		if p.ReopenEventID != "" {
			t.EventSource = t.Repo + "/reopen"
			t.ExternalID = p.ReopenEventID
		}
		t.QueuePosition = int64(len(rank))
		if pos, ok := positions[queue.Key{Repo: j.repo, Number: j.sub.Number}]; ok {
			t.QueuePosition = int64(pos)
		}
		reserved := false
		var repairPreparationErr error
		var coordinationPreparationErr error
		_, err = s.EnqueuePrepared(ctx, t, func(t *coordinator.Task) error {
			if j.coordination != nil {
				fresh, e := collectCoordination(ctx, j, nil, "")
				if e != nil {
					coordinationPreparationErr = e
					return e
				}
				fresh.Tasks = j.coordination.Tasks
				if coordination.Digest(fresh) != coordination.Digest(*j.coordination) {
					coordinationPreparationErr = errCoordinationChanged
					return coordinationPreparationErr
				}
			}
			if j.repair != nil {
				repairPreparationErr = prepareCIRepairContext(ctx, j, time.Now().UTC())
				if repairPreparationErr != nil {
					return repairPreparationErr
				}
				reserved = true
				_, refreshed := coordinatorTask(j, timeout)
				p.Repair = refreshed.Repair
			}
			p.TaskID = t.ID
			var err error
			t.Payload, err = json.Marshal(p)
			return err
		})
		if err != nil {
			if errors.Is(err, coordinator.ErrCoordinationChanged) {
				if reserved {
					_ = j.releaseRepair("coordination-changed", time.Now().UTC())
				}
				runLog.Info("run/coordination_wait", "repo", j.repo, "issue", j.sub.Number, "err", err.Error())
				continue
			}
			if coordinationPreparationErr != nil {
				runLog.Info("run/coordination_wait", "repo", j.repo, "issue", j.sub.Number, "err", coordinationPreparationErr.Error())
				continue
			}
			// Match local execution: a repair preparation failure skips only
			// this job. Store failures and pass cancellation remain fatal.
			if repairPreparationErr != nil && errors.Is(err, repairPreparationErr) && ctx.Err() == nil {
				runLog.Warn("run/ci_repair_skip", "repo", j.repo, "issue", j.sub.Number, "err", repairPreparationErr.Error())
				continue
			}
			if reserved {
				_ = j.releaseRepair("enqueue", time.Now().UTC())
			}
			return fired, failed, fmt.Errorf("enqueue %s#%d: %w", j.repo, j.sub.Number, err)
		}
		fired = append(fired, firedKey{Repo: j.repo, IssueNumber: j.sub.Number, Operator: j.op.Name})
	}
	return fired, failed, nil
}

// coordinatorResults validates reports before sealing them with durable intents.
// The separate applier reconciles remote effects after startup and on each tick.
type coordinatorResults struct {
	coordinator.WorkerStore
	tasks interface {
		Task(context.Context, string) (*coordinator.Task, error)
		AcceptResult(context.Context, string, string, int64, json.RawMessage) error
		FinishResult(context.Context, string, string, int64, bool) error
	}
	loadJob func(context.Context, *coordinator.Task) (*runJob, error)
	apply   func(context.Context, *coordinator.Task, *runJob, *coordinatorReport) error
}

type coordinatorReport struct {
	ProvenanceWarnings []string           `json:"provenance_warnings,omitempty"`
	TerminalState      string             `json:"coordinator_state,omitempty"`
	Acceptance         *acceptance.Record `json:"acceptance,omitempty"`
	jobResult
	Events []json.RawMessage `json:"events,omitempty"`
}

func (s *coordinatorResults) Task(ctx context.Context, id string) (*coordinator.Task, error) {
	return s.tasks.Task(ctx, id)
}

func (s *coordinatorResults) Complete(ctx context.Context, id, worker string, fence int64, raw json.RawMessage) error {
	return s.finish(ctx, id, worker, fence, raw, false)
}
func (s *coordinatorResults) Fail(ctx context.Context, id, worker string, fence int64, raw json.RawMessage) error {
	return s.finish(ctx, id, worker, fence, raw, true)
}
func (s *coordinatorResults) finish(ctx context.Context, id, worker string, fence int64, raw json.RawMessage, failed bool) error {
	type Result = jobResult
	var wire struct {
		Result
		Usage      json.RawMessage    `json:"usage,omitempty"`
		Acceptance *acceptance.Record `json:"acceptance,omitempty"`
		Events     []json.RawMessage  `json:"events,omitempty"`
	}
	if err := acceptance.DecodeExact(raw, &wire); err != nil {
		return coordinator.ErrInvalid
	}
	warnings := wire.Result.Provenance.normalize()
	// Usage contains a dynamic model-name map, unlike the fixed evidence schema.
	if len(wire.Usage) > 0 && json.Unmarshal(wire.Usage, &wire.Result.Usage) != nil {
		return coordinator.ErrInvalid
	}
	r := coordinatorReport{ProvenanceWarnings: warnings, jobResult: wire.Result, Acceptance: wire.Acceptance, Events: wire.Events, TerminalState: "done"}
	if failed {
		r.TerminalState = "failed"
	}
	switch r.Status {
	case "blocked", "success", "failed", "disallowed-outcome", "skipped-empty", "output-limit", "lease-lost", "skipped", "cancelled", "auth-error", "no-marker", "rate-limited", "cost-limit":
	default:
		return coordinator.ErrInvalid
	}
	ctx = coordinator.WithReportDigest(ctx, raw, failed)
	t, err := s.tasks.Task(ctx, id)
	if err != nil {
		return err
	}
	if coordinator.MatchingReport(t, worker, fence, raw, failed) {
		return nil
	}
	if receipts, ok := s.tasks.(interface {
		ReportAcknowledged(context.Context, string, string, int64, json.RawMessage, bool) (bool, error)
	}); ok {
		acknowledged, err := receipts.ReportAcknowledged(ctx, id, worker, fence, raw, failed)
		if err != nil {
			return err
		}
		if acknowledged {
			return nil
		}
	}
	// Fast rejection before loading config or creating a VCS client. The store
	// checks again under a row lock when sealing, including lease expiry.
	if t.State != "leased" || t.LeaseOwner != worker || t.FencingToken != fence || t.LeaseExpiresAt == nil || (!coordinator.DurableTask(t) && !t.LeaseExpiresAt.After(time.Now())) {
		return coordinator.ErrStaleLease
	}
	if r.Status == "blocked" {
		if !failed || r.NextAction == "" || r.NextOwner == "" || r.Error == "" || len(r.Effects) != 0 || r.Acceptance != nil {
			return coordinator.ErrInvalid
		}
	}
	if t.Kind == "acceptance" {
		return s.finishAcceptance(ctx, t, worker, fence, raw, &r, failed)
	}
	if r.Acceptance != nil {
		return coordinator.ErrInvalid
	}
	if r.Status == "blocked" {
		r.Output = testingBlockedBody(&r.jobResult)
		r.Effects = []jobEffect{{Kind: "issue-comment", Repo: t.Repo, Number: t.Issue, Body: r.Output}}
	}
	j, err := s.loadJob(ctx, t)
	if err != nil {
		return err
	}
	for _, e := range r.Effects {
		if e.Repo != t.Repo || (e.Kind != "pr-create" && ((e.Number != t.Issue && e.Number != t.PR) || e.Number <= 0)) {
			return coordinator.ErrInvalid
		}
		switch e.Kind {
		case "pr-create":
			if !acceptanceSHA(r.HeadSHA) || e.Head == "" || e.Base == "" || e.Title == "" || t.Kind != "implement" {
				return coordinator.ErrInvalid
			}
		case "issue-comment", "pr-comment", "add-label", "remove-label", "close-issue":
		default:
			return coordinator.ErrInvalid
		}
	}
	// Keep the most recent events, bounded independently of the HTTP body cap.
	if len(r.Events) > 100 {
		r.Events = r.Events[len(r.Events)-100:]
	}
	size := 0
	for i := len(r.Events) - 1; i >= 0; i-- {
		size += len(r.Events[i])
		if size > 64<<10 {
			r.Events = r.Events[i+1:]
			break
		}
	}
	bounded, err := json.Marshal(r)
	if err != nil {
		return err
	}

	if err = s.tasks.AcceptResult(ctx, id, worker, fence, bounded); err != nil {
		return err
	}
	if _, ok := s.tasks.(coordinator.EffectStore); ok {
		// The startup/periodic applier owns VCS I/O. A worker response only
		// acknowledges the committed report, independent of remote latency.
		return nil
	}
	if err = s.apply(ctx, t, j, &r); err != nil {
		_ = s.WorkerStore.AppendAudit(ctx, worker, id, "result-apply-failed")
		return err
	}
	return s.tasks.FinishResult(ctx, id, worker, fence, failed)

}

func loadCoordinatorJob(ctx context.Context, t *coordinator.Task) (*runJob, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	rc, ok := cfg.Repos[t.Repo]
	if !ok {
		return nil, fmt.Errorf("task repository is no longer configured")
	}
	client, err := newVCSClient(rc)
	if err != nil {
		return nil, err
	}
	if c, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
		c.SetRequestContext(ctx)
	}
	reg, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	var op *operator.Operator
	for _, o := range reg.All() {
		if o.Name == t.Operator {
			op = o
			break
		}
	}
	if op == nil {
		return nil, fmt.Errorf("task operator is no longer registered")
	}
	var p coordinatorPayload
	if err = json.Unmarshal(t.Payload, &p); err != nil {
		return nil, err
	}
	j := &runJob{repo: t.Repo, repoCfg: rc, client: client, op: op, sub: &operator.Subject{Number: t.Issue}}
	if p.Repair != nil {
		r := p.Repair
		j.repair = &ciRepairContext{PR: vcs.PR{Number: r.PR, HeadSHA: r.HeadSHA, HeadBranch: r.HeadBranch, HeadRepo: r.HeadRepo, BaseSHA: r.BaseSHA, BaseBranch: r.BaseBranch}, Attempt: r.Attempt, Acceptance: r.Acceptance, MergeConflict: r.Reason == "merge_conflict", DevelopmentVerification: r.Reason == "development_verification"}
	}
	return j, nil
}

func applyCoordinatorReport(ctx context.Context, t *coordinator.Task, j *runJob, r *coordinatorReport) (applyErr error) {
	now := time.Now().UTC()
	status := r.Status
	if r.Status == "lease-lost" {
		status = "cancelled"
	}
	if j.repair != nil {
		j.repair.FailingCheck = r.FailingCheck
		if err := repairledger.ApplyResult(snapshot.DataDir(), j.repo, j.repair.PR, j.repair.Attempt, t.ID, j.repair.reason(), r.InfraKind, now); err != nil {
			return err
		}
		if r.InfraKind != "" {
			runLog.Warn("run/ci_repair_infra", "repo", j.repo, "issue", j.sub.Number, "pr", j.repair.PR.Number, "kind", r.InfraKind, "err", r.Error)
		}
		if r.InfraKind != "" && status == "failed" {
			status = "skipped"
		}
	}
	meta := snapshot.RunMeta{Repo: t.Repo, IssueNumber: t.Issue, Operator: t.Operator, StartedAt: t.CreatedAt, EndedAt: &now, Status: status, Outcome: r.Outcome, Error: r.Error, Usage: r.Usage, HeadSHA: r.HeadSHA, PRUrl: r.PRURL, PRNumber: r.PRNumber, Summary: r.Output}
	if j.repair != nil {
		meta.RepairReason = j.repair.reason()
	}
	if r.Outcome == "no-change" {
		meta.OutcomeReason = r.Output
	}
	meta.Warnings = r.ProvenanceWarnings
	meta.Machine = t.LeaseOwner
	if p := r.Provenance; p != nil {
		if p.Machine != "" {
			meta.Machine = p.Machine
		}
		meta.Profile, meta.Backend, meta.Model = p.Profile, p.Backend, p.Model
		meta.ExecutionMetadata = snapshot.ExecutionMetadata{OffloadRunID: p.OffloadRunID, CallerRunID: p.CallerRunID, SessionID: p.SessionID, ExitCode: p.ExitCode, ErrorKind: p.ErrorKind, ElapsedMs: p.ElapsedMs, ExecutionStatus: p.ExecutionStatus}
	}
	dir := snapshot.RunDir(t.Repo, t.Issue, t.CreatedAt) + "-task-" + t.ID
	if err := persistDevelopmentEvents(ctx, t, j, r, dir); err != nil {
		return err
	}
	if err := snapshot.WriteRunMeta(dir, meta); err != nil {
		return err
	}
	defer func() {
		if applyErr != nil {
			meta.WritebackError = "coordinator result effects incomplete; inspect task and VCS before recovery"
			_ = snapshot.WriteRunMeta(dir, meta)
		}
	}()
	if r.Status == "lease-lost" {
		return nil
	}
	for _, e := range r.Effects {
		var err error
		switch e.Kind {
		case "issue-comment":
			if e.RunID != "" {
				err = j.client.PostIssueCommentIdempotent(e.Repo, e.Number, e.Body, e.RunID)
			} else {
				err = j.client.PostIssueComment(e.Repo, e.Number, e.Body)
			}
		case "pr-comment":
			err = j.client.PostPRComment(e.Repo, e.Number, e.Body)
		case "add-label":
			err = j.client.AddLabel(e.Repo, e.Number, e.Labels...)
		case "remove-label":
			err = j.client.RemoveLabel(e.Repo, e.Number, e.Labels...)
		case "close-issue":
			err = j.client.CloseIssue(e.Repo, e.Number)
		}
		if err != nil {
			return err
		}
	}
	prefix := fmt.Sprintf("[%s#%d %s]", j.repo, j.sub.Number, j.op.Name)
	if r.Status == "success" {
		runPostAutomation(ctx, j, r.Outcome, r.Output, prefix)
	}
	if j.repair != nil {
		if status != "success" && r.InfraKind == "" {
			return finishRepairFailure(ctx, j, r.Error)
		}
		return nil
	}
	switch status {
	case "failed", "disallowed-outcome", "skipped-empty", "output-limit":
		checkCircuitBreaker(j, prefix)
	}
	return nil
}

func activeCoordinatorIssues(ctx context.Context) (map[string]map[int]bool, error) {
	db, err := requireStore(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tasks, err := coordinator.New(db.DB()).Active(ctx)
	if err != nil {
		return nil, err
	}
	active := map[string]map[int]bool{}
	for _, t := range tasks {
		if t.Kind != "implement" && t.Kind != "repair" {
			continue
		}
		if active[t.Repo] == nil {
			active[t.Repo] = map[int]bool{}
		}
		active[t.Repo][t.Issue] = true
	}
	return active, nil
}

// Only native metadata validated by the local provenance checker is persisted.
func persistDevelopmentEvents(ctx context.Context, t *coordinator.Task, j *runJob, r *coordinatorReport, dir string) error {
	if (t.Kind != "implement" && t.Kind != "repair") || r.Status != "success" || r.DevelopmentEvents == "" || ctx.Err() != nil {
		return nil
	}
	if _, err := developmentIdentityBytes([]byte(r.DevelopmentEvents)); err != nil {
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewBufferString(r.DevelopmentEvents))
	scanner.Buffer(make([]byte, 4096), execution.MaxEventBytes)
	for scanner.Scan() {
		var event execution.Event
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil {
			return nil
		}
	}
	if scanner.Err() != nil {
		return nil
	}
	number := t.PR
	if number == 0 {
		number = r.PRNumber
	}
	if number <= 0 || !acceptanceSHA(r.HeadSHA) {
		return nil
	}
	pr, err := j.client.GetPR(t.Repo, number)
	if err != nil || pr.HeadSHA != r.HeadSHA || extractClosesIssue(pr.Body) != t.Issue {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(r.DevelopmentEvents), 0600)
}

func (s *coordinatorResults) RecoverExecution(ctx context.Context, id, worker string, epoch int64) (*coordinator.Task, error) {
	recovery, ok := s.WorkerStore.(interface {
		RecoverExecution(context.Context, string, string, int64) (*coordinator.Task, error)
	})
	if !ok {
		return nil, coordinator.ErrInvalid
	}
	return recovery.RecoverExecution(ctx, id, worker, epoch)
}

func (s *coordinatorResults) ClaimWithID(ctx context.Context, worker, id string) (*coordinator.Task, error) {
	claimer, ok := s.WorkerStore.(interface {
		ClaimWithID(context.Context, string, string) (*coordinator.Task, error)
	})
	if !ok {
		return nil, coordinator.ErrInvalid
	}
	return claimer.ClaimWithID(ctx, worker, id)
}
