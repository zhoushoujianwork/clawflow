package commands

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestUploadedDevelopmentProvenance(t *testing.T) {
	for _, mode := range []string{"valid", "tampered", "truncated", "head", "lost", "missing"} {
		t.Run(mode, func(t *testing.T) {
			f, rc := acceptanceFixture(t)
			dir := writeAcceptanceDevelopment(t, "success", time.Now().Add(-2*time.Minute), nil)
			events, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			task := coordinator.Task{ID: "uploaded", Repo: "o/r", Issue: 42, Kind: "implement", Operator: "implement", CreatedAt: time.Now().Add(-time.Second)}
			r := coordinatorReport{jobResult: jobResult{Status: "success", HeadSHA: f.pr.HeadSHA, PRNumber: 11, DevelopmentEvents: string(events)}}
			switch mode {
			case "tampered":
				r.DevelopmentEvents = `{"type":"final"}` + "\n"
			case "truncated":
				r.DevelopmentEvents = r.DevelopmentEvents[:len(r.DevelopmentEvents)-2]
			case "head":
				r.HeadSHA = f.pr.BaseSHA
			case "lost":
				r.Status = "lease-lost"
			case "missing":
				r.DevelopmentEvents = ""
			}
			// Avoid post-automation: this test exercises the exact persistence boundary.
			out := snapshot.RunDir(task.Repo, task.Issue, task.CreatedAt) + "-task-" + task.ID
			j := &runJob{repo: task.Repo, repoCfg: rc, client: f}
			if err := persistDevelopmentEvents(context.Background(), &task, j, &r, out); err != nil {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(out, "events.jsonl"))
			if mode != "valid" {
				if !os.IsNotExist(err) {
					t.Fatalf("invalid upload has provenance: %v", err)
				}
				return
			}
			ended := time.Now()
			if err := snapshot.WriteRunMeta(out, snapshot.RunMeta{Repo: task.Repo, IssueNumber: 42, Operator: "implement", Status: "success", HeadSHA: r.HeadSHA, StartedAt: task.CreatedAt, EndedAt: &ended}); err != nil {
				t.Fatal(err)
			}
			identity, err := acceptanceDevelopment("o/r", 42, r.HeadSHA, 11)
			if err != nil || identity.SessionID != "dev-session" {
				t.Fatalf("uploaded identity: %+v %v", identity, err)
			}
			st, _ := os.Stat(filepath.Join(out, "events.jsonl"))
			if st.Mode().Perm() != 0600 {
				t.Fatal("events not private")
			}
		})
	}
}

func acceptanceUploadTask(t *testing.T, c acceptance.Contract) *resultStoreFake {
	t.Helper()
	expires := time.Now().Add(time.Minute)
	p := coordinatorPayload{TaskID: "upload", Kind: "acceptance", Repo: "o/r", Issue: 42, Acceptance: &c, AcceptanceWorker: "different", DevelopmentWorker: "dev"}
	payload, _ := json.Marshal(p)
	return &resultStoreFake{task: coordinator.Task{ID: "upload", Kind: "acceptance", Repo: "o/r", Issue: 42, PR: 11, Operator: "acceptance", Payload: payload, State: "leased", LeaseOwner: "w", FencingToken: 1, LeaseExpiresAt: &expires}}
}

func (s *resultStoreFake) RetireClosedAcceptance(_ context.Context, id, worker string, fence int64) error {
	if s.task.ID != id || s.task.LeaseOwner != worker || s.task.FencingToken != fence || s.task.State != "leased" {
		return coordinator.ErrStaleLease
	}
	s.task.State = "cancelled"
	s.task.FencingToken++
	s.task.LeaseExpiresAt = nil
	return nil
}

func TestClosedPRAcceptanceUploadRetiresOnlyLateTask(t *testing.T) {
	f, _ := acceptanceFixture(t)
	r := produceAcceptance(t, f)
	backing := acceptanceUploadTask(t, r.Contract)
	f.pr.State = "closed"
	s := &coordinatorResults{WorkerStore: backing, tasks: backing}
	raw, _ := json.Marshal(coordinatorReport{jobResult: jobResult{Status: "success"}, Acceptance: &r})
	err := s.Complete(context.Background(), "upload", "w", 1, raw)
	if !errors.Is(err, coordinator.ErrStaleLease) || backing.task.State != "cancelled" || backing.task.FencingToken != 2 {
		t.Fatalf("late result left retrying: state=%s epoch=%d err=%v", backing.task.State, backing.task.FencingToken, err)
	}
	if len(backing.task.Result) != 0 {
		t.Fatal("late result accepted as current evidence")
	}
	store, err := acceptance.OpenStore(acceptanceStorePath("o/r", 11))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	latest, err := store.LatestRecord(r.Contract)
	if err != nil || latest.AttemptID != r.AttemptID || latest.Verdict != acceptance.Passed {
		t.Fatalf("original passing evidence changed: %+v %v", latest, err)
	}
}

func TestPassedAcceptanceIsNotRequeued(t *testing.T) {
	f, rc := acceptanceFixture(t)
	_ = produceAcceptance(t, f)
	old := requireStore
	t.Cleanup(func() { requireStore = old })
	requireStore = func(context.Context) (*store.Store, error) {
		t.Fatal("passing contract attempted database dispatch again")
		return nil, errors.New("unexpected enqueue")
	}
	oldAllowed := coordinatorPRAllowedFn
	t.Cleanup(func() { coordinatorPRAllowedFn = oldAllowed })
	coordinatorPRAllowedFn = func(context.Context, string, int) error { return nil }
	if err := enqueueWorkerAcceptance(context.Background(), f, "o/r", rc, f.pr, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorAcceptanceUpload(t *testing.T) {
	for _, mode := range []string{"valid", "valid-any", "stale", "self-worker", "self-worker-any", "missing-worker-any", "self-session", "tampered", "head", "command"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := acceptanceFixture(t)
			r := produceAcceptance(t, f)
			// Remove worker-local evidence before uploading to the coordinator store.
			if err := os.RemoveAll(acceptanceStorePath("o/r", 11)); err != nil {
				t.Fatal(err)
			}
			backing := acceptanceUploadTask(t, r.Contract)
			s := &coordinatorResults{WorkerStore: backing, tasks: backing}
			fence := int64(1)
			switch mode {
			case "stale":
				fence = 0
			case "valid-any", "self-worker", "self-worker-any", "missing-worker-any":
				var p coordinatorPayload
				_ = json.Unmarshal(backing.task.Payload, &p)
				if mode != "self-worker" {
					p.AcceptanceWorker = "any"
				}
				if mode == "self-worker" || mode == "self-worker-any" {
					p.DevelopmentWorker = "w"
				}
				if mode == "missing-worker-any" {
					p.DevelopmentWorker = ""
				}
				backing.task.Payload, _ = json.Marshal(p)
			case "self-session":
				r.Reviewer.SessionID = r.Contract.Development.SessionID
			case "tampered":
				r.Plan.Digest = "bad"
			case "head":
				f.pr.HeadSHA = f.pr.BaseSHA
			case "command":
				r.Evidence = []string{"looks fine"}
			}
			raw, _ := json.Marshal(coordinatorReport{jobResult: jobResult{Status: "success"}, Acceptance: &r})
			err := s.Complete(context.Background(), "upload", "w", fence, raw)
			if mode != "valid" && mode != "valid-any" {
				if err == nil {
					t.Fatal("accepted invalid upload")
				}
				if acceptanceStoreExists("o/r", 11) {
					t.Fatal("invalid upload persisted evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := currentAcceptance(context.Background(), f, "o/r", 11); err != nil {
				t.Fatalf("uploaded gate: %v", err)
			}
			if err := s.Complete(context.Background(), "upload", "w", fence, raw); !errors.Is(err, coordinator.ErrStaleLease) {
				t.Fatalf("replay: %v", err)
			}
			if len(f.expected) != 0 {
				t.Fatal("upload merged PR")
			}
		})
	}
}

func TestUploadedFailureStillSchedulesRepair(t *testing.T) {
	f, _, reg, rc := repairAcceptanceFixture(t, "failed")
	r := recordVerdict(t, f)
	if err := os.RemoveAll(acceptanceStorePath("o/r", 11)); err != nil {
		t.Fatal(err)
	}
	backing := acceptanceUploadTask(t, r.Contract)
	s := &coordinatorResults{WorkerStore: backing, tasks: backing}
	raw, _ := json.Marshal(coordinatorReport{jobResult: jobResult{Status: "success"}, Acceptance: &r})
	if err := s.Complete(context.Background(), "upload", "w", 1, raw); err != nil {
		t.Fatal(err)
	}
	jobs := scanRepair(t, reg, f, rc, 0)
	if len(jobs) != 1 || jobs[0].repair == nil || jobs[0].repair.Acceptance == nil {
		t.Fatalf("uploaded failed verdict not repaired: %d jobs", len(jobs))
	}
}

func TestWorkerAcceptanceUsesCoordinatorProvenance(t *testing.T) {
	f, rc := acceptanceFixture(t)
	c, _, _, _, err := acceptanceContract(context.Background(), f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	task, p := coordinatorTask(&runJob{repo: "o/r", repoCfg: rc, op: &operator.Operator{Name: "acceptance"}, sub: &operator.Subject{Number: 42}}, time.Minute)
	task.ID, task.PR, task.FencingToken = "worker-review", 11, 9
	p.TaskID, p.Acceptance = task.ID, &c
	task.Payload, _ = json.Marshal(p)
	var uploaded coordinatorReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			FencingToken int64             `json:"fencing_token"`
			Result       coordinatorReport `json:"result"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/worker/v1/tasks/worker-review/complete" || input.FencingToken != 9 {
			t.Errorf("bad upload %s", r.URL.Path)
		}
		uploaded = input.Result
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, client: server.Client()}
	err = runWorkerTask(context.Background(), api, workerOptions{Tags: task.Tags, Roles: []string{"acceptance"}, RunUntagged: true}, &task, time.Hour, func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) {
		t.Fatal("operator builder invoked")
		return nil, 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.Acceptance == nil {
		t.Fatal("no worker evidence")
	}
	if err := acceptance.ValidateUpload(c, *uploaded.Acceptance); err != nil {
		t.Fatal(err)
	}
	if uploaded.DevelopmentEvents != "" || len(uploaded.Effects) != 0 || len(f.expected) != 0 {
		t.Fatal("worker produced unauthorized effects")
	}
}

func TestAcceptanceTaskTagsOverride(t *testing.T) {
	rc := config.Repo{WorkerTags: []string{"dev"}, WorkerTagsByKind: map[string][]string{"acceptance": {"review"}}}
	task, _ := coordinatorTask(&runJob{repo: "o/r", repoCfg: rc, op: &operator.Operator{Name: "acceptance"}, sub: &operator.Subject{Number: 42, Labels: []string{"worker:linux"}}}, time.Minute)
	if task.Kind != "acceptance" || !reflect.DeepEqual(task.Tags, []string{"linux", "review"}) {
		t.Fatalf("wrong task tags: %+v", task)
	}
}

func TestCoordinatorHarvestEnqueuesAcceptance(t *testing.T) {
	for _, mode := range []string{"missing", "present", "pending-ci"} {
		t.Run(mode, func(t *testing.T) {
			f, _, calls := harvestFixture(t)
			if mode == "present" {
				produceAcceptance(t, f)
			}
			if mode == "pending-ci" {
				f.ciStatus = vcs.CIStatusPending
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Settings.Coordinator.Enabled = true
			enqueued := 0
			old := enqueueWorkerAcceptanceFn
			t.Cleanup(func() { enqueueWorkerAcceptanceFn = old })
			enqueueWorkerAcceptanceFn = func(_ context.Context, _ vcs.Client, repo string, rc config.Repo, pr vcs.PR, _ time.Duration) error {
				if !rc.CoordinatorMode || repo != "o/r" || pr.Number != 11 {
					t.Fatal("wrong coordinator task")
				}
				enqueued++
				return nil
			}
			failures := runHarvestPhase(context.Background(), cfg, "o/r", 0, 30*time.Minute, time.Now().Add(time.Hour))
			want := 0
			if mode == "missing" {
				want = 1
			}
			if len(failures) != 0 || enqueued != want || *calls != 0 {
				t.Fatalf("failures=%v enqueued=%d local=%d", failures, enqueued, *calls)
			}
		})
	}
}

func TestFailedWorkerAcceptanceRecordsInfrastructure(t *testing.T) {
	f, _ := acceptanceFixture(t)
	c, _, _, _, err := acceptanceContract(context.Background(), f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	backing := acceptanceUploadTask(t, c)
	svc := &coordinatorResults{WorkerStore: backing, tasks: backing}
	if err := svc.Fail(context.Background(), "upload", "w", 1, json.RawMessage(`{"status":"failed"}`)); err != nil {
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
	if !acceptance.IsInfrastructure(r) {
		t.Fatal("setup failure did not consume retry budget")
	}
	if acceptance.Evaluate(c, r) == nil {
		t.Fatal("infrastructure failure passed gate")
	}
}

type acceptanceDevelopmentLookupFake struct {
	head    string
	queried []string
}

func (s *acceptanceDevelopmentLookupFake) LatestDevelopment(_ context.Context, _ string, _, _ int, head string) (*coordinator.Task, error) {
	s.queried = append(s.queried, head)
	if head != s.head {
		return nil, gorm.ErrRecordNotFound
	}
	return &coordinator.Task{LeaseOwner: "original-developer"}, nil
}

func TestAcceptanceDevelopmentWorkerBaseMerge(t *testing.T) {
	for _, mode := range []string{"exact", "base-merge", "unproven", "identity-changed"} {
		t.Run(mode, func(t *testing.T) {
			f, rc := acceptanceFixture(t)
			git, proven := provenanceGitFixture(t, f)
			f.pr.HeadSHA, f.pr.BaseSHA = proven, git("rev-parse", "main")
			if mode != "exact" {
				git("checkout", "main")
				git("commit", "--allow-empty", "-m", "base update")
				git("checkout", f.pr.HeadBranch)
				git("merge", "--no-ff", "main", "-m", "merge")
				f.pr.HeadSHA, f.pr.BaseSHA = git("rev-parse", "HEAD"), git("rev-parse", "main")
			}
			c, _, _, _, err := acceptanceContract(context.Background(), f, "o/r", 11)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unproven" {
				git("commit", "--allow-empty", "-m", "outside development")
				f.pr.HeadSHA = git("rev-parse", "HEAD")
				c.HeadSHA = f.pr.HeadSHA
			}
			if mode == "identity-changed" {
				c.Development.SessionID = "another-session"
			}
			lookup := &acceptanceDevelopmentLookupFake{head: proven}
			dev, err := acceptanceDevelopmentWorker(context.Background(), lookup, rc, "o/r", 42, f.pr, c)
			if mode == "unproven" || mode == "identity-changed" {
				if err == nil {
					t.Fatal("accepted unproven development worker")
				}
				if len(lookup.queried) != 1 {
					t.Fatalf("queried untrusted ancestor: %v", lookup.queried)
				}
				return
			}
			if err != nil || dev.LeaseOwner != "original-developer" {
				t.Fatalf("development worker: %+v %v", dev, err)
			}
			if lookup.queried[len(lookup.queried)-1] != proven {
				t.Fatalf("wrong development head: %v", lookup.queried)
			}
			if mode == "base-merge" && c.HeadSHA == proven {
				t.Fatal("contract lost merged head")
			}
		})
	}
}

func TestEnqueueAcceptanceFromPRSummaryAfterBaseMerge(t *testing.T) {
	f, rc := acceptanceFixture(t)
	git, proven := provenanceGitFixture(t, f)
	git("checkout", "main")
	git("commit", "--allow-empty", "-m", "base update")
	git("checkout", f.pr.HeadBranch)
	git("merge", "--no-ff", "main", "-m", "merge")
	f.pr.HeadSHA, f.pr.BaseSHA = git("rev-parse", "HEAD"), git("rev-parse", "main")
	db, dsn := storetest.NewWithPath(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	oldStore, oldAllowed := requireStore, coordinatorPRAllowedFn
	t.Cleanup(func() { requireStore, coordinatorPRAllowedFn = oldStore, oldAllowed })
	requireStore = func(ctx context.Context) (*store.Store, error) { return store.Open(ctx, dsn) }
	coordinatorPRAllowedFn = func(context.Context, string, int) error { return nil }
	s := coordinator.New(db.DB())
	payload, _ := json.Marshal(coordinatorPayload{Testing: testingRequirements{SOPRequired: true, SOPDigest: "approved-revision"}})
	_, err := s.Enqueue(context.Background(), coordinator.Task{Repo: "o/r", Issue: 42, PR: 11, Kind: "implement", Operator: "implement", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(context.Background(), coordinator.Worker{ID: "dev", MaxParallel: 1, RunUntagged: true}); err != nil {
		t.Fatal(err)
	}
	dev, err := s.Claim(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(jobResult{Status: "success", HeadSHA: proven, PRNumber: 11, SOPDigest: "actually-read-revision"})
	if err := s.Complete(context.Background(), dev.ID, "dev", dev.FencingToken, result); err != nil {
		t.Fatal(err)
	}
	// PR listings need not contain branch names. The contract's authoritative
	// GetPR response must supply the graph used to inherit developer ownership.
	summary := vcs.PR{Number: 11, Body: f.pr.Body, HeadSHA: f.pr.HeadSHA}
	if err := enqueueWorkerAcceptance(context.Background(), f, "o/r", rc, summary, time.Minute); err != nil {
		t.Fatal(err)
	}
	active, err := s.Active(context.Background())
	if err != nil || len(active) != 1 || active[0].Kind != "acceptance" {
		t.Fatalf("acceptance was not queued: %+v, %v", active, err)
	}
	var queued coordinatorPayload
	if err := json.Unmarshal(active[0].Payload, &queued); err != nil {
		t.Fatal(err)
	}
	if queued.Acceptance.HeadSHA != f.pr.HeadSHA || queued.DevelopmentWorker != "dev" || !queued.Testing.RevisionPinned || queued.Testing.SOPDigest != "actually-read-revision" {
		t.Fatalf("lost authoritative head, ownership or testing baseline: %+v", queued)
	}
}

func TestAcceptanceInheritsCredentialLabelWithKindOverride(t *testing.T) {
	rc := config.Repo{WorkerTags: []string{"dev"}, WorkerTagsByKind: map[string][]string{"acceptance": {"review"}}}
	task, payload := coordinatorTask(&runJob{repo: "o/r", repoCfg: rc, op: &operator.Operator{Name: "acceptance"}, sub: &operator.Subject{Number: 42, Labels: []string{"worker:creds-laptop"}}}, time.Minute)
	want := []string{"creds-laptop", "review"}
	if !reflect.DeepEqual(task.Tags, want) || !reflect.DeepEqual(payload.Tags, want) {
		t.Fatalf("credential label lost: task=%v payload=%v", task.Tags, payload.Tags)
	}
}
