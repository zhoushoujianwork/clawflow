package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type reopenedLifecycleTransport func(*http.Request) (*http.Response, error)

func (f reopenedLifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// This crosses the real scanner, provider subprocess and write-back boundary.
// Only HTTP transport and the claude executable are fakes; Git uses a local
// bare remote, and every request is handled in memory without opening sockets.
func TestReopenedLifecycleScanToCompletion(t *testing.T) {
	for _, race := range []bool{false, true} {
		name := "complete and consume"
		if race {
			name = "open PR appears after scan"
		}
		t.Run(name, func(t *testing.T) {
			withRunLog(t)
			for _, key := range []string{"GH_TOKEN", "GITLAB_TOKEN", "CLAWFLOW_CLAUDE_API_KEY", "ANTHROPIC_API_KEY"} {
				t.Setenv(key, "integration-test-only")
			}
			t.Setenv("CLAWFLOW_PILOT_BUDGET_PATH", "")
			t.Setenv("CLAWFLOW_CLAUDE_BASE_URL", "https://provider.invalid")
			local := newIssue329Repo(t)
			capture := t.TempDir()
			promptPath := filepath.Join(capture, "prompt.txt")
			cwdPath := filepath.Join(capture, "cwd.txt")
			resultPath := filepath.Join(capture, "result.json")
			result, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "Follow-up implemented. PR: https://example.invalid/o/r/pull/11\n<!-- clawflow:outcome=agent-implemented -->"})
			if err := os.WriteFile(resultPath, append(result, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			script := `#!/bin/sh
set -eu
for argument do prompt="$argument"; done
printf '%s' "$prompt" > "$REOPEN_TEST_PROMPT"
pwd > "$REOPEN_TEST_CWD"
/bin/cat "$REOPEN_TEST_RESULT"
`
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("REOPEN_TEST_PROMPT", promptPath)
			t.Setenv("REOPEN_TEST_CWD", cwdPath)
			t.Setenv("REOPEN_TEST_RESULT", resultPath)

			implemented := true
			openPR := false
			writes := []string{}
			posted := ""
			followup := strings.Repeat("latest detailed follow-up; ", 50) + "END OF FULL COMMENT"
			events := []map[string]any{}
			for index, e := range reopenEvents() {
				events = append(events, map[string]any{"id": index + 1, "event": e.Type, "created_at": e.CreatedAt, "actor": map[string]string{"login": "alice"}, "label": map[string]string{"name": e.Label}})
			}
			issue := func() map[string]any {
				labels := []map[string]string{}
				if implemented {
					labels = append(labels, map[string]string{"name": "agent-implemented"})
				}
				return map[string]any{"id": 9, "number": 9, "title": "Fix reopened follow-up", "body": "Current issue body for reopened work", "state": "open", "labels": labels, "html_url": "https://example.invalid/o/r/issues/9"}
			}
			originalTransport := http.DefaultTransport
			http.DefaultTransport = reopenedLifecycleTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "vcs.invalid" {
					return nil, fmt.Errorf("unexpected external request: %s", r.URL.Host)
				}
				var body any
				status := 200
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/o/r":
					body = map[string]int{"id": 123}
				case "GET /repos/o/r/issues":
					body = []any{issue()}
				case "GET /repos/o/r/issues/9":
					body = issue()
				case "GET /repos/o/r/issues/9/sub_issues":
					body = []any{}
				case "GET /repos/o/r/issues/9/events":
					body = events
				case "GET /repos/o/r/issues/9/comments":
					body = []map[string]any{{"id": 42, "body": followup, "created_at": "2026-01-04T00:00:00Z", "user": map[string]string{"login": "alice"}}}
				case "GET /repos/o/r/pulls":
					if r.URL.Query().Get("state") == "open" {
						if openPR {
							body = []map[string]any{{"number": 11, "state": "open", "body": "Fixes #9", "head": map[string]string{"ref": "fix/issue-9"}}}
						} else {
							body = []any{}
						}
					} else {
						body = []map[string]any{{"number": 10, "state": "closed", "merged_at": "2026-01-02T00:00:00Z", "closed_at": "2026-01-02T00:00:00Z", "body": "Fixes #9\nPrevious PR complete implementation details", "html_url": "https://example.invalid/o/r/pull/10", "head": map[string]string{"ref": "fix/issue-9"}}}
					}
				case "DELETE /repos/o/r/issues/9/labels/agent-implemented":
					if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
						t.Error("provider ran before stale label removal")
					}
					implemented = false
					writes = append(writes, "remove")
					body = []any{}
				case "POST /repos/o/r/issues/9/comments":
					if _, err := os.Stat(promptPath); err != nil {
						t.Error("comment write preceded provider invocation")
					}
					var request struct {
						Body string `json:"body"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					posted = request.Body
					writes = append(writes, "comment")
					status = 201
					body = map[string]int{"id": 99}
				case "POST /repos/o/r/issues/9/labels":
					var request struct {
						Labels []string `json:"labels"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					for _, label := range request.Labels {
						writes = append(writes, "label:"+label)
						if label == "agent-implemented" {
							implemented = true
							events = append(events, map[string]any{"id": 4, "event": "labeled", "created_at": "2026-01-05T00:00:00Z", "actor": map[string]string{"login": "bot"}, "label": map[string]string{"name": label}})
						}
					}
					body = []any{}
				default:
					return nil, fmt.Errorf("unexpected fake VCS call: %s %s", r.Method, r.URL.Path)
				}
				data, err := json.Marshal(body)
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = originalTransport })

			reg := operator.NewRegistry()
			if err := reg.LoadEmbedded(os.DirFS("../../../skills"), "."); err != nil {
				t.Fatal(err)
			}
			cfg := config.Repo{Enabled: true, Platform: "github", BaseURL: "https://vcs.invalid", LocalPath: local, BaseBranch: "main"}
			var issues []snapshot.IssueEntry
			pending, jobs, err := scanRepoOnce(context.Background(), reg, "o/r", cfg, true, 9, &issues)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 || jobs[0].op.Name != "implement" || jobs[0].reopen == nil || len(pending) != 1 {
				t.Fatalf("pending=%v jobs=%v", pending, jobs)
			}
			job := jobs[0]
			expectedBranch := job.reopen.branch(9)
			if !implemented || len(writes) != 0 || !job.sub.HasLabel("agent-implemented") {
				t.Fatal("scan mutated stale terminal label")
			}
			if race {
				openPR = true
			}
			fired, limited := runOneOperator(context.Background(), job, 20*time.Second)
			if limited {
				t.Fatal("unexpected rate limit")
			}
			if race {
				if fired || !implemented || len(writes) != 0 {
					t.Fatalf("race fired=%v implemented=%v writes=%v", fired, implemented, writes)
				}
				if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
					t.Fatal("provider invoked despite post-scan open PR")
				}
				return
			}
			if !fired || !implemented {
				t.Fatalf("fired=%v implemented=%v", fired, implemented)
			}
			if !reflect.DeepEqual(writes, []string{"remove", "comment", "label:agent-implemented"}) {
				t.Fatalf("write order=%v", writes)
			}
			if !strings.Contains(posted, "Follow-up implemented") || strings.Contains(posted, "clawflow:outcome=") {
				t.Fatalf("unexpected result comment: %q", posted)
			}
			prompt, err := os.ReadFile(promptPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"Current issue body for reopened work", followup, "alice", "42", "2026-01-04T00:00:00Z", "Previous PR complete implementation details", "https://example.invalid/o/r/pull/10", "merged", expectedBranch, "do not reimplement resolved work"} {
				if !strings.Contains(string(prompt), part) {
					t.Errorf("provider context missing %q", part)
				}
			}
			cwd, err := os.ReadFile(cwdPath)
			if err != nil {
				t.Fatal(err)
			}
			workdir := strings.TrimSpace(string(cwd))
			if !strings.Contains(workdir, "reopen-"+job.reopen.token()) {
				t.Fatalf("workdir not cycle-scoped: %s", workdir)
			}
			if _, err := os.Stat(workdir); !os.IsNotExist(err) {
				t.Fatalf("successful worktree not cleaned: %v", err)
			}
			if snapshot.IsLocked("o/r", 9) {
				t.Fatal("issue execution lock not released")
			}
			pending, jobs, err = scanRepoOnce(context.Background(), reg, "o/r", cfg, true, 9, &issues)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 0 || len(pending) != 0 {
				t.Fatalf("consumed event rescheduled: pending=%v jobs=%v", pending, jobs)
			}
		})
	}
}
