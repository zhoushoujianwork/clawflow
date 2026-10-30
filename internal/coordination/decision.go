// Package coordination validates bounded, read-only dispatch decisions.
package coordination

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

const Version = 1

// WorkerTag prevents older workers from silently ignoring the new guard.
const WorkerTag = "protocol:dispatch-coordination-v1"
const WaitingLabel = "agent-waiting"
const ConfirmationLabel = "agent-needs-confirmation"

type Task struct {
	ID       string `json:"id"`
	Issue    int    `json:"issue"`
	PR       int    `json:"pr,omitempty"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Priority int    `json:"priority"`
}
type PullRequest struct {
	vcs.PR
	Files []string `json:"files,omitempty"`
}
type Snapshot struct {
	Operation string `json:"operation"`
	TargetPR  int    `json:"target_pr,omitempty"`

	ManualOrder []int `json:"manual_order"`

	Version    int                `json:"version"`
	Repository string             `json:"repository"`
	Issue      vcs.Issue          `json:"issue"`
	Comments   []vcs.IssueComment `json:"comments"`
	Issues     []vcs.Issue        `json:"related_issues"`
	PRs        []PullRequest      `json:"pull_requests"`
	Tasks      []Task             `json:"active_tasks"`
	Incomplete []string           `json:"incomplete,omitempty"`
}
type Decision struct {
	Outcome    string   `json:"outcome"`
	Reason     string   `json:"reason"`
	NextAction string   `json:"next_action"`
	NextOwner  string   `json:"next_owner"`
	Issues     []int    `json:"issues,omitempty"`
	PRs        []int    `json:"prs,omitempty"`
	Tasks      []string `json:"tasks,omitempty"`
}
type Record struct {
	Snapshot *Snapshot `json:"snapshot,omitempty"`

	Applied     bool   `json:"applied"`
	IssueDigest string `json:"issue_digest"`

	Version    int       `json:"version"`
	SnapshotID string    `json:"snapshot_id"`
	ObservedAt time.Time `json:"observed_at"`
	Decision   Decision  `json:"decision"`
}

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Normalize excludes our informational labels and comments from cache identity.
// Owner edits and comments still change the identity and trigger reconsideration.
func Normalize(s Snapshot) Snapshot {
	s.Version = Version
	clean := func(i vcs.Issue) vcs.Issue {
		i.Labels = slices.DeleteFunc(slices.Clone(i.Labels), func(l string) bool { return l == WaitingLabel || l == ConfirmationLabel })
		slices.Sort(i.Labels)
		if len(i.Labels) == 0 {
			i.Labels = nil
		}
		i.UpdatedAt = "" // informational label/comment writes update this timestamp too
		return i
	}
	s.Issue = clean(s.Issue)
	s.Issues = slices.Clone(s.Issues)
	for i := range s.Issues {
		s.Issues[i] = clean(s.Issues[i])
	}
	s.Comments = slices.DeleteFunc(slices.Clone(s.Comments), func(c vcs.IssueComment) bool {
		return strings.Contains(c.Body, "<!-- clawflow:coordination=")
	})
	if len(s.Comments) == 0 {
		s.Comments = nil
	}
	slices.SortFunc(s.Issues, func(a, b vcs.Issue) int { return a.Number - b.Number })
	slices.SortFunc(s.PRs, func(a, b PullRequest) int { return a.Number - b.Number })
	slices.SortFunc(s.Tasks, func(a, b Task) int { return strings.Compare(a.ID, b.ID) })
	if len(s.Tasks) == 0 {
		s.Tasks = nil
	}
	if len(s.PRs) == 0 {
		s.PRs = nil
	}
	if len(s.Issues) == 0 {
		s.Issues = nil
	}
	if len(s.ManualOrder) == 0 {
		s.ManualOrder = nil
	}
	return s
}

func Waiting(reason, action, owner string) Decision {
	return Decision{Outcome: "waiting", Reason: reason, NextAction: action, NextOwner: owner}
}

// Validate prevents untrusted model output from inventing references or granting
// closure authority. The model can propose closure, but explicit issue-body
// directives plus fresh provider facts are required by the application layer.
func Validate(s Snapshot, d Decision) error {
	if len(d.Reason) == 0 || len(d.Reason) > 2000 || len(d.NextAction) == 0 || len(d.NextAction) > 1000 || len(d.NextOwner) == 0 || len(d.NextOwner) > 100 {
		return fmt.Errorf("missing or oversized decision explanation")
	}
	switch d.Outcome {
	case "proceed", "waiting", "needs-confirmation", "already-delivered", "superseded":
	default:
		return fmt.Errorf("invalid outcome")
	}
	if len(d.Issues)+len(d.PRs)+len(d.Tasks) > 20 {
		return fmt.Errorf("too many evidence references")
	}
	for _, n := range d.Issues {
		if !slices.ContainsFunc(s.Issues, func(i vcs.Issue) bool { return i.Number == n }) {
			return fmt.Errorf("unknown issue #%d", n)
		}
	}
	for _, n := range d.PRs {
		if !slices.ContainsFunc(s.PRs, func(p PullRequest) bool { return p.Number == n }) {
			return fmt.Errorf("unknown PR #%d", n)
		}
	}
	for _, id := range d.Tasks {
		if !slices.ContainsFunc(s.Tasks, func(t Task) bool { return t.ID == id }) {
			return fmt.Errorf("unknown task")
		}
	}
	if d.Outcome == "waiting" && len(d.Issues)+len(d.PRs)+len(d.Tasks) == 0 {
		return fmt.Errorf("waiting needs a concrete condition")
	}
	if len(s.Incomplete) > 0 && d.Outcome != "needs-confirmation" {
		return fmt.Errorf("incomplete snapshot")
	}
	if d.Outcome == "superseded" || d.Outcome == "already-delivered" {
		if s.Issue.SubIssuesTotal > 0 || s.Issue.HasLabel("tracking") {
			return fmt.Errorf("tracking issues cannot be auto-closed")
		}
		for _, t := range s.Tasks {
			if t.Issue == s.Issue.Number {
				return fmt.Errorf("unsettled execution prevents closure")
			}
		}
		for _, p := range s.PRs {
			if p.State == "open" && vcs.PRReferencesIssue(p.PR, s.Issue.Number) {
				return fmt.Errorf("open delivery prevents closure")
			}
		}
		if d.Outcome == "superseded" {
			if len(d.Issues) != 1 || !directive(s.Issue.Body, "Superseded by", d.Issues[0]) || d.Issues[0] == s.Issue.Number {
				return fmt.Errorf("missing explicit replacement directive")
			}
		} else {
			if len(d.PRs) != 1 || !directive(s.Issue.Body, "Completed by", d.PRs[0]) {
				return fmt.Errorf("missing explicit completion directive")
			}
			p := s.PRs[slices.IndexFunc(s.PRs, func(p PullRequest) bool { return p.Number == d.PRs[0] })]
			if !p.IsMerged() || p.MergeCommitSHA == "" || !vcs.PRReferencesIssue(p.PR, s.Issue.Number) {
				return fmt.Errorf("missing merged delivery evidence")
			}
		}
	}
	return nil
}
func directive(body, name string, n int) bool {
	want := fmt.Sprintf("%s: #%d", name, n)
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if !fenced && strings.EqualFold(line, want) {
			return true
		}
	}
	return false
}

func Comment(r Record) string {
	d := r.Decision
	clean := strings.NewReplacer("<!--", "", "-->", "")
	d.Reason = clean.Replace(d.Reason)
	d.NextAction = clean.Replace(d.NextAction)
	d.NextOwner = clean.Replace(d.NextOwner)
	return fmt.Sprintf("Dispatch coordination: **%s**\n\n%s\n\nNext action: %s\nNext owner: %s\nRelated issues: %v · PRs: %v · tasks: %v\n\nSnapshot: `%s`\n\n<!-- clawflow:coordination=%s -->", d.Outcome, d.Reason, d.NextAction, d.NextOwner, d.Issues, d.PRs, d.Tasks, r.SnapshotID, r.SnapshotID)
}
