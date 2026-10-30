package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/clone"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordination"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestWorkerTokenCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db, dsn := storetest.NewWithDSN(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	previous := requireStore
	requireStore = func(ctx context.Context) (*store.Store, error) { return store.Open(ctx, dsn) }
	t.Cleanup(func() { requireStore = previous })
	run := func(args ...string) string {
		t.Helper()
		cmd := NewWorkerCmd()
		var out, stderr bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if args[1] == "create" {
			if !strings.Contains(stderr.String(), "SHA-256 fingerprint: ") {
				t.Fatal("missing fingerprint")
			}
		}
		return strings.TrimSpace(out.String())
	}
	secret := run("token", "create", "--name", "cli")
	if len(secret) != 64 {
		t.Fatal("missing secret")
	}
	listing := run("token", "list", "--name", "cli")
	var tokens []coordinator.Token
	if err := json.Unmarshal([]byte(listing), &tokens); err != nil || len(tokens) != 1 || tokens[0].Name != "cli" {
		t.Fatalf("listing %s %v", listing, err)
	}
	if strings.Contains(listing, secret) {
		t.Fatal("secret leaked")
	}

	adminSecret := run("token", "create", "--name", "admin-cli", "--scope", "admin")
	adminToken, err := coordinator.New(db.DB()).VerifyToken(context.Background(), adminSecret)
	if err != nil || adminToken == nil || !adminToken.HasScope(coordinator.ScopeAdmin) {
		t.Fatalf("admin token: %+v %v", adminToken, err)
	}
	invalid := NewWorkerCmd()
	invalid.SetArgs([]string{"token", "create", "--name", "invalid", "--scope", "root"})
	invalid.SetOut(io.Discard)
	invalid.SetErr(io.Discard)
	if err := invalid.Execute(); err == nil {
		t.Fatal("invalid token scope accepted")
	}
	run("token", "revoke", "--name", "cli")
	if _, err := coordinator.New(db.DB()).VerifyToken(context.Background(), secret); err == nil {
		t.Fatal("revoked token valid")
	}
}
func TestWorkerTokenRequiredName(t *testing.T) {
	for _, action := range []string{"create", "revoke"} {
		cmd := NewWorkerCmd()
		cmd.SetArgs([]string{"token", action})
		if err := cmd.Execute(); err == nil {
			t.Fatalf("%s missing name accepted", action)
		}
	}
}

func TestCoordinatorDisabled(t *testing.T) {
	previous := requireStore
	requireStore = func(context.Context) (*store.Store, error) {
		t.Fatal("disabled coordinator opened database")
		return nil, errors.New("unexpected database")
	}
	t.Cleanup(func() { requireStore = previous })
	stop, err := startCoordinator(context.Background(), config.CoordinatorSettings{}, io.Discard, func(error) { t.Error("unexpected server") })
	if err != nil {
		t.Fatal(err)
	}
	stop()
}

// Exercise the wire protocol with the same pinned TLS client used by the CLI.
func TestWorkerClaimExecuteReport(t *testing.T) {
	for _, mode := range []string{"success", "lease-lost", "heartbeat-error", "tags", "untagged", "digest", "coordination-stale"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			task := coordinator.Task{ID: "task", Repo: "o/r", Issue: 1, Operator: "implement", Kind: "implement", FencingToken: 42, Tags: []string{"github"}}
			if mode == "untagged" {
				task.Tags = nil
			}
			p := coordinatorPayload{TaskID: task.ID, Repo: task.Repo, Issue: task.Issue, Operator: task.Operator, Kind: task.Kind, TimeoutSec: 60, SkillDigest: "wrong"}
			task.Payload, _ = json.Marshal(p)
			reports := make(chan jobResult, 1)
			renewed := make(chan struct{}, 1)
			var claimed atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing bearer")
					w.WriteHeader(401)
					return
				}
				switch r.URL.Path {
				case "/worker/v1/register":
					var registration struct {
						Tags        []string `json:"tags"`
						MaxParallel int      `json:"max_parallel"`
						Roles       []string `json:"roles"`
					}
					_ = json.NewDecoder(r.Body).Decode(&registration)
					if registration.MaxParallel != 1 || !slices.Contains(registration.Roles, "operator") {
						t.Error("bad registration")
					}
					_, _ = io.WriteString(w, `{"worker_id":"test"}`)
				case "/worker/v1/claim":
					if claimed.Swap(true) {
						if mode == "lease-lost" || mode == "heartbeat-error" {
							cancel()
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"task": nil})
					} else {
						_ = json.NewEncoder(w).Encode(map[string]any{"task": task})
					}
				default:
					var input struct {
						FencingToken int64     `json:"fencing_token"`
						Result       jobResult `json:"result"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					if input.FencingToken != 42 {
						t.Error("missing fencing token")
					}
					if strings.HasSuffix(r.URL.Path, "heartbeat") {
						// Rejection tests must not race an unintended lease
						// loss while preparing their completion report.
						if mode != "lease-lost" && mode != "heartbeat-error" {
							select {
							case renewed <- struct{}{}:
							default:
							}
							_, _ = io.WriteString(w, `{}`)
							return
						}
						status := 409
						if mode == "heartbeat-error" {
							status = 500
						}
						w.WriteHeader(status)
						return
					}
					reports <- input.Result
					_, _ = io.WriteString(w, `{}`)
					cancel()
				}
			}))
			defer server.Close()
			tokenFile := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(server.Certificate().Raw)
			o := workerOptions{Coordinator: server.URL, TokenFile: tokenFile, Fingerprint: hex.EncodeToString(sum[:]), Tags: []string{"github"}, Roles: []string{"operator"}, MaxParallel: 1, RunUntagged: true}
			if mode == "tags" {
				o.Tags = []string{"other"}
			}
			if mode == "untagged" {
				o.RunUntagged = false
			}
			api, err := newWorkerAPI(o)
			if err != nil {
				t.Fatal(err)
			}
			defer api.client.CloseIdleConnections()
			old := runOneOperatorFn
			defer func() { runOneOperatorFn = old }()
			var executed atomic.Bool
			runOneOperatorFn = func(ctx context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
				executed.Store(true)
				if mode == "success" {
					select {
					case <-renewed:
					case <-ctx.Done():
						t.Error("renewal did not arrive")
					}
				}
				if j.hooks == nil || !j.hooks.SkipIndex {
					t.Error("worker hooks missing")
				}
				// The worker run holds the host issue lock, so worktree pruning skips it.
				if err := j.acquireLock(ctx); err != nil {
					t.Errorf("acquire worker issue lock: %v", err)
				} else {
					if !snapshot.IsLocked(j.repo, j.sub.Number) {
						t.Error("worker run does not hold the host issue lock")
					}
					j.releaseLock()
					if snapshot.IsLocked(j.repo, j.sub.Number) {
						t.Error("worker issue lock not released")
					}
				}
				if mode == "lease-lost" || mode == "heartbeat-error" {
					<-ctx.Done()
				}
				j.hooks.AfterRun(&jobResult{Status: "success", Effects: []jobEffect{{Kind: "add-label", Repo: "o/r", Number: 1, Labels: []string{"agent-implemented"}}}})
				return true, false, false
			}
			build := func(ctx context.Context, task *coordinator.Task) (*runJob, time.Duration, error) {
				if mode == "coordination-stale" {
					return nil, 0, errCoordinationChanged
				}
				if mode == "digest" {
					return buildWorkerJob(ctx, task, operator.NewRegistry())
				}
				return &runJob{repo: "o/r", sub: &operator.Subject{Number: 1}, op: &operator.Operator{Name: "implement"}}, time.Minute, nil
			}
			deadline, stop := context.WithTimeout(ctx, 3*time.Second)
			defer stop()
			if err := runWorker(deadline, api, o, 5*time.Millisecond, time.Millisecond, build); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			select {
			case result := <-reports:
				switch mode {
				case "success":
					if result.Status != "success" || len(result.Effects) != 1 {
						t.Fatalf("bad result: %+v", result)
					}
				case "coordination-stale":
					if executed.Load() || result.Status != "blocked" || result.NextOwner != "scheduling" || result.InfraKind != "launch" || len(result.Effects) > 0 {
						t.Fatalf("stale context reached execution or terminal outcome: %+v", result)
					}
				case "lease-lost", "heartbeat-error":
					t.Fatalf("lost lease uploaded a result: %+v", result)
				default:
					if executed.Load() || result.Status != "failed" || result.Error == "" {
						t.Fatalf("task not refused: %+v", result)
					}
				}
			default:
				if mode != "lease-lost" && mode != "heartbeat-error" {
					t.Fatal("no completion report")
				}
			}
		})
	}
}

func TestWorkerPinAndURL(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted server received credentials") }))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(token, []byte("secret"), 0600)
	o := workerOptions{Coordinator: server.URL, TokenFile: token, Fingerprint: strings.Repeat("0", 64)}
	api, err := newWorkerAPI(o)
	if err != nil {
		t.Fatal(err)
	}
	defer api.client.CloseIdleConnections()
	if err := api.post(context.Background(), "register", struct{}{}, nil); err == nil {
		t.Fatal("wrong pin accepted")
	}
	for _, address := range []string{"http://localhost", server.URL + "/unexpected", server.URL + "?secret=1"} {
		o.Coordinator = address
		if _, err := newWorkerAPI(o); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
}

func TestWorkerStateIsolation(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", state)
	for _, path := range []string{snapshot.DataDir(), snapshot.LockDir(), snapshot.RunDir("o/r", 1, time.Now()), config.WorktreePath("o/r", 1)} {
		if !strings.HasPrefix(path, state+string(os.PathSeparator)) {
			t.Fatalf("state escaped: %s", path)
		}
	}
	for _, path := range []string{config.ConfigPath(), config.CredentialsPath()} {
		if !strings.HasPrefix(path, home+string(os.PathSeparator)) {
			t.Fatalf("config moved: %s", path)
		}
	}
	local := filepath.Join(state, "worktrees", "old__repo", "analysis-main")
	coordinatorPath := filepath.Join(home, ".clawflow", "worktrees", "old__repo", "analysis-main")
	for _, p := range []string{local, coordinatorPath} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if n := pruneOrphanedAnalysisWorktrees(&config.Config{}); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if _, err := os.Stat(coordinatorPath); err != nil {
		t.Fatal("coordinator worktree touched", err)
	}
}

func TestBuildWorkerJobLocalConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/issues/1" {
			t.Errorf("unexpected read %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if !strings.Contains(r.Header.Get("Authorization"), "worker-local-token") {
			t.Error("not using worker credentials")
		}
		_, _ = io.WriteString(w, `{"number":1,"title":"fresh title","body":"fresh body","state":"open","labels":[{"name":"clawflow"}]}`)
	}))
	defer server.Close()
	local := t.TempDir()
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {Platform: "github", BaseURL: server.URL, LocalPath: local, AutoMerge: true}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveCredentials(&config.Credentials{GHToken: "worker-local-token"}); err != nil {
		t.Fatal(err)
	}
	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	op, ok := reg.Get("implement")
	if !ok {
		t.Fatal("missing implement")
	}
	p := coordinatorPayload{TaskID: "task", Repo: "o/r", Issue: 1, Operator: op.Name, Kind: "implement", Platform: "github", BaseURL: server.URL, TimeoutSec: 60, SkillDigest: skillDigest(op)}
	p.RepoPolicy.BaseBranch = "develop"
	raw, _ := json.Marshal(p)
	task := &coordinator.Task{ID: "task", Repo: "o/r", Issue: 1, Operator: op.Name, Kind: "implement", Payload: raw}
	j, timeout, err := buildWorkerJob(context.Background(), task, reg)
	if err != nil {
		t.Fatal(err)
	}
	if j.sub.Title != "fresh title" || j.repoCfg.LocalPath != local || j.repoCfg.AutoMerge || j.repoCfg.BaseBranch != "develop" || timeout != time.Minute {
		t.Fatalf("wrong job: %+v", j)
	}
	p.SkillDigest = "wrong"
	task.Payload, _ = json.Marshal(p)
	if _, _, err := buildWorkerJob(context.Background(), task, reg); err == nil || !strings.Contains(err.Error(), "skill_digest") {
		t.Fatal("digest mismatch accepted", err)
	}
}

func TestWorkerCloneCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "missing")
	_, err := clone.EnsureLocalCloneContext(ctx, &config.Config{}, "o/r", config.Repo{LocalPath: dest}, io.Discard, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("cancelled clone created directory")
	}
}

func TestWorkerCommandRegistration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	envState, flagState := t.TempDir(), t.TempDir()
	t.Setenv("CLAWFLOW_STATE_DIR", envState)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var registered atomic.Bool
	hostname, _ := os.Hostname()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "register") {
			var input struct {
				Tags        []string `json:"tags"`
				MaxParallel int      `json:"max_parallel"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if !slices.Contains(input.Tags, coordination.WorkerTag) || !slices.Contains(input.Tags, "github") || !slices.Contains(input.Tags, "host-"+strings.TrimSuffix(strings.ToLower(hostname), ".local")) || input.MaxParallel != 2 {
				t.Errorf("bad capabilities: %+v", input)
			}
			if snapshot.StateDir() != flagState {
				t.Error("state-dir flag did not override env")
			}
			registered.Store(true)
			_, _ = io.WriteString(w, `{}`)
		} else {
			_, _ = io.WriteString(w, `{"task":null}`)
			cancel()
		}
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(token, []byte("test"), 0600)
	sum := sha256.Sum256(server.Certificate().Raw)
	cmd := NewWorkerCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--coordinator", server.URL, "--token-file", token, "--fingerprint", hex.EncodeToString(sum[:]), "--tags", "github", "--max-parallel", "2", "--state-dir", flagState})
	if err := cmd.Execute(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !registered.Load() {
		t.Fatal("did not register")
	}
	if snapshot.StateDir() != envState {
		t.Fatal("state env not restored")
	}
}

func TestCredentialWorkerCommandMatchesGeneratedTasks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var registered atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "register") {
			var input coordinator.Worker
			// The registration protocol uses snake_case field names.
			var registration struct {
				Tags        []string `json:"tags"`
				RunUntagged bool     `json:"run_untagged"`
			}
			if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
				t.Error(err)
			}
			if registration.RunUntagged || !slices.Contains(registration.Tags, "creds-laptop") {
				t.Errorf("unexpected credential-only registration: %+v", registration)
			}
			input.Tags, input.RunUntagged, input.Roles = registration.Tags, registration.RunUntagged, []string{"acceptance"}
			for _, kind := range []string{"implement", "acceptance"} {
				task, payload := coordinatorTask(&runJob{repo: "o/r", op: &operator.Operator{Name: kind}, sub: &operator.Subject{Number: 7, Labels: []string{"worker:creds-laptop"}}}, time.Minute)
				// AcceptanceEligible shares the coordinator's all-tags eligibility check.
				if !coordinator.AcceptanceEligible(task.Tags, input) || !slices.Equal(task.Tags, payload.Tags) {
					t.Errorf("%s task tags %v cannot be leased by CLI tags %v", kind, task.Tags, input.Tags)
				}
				if coordinator.AcceptanceEligible(task.Tags, coordinator.Worker{Tags: []string{"github"}, Roles: []string{"acceptance"}, RunUntagged: true}) {
					t.Errorf("%s credential task eligible for worker without credential tag", kind)
				}
			}
			untagged, _ := coordinatorTask(&runJob{repo: "o/r", op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 8}}, time.Minute)
			if coordinator.AcceptanceEligible(untagged.Tags, input) {
				t.Error("credential-only worker eligible for task without configured tags")
			}
			registered.Store(true)
			_, _ = io.WriteString(w, `{}`)
		} else {
			_, _ = io.WriteString(w, `{"task":null}`)
			cancel()
		}
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(server.Certificate().Raw)
	cmd := NewWorkerCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"--coordinator", server.URL, "--token-file", token, "--fingerprint", hex.EncodeToString(sum[:]), "--tags", "creds-laptop", "--run-untagged=false"})
	if err := cmd.Execute(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !registered.Load() {
		t.Fatal("did not register")
	}
}

func TestWorkerAcceptanceRepairUsesCoordinatorSnapshot(t *testing.T) {
	j := &runJob{hooks: &jobHooks{}, repair: &ciRepairContext{PR: vcs.PR{HeadSHA: "head"}, Acceptance: &acceptanceFindings{Verdict: "failed", AttemptID: "attempt", ContractDigest: "digest"}}}
	if err := recheckAcceptanceRepair(context.Background(), j, j.repair.PR); err != nil {
		t.Fatal(err)
	}
	j.repair.Acceptance.Verdict = "blocked"
	if err := recheckAcceptanceRepair(context.Background(), j, j.repair.PR); err == nil {
		t.Fatal("blocked verdict accepted")
	}
	j.repair.Acceptance.Verdict = "failed"
	if err := recheckAcceptanceRepair(context.Background(), j, vcs.PR{HeadSHA: "changed"}); err == nil {
		t.Fatal("changed head accepted")
	}
}

func TestWorkerRetriesOutageAndReregisters(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var registrations, claims atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/worker/v1/register":
			registrations.Add(1)
		case "/worker/v1/claim":
			n := claims.Add(1)
			if n == 1 {
				w.WriteHeader(503)
				return
			}
			_, _ = w.Write([]byte(`{"task":null}`))
			cancel()
		}
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, client: server.Client()}
	if err := runWorker(ctx, api, workerOptions{MaxParallel: 1}, time.Second, time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	if claims.Load() != 2 || registrations.Load() < 2 {
		t.Fatalf("claims=%d registrations=%d", claims.Load(), registrations.Load())
	}
}

func TestWorkerAuthenticationFailureIsFatal(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer server.Close()
	api := &workerAPI{base: server.URL, client: server.Client()}
	if err := runWorker(context.Background(), api, workerOptions{MaxParallel: 1}, time.Second, time.Millisecond, nil); !errors.Is(err, errWorkerAuth) {
		t.Fatalf("auth: %v", err)
	}
}
