package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	clog "github.com/zhoushoujianwork/clawflow/internal/log"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// openRunLogHere points runLog at run.log under the current HOME without
// touching HOME or the saved config (unlike withRunLog), so it composes with
// fixtures that already prepared both.
func openRunLogHere(t *testing.T) func() string {
	t.Helper()
	old := runLog
	lg, err := clog.Open("run")
	if err != nil {
		t.Fatalf("clog.Open: %v", err)
	}
	runLog = lg
	t.Cleanup(func() {
		lg.Close()
		runLog = old
	})
	return func() string {
		data, err := os.ReadFile(filepath.Join(clog.LogsDir(), "run.log"))
		if err != nil {
			t.Fatalf("read run.log: %v", err)
		}
		return string(data)
	}
}

// skipLine returns the single run/automerge_skip line, failing when absent.
func skipLine(t *testing.T, log string) string {
	t.Helper()
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "run/automerge_skip") {
			return l
		}
	}
	t.Fatalf("no run/automerge_skip line in:\n%s", log)
	return ""
}

func TestAutoMergeSkipReason(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T) (vcs.Client, config.Repo)
		level  string
		reason string
	}{
		{
			name: "acceptance_missing",
			setup: func(t *testing.T) (vcs.Client, config.Repo) {
				f, rc := acceptanceFixture(t)
				return f, rc
			},
			level:  "INFO",
			reason: "reason=acceptance_missing",
		},
		{
			name: "ci_failed",
			setup: func(t *testing.T) (vcs.Client, config.Repo) {
				writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"o/r": {}}})
				f := &sweepFake{openPRs: []vcs.PR{{Number: 5, Body: "Closes #4"}}, ciStatus: vcs.CIStatusFailure}
				return f, config.Repo{AutoMerge: true, CIRequired: true}
			},
			level:  "INFO",
			reason: "reason=ci_failed",
		},
		{
			name: "ci_pending",
			setup: func(t *testing.T) (vcs.Client, config.Repo) {
				writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"o/r": {}}})
				f := &sweepFake{openPRs: []vcs.PR{{Number: 5, Body: "Closes #4"}}, ciStatus: vcs.CIStatusPending}
				return f, config.Repo{AutoMerge: true, CIRequired: true}
			},
			level:  "INFO",
			reason: "reason=ci_pending",
		},
		{
			name: "provider_merge_error",
			setup: func(t *testing.T) (vcs.Client, config.Repo) {
				writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"o/r": {}}})
				f := &sweepFake{openPRs: []vcs.PR{{Number: 5, Body: "Closes #4"}}, mergeErr: errors.New("HTTP 403: Resource not accessible by integration")}
				return f, config.Repo{AutoMerge: true}
			},
			level:  "WARN",
			reason: "reason=merge_failed",
		},
		{
			name: "conditional_merge_unsupported",
			setup: func(t *testing.T) (vcs.Client, config.Repo) {
				f, rc := acceptanceFixture(t)
				produceAcceptance(t, f)
				// Embedding the interface hides MergePRAtHead.
				return struct{ vcs.Client }{f}, rc
			},
			level:  "WARN",
			reason: "reason=conditional_merge_unsupported",
		},
	}
	for _, tc := range cases {
		for _, path := range []string{"sweep", "inline"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				client, rc := tc.setup(t)
				readLog := openRunLogHere(t)
				if path == "sweep" {
					sweepAutoMergePRs(context.Background(), client, "o/r", rc, 0)
				} else {
					prNum := 5
					if tc.name == "acceptance_missing" || tc.name == "conditional_merge_unsupported" {
						prNum = 11
					}
					runPostAutomation(context.Background(), &runJob{repo: "o/r", repoCfg: rc, client: client, op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 42}},
						"agent-implemented", "https://github.com/o/r/pull/"+strconv.Itoa(prNum), "test")
				}
				log := readLog()
				line := skipLine(t, log)
				if !strings.Contains(line, " "+tc.level+" ") {
					t.Errorf("want level %s, got line: %s", tc.level, line)
				}
				if !strings.Contains(line, tc.reason) {
					t.Errorf("want %s, got line: %s", tc.reason, line)
				}
				if strings.Contains(line, "reason=merge_failed") && tc.reason != "reason=merge_failed" {
					t.Errorf("gate block mislabelled as merge_failed: %s", line)
				}
				if path == "sweep" && !strings.Contains(log, "skip_"+strings.TrimPrefix(tc.reason, "reason=")+"=1") {
					t.Errorf("aggregate line missing skip_ count for %s:\n%s", tc.reason, log)
				}
			})
		}
	}
}
