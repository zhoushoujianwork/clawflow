package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestReopenUnavailableGitLabScan(t *testing.T) {
	for _, implemented := range []bool{false, true} {
		t.Run(fmt.Sprintf("implemented=%v", implemented), func(t *testing.T) {
			readLog := withRunLog(t)
			t.Setenv("GITLAB_TOKEN", "test-only")
			labels := []string{}
			if implemented {
				labels = append(labels, "agent-implemented")
			}
			issue := map[string]any{"id": 9, "iid": 9, "state": "opened", "title": "test", "labels": labels}
			historyCalls := 0
			original := http.DefaultTransport
			http.DefaultTransport = reopenedLifecycleTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "vcs.invalid" || r.Method != "GET" {
					return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				var body any = []any{}
				status := 200
				switch {
				case strings.Contains(r.URL.Path, "/resource_"):
					historyCalls++
					status = 404
					body = map[string]string{"message": "404 Not Found"}
				case strings.HasSuffix(r.URL.Path, "/projects/o/r"):
					body = map[string]int{"id": 1}
				case strings.HasSuffix(r.URL.Path, "/issues"):
					body = []any{issue}
				case strings.HasSuffix(r.URL.Path, "/issues/9"):
					body = issue
				case strings.HasSuffix(r.URL.Path, "/notes"), strings.HasSuffix(r.URL.Path, "/merge_requests"):
				default:
					return nil, fmt.Errorf("unexpected path: %s", r.URL.Path)
				}
				data, _ := json.Marshal(body)
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = original })
			reg := operator.NewRegistry()
			if err := reg.LoadEmbedded(os.DirFS("../../../skills"), "."); err != nil {
				t.Fatal(err)
			}
			cfg := config.Repo{Enabled: true, Platform: "gitlab", BaseURL: "https://vcs.invalid", LocalPath: t.TempDir(), BaseBranch: "main"}
			var issues []snapshot.IssueEntry
			oldDebug := Debug
			Debug = true
			t.Cleanup(func() { Debug = oldDebug })
			dispatched := recordDispatch(t)
			output := captureStderr(t, func() {
				pending, jobs, err := scanRepoOnce(context.Background(), reg, "o/r", cfg, true, 9, &issues)
				if err != nil {
					t.Fatal(err)
				}
				want := 1
				if implemented {
					want = 0
				}
				if len(jobs) != want || len(pending) != want {
					t.Fatalf("pending=%v jobs=%v", pending, jobs)
				}
				if !implemented {
					if err := prepareImplementation(jobs[0]); err != nil {
						t.Fatal(err)
					}
				}
				runJobsParallel(context.Background(), jobs, 1, time.Second)
			})
			if !strings.Contains(output, "implement #9:") {
				t.Fatalf("missing decision: %s", output)
			}
			if implemented {
				if len(dispatched()) != 0 || historyCalls == 0 {
					t.Fatalf("dispatch=%v history=%d", dispatched(), historyCalls)
				}
				log := readLog()
				if !strings.Contains(log, "INFO") || !strings.Contains(log, "reason=history_unavailable") || strings.Contains(log, "WARN") {
					t.Fatalf("log: %s", log)
				}
			} else if len(dispatched()) != 1 || historyCalls != 0 {
				t.Fatalf("dispatch=%v history=%d", dispatched(), historyCalls)
			}
		})
	}
}

func TestDiscoverReopenUnavailable(t *testing.T) {
	readLog := withRunLog(t)
	sub := &operator.Subject{Number: 9, State: "open", Labels: []string{"agent-implemented"}}
	for _, client := range []vcs.Client{
		&reopenFake{eventErr: fmt.Errorf("HTTP 404")},
		&commentsFake{}, // No lifecycle capability.
		&reopenFake{events: reopenEvents(), errorComments: fmt.Errorf("history unavailable")},
	} {
		r, err := discoverReopen(client, "o/r", sub)
		if r != nil || err != nil {
			t.Fatalf("context=%v err=%v", r, err)
		}
	}
	if strings.Count(readLog(), "reason=history_unavailable") != 3 {
		t.Fatal(readLog())
	}
}
