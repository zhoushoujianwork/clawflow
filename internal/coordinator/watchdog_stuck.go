package coordinator

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Rules for work that stopped moving after its tasks ended. Both are
// deterministic, never call a model and only record findings.
const (
	RuleRepeatedFailure = "repeated_failure"
	RuleOrphanedPR      = "orphaned_pr"
	// RuleSameHeadCycle: repair and acceptance tasks keep ending on one unchanged head.
	RuleSameHeadCycle = "same_head_cycle"
)

// Orphaned PR causes reported by the PR state provider.
const (
	OrphanAcceptanceBlocked = "acceptance_blocked"
	// OrphanAcceptanceUnstructured: the report stayed invalid after all automatic fresh reviews.
	OrphanAcceptanceUnstructured = "acceptance_unstructured_exhausted"
	OrphanRepairExhausted        = "repair_exhausted_or_locked"
	// OrphanCICancelled: required CI stays cancelled after automatic reruns were used up.
	OrphanCICancelled     = "ci_cancelled_reruns_exhausted"
	OrphanBranchUnmanaged = "branch_unmanaged"
	OrphanIssueUnlinked   = "issue_unlinked"
	// OrphanRepairNoChange: an acceptance repair found nothing to change for this head.
	OrphanRepairNoChange = "repair_no_change_needs_human"
)

// WatchdogConfig holds the observation thresholds of the stuck-work rules.
// Zero fields use the defaults.
type WatchdogConfig struct {
	FailureThreshold int           `json:"failure_threshold"`
	FailureWindow    time.Duration `json:"failure_window"`
	OrphanAfter      time.Duration `json:"orphan_after"`
}

const (
	defaultFailureThreshold = 3
	defaultFailureWindow    = 6 * time.Hour
	defaultOrphanAfter      = 30 * time.Minute
)

func (c WatchdogConfig) withDefaults() WatchdogConfig {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = defaultFailureThreshold
	}
	if c.FailureWindow <= 0 {
		c.FailureWindow = defaultFailureWindow
	}
	if c.OrphanAfter <= 0 {
		c.OrphanAfter = defaultOrphanAfter
	}
	return c
}

// FinishedTask is the minimal history of one ended task.
type FinishedTask struct {
	Repo      string    `json:"repo"`
	Issue     int       `json:"issue"`
	PR        int       `json:"pr"`
	State     string    `json:"state"`
	Kind      string    `json:"kind"`
	InfraKind string    `json:"infra_kind"`
	Err       string    `json:"err"`
	Head      string    `json:"head"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OrphanedPR describes an open PR in a managed repository that no automatic
// flow will advance. Since is when the cause was first true.
type OrphanedPR struct {
	Repo  string    `json:"repo"`
	PR    int       `json:"pr"`
	Issue int       `json:"issue"`
	Cause string    `json:"cause"`
	Since time.Time `json:"since"`
}

func failureReason(f FinishedTask) string {
	r := f.InfraKind
	if r == "" {
		r = strings.ToLower(strings.Join(strings.Fields(f.Err), " "))
	}
	if len(r) > 80 {
		r = r[:80]
	}
	if r == "" {
		r = "unknown"
	}
	return r
}

func subjectKey(repo string, issue, pr int) string {
	if issue > 0 {
		return fmt.Sprintf("%s#%d", repo, issue)
	}
	return fmt.Sprintf("%s!%d", repo, pr)
}

// repeatedFailureFindings reports subjects whose newest ended tasks are an
// unbroken run of failures with one reason. A done task or a different head
// ends the run. Cost is not consulted, so zero-cost failures count.
func repeatedFailureFindings(now time.Time, history []FinishedTask, cfg WatchdogConfig) []Finding {
	cfg = cfg.withDefaults()
	groups := map[string][]FinishedTask{}
	for _, h := range history {
		if now.Sub(h.UpdatedAt) > cfg.FailureWindow {
			continue
		}
		k := subjectKey(h.Repo, h.Issue, h.PR)
		groups[k] = append(groups[k], h)
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Finding
	for _, k := range keys {
		g := groups[k]
		sort.SliceStable(g, func(i, j int) bool { return g[i].UpdatedAt.After(g[j].UpdatedAt) })
		if g[0].State != "failed" {
			continue
		}
		reason, head := failureReason(g[0]), g[0].Head
		count, since := 0, g[0].UpdatedAt
		for _, h := range g {
			if h.State != "failed" || failureReason(h) != reason || (head != "" && h.Head != "" && h.Head != head) {
				break
			}
			if head == "" {
				head = h.Head
			}
			count++
			since = h.UpdatedAt
		}
		if count < cfg.FailureThreshold {
			continue
		}
		age := now.Sub(since)
		out = append(out, Finding{Repo: g[0].Repo, Issue: g[0].Issue, PR: g[0].PR, Rule: RuleRepeatedFailure, Since: since, AgeSec: int64(age.Seconds()),
			Reason: fmt.Sprintf("Watchdog: %d consecutive failures (threshold %d, window %s) with reason %q and no new head or success.", count, cfg.FailureThreshold, cfg.FailureWindow, reason)})
	}
	return out
}

// orphanedPRFindings reports PRs with no active task whose cause has lasted
// at least the threshold.
func orphanedPRFindings(now time.Time, prs []OrphanedPR, tasks []BoardTask, cfg WatchdogConfig) []Finding {
	cfg = cfg.withDefaults()
	var out []Finding
	for _, p := range prs {
		age := now.Sub(p.Since)
		if age < cfg.OrphanAfter || activeFor(tasks, p) {
			continue
		}
		out = append(out, Finding{Repo: p.Repo, Issue: p.Issue, PR: p.PR, Rule: RuleOrphanedPR, Since: p.Since, AgeSec: int64(age.Seconds()),
			Reason: fmt.Sprintf("Watchdog: open PR has no owner (%s) for %d min (threshold %d min).", p.Cause, int(age.Minutes()), int(cfg.OrphanAfter.Minutes()))})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].PR < out[j].PR
	})
	return out
}

func activeFor(tasks []BoardTask, p OrphanedPR) bool {
	for _, t := range tasks {
		if t.Repo != p.Repo || (t.State != "pending" && t.State != "leased") {
			continue
		}
		if t.PR == p.PR || (p.Issue > 0 && t.Issue == p.Issue) {
			return true
		}
	}
	return false
}

// sameHeadCycleFindings reports PRs (or issues) where repair and acceptance
// tasks keep finishing, in any state, on one unchanged head. A done task does
// not break the run, because no-change repairs end as done. Tasks without a
// recorded head are not counted.
func sameHeadCycleFindings(now time.Time, history []FinishedTask, cfg WatchdogConfig) []Finding {
	cfg = cfg.withDefaults()
	type group struct {
		first          FinishedTask
		count, repairs int
		since          time.Time
	}
	groups := map[string]*group{}
	var keys []string
	for _, h := range history {
		if h.Head == "" || (h.Kind != "repair" && h.Kind != "acceptance") || now.Sub(h.UpdatedAt) > cfg.FailureWindow {
			continue
		}
		k := subjectKey(h.Repo, h.Issue, h.PR) + "@" + h.Head
		g := groups[k]
		if g == nil {
			g = &group{first: h, since: h.UpdatedAt}
			groups[k] = g
			keys = append(keys, k)
		}
		g.count++
		if h.Kind == "repair" {
			g.repairs++
		}
		if h.UpdatedAt.Before(g.since) {
			g.since = h.UpdatedAt
		}
	}
	sort.Strings(keys)
	var out []Finding
	for _, k := range keys {
		g := groups[k]
		if g.count < cfg.FailureThreshold || g.repairs == 0 {
			continue
		}
		f := g.first
		out = append(out, Finding{Repo: f.Repo, Issue: f.Issue, PR: f.PR, Rule: RuleSameHeadCycle, Since: g.since, AgeSec: int64(now.Sub(g.since).Seconds()),
			Reason: fmt.Sprintf("Watchdog: %d repair and acceptance tasks (%d repairs) ended on head %s without a new head (threshold %d, window %s).", g.count, g.repairs, f.Head, cfg.FailureThreshold, cfg.FailureWindow)})
	}
	return out
}
