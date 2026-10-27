package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func qItem(n int, labels ...string) vcs.Issue {
	return vcs.Issue{Number: n, State: "open", Labels: labels}
}

func qInput(items []vcs.Issue, locked map[int]bool, prs []vcs.PR, prErr error, ineligible map[int]string) queueInput {
	return queueInput{
		Items:  items,
		Locked: func(n int) bool { return locked[n] },
		OpenPRs: func() ([]vcs.PR, error) {
			return prs, prErr
		},
		Eligible: func(n int) (bool, string) {
			if r, bad := ineligible[n]; bad {
				return false, r
			}
			return true, ""
		},
	}
}

func TestDecideQueue(t *testing.T) {
	closing := func(n, pr int) vcs.PR {
		return vcs.PR{Number: pr, State: "open", HeadBranch: "other", Body: fmt.Sprintf("Fixes #%d", n)}
	}
	cases := []struct {
		name       string
		in         queueInput
		wantAction string
		wantIssue  int
		wantReason string
	}{
		{
			name:       "first item only, order respected",
			in:         qInput([]vcs.Issue{qItem(30), qItem(10), qItem(20)}, nil, nil, nil, nil),
			wantAction: queueActionPick, wantIssue: 30,
		},
		{
			name:       "closed items are not queue items",
			in:         qInput([]vcs.Issue{{Number: 5, State: "closed"}, qItem(7)}, nil, nil, nil, nil),
			wantAction: queueActionPick, wantIssue: 7,
		},
		{
			name:       "empty queue waits",
			in:         qInput(nil, nil, nil, nil, nil),
			wantAction: queueActionWait, wantReason: "queue_empty",
		},
		{
			name:       "locked head waits",
			in:         qInput([]vcs.Issue{qItem(1), qItem(2)}, map[int]bool{1: true}, nil, nil, nil),
			wantAction: queueActionWait, wantIssue: 1, wantReason: "in_flight_locked",
		},
		{
			name:       "locked later item blocks the head",
			in:         qInput([]vcs.Issue{qItem(1), qItem(2)}, map[int]bool{2: true}, nil, nil, nil),
			wantAction: queueActionWait, wantIssue: 2, wantReason: "in_flight_locked",
		},
		{
			name:       "implemented item with open closing PR waits",
			in:         qInput([]vcs.Issue{qItem(1, "agent-implemented"), qItem(2)}, nil, []vcs.PR{closing(1, 90)}, nil, nil),
			wantAction: queueActionWait, wantIssue: 1, wantReason: "in_flight_open_pr #90",
		},
		{
			name:       "implemented item without open PR is not in flight, head excluded blocks",
			in:         qInput([]vcs.Issue{qItem(1, "agent-implemented"), qItem(2)}, nil, []vcs.PR{closing(5, 91)}, nil, map[int]string{1: `excluded label "agent-implemented" present`}),
			wantAction: queueActionBlocked, wantIssue: 1, wantReason: `excluded label "agent-implemented" present`,
		},
		{
			name:       "excluded head blocks and does not skip ahead",
			in:         qInput([]vcs.Issue{qItem(1, "agent-failed"), qItem(2)}, nil, nil, nil, map[int]string{1: `excluded label "agent-failed" present`}),
			wantAction: queueActionBlocked, wantIssue: 1, wantReason: `excluded label "agent-failed" present`,
		},
		{
			name:       "parent head blocks",
			in:         qInput([]vcs.Issue{qItem(1), qItem(2)}, nil, nil, nil, map[int]string{1: "not_a_leaf"}),
			wantAction: queueActionBlocked, wantIssue: 1, wantReason: "not_a_leaf",
		},
		{
			name:       "unreadable PR list fails closed",
			in:         qInput([]vcs.Issue{qItem(1, "agent-implemented"), qItem(2)}, nil, nil, errors.New("boom"), nil),
			wantAction: queueActionBlocked, wantIssue: 1,
		},
		{
			name:       "PR list is not read without delivered items",
			in:         queueInput{Items: []vcs.Issue{qItem(1)}, OpenPRs: func() ([]vcs.PR, error) { t.Fatal("OpenPRs must not be called"); return nil, nil }, Eligible: func(int) (bool, string) { return true, "" }},
			wantAction: queueActionPick, wantIssue: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := decideQueue(tc.in)
			if d.Action != tc.wantAction || d.Issue != tc.wantIssue {
				t.Fatalf("decision = %+v, want action=%s issue=%d", d, tc.wantAction, tc.wantIssue)
			}
			if tc.wantReason != "" && !strings.HasPrefix(d.Reason, tc.wantReason) {
				t.Fatalf("reason = %q, want prefix %q", d.Reason, tc.wantReason)
			}
		})
	}
}

func TestApplyImplementQueueKeepsOtherOperatorsAndFailsClosed(t *testing.T) {
	logs := withRunLog(t)
	impl := &operator.Operator{Name: "implement"}
	reply := &operator.Operator{Name: "reply-comment"}
	mk := func() []*runJob {
		return []*runJob{
			{op: impl, sub: &operator.Subject{Number: 1}},
			{op: impl, sub: &operator.Subject{Number: 2}},
			{op: impl, sub: &operator.Subject{Number: 3}},
			{op: reply, sub: &operator.Subject{Number: 3}},
		}
	}
	got := applyImplementQueue("o/r", 100, mk(), qInput([]vcs.Issue{qItem(2), qItem(1)}, nil, nil, nil, nil), nil)
	if len(got) != 2 || got[0].sub.Number != 2 || got[0].op != impl || got[1].op != reply {
		t.Fatalf("kept = %+v", got)
	}
	got = applyImplementQueue("o/r", 100, mk(), queueInput{}, errors.New("HTTP 502"))
	if len(got) != 1 || got[0].op != reply {
		t.Fatalf("unreadable queue must drop every implement job, kept %d", len(got))
	}
	out := logs()
	for _, want := range []string{"run/queue_pick", "run/queue_blocked", "sub_issues_unreadable"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

func TestValidateQueueIssue(t *testing.T) {
	if err := validateQueueIssue(config.Repo{Platform: "github"}, 12); err != nil {
		t.Fatal(err)
	}
	if err := validateQueueIssue(config.Repo{Platform: "gitlab"}, 0); err != nil {
		t.Fatalf("clearing must be allowed on GitLab: %v", err)
	}
	if err := validateQueueIssue(config.Repo{Platform: "gitlab"}, 12); err == nil || !strings.Contains(err.Error(), "GitLab") {
		t.Fatalf("gitlab err = %v", err)
	}
	if err := validateQueueIssue(config.Repo{}, -1); err == nil {
		t.Fatal("negative must be rejected")
	}
}

func TestRepoSetQueueIssueCommand(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{
		"o/gh": {Platform: "github"},
		"o/gl": {Platform: "gitlab", BaseURL: "https://gitlab.invalid"},
	}})
	run := func(args ...string) error {
		cmd := newRepoSetCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}
	if err := run("o/gh", "--queue-issue", "42"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if cfg.Repos["o/gh"].QueueIssue != 42 {
		t.Fatalf("queue_issue = %d", cfg.Repos["o/gh"].QueueIssue)
	}
	if err := run("o/gh", "--auto-merge", "on"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = config.Load(); cfg.Repos["o/gh"].QueueIssue != 42 {
		t.Fatal("unrelated set must keep queue_issue")
	}
	if err := run("o/gh", "--queue-issue", "0"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = config.Load(); cfg.Repos["o/gh"].QueueIssue != 0 {
		t.Fatal("--queue-issue 0 must clear")
	}
	if err := run("o/gl", "--queue-issue", "5"); err == nil || !strings.Contains(err.Error(), "GitLab") {
		t.Fatalf("gitlab err = %v", err)
	}
	if cfg, _ = config.Load(); cfg.Repos["o/gl"].QueueIssue != 0 {
		t.Fatal("rejected value must not be saved")
	}
}

// queueVCS is an in-memory GitHub for runOnce queue tests. Queue issue 100 has
// the given sub-issue numbers (in order); every number in `issues` is open.
func queueVCS(t *testing.T, issues []map[string]any, subs map[int][]int, prs []map[string]any, subErr map[int]bool) {
	t.Helper()
	t.Setenv("GH_TOKEN", "queue-test-only")
	t.Setenv("GITLAB_TOKEN", "queue-test-only")
	t.Setenv("CLAWFLOW_PILOT_BUDGET_PATH", "")
	byNum := map[int]map[string]any{}
	for _, is := range issues {
		byNum[is["number"].(int)] = is
	}
	original := http.DefaultTransport
	http.DefaultTransport = reopenedLifecycleTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "vcs.invalid" {
			return nil, fmt.Errorf("unexpected external request: %s", r.URL.Host)
		}
		status := 200
		var body any = []any{}
		var n int
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			body = map[string]int{"id": 1}
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/issues":
			body = issues
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls":
			body = prs
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/events"):
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/sub_issues"):
			if _, err := fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/%d/sub_issues", &n); err != nil {
				return nil, err
			}
			if subErr[n] {
				status = 502
				break
			}
			var list []map[string]any
			for _, c := range subs[n] {
				list = append(list, byNum[c])
			}
			body = list
		default:
			return nil, fmt.Errorf("unexpected fake VCS call: %s %s", r.Method, r.URL.Path)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func queueParent(n, total int) map[string]any {
	is := targetedIssue(n)
	is["sub_issues_summary"] = map[string]int{"total": total, "completed": 0}
	return is
}

func queueConfig(t *testing.T, queue int) {
	t.Helper()
	targetedConfig(t, func(r *config.Repo) { r.Enabled = true; r.QueueIssue = queue })
}

func runQueueOnce(t *testing.T, onlyIssue int) []int {
	t.Helper()
	dispatched := recordDispatch(t)
	if err := runOnce(context.Background(), "o/r", onlyIssue, time.Minute); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	return dispatched()
}

func TestRunOnceQueueDispatchesOnlyHead(t *testing.T) {
	withRunLog(t)
	queueConfig(t, 100)
	// 7 is a queue item listed second by GitHub order but lower number; 9 is
	// the head. 5 is not a queue item.
	queueVCS(t,
		[]map[string]any{queueParent(100, 2), targetedIssue(9), targetedIssue(7), targetedIssue(5)},
		map[int][]int{100: {9, 7}}, nil, nil)
	if got := runQueueOnce(t, 0); len(got) != 1 || got[0] != 9 {
		t.Fatalf("dispatched = %v, want [9]", got)
	}
}

func TestRunOnceNoQueueSettingUnchanged(t *testing.T) {
	withRunLog(t)
	queueConfig(t, 0)
	queueVCS(t,
		[]map[string]any{queueParent(100, 1), targetedIssue(9), targetedIssue(5)},
		map[int][]int{100: {9}}, nil, nil)
	// Without queue_issue every scanned issue is queued as before. The list
	// API carries no sub-issue counts, so the parent (#100) is still queued
	// here and filtered later by the execution-time stale-leaf recheck.
	if got := runQueueOnce(t, 0); len(got) != 3 || got[0] != 5 || got[1] != 9 || got[2] != 100 {
		t.Fatalf("dispatched = %v, want [5 9 100]", got)
	}
}

func TestRunOnceQueueExcludedHeadBlocks(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t, 100)
	queueVCS(t,
		[]map[string]any{queueParent(100, 2), targetedIssue(9, "agent-failed"), targetedIssue(7), targetedIssue(5)},
		map[int][]int{100: {9, 7}}, nil, nil)
	if got := runQueueOnce(t, 0); len(got) != 0 {
		t.Fatalf("dispatched = %v, want none", got)
	}
	if out := logs(); !strings.Contains(out, "run/queue_blocked") || !strings.Contains(out, "agent-failed") {
		t.Fatalf("missing queue_blocked log:\n%s", out)
	}
}

func TestRunOnceQueueOpenPRBlocksRest(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t, 100)
	pr := map[string]any{"number": 90, "title": "t", "body": "Fixes #9", "state": "open", "head": map[string]string{"ref": "x"}}
	queueVCS(t,
		[]map[string]any{queueParent(100, 2), targetedIssue(9, "agent-implemented"), targetedIssue(7)},
		map[int][]int{100: {9, 7}}, []map[string]any{pr}, nil)
	if got := runQueueOnce(t, 0); len(got) != 0 {
		t.Fatalf("dispatched = %v, want none", got)
	}
	if out := logs(); !strings.Contains(out, "run/queue_wait") {
		t.Fatalf("missing queue_wait log:\n%s", out)
	}
}

func TestRunOnceQueueUnreadableSubIssuesDispatchNothing(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t, 100)
	queueVCS(t,
		[]map[string]any{targetedIssue(100), targetedIssue(9), targetedIssue(5)},
		nil, nil, map[int]bool{100: true})
	if got := runQueueOnce(t, 0); len(got) != 0 {
		t.Fatalf("dispatched = %v, want none", got)
	}
	out := logs()
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "sub_issues_unreadable") {
		t.Fatalf("missing WARN log:\n%s", out)
	}
}

func TestRunOnceTargetedIgnoresQueue(t *testing.T) {
	withRunLog(t)
	queueConfig(t, 100)
	queueVCS(t,
		[]map[string]any{queueParent(100, 1), targetedIssue(9), targetedIssue(5)},
		map[int][]int{100: {9}}, nil, nil)
	if got := runQueueOnce(t, 5); len(got) != 1 || got[0] != 5 {
		t.Fatalf("dispatched = %v, want [5]", got)
	}
}

func TestRunOnceQueueParentHeadBlocks(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t, 100)
	queueVCS(t,
		[]map[string]any{queueParent(100, 2), targetedIssue(9), targetedIssue(7), targetedIssue(8)},
		map[int][]int{100: {9, 7}, 9: {8}}, nil, nil)
	if got := runQueueOnce(t, 0); len(got) != 0 {
		t.Fatalf("dispatched = %v, want none", got)
	}
	if out := logs(); !strings.Contains(out, "run/queue_blocked") || !strings.Contains(out, "not_a_leaf") {
		t.Fatalf("missing queue_blocked log:\n%s", out)
	}
}
