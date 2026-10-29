package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// queueRuntime carries pass-wide queue inputs into scanRepoOnce.
type queueRuntime struct {
	Settings config.QueueSettings
	// DepOpen answers dependencies outside the scanned repo. nil = unknown,
	// which fails closed (the dependency counts as open).
	DepOpen func(queue.Key) bool
	// Now overrides the clock in tests.
	Now func() time.Time
}

func (q queueRuntime) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now().UTC()
}

// repoQueueInput is everything planRepoQueue needs as plain data/callbacks.
type repoQueueInput struct {
	Repo           string
	Issues         []vcs.Issue
	HasChildren    map[int]bool
	ImplementState map[int]string // "" = implement would be dispatched
	// ImplementJobs holds the issues that actually got an implement job this
	// scan (a custom operator may win an issue, or a lock may hold it); a
	// pickable candidate without a job is not a queue candidate.
	ImplementJobs map[int]bool
	Locked        func(number int) bool
	// OpenPRs and PRFiles are read once per pass as needed.
	PRFiles func(int) ([]string, error)
	OpenPRs func() ([]vcs.PR, error)
	// FlightBlocked names why delivery of an in-flight issue is not
	// progressing (CI red without repair, no acceptance verdict, ...). pr is
	// nil while the issue is merely locked.
	FlightBlocked func(issue vcs.Issue, pr *vcs.PR) string
	MaxInFlight   int
	ParkAfter     time.Duration
	Rank          []queue.Key
	DepOpen       func(queue.Key) bool // cross-repo dependencies
	State         queue.RepoState
	Now           time.Time
}

// repoQueuePlan is the decision for one repo in one pass.
type repoQueuePlan struct {
	Picks    map[int]bool
	Result   queue.SelectResult
	State    queue.RepoState // updated; persist after applying
	Warnings []string        // newly seen format warnings, "#N: message"
	Err      string          // non-empty: fail closed, no implement job runs
	Previous map[int]queue.EntryRecord
}

// planRepoQueue turns the scan of one repo into the dispatch decision. It
// fails closed when open PRs cannot be read.
func planRepoQueue(in repoQueueInput) repoQueuePlan {
	plan := repoQueuePlan{Picks: map[int]bool{}, State: in.State, Previous: map[int]queue.EntryRecord{}}
	for _, e := range in.State.Entries {
		plan.Previous[e.Number] = e
	}
	plan.State.Repo = in.Repo
	plan.State.Skipped = 0
	for _, is := range in.Issues {
		if (is.State == "" || is.State == "open") && hasLabel(is.Labels, queue.SkipLabel) {
			plan.State.Skipped++
		}
	}

	open := map[int]bool{}
	for _, is := range in.Issues {
		open[is.Number] = true
	}
	var prs []vcs.PR
	prsLoaded := false
	loadPRs := func() error {
		if prsLoaded {
			return nil
		}
		var err error
		if prs, err = in.OpenPRs(); err != nil {
			return err
		}
		prsLoaded = true
		return nil
	}

	prFiles := map[int][]string{}
	var cands []queue.Candidate
	meta := map[queue.Key]vcs.Issue{}
	for _, is := range in.Issues {
		if is.State != "" && is.State != "open" {
			continue
		}
		// Owner opt-out prevents new work, but an existing run still occupies
		// its slot until the execution lock is released.
		locked := in.Locked != nil && in.Locked(is.Number)
		skipped := hasLabel(is.Labels, queue.SkipLabel)
		if skipped && !locked {
			continue
		}
		if in.HasChildren[is.Number] || is.SubIssuesTotal > 0 {
			continue // parents are coordinated by track-progress, not queued
		}
		reason, scanned := in.ImplementState[is.Number]
		if scanned && reason == "not_eligible_leaf_issue" {
			continue
		}
		key := queue.Key{Repo: in.Repo, Number: is.Number}
		c := queue.Candidate{}
		var flightPR *vcs.PR
		switch {
		case locked:
			c.InFlight = true
		case hasLabel(is.Labels, "agent-implemented"):
			if err := loadPRs(); err != nil {
				plan.Err = "open_prs_unreadable: " + err.Error()
				return plan
			}
			for i := range prs {
				if vcs.PRReferencesIssue(prs[i], is.Number) {
					c.InFlight, flightPR = true, &prs[i]
					break
				}
			}
			if !c.InFlight {
				continue // delivered; nothing left for the queue to do
			}
		case hasLabel(is.Labels, "agent-skipped"):
			continue
		}
		if c.InFlight && !skipped && in.FlightBlocked != nil {
			c.FlightBlocked = in.FlightBlocked(is, flightPR)
		}
		if !c.InFlight {
			switch {
			case !scanned:
				c.Ineligible = "not_in_open_issue_scan"
			case reason != "":
				c.Ineligible = queueReasonSlug(reason)
			case !in.ImplementJobs[is.Number]:
				continue // another operator owns the issue this pass
			}
		}
		if !c.InFlight && c.Ineligible == "" && in.PRFiles != nil {
			savedPlan, err := queueAcceptancePlan(in.Repo, is)
			if err != nil {
				plan.Err = "acceptance_plan_unreadable: " + err.Error()
				return plan
			}
			estimate := append(queue.EstimateFiles(is.Body), queue.EstimateFiles(savedPlan)...)
			if len(estimate) > 0 {
				if err := loadPRs(); err != nil {
					plan.Err = "open_prs_unreadable: " + err.Error()
					return plan
				}
				for _, pr := range prs {
					if pr.State != "open" || pr.IsMerged() || (pr.HeadRepo != "" && !strings.EqualFold(pr.HeadRepo, in.Repo)) {
						continue
					}
					n := pipelinePRIssue(pr)
					if n == 0 || n == is.Number || !vcs.ManagedIssueBranch(pr.HeadBranch, n) {
						continue
					}
					files, ok := prFiles[pr.Number]
					if !ok {
						var err error
						files, err = in.PRFiles(pr.Number)
						if err != nil {
							plan.Err = "pr_files_unreadable: " + err.Error()
							return plan
						}
						prFiles[pr.Number] = files
					}
					if queue.FilesOverlap(estimate, files) && (c.BlockingPR == 0 || pr.Number < c.BlockingPR) {
						c.BlockingPR = pr.Number
					}
				}
				if prev := plan.Previous[is.Number]; c.BlockingPR > 0 && prev.BlockingPR == c.BlockingPR && prev.Reason == "area_busy" {
					c.AreaPasses = prev.AreaPasses
				}
			}
		}
		prio, pw := queue.PriorityFromLabels(is.Labels)
		deps, dw := queue.ParseDependsOn(is.Body, in.Repo)
		c.Item = queue.Item{Key: key, Priority: prio, CreatedAt: is.CreatedAt, Deps: deps}
		cands = append(cands, c)
		meta[key] = is
		for _, w := range append(pw, dw...) {
			if plan.State.Warn(is.Number, w) {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("#%d: %s", is.Number, w))
			}
		}
	}
	plan.State.PruneWarned(open)

	depOpen := func(d queue.Key) bool {
		if d.Repo == in.Repo {
			return open[d.Number]
		}
		if in.DepOpen == nil {
			return true
		}
		return in.DepOpen(d)
	}
	res := queue.Select(queue.SelectInput{
		Candidates:   cands,
		Rank:         queue.Positions(in.Rank),
		MaxInFlight:  in.MaxInFlight,
		ParkAfter:    in.ParkAfter,
		Now:          in.Now,
		BlockedSince: in.State.BlockedSinceKeys(),
		WasParked:    in.State.ParkedKeys(),
		DepOpen:      depOpen,
	})
	plan.Result = res
	plan.State.UpdatedAt = in.Now
	plan.State.BlockedSince = map[int]time.Time{}
	for k, t := range res.BlockedSince {
		plan.State.BlockedSince[k.Number] = t
	}
	plan.State.Parked = nil
	plan.State.Entries = nil
	for _, e := range res.Entries {
		if e.Pick {
			plan.Picks[e.Key.Number] = true
		}
		if e.State == queue.StateParked {
			plan.State.Parked = append(plan.State.Parked, e.Key.Number)
		}
		is := meta[e.Key]
		plan.State.Entries = append(plan.State.Entries, queue.EntryRecord{
			Number: e.Key.Number, Title: is.Title, Priority: e.Priority.String(), Rank: e.Rank,
			State: e.State, Reason: e.Reason, Created: is.CreatedAt, BlockingPR: e.BlockingPR, AreaPasses: e.AreaPasses,
		})
	}
	return plan
}

// queueReasonSlug shortens an implement-ineligibility reason for the queue
// state column: `excluded label "agent-failed" present` -> label_agent-failed.
func queueReasonSlug(reason string) string {
	if rest, ok := strings.CutPrefix(reason, `excluded label "`); ok {
		if name, _, found := strings.Cut(rest, `"`); found {
			return "label_" + name
		}
	}
	if r := []rune(reason); len(r) > 80 {
		reason = string(r[:80]) + "…"
	}
	return reason
}

// applyUnifiedQueue drops every implement job the plan did not pick, logs the
// decisions and persists the repo's queue state. Jobs of other operators are
// returned untouched. A plan error fails closed: no implement job survives.
func applyUnifiedQueue(repo string, jobs []*runJob, plan repoQueuePlan) []*runJob {
	if plan.Err != "" {
		runLog.Warn("queue/skip", "repo", repo, "reason", plan.Err, "scope", "repo")
	} else {
		for _, w := range plan.Warnings {
			runLog.Warn("queue/format_warning", "repo", repo, "detail", w)
		}
		for _, e := range plan.Result.Entries {
			switch {
			case e.Pick:
				runLog.Info("queue/pick", "repo", repo, "issue", e.Key.Number, "priority", e.Priority.String(), "rank", e.Rank)
			case e.State == queue.StateBlocked || e.State == queue.StateWaiting || (e.State == queue.StateEligible && e.Reason != ""):
				if prev, ok := plan.Previous[e.Key.Number]; !ok || prev.State != e.State || prev.Reason != e.Reason {
					runLog.Info("queue/skip", "repo", repo, "issue", e.Key.Number, "reason", e.Label())
				}
			}
		}
		for _, k := range plan.Result.Newly.Parked {
			runLog.Info("queue/park", "repo", repo, "issue", k.Number, "reason", plan.Result.Entries[entryIndex(plan.Result, k)].Reason, "blocking_pr", plan.Result.Entries[entryIndex(plan.Result, k)].BlockingPR)
		}
		for _, k := range plan.Result.Newly.Unparked {
			runLog.Info("queue/unpark", "repo", repo, "issue", k.Number)
		}
	}
	if err := queue.SaveState(plan.State); err != nil {
		runLog.Warn("queue/state_write", "repo", repo, "err", err.Error())
	}
	kept := make([]*runJob, 0, len(jobs))
	for _, j := range jobs {
		if j.op != nil && j.op.Name == "implement" && (plan.Err != "" || j.sub == nil || !plan.Picks[j.sub.Number]) {
			continue
		}
		kept = append(kept, j)
	}
	return kept
}

func entryIndex(r queue.SelectResult, k queue.Key) int {
	for i, e := range r.Entries {
		if e.Key == k {
			return i
		}
	}
	return 0
}

// queueRepoParams extracts concurrency and parking settings with defaults.
func queueRepoParams(repoCfg config.Repo, qs config.QueueSettings) (maxInFlight int, parkAfter time.Duration) {
	maxInFlight = repoCfg.MaxInFlight
	if maxInFlight < 1 {
		maxInFlight = queue.DefaultMaxInFlight
	}
	parkAfter = queue.DefaultParkAfter
	if d, err := time.ParseDuration(qs.ParkAfter); err == nil && d > 0 {
		parkAfter = d
	}
	return
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// flightBlockedReason reports why delivery of a PR-backed issue is stuck.
// CI red counts only when no repair can follow; with acceptance required, a
// missing or stale reviewer verdict counts. Everything else is progress.
func flightBlockedReason(ctx context.Context, client vcs.Client, repo string, repoCfg config.Repo, pr *vcs.PR, now time.Time) string {
	if pr == nil {
		return ""
	}
	if repoCfg.CIRequired {
		if st, err := client.GetCIStatus(repo, pr.Number); err == nil && st == vcs.CIStatusFailure {
			if !ciRepairEnabled(repoCfg) || checkRepairBudget(snapshot.DataDir(), repo, pr.Number, now) != nil {
				return mergeReasonCIFailed
			}
		}
	}
	if repoCfg.AcceptanceRequired {
		if _, _, err := currentAcceptance(ctx, client, repo, pr.Number); err != nil {
			if errors.Is(err, errAcceptanceMissing) {
				return "no_verdict"
			}
			if ctx.Err() != nil {
				return ""
			}
			return mergeReasonAcceptanceStale
		}
	}
	return ""
}

// queueDepResolver answers cross-repo dependencies by reading the issue from
// its repo's VCS (read-only). Unknown repos and read errors fail closed.
func queueDepResolver(cfg *config.Config) func(queue.Key) bool {
	cache := map[queue.Key]bool{}
	return func(k queue.Key) bool {
		if v, ok := cache[k]; ok {
			return v
		}
		open := true
		if rc, ok := cfg.Repos[k.Repo]; ok {
			if client, err := newVCSClient(rc); err == nil {
				if is, err := client.GetIssue(k.Repo, k.Number); err == nil && is != nil {
					open = is.State != "closed"
				}
			}
		}
		cache[k] = open
		return open
	}
}

// pruneRepoRank removes this repo's closed/unknown keys from the rank file.
// It only touches the file when something is stale.
func pruneRepoRank(repo string, issues []vcs.Issue) {
	order, err := queue.LoadRank()
	if err != nil {
		return
	}
	open := map[int]bool{}
	for _, is := range issues {
		open[is.Number] = true
	}
	stale := false
	for _, k := range order {
		if k.Repo == repo && !open[k.Number] {
			stale = true
			break
		}
	}
	if !stale {
		return
	}
	if _, err := queue.UpdateRank(nil, func(k queue.Key) bool { return k.Repo != repo || open[k.Number] }); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] ⚠ queue rank prune: %v\n", repo, err)
	}
}

// queueAcceptancePlan finds historical plans for this issue, including plans
// from closed PRs. Only unchanged requirements are applicable to a new attempt.
func queueAcceptancePlan(repo string, issue vcs.Issue) (string, error) {
	root := filepath.Dir(acceptanceStorePath(repo, 1))
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var latest acceptance.Plan
	for _, entry := range entries {
		pr, err := strconv.Atoi(entry.Name())
		if err != nil || pr <= 0 {
			continue
		}
		store, err := acceptance.OpenStore(filepath.Join(root, entry.Name()))
		if err != nil {
			return "", err
		}
		p, readErr := store.LatestPlanForTask(repo, repo+":issue:"+strconv.Itoa(issue.Number), issue.Title+"\n\n"+issue.Body)
		closeErr := store.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return "", err
		}
		if p.CompletedAt.After(latest.CompletedAt) {
			latest = p
		}
	}
	return latest.Text, nil
}
