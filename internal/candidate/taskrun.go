package candidate

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// TaskLayer is the evidence layer that records one real task run. It is
// separate from "lifecycle" (processes started and stopped): a controller and
// workers can be up while no task is ever claimed.
const TaskLayer = "task-lifecycle"

// Step is one observed stage of the task, with the observation that proves it.
type Step struct {
	Name     string `json:"name"` // claimed, executed, written-back, finished
	Passed   bool   `json:"passed"`
	Evidence string `json:"evidence"`
}

// TaskRun is the evidence of one real task through the candidate: claimed by a
// worker, executed by the agent process, written back to the forge, finished in
// the coordinator, with no process or lock left behind. It holds no secrets.
type TaskRun struct {
	Commit            string      `json:"candidate_commit"`
	RuntimeID         string      `json:"runtime_id"`
	TaskID            string      `json:"task_id,omitempty"`
	State             string      `json:"state,omitempty"`
	Steps             []Step      `json:"steps"`
	Forge             ForgeCounts `json:"forge"`
	Audit             []string    `json:"audit,omitempty"`
	ResidualProcesses []string    `json:"residual_processes"`
	ResidualLocks     []string    `json:"residual_locks"`
	// Settled is set once the processes were checked after the candidate stopped.
	// Until then "no residual process" is unproven and the run cannot pass.
	Settled bool   `json:"settled"`
	Failure string `json:"failure,omitempty"`
	// Diagnostics are bounded, redacted tails of the task-creating command and
	// the services' logs, kept only on a failed run. They never affect the verdict.
	Diagnostics map[string]string `json:"diagnostics,omitempty"`
	Started     string            `json:"started"`
	Finished    string            `json:"finished"`
}

// Passed is true only when every step passed and nothing was left behind. The
// verdict is recomputed from the recorded facts, never read from a flag.
func (t TaskRun) Passed() bool {
	if t.Failure != "" || !t.Settled || len(t.Steps) != 4 || len(t.ResidualProcesses) != 0 || len(t.ResidualLocks) != 0 {
		return false
	}
	for i, want := range []string{"claimed", "executed", "written-back", "finished"} {
		if t.Steps[i].Name != want || !t.Steps[i].Passed {
			return false
		}
	}
	return true
}

func (t *TaskRun) step(name string, ok bool, evidence string) {
	t.Steps = append(t.Steps, Step{Name: name, Passed: ok, Evidence: evidence})
}

// TaskOptions bounds one run.
type TaskOptions struct {
	// Timeout bounds the wait for the task to reach a terminal state.
	Timeout time.Duration
	// Settle is how long a lock may stay held after the task finished before it
	// counts as left behind.
	Settle time.Duration
}

func (o TaskOptions) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return 3 * time.Minute
}

func (o TaskOptions) settle() time.Duration {
	if o.Settle > 0 {
		return o.Settle
	}
	return 5 * time.Second
}

// FixtureDir holds the fixture forge's repository and the agent log.
func (p Plan) FixtureDir() string { return filepath.Join(p.Root, "fixture") }

// FixturePaths returns the bare remote and the clone the worker uses.
func (p Plan) FixturePaths() (bare, local string) {
	return filepath.Join(p.FixtureDir(), "remote.git"), filepath.Join(p.FixtureDir(), "repo")
}

// AgentLog is appended by the fixture agent once per execution.
func (p Plan) AgentLog() string { return filepath.Join(p.Logs, "agent.log") }

// DatabasePath is the controller's embedded SQLite file in the candidate state.
func (p Plan) DatabasePath() string { return filepath.Join(p.State, "data", store.FileName) }

// RunTask drives one fixture issue through the running candidate and checks the
// outcome. m must already be running (Up). The candidate binary creates the
// task (`run --repo --issue`), a candidate worker claims and executes it, and
// the forge must observe the write-back. Locks are checked while the workers
// are still up, since a lock held by an idle live worker is the defect.
func RunTask(ctx context.Context, m *Manifest, binary string, forge *Forge, opts TaskOptions) *TaskRun {
	p := m.Plan
	run := &TaskRun{Commit: p.Commit, RuntimeID: p.ID, Started: time.Now().UTC().Format(time.RFC3339)}
	defer func() { run.Finished = time.Now().UTC().Format(time.RFC3339) }()
	var created string
	fail := func(format string, a ...any) *TaskRun {
		run.Failure = fmt.Sprintf(format, a...)
		run.Diagnostics = diagnostics(p, created)
		return run
	}
	forge.AddIssue(FixtureIssue, "Candidate run", "Make the requested change")
	db, err := store.Open(ctx, p.DatabasePath())
	if err != nil {
		return fail("cannot read the candidate database: %v", err)
	}
	defer db.Close()
	// A worker registers after it took its service-lifetime locks, so waiting for
	// both lanes makes the baseline below include exactly the locks that live
	// services hold by design. Only locks a task introduces are then leaks.
	if err := waitWorkers(ctx, db, 2, opts.timeout()); err != nil {
		run.step("claimed", false, "worker lanes did not come online")
		return fail("%v", err)
	}
	baseline := scanLocks(p)
	out, errOut, err := m.Output(ctx, binary, []string{"run", "--repo", FixtureRepo, "--issue", strconv.Itoa(FixtureIssue)}, "")
	created = out + errOut
	if err != nil {
		return fail("creating the task failed: %v: %s", err, tail(out+errOut, 400))
	}
	task, err := waitTask(ctx, db, opts.timeout())
	if task != nil {
		run.TaskID, run.State = task.ID, task.State
		run.Audit = auditActions(db, task.ID)
	}
	if err != nil {
		run.step("claimed", task != nil && hasAudit(run.Audit, "claim"), "task state "+stateOf(task))
		return fail("%v", err)
	}
	dev, _ := p.Lanes()
	claimed := task.LeaseOwner == dev.Name || auditWorker(db, task.ID, "claim") == dev.Name
	run.step("claimed", claimed && hasAudit(run.Audit, "claim"), fmt.Sprintf("claim audited for worker %s", dev.Name))
	invocations := countLines(p.AgentLog())
	run.step("executed", invocations == 1, fmt.Sprintf("agent process ran %d time(s)", invocations))
	run.Forge = forge.Counts()
	labelled := forge.HasLabel(FixtureIssue, "agent-implemented")
	run.step("written-back", run.Forge.MergeRequests == 1 && run.Forge.Comments >= 1 && labelled,
		fmt.Sprintf("forge saw %d merge request(s), %d comment(s), outcome label present=%v", run.Forge.MergeRequests, run.Forge.Comments, labelled))
	run.step("finished", task.State == "done" && hasAudit(run.Audit, "result-applied"), "coordinator state "+task.State+", result applied")
	// Locks are examined with the workers still alive: a kernel lock held by an
	// idle worker after its task ended is a leak, one released at exit is not.
	run.ResidualLocks = newlyHeld(p, baseline, opts.settle())
	return run
}

// waitWorkers waits until n worker lanes are registered with the controller.
func waitWorkers(ctx context.Context, db *store.Store, n int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var count int64
		if err := db.DB().Model(&coordinator.Worker{}).Count(&count).Error; err == nil && count >= int64(n) {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("fewer than %d worker lanes registered within %s", n, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func stateOf(t *coordinator.Task) string {
	if t == nil {
		return "no task was created"
	}
	return t.State
}

func waitTask(ctx context.Context, db *store.Store, timeout time.Duration) (*coordinator.Task, error) {
	deadline := time.Now().Add(timeout)
	var last *coordinator.Task
	for {
		var t coordinator.Task
		err := db.DB().Where("repo = ? AND issue = ?", FixtureRepo, FixtureIssue).Order("created_at DESC").First(&t).Error
		if err == nil {
			last = &t
			switch t.State {
			case "done":
				return last, nil
			case "failed", "cancelled":
				return last, fmt.Errorf("task ended %s", t.State)
			}
		}
		if ctx.Err() != nil {
			return last, ctx.Err()
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("task did not finish within %s (state %s)", timeout, stateOf(last))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func auditActions(db *store.Store, id string) []string {
	var rows []coordinator.Audit
	_ = db.DB().Where("task = ?", id).Order("id ASC").Find(&rows).Error
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Action)
	}
	return out
}

func auditWorker(db *store.Store, id, action string) string {
	var row coordinator.Audit
	if db.DB().Where("task = ? AND action = ?", id, action).Order("id ASC").First(&row).Error != nil {
		return ""
	}
	return row.Worker
}

func hasAudit(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(data)))
}

// newlyHeld lists locks held after the settle period that were not held before
// the task started, so a lock an idle service owns by design is not a leak.
func newlyHeld(p Plan, baseline []string, settle time.Duration) []string {
	was := map[string]bool{}
	for _, l := range baseline {
		was[l] = true
	}
	deadline := time.Now().Add(settle)
	for {
		leaked := []string{}
		for _, l := range scanLocks(p) {
			if !was[l] {
				leaked = append(leaked, l)
			}
		}
		if len(leaked) == 0 || time.Now().After(deadline) {
			return leaked
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func scanLocks(p Plan) []string {
	held := []string{}
	for _, root := range []string{p.State, p.ReviewDir, p.Tmp, p.Home} {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".lock") {
				return nil
			}
			if filelock.Held(path) {
				rel, relErr := filepath.Rel(p.Root, path)
				if relErr != nil {
					rel = d.Name()
				}
				held = append(held, rel)
			}
			return nil
		})
	}
	sort.Strings(held)
	return held
}

// ResidualProcesses lists processes still running from this candidate: anything
// the manifest owns, plus any process that names the candidate root or works
// inside it (children the services started, such as the agent or git).
func ResidualProcesses(m *Manifest) []string {
	left := []string{}
	for _, pr := range m.Processes {
		if m.owned(pr) {
			left = append(left, fmt.Sprintf("%s (pid %d)", pr.Role, pr.PID))
		}
	}
	for _, p := range processesUnder(m.Plan.Root) {
		left = append(left, fmt.Sprintf("%s (pid %d)", p.Name, p.PID))
	}
	return dedupe(left)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// SettleProcesses waits for candidate processes to exit and returns what stayed.
// Descendants that outlive a stopped service are terminated: they are reported
// as residual either way, because they were left behind.
func SettleProcesses(m *Manifest, wait time.Duration) []string {
	deadline := time.Now().Add(wait)
	for {
		left := ResidualProcesses(m)
		if len(left) == 0 || time.Now().After(deadline) {
			return left
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// KillResidual terminates stray candidate processes after they were recorded as
// residual. Only processes that name this candidate root or work inside it are
// signalled.
func KillResidual(m *Manifest) {
	for _, p := range processesUnder(m.Plan.Root) {
		_ = syscall.Kill(p.PID, syscall.SIGKILL)
	}
}

// diagnostics collects the tails of the logs that explain a failed run.
func diagnostics(p Plan, runOutput string) map[string]string {
	d := map[string]string{}
	if strings.TrimSpace(runOutput) != "" {
		d["run-output"] = clipText(tail(runOutput, 1500), 1500)
	}
	for _, name := range []string{"controller", "worker-operator", "worker-acceptance"} {
		if data, err := os.ReadFile(filepath.Join(p.Logs, name+".log")); err == nil && len(data) > 0 {
			d[name] = clipText(tail(string(data), 1500), 1500)
		}
	}
	if data, err := os.ReadFile(filepath.Join(p.Home, ".clawflow", "logs", "run.log")); err == nil && len(data) > 0 {
		d["run.log"] = clipText(tail(string(data), 1500), 1500)
	}
	return d
}

// Bounds on the recorded run, which is stored in the acceptance record and shown
// to the reviewer as untrusted data (parts of it come from the program under test).
const (
	maxRunText  = 400
	maxRunItems = 20
)

func clipText(s string, n int) string {
	s = scrubText(strings.ToValidUTF8(s, "?"))
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func clipList(in []string) []string {
	out := make([]string, 0, len(in))
	for i, v := range in {
		if i == maxRunItems {
			out = append(out, fmt.Sprintf("… %d more", len(in)-maxRunItems))
			break
		}
		out = append(out, clipText(v, 160))
	}
	return out
}

// Clip bounds and redacts every free-text field. It never changes a Passed flag
// or the step names, so clipping cannot turn a failure into a pass.
func (t *TaskRun) Clip() {
	t.Failure = clipText(t.Failure, maxRunText)
	for i := range t.Steps {
		t.Steps[i].Evidence = clipText(t.Steps[i].Evidence, maxRunText)
	}
	t.Audit, t.ResidualProcesses, t.ResidualLocks = clipList(t.Audit), clipList(t.ResidualProcesses), clipList(t.ResidualLocks)
	for k, v := range t.Diagnostics {
		t.Diagnostics[k] = clipText(v, 1500)
	}
}

// Summary is a one-paragraph account of the run for reports and prompts.
func (t TaskRun) Summary() string {
	var b strings.Builder
	state := "passed"
	if !t.Passed() {
		state = "FAILED"
	}
	fmt.Fprintf(&b, "isolated run %s of candidate %s (commit %s)", state, t.RuntimeID, t.Commit)
	for _, s := range t.Steps {
		mark := "ok"
		if !s.Passed {
			mark = "failed"
		}
		fmt.Fprintf(&b, "; %s: %s (%s)", s.Name, mark, s.Evidence)
	}
	if t.Failure != "" {
		fmt.Fprintf(&b, "; failure: %s", t.Failure)
	}
	fmt.Fprintf(&b, "; residual processes: %d; residual locks: %d; settled: %v", len(t.ResidualProcesses), len(t.ResidualLocks), t.Settled)
	return b.String()
}

// Marshal renders the run for the evidence file.
func (t TaskRun) Marshal() []byte {
	t.Clip()
	data, _ := json.MarshalIndent(t, "", "  ")
	return append(data, '\n')
}

// ForgeURL is the loopback base URL of the fixture forge.
func (p Plan) ForgeURL() string { return "http://127.0.0.1:" + strconv.Itoa(p.ForgePort) }

// WithForge returns the plan with a fixture forge port. Zero derives a
// reproducible port from the commit, away from the controller port.
func (p Plan) WithForge(port int) Plan {
	if port == 0 {
		sum := sha256.Sum256([]byte("clawflow-candidate-forge\x00" + p.Commit))
		port = 20000 + int(binary.BigEndian.Uint16(sum[:2]))%20000
		if port == p.Port {
			port++
		}
	}
	p.ForgePort = port
	return p
}

// TaskConfigOptions is the configuration of a candidate that runs the fixture
// task: the loopback forge, the clone below the candidate root, the fixture
// author and the in-process execution engine with the candidate's own profile.
func (p Plan) TaskConfigOptions() ConfigOptions {
	_, local := p.FixturePaths()
	return ConfigOptions{
		Fixtures:        map[string]string{FixtureRepo: p.ForgeURL()},
		LocalPaths:      map[string]string{FixtureRepo: local},
		AllowedAuthors:  []string{"tester"},
		ExecutionEngine: "library",
		Profile:         "candidate",
		Dispatch:        true,
		NoAcceptance:    true,
	}
}

// SetupTaskFixture creates what a task run needs below the candidate root: a git
// remote and clone, the fixture agent that stands in for the model CLI, and an
// Offload profile in the candidate's own home. It touches nothing outside it.
func SetupTaskFixture(p Plan) error {
	if p.ForgePort == 0 {
		return fmt.Errorf("plan has no fixture forge port")
	}
	bare, local := p.FixturePaths()
	if err := os.MkdirAll(p.FixtureDir(), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(bare); os.IsNotExist(err) {
		if err := SeedGit(bare, local); err != nil {
			return err
		}
	}
	agent := filepath.Join(p.Bin, "claude")
	if err := os.WriteFile(agent, []byte(AgentScript(filepath.Join(p.Bin, "clawflow"))), 0o700); err != nil {
		return err
	}
	dir := filepath.Join(p.Home, ".config", "offload")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	profile := fmt.Sprintf(`{"default":"candidate","profiles":{"candidate":{"backend":"claude","base_url":%q,"model":"fixture","token":"candidate-token"}}}`, p.ForgeURL())
	return os.WriteFile(filepath.Join(dir, "config.json"), []byte(profile), 0o600)
}

// Exercise serves the fixture forge for the duration of one task run on the
// running candidate m and returns the recorded run. The binary is the candidate's
// own build. Exercise does not stop the candidate; Down does, and the caller
// then settles the run with FinishTask.
func Exercise(ctx context.Context, m *Manifest, opts TaskOptions) (*TaskRun, error) {
	p := m.Plan
	if p.ForgePort == 0 {
		return nil, fmt.Errorf("candidate %s was not prepared with a task fixture", p.ID)
	}
	if !m.Running() {
		return nil, fmt.Errorf("candidate %s is not running; run `clawflow candidate up` first", p.ID)
	}
	bare, _ := p.FixturePaths()
	forge := NewForge(bare)
	if err := forge.Serve(net.JoinHostPort("127.0.0.1", strconv.Itoa(p.ForgePort))); err != nil {
		return nil, fmt.Errorf("fixture forge: %w", err)
	}
	defer forge.Close()
	return RunTask(ctx, m, filepath.Join(p.Bin, "clawflow"), forge, opts), nil
}

// FinishTask completes the run after the candidate was stopped: it records
// whatever process is still alive from the candidate (and ends it), and marks the
// run settled. Without this the run cannot pass.
func FinishTask(m *Manifest, run *TaskRun, wait time.Duration) {
	run.ResidualProcesses = SettleProcesses(m, wait)
	if len(run.ResidualProcesses) > 0 {
		KillResidual(m)
	}
	run.Settled = true
}
