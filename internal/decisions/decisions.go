// Package decisions derives the read-only list of things only a person can
// decide. It reads evidence the pipeline board and the execution status
// already hold; it writes no label, comment or state, and adds no
// confirmation step. A person resolves an item through the existing route
// named in its Action (edit the issue, remove a label, fix the setup) and the
// item disappears on the next snapshot.
package decisions

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
)

// Kind classifies why a person is needed.
type Kind string

const (
	// ExecutionConfig: an Offload worker is blocked or its setup cannot be read.
	ExecutionConfig Kind = "execution_config"
	// NeedsConfirmation: dispatch coordination parked the issue with agent-needs-confirmation.
	NeedsConfirmation Kind = "needs_confirmation"
	// AcceptanceBlocked: the reviewer verdict is blocked and the implementer cannot fix it.
	AcceptanceBlocked Kind = "acceptance_blocked"
	// RepairStopped: automatic repair spent its budget and the PR still fails.
	RepairStopped Kind = "repair_stopped"
	// OwnerSkipped: the owner put clawflow-skip on an open issue.
	OwnerSkipped Kind = "owner_skipped"
)

// kindOrder puts what blocks all work first and deliberate owner choices last.
var kindOrder = map[Kind]int{ExecutionConfig: 0, NeedsConfirmation: 1, AcceptanceBlocked: 2, RepairStopped: 3, OwnerSkipped: 4}

// ConfigDocsURL explains how to check and repair the execution setup.
const ConfigDocsURL = "https://github.com/zhoushoujianwork/clawflow/blob/main/docs/execution-capability-check.md"

// Decision is one item that needs a person.
type Decision struct {
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	Repo   string `json:"repo,omitempty"`
	Issue  int    `json:"issue,omitempty"`
	PR     int    `json:"pr,omitempty"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
	Action string `json:"action"`
	// URL is the page to act on: the PR when there is one, else the issue,
	// else the setup guide. Empty when the repository's web address is unknown.
	URL      string `json:"url,omitempty"`
	IssueURL string `json:"issue_url,omitempty"`
}

// Gap is a source whose evidence is missing or old, so its absence from the
// list does not prove that nothing needs a decision there. Source is a
// repository name or "execution setup".
type Gap struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// ExecutionSource names the execution setup in Gap.Source.
const ExecutionSource = "execution setup"

// Report is the full answer to "what do I need to decide now".
type Report struct {
	GeneratedAt string     `json:"generated_at,omitempty"`
	Decisions   []Decision `json:"decisions"`
	Incomplete  []Gap      `json:"incomplete,omitempty"`
}

// MaxSnapshotAge is how old a board snapshot may be before the report says its
// absence of items proves little.
const MaxSnapshotAge = 15 * time.Minute

// Build derives the report from a board snapshot and the execution status of
// the workers. A nil status means the setup has not been checked yet, and a
// missing or old snapshot means the repositories have not been read recently;
// both are reported as gaps instead of as a healthy empty list.
func Build(snap pipeline.Snapshot, status *execution.StatusDoc, now time.Time) Report {
	r := Report{GeneratedAt: snap.GeneratedAt, Decisions: []Decision{}}
	switch at := pipeline.ParseTime(snap.GeneratedAt); {
	case snap.GeneratedAt == "":
		r.Incomplete = append(r.Incomplete, Gap{Source: "repositories", Reason: "no board snapshot has been recorded yet"})
	case !at.IsZero() && now.Sub(at) > MaxSnapshotAge:
		r.Incomplete = append(r.Incomplete, Gap{Source: "repositories", Reason: "board snapshot is " + now.Sub(at).Round(time.Minute).String() + " old"})
	}
	if status != nil {
		r.Decisions = append(r.Decisions, executionDecisions(*status)...)
	} else {
		r.Incomplete = append(r.Incomplete, Gap{Source: ExecutionSource, Reason: "not checked yet"})
	}
	for _, repo := range snap.Repos {
		if !repo.Enabled {
			continue // Only enabled repositories dispatch work a person could unblock.
		}
		if repo.Error != "" || repo.Stale {
			reason := repo.Error
			if reason == "" {
				reason = "snapshot is stale"
			}
			r.Incomplete = append(r.Incomplete, Gap{Source: repo.Repo, Reason: clip(reason, 200)})
		}
		r.Decisions = append(r.Decisions, repoDecisions(repo)...)
	}
	sort.SliceStable(r.Decisions, func(i, j int) bool {
		a, b := r.Decisions[i], r.Decisions[j]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.Issue != b.Issue {
			return a.Issue < b.Issue
		}
		return a.ID < b.ID
	})
	return r
}

func repoDecisions(repo pipeline.RepoBoard) []Decision {
	var out []Decision
	link := func(number int) string {
		if repo.URL == "" {
			return ""
		}
		return pipeline.IssueWebURL(repo.URL, number)
	}
	add := func(d Decision) {
		d.Repo = repo.Repo
		if d.ID == "" {
			d.ID = fmt.Sprintf("%s|%s|%d|%d", d.Kind, repo.Repo, d.Issue, d.PR)
		}
		out = append(out, d)
	}

	for _, item := range repo.ConfirmationIssues {
		reason := item.Why
		if reason == "" {
			reason = "Dispatch coordination paused this issue and asked a maintainer to decide."
		}
		add(Decision{Kind: NeedsConfirmation, Issue: item.Issue, Title: item.Title,
			Reason: reason, URL: link(item.Issue), IssueURL: link(item.Issue),
			Action: "Settle the question by editing the issue body (comments alone do not resume work), then remove the agent-needs-confirmation label."})
	}

	for _, item := range repo.SkippedIssues {
		add(Decision{Kind: OwnerSkipped, Issue: item.Issue, Title: item.Title,
			Reason: "The owner added clawflow-skip, so ClawFlow starts no new work on this issue.",
			URL:    link(item.Issue), IssueURL: link(item.Issue),
			Action: "Remove the clawflow-skip label to let ClawFlow work on it, or close the issue if it is no longer wanted."})
	}

	// A provider authentication failure is account-level: report it once per
	// repository, linked to the most recent failing issue.
	var authIssues []pipeline.Item
	for _, item := range repo.Items {
		if item.Stage == pipeline.StageBlocked && item.State != "closed" && item.Run != nil && item.Run.Status == "auth-error" && !item.Run.Live {
			authIssues = append(authIssues, item)
		}
	}
	if len(authIssues) > 0 {
		sort.SliceStable(authIssues, func(i, j int) bool {
			return pipeline.ParseTime(authIssues[i].Run.StartedAt).After(pipeline.ParseTime(authIssues[j].Run.StartedAt))
		})
		latest := authIssues[0]
		sort.SliceStable(authIssues, func(i, j int) bool { return authIssues[i].Issue < authIssues[j].Issue })
		numbers := make([]string, 0, len(authIssues))
		for _, item := range authIssues {
			numbers = append(numbers, "#"+fmt.Sprint(item.Issue))
		}
		add(Decision{ID: fmt.Sprintf("%s|auth|%s", ExecutionConfig, repo.Repo), Kind: ExecutionConfig, Issue: latest.Issue, Title: "Provider authentication failed",
			Reason: clip("The Offload provider rejected the credentials on "+strings.Join(numbers, ", ")+"; the profile's login is expired or invalid.", 400),
			Action: "Refresh the login for the selected Offload profile (offload config), then run " + execution.RecheckCommand + ".",
			URL:    link(latest.Issue), IssueURL: link(latest.Issue)})
	}

	for _, item := range repo.Items {
		pr := item.PR
		if pr == nil || pr.State != "open" || item.State == "closed" {
			continue
		}
		live := item.Run != nil && item.Run.Live
		prLink := pr.URL
		if prLink == "" && repo.URL != "" {
			prLink = pipeline.PRWebURL(repo.URL, repo.Platform, pr.Number)
		}
		a := item.Acceptance
		// A blocked verdict the implementer can repair is handled automatically.
		// Anything else (environment, owner, human, unknown or no evidence) is not.
		if repo.AcceptanceRequired && a != nil && a.Attempts > 0 && !a.LatestIsInfra && a.Verdict == "blocked" && !a.BlockedRepairable && !live {
			reason := "Independent acceptance is blocked on PR #" + fmt.Sprint(pr.Number)
			if a.NextOwner != "" {
				reason += " and handed to " + a.NextOwner
			}
			reason += "."
			if a.Summary != "" {
				reason += " " + a.Summary
			}
			action := a.NextAction
			if action == "" {
				action = "Read the reviewer report on the PR, then push a fix, update the issue requirements, or decide manually."
			}
			add(Decision{Kind: AcceptanceBlocked, Issue: item.Issue, PR: pr.Number, Title: item.Title,
				Reason: reason, Action: action, URL: prLink, IssueURL: link(item.Issue)})
		}
		if item.RepairStop != "" && !live {
			add(Decision{Kind: RepairStopped, Issue: item.Issue, PR: pr.Number, Title: item.Title,
				Reason: "Automatic repair of PR #" + fmt.Sprint(pr.Number) + " stopped and it still fails: " + item.RepairStop + ".",
				Action: "Review the PR and push a fix, or close it. Automation will not reset the repair budget.",
				URL:    prLink, IssueURL: link(item.Issue)})
		}
	}
	return out
}

// executionDecisions lists workers whose Offload setup is blocked or unreadable.
func executionDecisions(doc execution.StatusDoc) []Decision {
	var out []Decision
	for _, w := range doc.Workers {
		if w.State != execution.StateBlocked && w.State != execution.StateUnreported {
			continue
		}
		name := w.Worker
		if name == "" {
			name = "this machine"
		}
		action := "Run " + execution.RecheckCommand + " and follow its setup steps."
		url := ConfigDocsURL
		if g := w.Guidance; g != nil {
			if len(g.Steps) > 0 {
				action = strings.Join(g.Steps, " ") + " Then run " + execution.RecheckCommand + "."
			}
			if g.Link != "" {
				url = g.Link
			}
		}
		out = append(out, Decision{ID: fmt.Sprintf("%s|worker|%s", ExecutionConfig, name), Kind: ExecutionConfig,
			Title:  "Execution setup: " + name,
			Reason: clip(fmt.Sprintf("Worker %s is %s: %s", name, w.State, w.Summary), 400),
			Action: clip(action, 600), URL: url})
	}
	return out
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
