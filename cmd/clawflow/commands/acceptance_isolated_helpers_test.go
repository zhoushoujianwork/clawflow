package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Fixtures for the isolated-run acceptance tests. The legacy suite that used to
// provide them was removed, so this file carries only what these tests use.
// The embedded nil vcs.Client makes any call the acceptance flow should not
// make panic, so an unexpected provider request is loud.
type acceptanceFake struct {
	vcs.Client
	pr       vcs.PR
	issue    *vcs.Issue
	openPRs  []vcs.PR
	ciStatus vcs.CIStatus
	comments []string
	expected []string
}

func (f *acceptanceFake) ListOpenPRs(string) ([]vcs.PR, error)         { return f.openPRs, nil }
func (f *acceptanceFake) GetPR(string, int) (vcs.PR, error)            { return f.pr, nil }
func (f *acceptanceFake) GetIssue(string, int) (*vcs.Issue, error)     { return f.issue, nil }
func (f *acceptanceFake) GetIssueLabels(string, int) ([]string, error) { return f.issue.Labels, nil }
func (f *acceptanceFake) ListIssueComments(string, int) ([]string, error) {
	return f.comments, nil
}
func (f *acceptanceFake) PostIssueComment(_ string, _ int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}
func (f *acceptanceFake) PostPRComment(_ string, _ int, body string) error {
	f.comments = append(f.comments, body)
	return nil
}
func (f *acceptanceFake) GetCIStatus(string, int) (vcs.CIStatus, error) {
	if f.ciStatus == "" {
		return vcs.CIStatusNone, nil
	}
	return f.ciStatus, nil
}
func (f *acceptanceFake) GetPRMergeability(string, int) (vcs.MergeStatus, error) {
	return vcs.MergeStatusClean, nil
}
func (f *acceptanceFake) MergePRAtHead(_ string, _ int, expected string) (string, error) {
	f.expected = append(f.expected, expected)
	return strings.Repeat("c", 40), nil
}

func acceptanceFixture(t *testing.T) (*acceptanceFake, config.Repo) {
	t.Helper()
	// Preserve legacy record fixtures; structured evidence has dedicated fixtures.
	oldSchema := acceptanceEvidenceSchema
	acceptanceEvidenceSchema = 0
	t.Cleanup(func() { acceptanceEvidenceSchema = oldSchema })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", "/usr/bin/true")
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "acceptance-profile")
	t.Setenv("CLAWFLOW_OFFLOAD_CODEX_SANDBOX", "")
	rc := config.Repo{Platform: "github", BaseBranch: "main", LocalPath: t.TempDir(), AcceptanceRequired: true, CIRequired: true, AutoMerge: true}
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": rc}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	f := &acceptanceFake{ciStatus: vcs.CIStatusSuccess,
		pr:    vcs.PR{Number: 11, Title: "Implementation", Body: "Fixes #42", State: "open", HeadRepo: "o/r", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), BaseBranch: "main", HeadBranch: "fix/issue-42"},
		issue: &vcs.Issue{Number: 42, Title: "Required behavior", Body: "Meet the original goal", State: "open"}}
	f.openPRs = []vcs.PR{f.pr}
	oldGit, oldNative, oldFactory := acceptanceGit, acceptanceNativeRun, acceptanceClientFactory
	t.Cleanup(func() { acceptanceGit, acceptanceNativeRun, acceptanceClientFactory = oldGit, oldNative, oldFactory })
	acceptanceGit = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "config":
			return "https://github.com/o/r.git", nil
		case "rev-parse":
			return f.pr.HeadSHA, nil
		default:
			return "", nil
		}
	}
	acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return f, nil }
	reviewerSession := ""
	acceptanceNativeRun = func(_ context.Context, c execution.Config, r execution.Request, _ []string, _ io.Writer, _ func(execution.Observation)) (execution.Result, error) {
		wantMode, wantPolicy := "default", "default"
		if !r.ToolsDisabled {
			wantMode, wantPolicy = "bypassPermissions", "managed-development"
		}
		if c.Profile != "acceptance-profile" || (r.Resume && (r.SessionKey != reviewerSession || r.ToolsDisabled)) || (!r.Resume && r.SessionKey != "") || r.PermissionMode != wantMode {
			t.Fatalf("unpinned native request: %+v %+v", c, r)
		}
		result := execution.Result{Status: "ok", ExecutionPolicy: wantPolicy, Profile: c.Profile, Backend: "fixture", Model: "fixture-model", NativeSessionID: execution.NewSessionKey(), RunID: execution.NewSessionKey(), Text: "Independent plan"}
		if !r.ToolsDisabled {
			if r.Resume {
				result.NativeSessionID = reviewerSession
			} else {
				reviewerSession = result.NativeSessionID
			}
			result.Text = `{"status":"passed","summary":"Independent checks complete","evidence":["Source fulfills the plan","command: go test ./... => ok"]}`
		}
		return result, nil
	}
	writeAcceptanceDevelopment(t, "success", time.Now().Add(-time.Minute))
	return f, rc
}

// writeAcceptanceDevelopment records a successful development run of issue 42,
// the provenance every acceptance contract is built from.
func writeAcceptanceDevelopment(t *testing.T, status string, started time.Time) string {
	t.Helper()
	dir := snapshot.RunDir("o/r", 42, started)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ended := started.Add(time.Second)
	meta := snapshot.RunMeta{HeadSHA: strings.Repeat("a", 40), Operator: "implement", Repo: "o/r", IssueNumber: 42, IssueTitle: "Required behavior", IssueBody: "Meet the original goal", StartedAt: started, EndedAt: &ended, Status: status}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	events := []execution.Event{
		{Version: 1, Type: "start", Seq: 1, Session: "dev-session", RunID: "dev-run", CallerRunID: "dev-caller", Timestamp: started.Format(time.RFC3339Nano)},
		{Version: 1, Type: "final", Seq: 2, Session: "dev-session", RunID: "dev-run", CallerRunID: "dev-caller", Status: "ok", Timestamp: ended.Format(time.RFC3339Nano)},
	}
	var output bytes.Buffer
	for _, e := range events {
		_ = json.NewEncoder(&output).Encode(e)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
