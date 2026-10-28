package pilot

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// ActionSet is shared by stdout and the CLI. IDs must be reused on retry.
type ActionSet struct {
	RunID   string            `json:"run_id"`
	Actions []json.RawMessage `json:"actions"`
}
type Action struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Repo     string `json:"repo,omitempty"`
	Issue    int    `json:"issue,omitempty"`
	Body     string `json:"body,omitempty"`
	Title    string `json:"title,omitempty"`
	Label    string `json:"label,omitempty"`
	Section  string `json:"section,omitempty"`
	Position string `json:"position,omitempty"`
}

// ActionRecord is the persisted board/audit representation. Pending records
// reserve budget before effects; uncertain effects are never automatically retried.
type ActionRecord struct {
	RunID        string    `json:"run_id"`
	Actor        string    `json:"actor"`
	Action       Action    `json:"action"`
	Digest       string    `json:"digest"`
	Outcome      string    `json:"outcome"`
	At           time.Time `json:"at"`
	CreatedIssue int       `json:"created_issue,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

var actionID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing_json")
	}
	return nil
}
func DecodeActions(b []byte) (ActionSet, error) {
	var s ActionSet
	if len(b) > 1024*1024 {
		return s, errors.New("actions_too_large")
	}
	if err := strictJSON(b, &s); err != nil {
		return s, err
	}
	if !actionID.MatchString(s.RunID) || s.Actions == nil {
		return s, errors.New("run_id_and_actions_required")
	}
	return s, nil
}

// ParseActions selects the last fence, including a malformed final fence.
func ParseActions(output string) (ActionSet, error) {
	lines := strings.Split(output, "\n")
	start := -1
	end := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "```pilot-actions" {
			start = i + 1
			end = -1
		} else if start >= 0 && end < 0 && strings.TrimSpace(line) == "```" {
			end = i
		}
	}
	if start < 0 {
		return ActionSet{}, errors.New("missing_pilot_actions")
	}
	if end < 0 {
		return ActionSet{}, errors.New("unclosed_pilot_actions")
	}
	return DecodeActions([]byte(strings.Join(lines[start:end], "\n")))
}

func validateAction(raw json.RawMessage) (Action, error) {
	var a Action
	// Reserved extensions are recognized even before their payload schemas land.
	if err := json.Unmarshal(raw, &a); err == nil && (a.Type == "rerun_ci" || a.Type == "override_merge") {
		return a, errors.New("not_implemented")
	}
	if err := strictJSON(raw, &a); err != nil {
		return a, errors.New("invalid_schema")
	}
	if !actionID.MatchString(a.ID) {
		return a, errors.New("invalid_id")
	}
	fields := "id type repo"
	switch a.Type {
	case "rerun_ci", "override_merge":
		return a, errors.New("not_implemented")
	case "comment", "ask_human", "release":
		fields += " issue body"
		if a.Issue < 1 || strings.TrimSpace(a.Body) == "" {
			return a, errors.New("issue_and_body_required")
		}
	case "edit_spec":
		fields += " issue body section"
		if a.Issue < 1 || strings.TrimSpace(a.Body) == "" || !actionID.MatchString(a.Section) || strings.Contains(a.Body, "<!-- clawflow:spec:") {
			return a, errors.New("invalid_spec_section")
		}
	case "create_issue":
		fields += " title body"
		if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Body) == "" {
			return a, errors.New("title_and_body_required")
		}
	case "label_add", "label_remove":
		fields += " issue label"
		if a.Issue < 1 || !slices.Contains([]string{"bug", "feature", "P0", "P1", "P2", "P3"}, a.Label) {
			return a, errors.New("label_not_allowed")
		}
	case "queue_add", "queue_remove":
		fields += " issue"
		if a.Issue < 1 {
			return a, errors.New("issue_required")
		}
	case "queue_move":
		fields += " issue position"
		if a.Issue < 1 || (a.Position != "top" && a.Position != "bottom") {
			return a, errors.New("invalid_position")
		}
	case "note":
		fields += " body"
		if strings.TrimSpace(a.Body) == "" {
			return a, errors.New("body_required")
		}
	default:
		return a, errors.New("action_not_allowed")
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	for k := range m {
		if !slices.Contains(strings.Fields(fields), k) {
			return a, errors.New("unexpected_field")
		}
	}
	return a, nil
}

// ActionExecutor serializes all local callers using a dedicated audit lock.
// Defaults are five actions/wake, two creations/wake and twenty actions/UTC day.
type ActionExecutor struct {
	Path            string
	Actor           string
	DefaultRepo     string
	Repos           []string
	Client          func(string) (vcs.Client, error)
	MaxWake, MaxDay int
	Now             func() time.Time
}

func NewActionExecutor(cfg *config.Config, creds *config.Credentials, repos []string, actor string) ActionExecutor {
	return ActionExecutor{Path: filepath.Join(snapshot.DataDir(), "pilot-actions.jsonl"), Actor: actor, Repos: repos, Client: func(repo string) (vcs.Client, error) { return clientFor(repo, cfg, creds) }}
}
func ReadActionRecords(path string) ([]ActionRecord, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var records []ActionRecord
	d := json.NewDecoder(f)
	for {
		var r ActionRecord
		err = d.Decode(&r)
		if err == io.EOF {
			return records, nil
		}
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
}
func (e ActionExecutor) Apply(s ActionSet, dry bool) ([]ActionRecord, error) {
	if !actionID.MatchString(s.RunID) || s.Actions == nil || e.Actor == "" {
		return nil, errors.New("invalid_action_set")
	}
	var f *os.File
	var lg *clog.Logger
	if !dry {
		if err := os.MkdirAll(filepath.Dir(e.Path), 0700); err != nil {
			return nil, err
		}
		lock, err := os.OpenFile(e.Path+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			return nil, err
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lg, err = clog.Open("pilot")
		if err != nil {
			return nil, err
		}
		defer lg.Close()
		f, err = os.OpenFile(e.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
	}
	history, err := ReadActionRecords(e.Path)
	if err != nil {
		return nil, err
	}
	persist := func(r ActionRecord) error {
		if dry {
			return nil
		}
		b, _ := json.Marshal(r)
		if _, err := f.Write(append(b, '\n')); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		lg.Info("pilot/action", "run_id", r.RunID, "actor", r.Actor, "repo", r.Action.Repo, "id", r.Action.ID, "type", r.Action.Type, "outcome", r.Outcome)
		return nil
	}
	now := time.Now().UTC()
	if e.Now != nil {
		now = e.Now().UTC()
	}
	wakeMax, dayMax := e.MaxWake, e.MaxDay
	if wakeMax <= 0 {
		wakeMax = 5
	}
	if dayMax <= 0 {
		dayMax = 20
	}
	wake, day, creates := 0, 0, 0
	previous := map[string]ActionRecord{}
	key := func(r ActionRecord) string { return r.RunID + "/" + r.Action.ID }
	for _, r := range history {
		if r.Outcome == "pending" {
			if r.RunID == s.RunID {
				wake++
				if r.Action.Type == "create_issue" {
					creates++
				}
			}
			if r.At.Format("2006-01-02") == now.Format("2006-01-02") {
				day++
			}
		}
		if r.Outcome == "pending" || r.Outcome == "applied" || r.Outcome == "failed" {
			previous[key(r)] = r
		}
	}
	results := []ActionRecord{}
	for _, raw := range s.Actions {
		a, validation := validateAction(raw)
		if a.Repo == "" {
			a.Repo = e.DefaultRepo
		}
		b, _ := json.Marshal(a)
		digest := fmt.Sprintf("%x", sha256.Sum256(b))
		r := ActionRecord{RunID: s.RunID, Actor: e.Actor, Action: a, Digest: digest, At: now}
		switch {
		case validation != nil:
			r.Outcome = validation.Error()
		case !slices.Contains(e.Repos, a.Repo):
			r.Outcome = "repo_not_allowed"
		default:
			if prev, ok := previous[key(r)]; ok {
				if prev.Digest != digest {
					r.Outcome = "id_conflict"
				} else if prev.Outcome == "applied" {
					r.Outcome = "already_applied"
					r.CreatedIssue = prev.CreatedIssue
				} else {
					r.Outcome = "uncertain_previous_attempt"
				}
			} else if wake >= wakeMax {
				r.Outcome = "wake_limit"
			} else if day >= dayMax {
				r.Outcome = "day_limit"
			} else if a.Type == "create_issue" && creates >= 2 {
				r.Outcome = "create_limit"
			} else {
				wake++
				day++
				if a.Type == "create_issue" {
					creates++
				}
				r.Outcome = "pending"
				if err := persist(r); err != nil {
					return results, err
				}
				previous[key(r)] = r
				if dry {
					r.Outcome = "valid"
				} else {
					n, err := e.execute(a, s.RunID)
					r.CreatedIssue = n
					r.Outcome = "applied"
					if err != nil {
						r.Outcome = "failed"
						r.Reason = "vcs_error"
						if slices.Contains([]string{"issue_not_found", "issue_running", "open_leaf_required", "ambiguous_spec_markers", "invalid_spec_markers"}, err.Error()) {
							r.Reason = err.Error()
						}
					}
				}
				previous[key(r)] = r
				if dry {
					simulated := r
					simulated.Outcome = "applied"
					previous[key(r)] = simulated
				}
			}
		}
		if err := persist(r); err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, nil
}

func replaceSpec(body, section, text string) (string, error) {
	start := "<!-- clawflow:spec:" + section + " -->"
	end := "<!-- clawflow:spec:" + section + ":end -->"
	replacement := start + "\n" + text + "\n" + end
	if !strings.Contains(body, start) && !strings.Contains(body, end) {
		return strings.TrimRight(body, "\n") + "\n\n" + replacement + "\n", nil
	}
	if strings.Count(body, start) != 1 || strings.Count(body, end) != 1 {
		return "", errors.New("ambiguous_spec_markers")
	}
	i, j := strings.Index(body, start), strings.Index(body, end)
	if j < i {
		return "", errors.New("invalid_spec_markers")
	}
	return body[:i] + replacement + body[j+len(end):], nil
}
func (e ActionExecutor) execute(a Action, run string) (int, error) {
	if a.Type == "note" || a.Type == "ask_human" {
		return 0, nil
	}
	c, err := e.Client(a.Repo)
	if err != nil {
		return 0, err
	}
	var issue *vcs.Issue
	if a.Issue > 0 {
		issue, err = c.GetIssue(a.Repo, a.Issue)
		if err != nil {
			return 0, err
		}
		if issue == nil {
			return 0, errors.New("issue_not_found")
		}
		if issue.HasLabel("agent-running") {
			return 0, errors.New("issue_running")
		}
	}
	commentID := fmt.Sprintf("pilot-%x", sha256.Sum256([]byte(run+"/"+a.ID)))
	switch a.Type {
	case "comment":
		return 0, c.PostIssueCommentIdempotent(a.Repo, a.Issue, a.Body, commentID)
	case "release":
		if err := c.PostIssueCommentIdempotent(a.Repo, a.Issue, a.Body, commentID); err != nil {
			return 0, err
		}
		for _, label := range []string{"agent-failed", "agent-skipped"} {
			if issue.HasLabel(label) {
				if err := c.RemoveLabel(a.Repo, a.Issue, label); err != nil {
					return 0, err
				}
			}
		}
		return 0, nil
	case "edit_spec":
		body, err := replaceSpec(issue.Body, a.Section, a.Body)
		if err != nil {
			return 0, err
		}
		_, err = c.UpdateIssue(a.Repo, a.Issue, vcs.IssueUpdate{Body: &body})
		return 0, err
	case "create_issue":
		is, err := c.CreateIssue(a.Repo, a.Title, a.Body)
		return is.Number, err
	case "label_add":
		return 0, c.AddLabel(a.Repo, a.Issue, a.Label)
	case "label_remove":
		return 0, c.RemoveLabel(a.Repo, a.Issue, a.Label)
	case "queue_add", "queue_move", "queue_remove":
		if a.Type != "queue_remove" && (issue.State != "open" || issue.SubIssuesTotal > 0) {
			return 0, errors.New("open_leaf_required")
		}
		k := queue.Key{Repo: a.Repo, Number: a.Issue}
		_, err := queue.UpdateRank(func(order []queue.Key) ([]queue.Key, error) {
			if a.Type == "queue_remove" {
				return slices.DeleteFunc(order, func(v queue.Key) bool { return v == k }), nil
			}
			if a.Type == "queue_add" && slices.Contains(order, k) {
				return order, nil
			}
			return queue.Move(order, k, queue.Placement{Top: a.Position == "top", Bottom: a.Position != "top"})
		}, nil)
		return 0, err
	}
	return 0, errors.New("action_not_allowed")
}
