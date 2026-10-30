package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

func testingSOPFixture(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	if _, err := project.Create("fixture"); err != nil {
		t.Fatal(err)
	}
	if err := project.AddRepo("fixture", "o/r"); err != nil {
		t.Fatal(err)
	}
	if err := project.WriteTesting("fixture", "approved procedure v1"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerTestingReadiness(t *testing.T) {
	for _, mode := range []string{"ready", "missing-sop", "missing-capability", "revision-changed", "unpinned"} {
		for _, kind := range []string{"implement", "acceptance"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				testingSOPFixture(t)
				required := declaredTesting("o/r", config.Repo{RequiredCapabilities: []string{"fixture-runtime"}})
				tags := []string{"host-fixture", "capability:fixture-runtime"}
				switch mode {
				case "missing-sop":
					if err := os.Remove(project.TestingPath("fixture")); err != nil {
						t.Fatal(err)
					}
				case "missing-capability":
					tags = tags[:1]
				case "revision-changed":
					if err := project.WriteTesting("fixture", "procedure v2"); err != nil {
						t.Fatal(err)
					}
				case "unpinned":
					required.SOPDigest = ""
				}
				payload, _ := json.Marshal(coordinatorPayload{Testing: required})
				task := &coordinator.Task{ID: "task", Kind: kind, Repo: "o/r", Payload: payload, Tags: []string{"host-fixture"}}
				ctx, blocked, digest := workerTestingContext(context.Background(), task, tags)
				if mode == "ready" {
					if blocked != nil || digest != required.SOPDigest {
						t.Fatal(blocked, digest)
					}
					// Re-reading a changed file must not replace the execution's pinned bytes.
					if err := project.WriteTesting("fixture", "procedure v2"); err != nil {
						t.Fatal(err)
					}
					dev := operator.BuildSystemPrompt(&operator.Operator{Name: "implement"}, "o/r", "", ctx)
					review := project.TestingHeaderForRepo("o/r", ctx)
					for _, prompt := range []string{dev, review} {
						if !strings.Contains(prompt, "approved procedure v1") || strings.Contains(prompt, "procedure v2") {
							t.Fatal("SOP snapshot not immutable")
						}
						for _, boundary := range []string{"control desktop apps", "replace daemons", "access credentials", "change production projects", "expand publishing scope"} {
							if !strings.Contains(prompt, boundary) {
								t.Fatal("missing authority boundary", boundary)
							}
						}
					}
					return
				}
				if blocked == nil || blocked.Status != "blocked" || blocked.NextAction == "" || blocked.NextOwner == "" {
					t.Fatal(blocked)
				}
				if mode == "revision-changed" && !strings.Contains(blocked.Error, "revision changed") {
					t.Fatal(blocked)
				}
				// Exercise the worker HTTP result path: neither checkout nor execution may start.
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.HasSuffix(r.URL.Path, "/fail") {
						t.Error("unexpected endpoint", r.URL.Path)
					}
					var report struct {
						Result jobResult `json:"result"`
					}
					if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
						t.Error(err)
					}
					if report.Result.Status != "blocked" || report.Result.NextAction == "" || report.Result.NextOwner == "" {
						t.Error(report.Result)
					}
					_, _ = w.Write([]byte(`{}`))
				}))
				defer server.Close()
				api := &workerAPI{base: server.URL, client: server.Client()}
				err := runWorkerTask(context.Background(), api, workerOptions{Tags: tags, Roles: []string{"operator", "acceptance"}}, task, time.Hour, func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) {
					t.Fatal("blocked task started execution")
					return nil, 0, nil
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestReviewInheritsDevelopmentSOPRevision(t *testing.T) {
	testingSOPFixture(t)
	p := coordinatorPayload{Testing: declaredTesting("o/r", config.Repo{})}
	developmentPayload, _ := json.Marshal(p)
	developmentResult, _ := json.Marshal(jobResult{Status: "success", SOPDigest: p.Testing.SOPDigest})
	if err := project.WriteTesting("fixture", "new revision"); err != nil {
		t.Fatal(err)
	}
	review := coordinatorPayload{Testing: declaredTesting("o/r", config.Repo{})}
	if err := inheritDevelopmentTesting(&review, &coordinator.Task{Payload: developmentPayload, Result: developmentResult}); err != nil {
		t.Fatal(err)
	}
	if review.Testing.SOPDigest != p.Testing.SOPDigest || !review.Testing.RevisionPinned {
		t.Fatal(review.Testing)
	}
	payload, _ := json.Marshal(review)
	_, blocked, _ := workerTestingContext(context.Background(), &coordinator.Task{Repo: "o/r", Payload: payload}, nil)
	if blocked == nil || !strings.Contains(blocked.Error, "revision changed") {
		t.Fatal(blocked)
	}
	if slices.Contains([]string{blocked.Status}, "success") {
		t.Fatal("offline downgrade")
	}
}

func TestCoordinatorPersistsTestingBlocked(t *testing.T) {
	s, _, c, svc := resultFixture(t)
	r := testingBlocked("Required SOP is missing", "Install approved SOP and re-enqueue", "project-maintainer")
	raw, _ := json.Marshal(r)
	if err := svc.Fail(context.Background(), "task", "w", 3, raw); err != nil {
		t.Fatal(err)
	}
	var saved coordinatorReport
	if err := json.Unmarshal(s.task.Result, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "blocked" || saved.NextAction == "" || saved.NextOwner == "" || s.task.State != "failed" {
		t.Fatal(saved, s.task.State)
	}
	if len(c.calls) != 1 || c.calls[0] != "comment" {
		t.Fatal(c.calls)
	}
}

func TestWorkerAdvertisesTestingCapabilitiesAndPreservesTags(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/register") {
			var registration struct {
				Tags []string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
				t.Error(err)
			}
			for _, tag := range []string{"host-fixture", "capability:fixture-runtime"} {
				if !slices.Contains(registration.Tags, tag) {
					t.Error("missing advertisement", tag)
				}
			}
		} else {
			cancel()
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, client: server.Client()}
	err := runWorker(ctx, api, workerOptions{MaxParallel: 1, Tags: []string{"host-fixture"}, Capabilities: []string{"fixture-runtime"}}, time.Hour, time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorAcceptancePersistsTestingBlocked(t *testing.T) {
	f, _ := acceptanceFixture(t)
	c, _, _, _, err := acceptanceContract(context.Background(), f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	backing := acceptanceUploadTask(t, c)
	svc := &coordinatorResults{WorkerStore: backing, tasks: backing}
	raw, _ := json.Marshal(testingBlocked("Testing SOP revision changed", "Restore approved revision and re-enqueue", "project-maintainer"))
	if err := svc.Fail(context.Background(), "upload", "w", 1, raw); err != nil {
		t.Fatal(err)
	}
	store, err := acceptance.OpenStore(acceptanceStorePath("o/r", 11))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	r, err := store.LatestRecord(c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != acceptance.Blocked || r.NextAction == "" || r.NextOwner == "" || !strings.Contains(r.Report, "revision changed") {
		t.Fatal(r)
	}
	if acceptance.Evaluate(c, r) == nil {
		t.Fatal("blocked review passed gate")
	}
}
