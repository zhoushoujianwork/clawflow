package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Bounds that keep one board computation to a small, predictable number of
// VCS requests per repository.
const (
	pipelineMaxPRDetails   = sweepMaxCandidates // PRs that get CI/mergeability/acceptance reads
	pipelineRecentRunDays  = 7
	pipelineRecentMerged   = 100
	pipelineMergedWindow   = 14 * 24 * time.Hour
	pipelineSummaryLimit   = 600
	pipelineLogTailBytes   = 4 << 20
	pipelineErrLimit       = 200
	pipelineRecentPRsLimit = 100
)

// pipelineBranchIssue extracts N from our managed fix|feat/issue-N[...] branches.
var pipelineBranchIssue = regexp.MustCompile(`^(?:fix|feat)/issue-(\d+)(?:-|$)`)

// pipelineRecentPRLister is implemented by clients that can list one cheap page
// of recently updated PRs including head and merge-commit SHAs.
type pipelineRecentPRLister interface {
	ListRecentPRs(repo, state string, limit int) ([]vcs.PR, error)
}

// pipelineDeps are the boundaries buildPipeline reads through; tests replace them.
type pipelineDeps struct {
	cfg       *config.Config
	newClient func(config.Repo) (vcs.Client, error)
	now       func() time.Time
	runs      func() []snapshot.RunIndexEntry
	events    func() ([]pipeline.Event, error)
	locked    func(repo string, issue int) bool
	// globalQueue returns the unified queue in dispatch order; nil = none.
	globalQueue func() []queue.ListEntry
	// acceptance returns nil when the repo has no acceptance evidence to show.
	acceptance func(ctx context.Context, client vcs.Client, repo string, rc config.Repo, pr vcs.PR, now time.Time) *pipeline.AcceptanceInfo
}

func defaultPipelineDeps() (pipelineDeps, error) {
	cfg, err := config.Load()
	if err != nil {
		return pipelineDeps{}, err
	}
	return pipelineDeps{
		cfg:       cfg,
		newClient: acceptanceClientFactory,
		now:       time.Now,
		runs:      snapshot.ReadRunsIndex,
		events: func() ([]pipeline.Event, error) {
			return pipeline.ReadEventsTail(filepath.Join(clog.LogsDir(), "run.log"), pipelineLogTailBytes)
		},
		locked:     snapshot.IsLocked,
		acceptance: pipelineAcceptance,
		globalQueue: func() []queue.ListEntry {
			rows, err := queueListRows("")
			if err != nil {
				runLogWarn("queue rank unreadable", err)
			}
			return rows
		},
	}, nil
}

// PipelineSnapshot is the api.PipelineSource used by `clawflow web`.
func PipelineSnapshot(ctx context.Context) (pipeline.Snapshot, error) {
	deps, err := defaultPipelineDeps()
	if err != nil {
		return pipeline.Snapshot{}, err
	}
	return buildPipeline(ctx, deps), nil
}

// buildPipeline gathers the delivery board. Per-repository failures are
// reported on that repository's board instead of failing the whole response.
func buildPipeline(ctx context.Context, d pipelineDeps) pipeline.Snapshot {
	now := d.now()
	snap := pipeline.Snapshot{GeneratedAt: now.UTC().Format(time.RFC3339), Repos: []pipeline.RepoBoard{}, Events: []pipeline.Event{}}

	events, evErr := d.events()
	if evErr != nil {
		runLogWarn("pipeline events unreadable", evErr)
	}

	names := make([]string, 0, len(d.cfg.Repos))
	for name, rc := range d.cfg.Repos {
		// Every enabled repo dispatches work through the unified queue, so it
		// belongs on the board even without auto-merge (e.g. MRs merged by hand).
		if rc.Enabled || rc.AutoMerge || rc.QueueIssue > 0 {
			names = append(names, name)
		}
	}
	slices.Sort(names)

	var queueRows []queue.ListEntry
	if d.globalQueue != nil {
		queueRows = d.globalQueue()
	}
	d.globalQueue = func() []queue.ListEntry { return queueRows }
	runs := d.runs()
	tracked := map[string]bool{}
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		tracked[name] = true
		snap.Repos = append(snap.Repos, buildRepoBoard(ctx, d, name, d.cfg.Repos[name], runs, events, now))
	}
	snap.Events = pipeline.Relevant(events, tracked)
	snap.GlobalQueue = []pipeline.GlobalQueueEntry{}
	if d.globalQueue != nil {
		for _, r := range d.globalQueue() {
			if _, ok := d.cfg.Repos[r.Repo]; !ok {
				continue
			}
			snap.GlobalQueue = append(snap.GlobalQueue, pipeline.GlobalQueueEntry{
				Position: len(snap.GlobalQueue) + 1, Key: r.Key, Repo: r.Repo, Number: r.Number,
				Title: pipelineClip(r.Title, 200), Tier: r.Tier, Rank: r.Rank, State: r.State,
			})
		}
	}
	for i := range snap.GlobalQueue {
		q := &snap.GlobalQueue[i]
		for _, repo := range snap.Repos {
			if repo.Repo != q.Repo {
				continue
			}
			for _, item := range repo.Items {
				if item.Issue == q.Number && (item.Run != nil && item.Run.Live || item.Acceptance != nil && item.Acceptance.Running) {
					q.State = "in_flight"
				}
			}
		}
	}
	return snap
}

func runLogWarn(msg string, err error) {
	webLog.Warn("web/pipeline", "msg", msg, "err", pipelineClip(err.Error(), pipelineErrLimit))
}

func pipelineClip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func buildRepoBoard(ctx context.Context, d pipelineDeps, repo string, rc config.Repo, runs []snapshot.RunIndexEntry, events []pipeline.Event, now time.Time) pipeline.RepoBoard {
	platform := rc.Platform
	if platform == "" {
		platform = "github"
	}
	board := pipeline.RepoBoard{
		Repo: repo, Enabled: rc.Enabled, Platform: platform, AutoMerge: rc.AutoMerge,
		CIRequired: rc.CIRequired, AcceptanceRequired: rc.AcceptanceRequired, QueueIssue: rc.QueueIssue,
		Items: []pipeline.Item{}, QueueScope: rc.QueueScope, MaxInFlight: rc.MaxInFlight,
	}
	if board.QueueScope == "" {
		board.QueueScope = "all"
	}
	if board.MaxInFlight <= 0 {
		board.MaxInFlight = 1
	}
	client, err := d.newClient(rc)
	if err != nil {
		board.Error = pipelineClip("VCS client unavailable: "+err.Error(), pipelineErrLimit)
		return board
	}
	if rcx, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
		rcx.SetRequestContext(ctx)
	}

	// Queue: ordered sub-issues plus the runner's latest decision from run.log.
	var decision *pipeline.QueueDecision
	var queueIssues []vcs.Issue
	if rc.QueueIssue > 0 {
		q := &pipeline.Queue{Issue: rc.QueueIssue, Entries: []pipeline.QueueEntry{}}
		board.Queue = q
		decision = pipeline.LastDecision(events, repo)
		q.Decision = decision
		if queueIssues, err = readQueueItems(client, repo, rc.QueueIssue); err != nil {
			q.Error = pipelineClip(err.Error(), pipelineErrLimit)
			queueIssues = nil
		}
		pos := 0
		for _, it := range queueIssues {
			e := pipeline.QueueEntry{Position: 0, Number: it.Number, Title: it.Title, State: it.State}
			if it.State == "open" {
				pos++
				e.Position = pos
			}
			if decision != nil && decision.Issue == it.Number {
				e.Action, e.Reason = decision.Action, decision.Reason
			}
			q.Entries = append(q.Entries, e)
		}
	}

	openIssues, err := client.ListOpenIssues(repo)
	if err != nil {
		board.Error = pipelineClip("list open issues: "+err.Error(), pipelineErrLimit)
		openIssues = nil
	}
	issueByNum := map[int]vcs.Issue{}
	for _, it := range openIssues {
		issueByNum[it.Number] = it
	}

	openPRs, mergedPRs := pipelinePRs(client, repo, now, &board)
	prByIssue := map[int]vcs.PR{}
	var prOrder []int
	for _, pr := range openPRs {
		if n := pipelinePRIssue(pr); n > 0 {
			if _, dup := prByIssue[n]; !dup {
				prByIssue[n] = pr
				prOrder = append(prOrder, n)
			}
		}
	}
	mergedByIssue := map[int]vcs.PR{}
	for _, pr := range mergedPRs {
		if n := pipelinePRIssue(pr); n > 0 {
			if _, dup := mergedByIssue[n]; !dup {
				mergedByIssue[n] = pr
			}
		}
	}

	// Latest relevant run per issue within the window.
	latestRun := map[int]snapshot.RunIndexEntry{}
	cutoff := now.Add(-pipelineRecentRunDays * 24 * time.Hour)
	for _, r := range runs {
		if r.Repo != repo || r.StartedAt.Before(cutoff) {
			continue
		}
		if prev, ok := latestRun[r.IssueNumber]; !ok || r.StartedAt.After(prev.StartedAt) {
			latestRun[r.IssueNumber] = r
		}
	}

	// Which issues become items.
	type cand struct {
		number, queuePos int
		title            string
		labels           []string
		state, updated   string
	}
	var cands []cand
	seen := map[int]bool{}
	add := func(c cand) {
		if !seen[c.number] {
			seen[c.number] = true
			cands = append(cands, c)
		}
	}
	if d.globalQueue != nil {
		for pos, q := range d.globalQueue() {
			if q.Repo != repo {
				continue
			}
			it, ok := issueByNum[q.Number]
			if !ok {
				it = vcs.Issue{Number: q.Number, Title: q.Title, State: "open"}
			}
			add(cand{number: it.Number, queuePos: pos + 1, title: it.Title, labels: it.Labels, state: it.State, updated: it.UpdatedAt})
		}
	}
	for _, e := range queueIssues {
		// Closed queue entries only matter when they delivered something.
		if e.State != "open" {
			if _, ok := mergedByIssue[e.Number]; !ok {
				continue
			}
		}
		add(cand{number: e.Number, queuePos: queuePosition(board.Queue, e.Number), title: e.Title, labels: e.Labels, state: e.State, updated: e.UpdatedAt})
	}
	for _, n := range prOrder {
		it, ok := issueByNum[n]
		if !ok {
			continue // PR closes an issue that is not open: not in flight
		}
		add(cand{number: n, title: it.Title, labels: it.Labels, state: it.State, updated: it.UpdatedAt})
	}
	var runIssues []int
	for n := range latestRun {
		runIssues = append(runIssues, n)
	}
	slices.Sort(runIssues)
	for _, n := range runIssues {
		it, ok := issueByNum[n]
		if !ok || (latestRun[n].Operator != "implement" && latestRun[n].Operator != "repair-ci") {
			continue
		}
		add(cand{number: n, title: it.Title, labels: it.Labels, state: it.State, updated: it.UpdatedAt})
	}
	merged := 0
	for _, pr := range mergedPRs {
		n := pipelinePRIssue(pr)
		if n <= 0 || seen[n] || merged >= pipelineRecentMerged {
			continue
		}
		merged++
		title := pr.Title
		if it, ok := issueByNum[n]; ok {
			title = it.Title
		}
		add(cand{number: n, title: title, state: "closed", updated: pr.MergedAt})
	}

	detailed := 0
	for _, c := range cands {
		item := pipeline.Item{State: c.state, Issue: c.number, Title: c.title, InQueue: c.queuePos > 0, QueuePosition: c.queuePos}
		ev := pipeline.Evidence{IssueState: c.state, Labels: c.labels, AutoMerge: rc.AutoMerge, CIRequired: rc.CIRequired,
			AcceptanceRequired: rc.AcceptanceRequired, InQueue: c.queuePos > 0, Now: now}
		if decision != nil && decision.Issue == c.number {
			ev.Decision = decision
		}
		if r, ok := latestRun[c.number]; ok {
			ri := &pipeline.RunInfo{Operator: r.Operator, Status: r.Status, Stage: r.RunMeta.Stage, StartedAt: r.StartedAt.UTC().Format(time.RFC3339),
				Live: d.locked(repo, c.number)}
			if r.EndedAt != nil {
				ri.EndedAt = r.EndedAt.UTC().Format(time.RFC3339)
			}
			item.Run, ev.Run = ri, ri
		} else if d.locked(repo, c.number) {
			ri := &pipeline.RunInfo{Operator: "implement", Status: "running", Live: true}
			item.Run, ev.Run = ri, ri
		}

		if pr, ok := prByIssue[c.number]; ok {
			pi := &pipeline.PRInfo{Number: pr.Number, URL: pr.URL, Title: pr.Title, State: pr.State, Draft: pr.Draft || isDraftPR(pr.Title),
				HeadBranch: pr.HeadBranch, HeadSHA: pr.HeadSHA, UpdatedAt: pr.UpdatedAt}
			if detailed < pipelineMaxPRDetails {
				detailed++
				fillPRDetail(client, repo, pi)
				if a := d.acceptance(ctx, client, repo, rc, pr, now); a != nil {
					item.Acceptance, ev.Acceptance = a, a
				}
			} else {
				pi.CI, pi.Mergeable = "unknown", "unknown"
				pi.CIError = "not checked: more open PRs than the per-repository detail limit"
			}
			item.PR, ev.PR = pi, pi
		} else if pr, ok := mergedByIssue[c.number]; ok && c.state != "open" {
			pi := &pipeline.PRInfo{Number: pr.Number, URL: pr.URL, Title: pr.Title, State: "merged", HeadBranch: pr.HeadBranch, HeadSHA: pr.HeadSHA, UpdatedAt: pr.UpdatedAt}
			item.PR, ev.PR = pi, pi
			item.Delivery = &pipeline.Delivery{PR: pr.Number, PRURL: pr.URL, MergeCommitSHA: pr.MergeCommitSHA, MergedAt: pr.MergedAt}
		}

		item.Stage, item.Why, item.Evidence = pipeline.Derive(ev)
		if item.Stage != pipeline.StageMerged {
			if item.Stage == pipeline.StageBlocked || item.Stage == pipeline.StageFailed || item.Stage == pipeline.StageRejected {
				item.Attention = append(item.Attention, item.Why)
			}
			if slices.Contains(c.labels, "agent-failed") && item.Stage != pipeline.StageFailed {
				item.Attention = append(item.Attention, "agent-failed")
			}
			if item.PR != nil && item.PR.State == "open" {
				if !rc.AutoMerge {
					item.Attention = append(item.Attention, "waiting for review (manual merge)")
				}
				if err := checkRepairBudget(snapshot.DataDir(), repo, item.PR.Number, now); errors.Is(err, errRepairExhausted) || errors.Is(err, errRepairBlocked) {
					item.Attention = append(item.Attention, err.Error())
				}
			}
			if d.globalQueue != nil && (item.Run == nil || !item.Run.Live) {
				for _, q := range d.globalQueue() {
					if q.Repo == repo && q.Number == c.number && strings.HasPrefix(q.State, "blocked:") && item.Stage == pipeline.StageQueued {
						item.Stage, item.Why = pipeline.StageBlocked, q.State
						item.Attention = append(item.Attention, q.State)
					}
				}
			}
		}
		if item.Stage == pipeline.StageMerged && item.Delivery == nil && item.PR != nil {
			item.Delivery = &pipeline.Delivery{PR: item.PR.Number, PRURL: item.PR.URL}
		}
		var stamps []string
		stamps = append(stamps, c.updated)
		if item.PR != nil {
			stamps = append(stamps, item.PR.UpdatedAt)
		}
		if item.Run != nil {
			stamps = append(stamps, item.Run.StartedAt, item.Run.EndedAt)
		}
		if item.Acceptance != nil {
			stamps = append(stamps, item.Acceptance.CompletedAt)
		}
		if item.Delivery != nil {
			stamps = append(stamps, item.Delivery.MergedAt)
		}
		item.UpdatedAt = pipeline.Latest(stamps...)
		board.Items = append(board.Items, item)
	}

	// Queue items first in queue order, then the rest by recency.
	sort.SliceStable(board.Items, func(i, j int) bool {
		a, b := board.Items[i], board.Items[j]
		if a.InQueue != b.InQueue {
			return a.InQueue
		}
		if a.InQueue {
			return a.QueuePosition < b.QueuePosition
		}
		return pipeline.ParseTime(a.UpdatedAt).After(pipeline.ParseTime(b.UpdatedAt))
	})
	return board
}

func queuePosition(q *pipeline.Queue, number int) int {
	if q == nil {
		return 0
	}
	for _, e := range q.Entries {
		if e.Number == number {
			return e.Position
		}
	}
	return 0
}

// pipelinePRs returns open PRs and recently merged PRs, using one cheap page
// each when the client supports it.
func pipelinePRs(client vcs.Client, repo string, now time.Time, board *pipeline.RepoBoard) (open, merged []vcs.PR) {
	var err error
	if l, ok := client.(pipelineRecentPRLister); ok {
		open, err = l.ListRecentPRs(repo, "open", pipelineRecentPRsLimit)
		if err == nil {
			var closed []vcs.PR
			if closed, err = l.ListRecentPRs(repo, "closed", pipelineRecentPRsLimit); err == nil {
				for _, pr := range closed {
					if t := pipeline.ParseTime(pr.MergedAt); pr.IsMerged() && !t.IsZero() && now.Sub(t) <= pipelineMergedWindow {
						merged = append(merged, pr)
					}
				}
			}
		}
	} else {
		open, err = client.ListOpenPRs(repo)
	}
	if err != nil && board.Error == "" {
		board.Error = pipelineClip("list pull requests: "+err.Error(), pipelineErrLimit)
	}
	return open, merged
}

// pipelinePRIssue maps a PR to the issue it delivers: our managed branch name
// first, then a closing keyword in the body. 0 means not a ClawFlow PR.
func pipelinePRIssue(pr vcs.PR) int {
	if m := pipelineBranchIssue.FindStringSubmatch(pr.HeadBranch); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return extractClosesIssue(pr.Body)
}

// fillPRDetail reads CI (exact head SHA aggregation) and mergeability.
func fillPRDetail(client vcs.Client, repo string, pi *pipeline.PRInfo) {
	status, err := client.GetCIStatus(repo, pi.Number)
	if err != nil {
		pi.CI, pi.CIError = "unknown", pipelineClip(err.Error(), pipelineErrLimit)
	} else {
		pi.CI = string(status)
	}
	if m, err := client.GetPRMergeability(repo, pi.Number); err != nil {
		pi.Mergeable = "unknown"
	} else {
		pi.Mergeable = string(m)
	}
}

// pipelineAcceptance reads the latest acceptance evidence for the PR's current
// contract. It is read-only: it never creates a store, runs a reviewer or
// takes the acceptance lock. nil means there is nothing to show.
func pipelineAcceptance(ctx context.Context, client vcs.Client, repo string, rc config.Repo, pr vcs.PR, now time.Time) *pipeline.AcceptanceInfo {
	path := acceptanceStorePath(repo, pr.Number)
	st, statErr := os.Lstat(path)
	haveStore := statErr == nil && st.IsDir() && st.Mode().Perm()&0077 == 0
	if !rc.AcceptanceRequired && !haveStore {
		return nil
	}
	info := &pipeline.AcceptanceInfo{Required: rc.AcceptanceRequired, MaxInfraRetries: harvestMaxInfra}
	info.Running = snapshot.IsLocked("acceptance-"+acceptanceHash(repo), pr.Number)

	c, _, _, _, err := acceptanceContract(ctx, client, repo, pr.Number)
	if err != nil {
		info.Error = pipelineClip(err.Error(), pipelineErrLimit)
		return info
	}
	if !haveStore {
		return info // contract is buildable; no attempt has been recorded yet
	}
	store, err := acceptance.OpenStore(path)
	if err != nil {
		info.Error = "acceptance evidence unavailable"
		return info
	}
	defer store.Close()
	records, err := store.CompleteRecords(c)
	if err != nil {
		info.Error = "acceptance evidence unreadable"
		return info
	}
	info.Attempts = len(records)
	var lastInfra time.Time
	for _, r := range records {
		if acceptance.IsInfrastructure(r) {
			info.InfraAttempts++
			if r.CompletedAt.After(lastInfra) {
				lastInfra = r.CompletedAt
			}
		}
	}
	if len(records) == 0 {
		return info
	}
	latest := records[len(records)-1]
	info.LatestIsInfra = acceptance.IsInfrastructure(latest)
	info.CompletedAt = latest.CompletedAt.UTC().Format(time.RFC3339)
	info.PlannerSession = pipelineSessionID(latest.Plan.Planner.SessionID)
	info.ReviewerSession = pipelineSessionID(latest.Reviewer.SessionID)
	info.Summary = pipelineSummary(latest.Report)
	if info.LatestIsInfra {
		info.NextRetryAt = lastInfra.Add(harvestInfraCooldown).UTC().Format(time.RFC3339)
		info.Verdict = ""
		return info
	}
	info.Verdict = string(latest.Verdict)
	info.Valid = acceptance.Evaluate(c, latest) == nil
	return info
}

var pipelineSessionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func pipelineSessionID(s string) string {
	if pipelineSessionRE.MatchString(s) {
		return s
	}
	return ""
}

// pipelineSummary bounds the reviewer summary and strips HTML comment
// delimiters, mirroring harvestNonpassComment.
func pipelineSummary(report string) string {
	s := strings.NewReplacer("<!--", "", "-->", "").Replace(strings.TrimSpace(report))
	return pipelineClip(s, pipelineSummaryLimit)
}
