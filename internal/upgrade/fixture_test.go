package upgrade_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

const (
	shaPrior  = "1111111111111111111111111111111111111111"
	shaTarget = "2222222222222222222222222222222222222222"
)

// fixture is an isolated stand-in for the production host. It uses a real
// PostgreSQL schema and the real coordinator store, so leases, fencing and
// result acceptance are the production code paths. Only process management
// and the service manager are simulated.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	db    *store.Store
	tasks *coordinator.Store
	dir   string

	mu         sync.Mutex
	running    bool
	runningSHA string
	lockHeld   bool
	fenced     bool
	installed  string // sha the service definition starts
	api        bool   // API answers
	viewer     bool
	downSince  time.Time
	startCount int
	// Two controllers serving at once would be a safety violation.
	maxServing int

	failProbeFor map[string]bool // sha -> API never becomes healthy
	failMigrate  bool
	failStop     bool
	executions   map[string]int // task id -> times a worker started executing it
	writeBacks   map[string]int // task id -> times the effect was written
	applied      []int
	rolled       []string
	rollFails    map[string]bool
	workerPause  map[string]bool
	reg          map[string]*time.Time // worker -> last_seen override
	clock        *fakeClock
	crashAfter   string // stop, install, start: perform the action, then die
	wg           sync.WaitGroup
	rollSnapshot map[string]int // descriptor -> leases the worker still had when rolled
	rollTouch    bool
	noRollTouch  map[string]bool
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, ctx: ctx, db: db, tasks: coordinator.New(db.DB()), dir: t.TempDir(),
		running: true, runningSHA: sha("prior"), lockHeld: true, installed: sha("prior"), api: true, viewer: true,
		failProbeFor: map[string]bool{}, executions: map[string]int{}, writeBacks: map[string]int{}, rollFails: map[string]bool{}, reg: map[string]*time.Time{},
		clock: &fakeClock{t: time.Now().UTC()}, rollSnapshot: map[string]int{}, noRollTouch: map[string]bool{}}
	t.Cleanup(func() { f.wg.Wait() })
	f.maxServing = 1
	f.applied = appliedOf(store.Migrations)
	return f
}

func appliedOf(migs []store.Migration) []int {
	var out []int
	for _, m := range migs {
		out = append(out, m.Version)
	}
	return out
}

func sha(name string) string {
	return fmt.Sprintf("%064x", []byte(name))[:64]
}

func (f *fixture) manifests() (prior, target upgrade.Manifest) {
	latest := store.LatestVersion(store.Migrations)
	prior = upgrade.Manifest{Version: "prior", Commit: shaPrior, SchemaLatest: latest, WorkerProtocol: 1}
	target = upgrade.Manifest{Version: "target", Commit: shaTarget, SchemaLatest: latest, WorkerProtocol: 1}
	return
}

func (f *fixture) artifacts() (prior, target upgrade.Artifact) {
	pm, tm := f.manifests()
	write := func(name string) string {
		p := filepath.Join(f.dir, "src-"+name)
		_ = os.WriteFile(p, []byte(name), 0o700)
		return p
	}
	prior = upgrade.Artifact{Commit: shaPrior, SHA256: realSHA(write("prior")), Path: filepath.Join(f.dir, "src-prior"), Manifest: pm}
	target = upgrade.Artifact{Commit: shaTarget, SHA256: realSHA(write("target")), Path: filepath.Join(f.dir, "src-target"), Manifest: tm}
	f.runningSHA, f.installed = prior.SHA256, prior.SHA256
	return
}

func realSHA(path string) string {
	s, err := upgrade.FileSHA(path)
	if err != nil {
		panic(err)
	}
	return s
}

func gates() upgrade.Gates {
	return upgrade.Gates{EvidenceDigest: "ev-digest", CIRef: "ci-run-1", AcceptanceRef: "acceptance-1", ApprovedSHA: shaTarget}
}

// ---- upgrade.Host --------------------------------------------------------

func (f *fixture) Preserve(_ context.Context, a upgrade.Artifact) (upgrade.Artifact, error) {
	return upgrade.PreserveFile(filepath.Join(f.dir, "artifacts"), a)
}

func (f *fixture) Describe(_ context.Context, a upgrade.Artifact) (upgrade.Manifest, error) {
	return a.Manifest, nil
}

func (f *fixture) Applied(ctx context.Context) ([]int, error) { return f.db.AppliedVersions(ctx) }

func (f *fixture) MigrateExpand(ctx context.Context, _ upgrade.Artifact) ([]int, error) {
	if f.failMigrate {
		return nil, errors.New("injected migration failure")
	}
	return f.db.Migrate(ctx, store.Migrations)
}

func (f *fixture) Snapshot(ctx context.Context, watch []string) (coordinator.UpgradeSnapshot, error) {
	return f.tasks.UpgradeSnapshot(ctx, watch)
}

func (f *fixture) SetPaused(ctx context.Context, worker string, paused bool) error {
	return f.tasks.SetWorkerPaused(ctx, worker, paused)
}

func (f *fixture) Probe(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running || !f.api {
		return errors.New("connection refused")
	}
	return nil
}

func (f *fixture) ViewerProbe(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running || !f.viewer {
		return errors.New("viewer endpoint down")
	}
	return nil
}

func (f *fixture) Observe(context.Context) (upgrade.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return upgrade.Observation{Running: f.running, RunningSHA: f.runningSHA, LockHeld: f.lockHeld}, nil
}

func (f *fixture) StopController(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStop {
		return errors.New("service refused to stop")
	}
	f.running, f.lockHeld, f.api, f.viewer = false, false, false, false
	f.downSince = f.clock.Now()
	if f.crashAfter == "stop" {
		f.crashAfter = ""
		return upgrade.ErrInterrupted
	}
	return nil
}

func (f *fixture) Fence(context.Context) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockHeld || f.fenced {
		return nil, errors.New("controller ownership lock is held")
	}
	f.fenced = true
	return func() { f.mu.Lock(); f.fenced = false; f.mu.Unlock() }, nil
}

func (f *fixture) Install(_ context.Context, a upgrade.Artifact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fenced {
		return errors.New("service definition changed without holding the fence")
	}
	f.installed = a.SHA256
	if f.crashAfter == "install" {
		f.crashAfter = ""
		return upgrade.ErrInterrupted
	}
	return nil
}

func (f *fixture) StartController(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running || f.lockHeld {
		return errors.New("a controller is already running")
	}
	f.startCount++
	f.running, f.lockHeld, f.runningSHA = true, true, f.installed
	f.api = !f.failProbeFor[f.installed]
	f.viewer = f.api
	if f.api {
		f.reconnect()
	}
	if f.crashAfter == "start" {
		f.crashAfter = ""
		return upgrade.ErrInterrupted
	}
	return nil
}

// reconnect simulates every worker contacting the new controller.
func (f *fixture) reconnect() {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		time.Sleep(15 * time.Millisecond)
		var names []string
		if err := f.db.DB().Raw("SELECT name FROM coordinator_tokens WHERE revoked = false").Scan(&names).Error; err != nil {
			return
		}
		for _, n := range names {
			_ = f.db.DB().Exec("UPDATE coordinator_tokens SET last_seen = clock_timestamp() WHERE name = ?", n).Error
		}
	}()
}

func (f *fixture) RollWorker(_ context.Context, descriptor string, a upgrade.Artifact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rollFails[descriptor] {
		return errors.New("candidate refused")
	}
	f.rolled = append(f.rolled, descriptor)
	if snap, err := f.tasks.UpgradeSnapshot(f.ctx, nil); err == nil {
		n := 0
		for _, l := range snap.Leases {
			if l.Owner == descriptor {
				n++
			}
		}
		f.rollSnapshot[descriptor] = n
	}
	if !f.noRollTouch[descriptor] {
		f.wg.Add(1)
		go func() {
			defer f.wg.Done()
			time.Sleep(15 * time.Millisecond)
			_ = f.db.DB().Exec("UPDATE coordinator_tokens SET last_seen = clock_timestamp() WHERE name = ?", descriptor).Error
		}()
	}
	return nil
}

var _ upgrade.Host = (*fixture)(nil)

// ---- helpers -------------------------------------------------------------

func (f *fixture) register(name string, slots int) {
	f.t.Helper()
	if err := f.tasks.RegisterWorker(f.ctx, coordinator.Worker{ID: name, MaxParallel: slots, RunUntagged: true, Durable: true}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.tasks.CreateToken(f.ctx, name); err != nil {
		f.t.Fatal(err)
	}
	f.touch(name)
}

// touch simulates worker contact now.
func (f *fixture) touch(name string) {
	f.t.Helper()
	if err := f.db.DB().Exec("UPDATE coordinator_tokens SET last_seen = clock_timestamp() WHERE name = ?", name).Error; err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) enqueue(issue int) *coordinator.Task {
	f.t.Helper()
	payload, _ := json.Marshal(map[string]any{"timeout_sec": 3600})
	task, err := f.tasks.Enqueue(f.ctx, coordinator.Task{Kind: "implement", Repo: "owner/repo", Issue: issue, Operator: "implement", Payload: payload})
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}

func (f *fixture) claim(worker string) *coordinator.Task {
	f.t.Helper()
	task, err := f.tasks.Claim(f.ctx, worker)
	if err != nil {
		f.t.Fatal(err)
	}
	f.executions[task.ID]++
	return task
}

func (f *fixture) engine(opt upgrade.Options) *upgrade.Engine {
	if opt.Poll == 0 {
		opt.Poll = 5 * time.Millisecond
	}
	if opt.SettleTimeout == 0 {
		opt.SettleTimeout = 2 * time.Second
	}
	if opt.VerifyTimeout == 0 {
		opt.VerifyTimeout = 2 * time.Second
	}
	if opt.WorkerTimeout == 0 {
		opt.WorkerTimeout = 2 * time.Second
	}
	return upgrade.NewEngine(f, upgrade.Store{Dir: filepath.Join(f.dir, "records")}, opt)
}

func (f *fixture) begin(e *upgrade.Engine, prior, target upgrade.Artifact) *upgrade.Record {
	f.t.Helper()
	rec, err := e.Begin(f.ctx, upgrade.KindController, target, gates(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	rec.Prior = prior
	if err := e.Records.Save(rec); err != nil {
		f.t.Fatal(err)
	}
	return rec
}

func (f *fixture) servingNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running {
		return 1
	}
	return 0
}

func (f *fixture) paused(name string) bool {
	snap, err := f.tasks.UpgradeSnapshot(f.ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, w := range snap.Workers {
		if w.Name == name {
			return w.Paused
		}
	}
	f.t.Fatalf("worker %s missing", name)
	return false
}
