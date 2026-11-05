package coordinator

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// Observation thresholds, not validated limits. Findings only explain state;
// they never cancel, recover or requeue a task.
const (
	recoveryStallAfter   = 30 * time.Minute
	finalizingStallAfter = 10 * time.Minute
	dispatchStallAfter   = 10 * time.Minute
	noProgressAfter      = 30 * time.Minute
)

// Finding rules.
const (
	RuleRecoveryStalled   = "recovery_stalled"
	RuleFinalizingStalled = "finalizing_stalled"
	RuleDispatchStalled   = "dispatch_stalled"
	RuleNoProgress        = "no_progress"
)

// Finding explains why one task looks stuck. It carries no payload or report.
type Finding struct {
	TaskID string    `json:"task_id"`
	Repo   string    `json:"repo"`
	Issue  int       `json:"issue"`
	PR     int       `json:"pr"`
	Rule   string    `json:"rule"`
	Since  time.Time `json:"since"`
	AgeSec int64     `json:"age_sec"`
	Reason string    `json:"reason"`
}

// dispatchLimits mirrors the platform and repository limits Claim enforces.
// Unknown limits disable the dispatch rule rather than risk a false finding.
type dispatchLimits struct {
	known bool
	total int
	repos map[string]int
}

// applyFindings derives findings from the snapshot. Workers must be the slice
// deriveBoard received, so its entries align with b.Workers.
func (b *Board) applyFindings(now time.Time, workers []Worker, limits dispatchLimits) {
	b.Findings = []Finding{}
	leased, repoLeased := 0, map[string]int{}
	workerLeased := map[string]int{}
	for i := range b.Tasks {
		if t := &b.Tasks[i]; t.State == "leased" {
			leased++
			repoLeased[t.Repo]++
			workerLeased[t.Worker]++
		}
	}
	for i := range b.Tasks {
		t := &b.Tasks[i]
		rule, since, after, reason := "", time.Time{}, time.Duration(0), ""
		switch {
		case t.Stage == "recovery_pending" && t.LeaseExpiresAt != nil:
			rule, since, after, reason = RuleRecoveryStalled, *t.LeaseExpiresAt, recoveryStallAfter, "recovery pending"
		case t.Stage == "finalizing":
			rule, since, after, reason = RuleFinalizingStalled, t.UpdatedAt, finalizingStallAfter, "result not applied"
		case t.Stage == "running" && t.LastEventAt != nil:
			rule, since, after, reason = RuleNoProgress, *t.LastEventAt, noProgressAfter, "no execution events"
		case t.State == "pending" && b.claimable(t, workers, limits, leased, repoLeased, workerLeased):
			rule, since, after, reason = RuleDispatchStalled, t.UpdatedAt, dispatchStallAfter, "not claimed despite a free matching worker"
		}
		age := now.Sub(since)
		if rule == "" || age < after {
			continue
		}
		text := fmt.Sprintf("Watchdog: %s for %d min.", reason, int(age.Minutes()))
		b.Findings = append(b.Findings, Finding{TaskID: t.ID, Repo: t.Repo, Issue: t.Issue, PR: t.PR, Rule: rule, Since: since, AgeSec: int64(age.Seconds()), Reason: text})
		if t.WaitingReason != "" {
			text += " " + t.WaitingReason
		}
		t.WaitingReason = text
	}
}

// claimable reports whether Claim's capacity and routing checks would admit t.
func (b *Board) claimable(t *BoardTask, workers []Worker, limits dispatchLimits, leased int, repoLeased, workerLeased map[string]int) bool {
	if !limits.known || (limits.total > 0 && leased >= limits.total) {
		return false
	}
	if limit := limits.repos[t.Repo]; limit > 0 && repoLeased[t.Repo] >= limit {
		return false
	}
	for i, w := range workers {
		if b.Workers[i].Live && !w.Paused && workerLeased[w.ID] < w.MaxParallel && eligible(t.Tags, w) && acceptanceAllowed(Task{Kind: t.Kind, Tags: t.Tags, Payload: t.Routing}, w) {
			return true
		}
	}
	return false
}

// key identifies a finding across snapshots. Task findings use the task ID;
// subject findings (no task) use repo, issue and PR.
func (f Finding) key() string {
	if f.TaskID == "" {
		return fmt.Sprintf("%s#%d!%d/%s", f.Repo, f.Issue, f.PR, f.Rule)
	}
	return f.TaskID + "/" + f.Rule
}

// FindingTracker turns repeated snapshots into open and resolve transitions.
// With a state file it survives restarts, so an existing finding is not opened again.
type FindingTracker struct {
	active map[string]Finding
	path   string
}

// NewFileFindingTracker loads the active findings saved at path, if any.
func NewFileFindingTracker(path string) *FindingTracker {
	f := &FindingTracker{path: path}
	if data, err := os.ReadFile(path); err == nil {
		var saved []Finding
		if json.Unmarshal(data, &saved) == nil {
			f.active = make(map[string]Finding, len(saved))
			for _, s := range saved {
				f.active[s.key()] = s
			}
		}
	}
	return f
}

func (f *FindingTracker) save() {
	if f.path == "" {
		return
	}
	saved := make([]Finding, 0, len(f.active))
	for _, v := range f.active {
		saved = append(saved, v)
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return
	}
	tmp := f.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, f.path)
	}
}

// Update returns findings that appeared and those that disappeared since the last call.
func (f *FindingTracker) Update(current []Finding) (opened, resolved []Finding) {
	next := make(map[string]Finding, len(current))
	for _, finding := range current {
		key := finding.key()
		next[key] = finding
		if _, ok := f.active[key]; !ok {
			opened = append(opened, finding)
		}
	}
	for key, finding := range f.active {
		if _, ok := next[key]; !ok {
			resolved = append(resolved, finding)
		}
	}
	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].key() != resolved[j].key() {
			return resolved[i].key() < resolved[j].key()
		}
		return resolved[i].Rule < resolved[j].Rule
	})
	f.active = next
	f.save()
	return opened, resolved
}
