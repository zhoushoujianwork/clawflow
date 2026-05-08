package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

var (
	runMu     sync.Mutex
	runActive bool

	schedMu       sync.Mutex
	schedNextFire time.Time // zero when no scheduler is running or interval=0
)

type runRequest struct {
	Repo  string `json:"repo,omitempty"`
	Issue int    `json:"issue,omitempty"`
}

type runResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// Scheduler fields — populated by HandleRunStatus so the dashboard
	// can render the auto-run banner ("every 5m · next in 2:14") in one
	// fetch alongside the running/idle indicator.
	IntervalMinutes int   `json:"interval_minutes,omitempty"`
	Paused          bool  `json:"paused,omitempty"`
	NextFireUnixMs  int64 `json:"next_fire_unix_ms,omitempty"`
}

// SetSchedulerNextFire is called by the web command's periodic-run
// goroutine to publish when the next tick will fire. Zero clears it.
func SetSchedulerNextFire(t time.Time) {
	schedMu.Lock()
	schedNextFire = t
	schedMu.Unlock()
}

// TriggerRun spawns `clawflow run` as a background subprocess if no
// run is currently in flight. Returns true if it actually started,
// false if a run was already running. Empty repo/issue arguments mean
// "all enabled repos / all issues" — same semantics as the CLI flags.
//
// Both the manual Run button (HandleRun) and the periodic ticker in
// `clawflow web` go through this helper so they share the runActive
// gate and can never overlap.
func TriggerRun(repo string, issue int) bool {
	runMu.Lock()
	if runActive {
		runMu.Unlock()
		return false
	}
	runActive = true
	runMu.Unlock()

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}

	args := []string{"run"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if issue > 0 {
		args = append(args, "--issue", fmt.Sprintf("%d", issue))
	}

	cmd := exec.Command(self, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	go func() {
		defer func() {
			runMu.Lock()
			runActive = false
			runMu.Unlock()
		}()
		_ = cmd.Run()
	}()
	return true
}

// HandleRun handles POST /api/run — triggers `clawflow run` in background.
func HandleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req runRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if !TriggerRun(req.Repo, req.Issue) {
		writeJSON(w, 409, runResponse{Status: "busy", Message: "a run is already in progress"})
		return
	}
	writeJSON(w, 200, runResponse{Status: "started"})
}

// HandleRunStatus handles GET /api/run/status. Returns whether a run
// is in flight plus the auto-run scheduler state so the dashboard can
// render everything in a single 3s poll.
func HandleRunStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runMu.Lock()
	active := runActive
	runMu.Unlock()

	status := "idle"
	if active {
		status = "running"
	}

	resp := runResponse{Status: status}
	if cfg, err := config.Load(); err == nil {
		resp.IntervalMinutes = cfg.Settings.RunIntervalMinutes
		resp.Paused = cfg.Settings.RunPaused
	}
	schedMu.Lock()
	next := schedNextFire
	schedMu.Unlock()
	if !next.IsZero() {
		resp.NextFireUnixMs = next.UnixMilli()
	}
	writeJSON(w, 200, resp)
}

// cancelRequest identifies which in-flight run the dashboard wants to stop.
// We key by (repo, issue) — same shape as the lockfile — so the dashboard's
// Cancel button can be wired straight from the running row's existing data.
type cancelRequest struct {
	Repo  string `json:"repo"`
	Issue int    `json:"issue"`
}

type cancelResponse struct {
	Status string `json:"status"`            // "cancelled" | "already_dead" | "not_locked"
	PID    int    `json:"pid,omitempty"`     // owner PID we signaled (informational)
	Killed []int  `json:"killed,omitempty"`  // every PID we sent SIGTERM to (parent + descendants)
}

// HandleRunCancel handles POST /api/run/cancel. Body: {repo, issue}.
//
// Cancelling kills the process tree owning the (repo, issue) lock and then
// removes the lock so the next clawflow run can re-pick the issue if the
// trigger labels still apply. We kill descendants first (claude subprocess)
// then the parent, escalating SIGTERM → SIGKILL after a short grace window
// so a stuck child doesn't leave the lock in place.
//
// The killed run's events.jsonl is NOT rewritten here — the dashboard's
// reconciler (snapshot.ReconcileStaleRuns) will mark the run dir as
// failed/interrupted on the next sweep, which keeps the cancel path
// self-contained and avoids two writers racing on meta.json.
func HandleRunCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req cancelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Repo == "" || req.Issue <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo and issue are required"})
		return
	}

	path := snapshot.LockPath(req.Repo, req.Issue)
	info, err := snapshot.ReadLock(path)
	if err != nil {
		// Missing or corrupt lock — treat as "nothing to cancel". The UI
		// should fall back to a fresh fetch which will already show the
		// row in a non-running state.
		writeJSON(w, http.StatusOK, cancelResponse{Status: "not_locked"})
		return
	}

	// Owner already exited but never released the lock (Ctrl+C, SIGKILL,
	// crash). Just clean the lock file so the next run isn't blocked.
	if !processAlive(info.PID) {
		snapshot.ReleaseLock(req.Repo, req.Issue)
		// Owner died without finalizing meta — flip running rows to
		// failed too so the dashboard doesn't keep showing a phantom
		// running run after we ack the cancel.
		snapshot.MarkRunningAsCancelled(req.Repo, req.Issue, "runner exited without finalizing; cleared by cancel")
		_, _ = snapshot.WriteRunsIndex(50)
		writeJSON(w, http.StatusOK, cancelResponse{Status: "already_dead", PID: info.PID})
		return
	}

	killed := killProcessTree(info.PID)
	snapshot.ReleaseLock(req.Repo, req.Issue)

	// Flip the run's meta.json to status=failed immediately so the
	// dashboard reflects the kill on its next 5s poll. Without this the
	// row sits as "running" until the per-operator quiet-window
	// reconciler fires (10 min for `implement`), which feels like the
	// cancel button did nothing.
	snapshot.MarkRunningAsCancelled(req.Repo, req.Issue, "cancelled by user via dashboard")
	if _, werr := snapshot.WriteRunsIndex(50); werr != nil {
		// Non-fatal: cancellation succeeded; index will catch up on the
		// next reconciler tick (~1 min).
		_ = werr
	}

	writeJSON(w, http.StatusOK, cancelResponse{
		Status: "cancelled",
		PID:    info.PID,
		Killed: killed,
	})
}

// processAlive reports whether a PID still names a running process. signal 0
// is the standard portable existence check on POSIX — it returns ESRCH when
// the PID is gone and never affects the target either way.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// pgrepChildren lists immediate children of `parent` via pgrep -P. Returns
// nil on any error (pgrep missing, no children, parse failure) — the caller
// treats nil as "no descendants" and keeps killing the parent only.
func pgrepChildren(parent int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(parent)).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// killProcessTree sends SIGTERM to `parent` and every descendant pgrep can
// find, then waits up to 2s for them to exit and escalates the still-alive
// ones to SIGKILL. Returns every PID we successfully signaled — useful for
// the cancel response so the user sees what was reaped.
//
// We kill descendants before the parent so a wedged child (e.g. claude
// blocked in syscall) can't continue running orphaned after the parent
// dies. exec.Cmd in Go doesn't auto-forward signals to the child, so a
// naive single-PID SIGTERM would leave the claude grandchild alive.
func killProcessTree(parent int) []int {
	descendants := collectDescendants(parent)
	all := append(descendants, parent)

	var signaled []int
	for _, pid := range all {
		if syscall.Kill(pid, syscall.SIGTERM) == nil {
			signaled = append(signaled, pid)
		}
	}

	// Grace window: wait up to 2s for graceful exit. Poll every 100ms so a
	// fast-exiting process doesn't make us sit on the full timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		anyAlive := false
		for _, pid := range all {
			if processAlive(pid) {
				anyAlive = true
				break
			}
		}
		if !anyAlive {
			return signaled
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Anything still alive gets SIGKILL.
	for _, pid := range all {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return signaled
}

// collectDescendants walks the process tree starting at root via repeated
// pgrep -P calls. Bounded by a max depth and a visited set so a kernel-
// level pid recycling race can't spin forever.
func collectDescendants(root int) []int {
	visited := map[int]bool{root: true}
	var out []int
	queue := []int{root}
	for len(queue) > 0 && len(visited) < 1024 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range pgrepChildren(parent) {
			if visited[child] {
				continue
			}
			visited[child] = true
			out = append(out, child)
			queue = append(queue, child)
		}
	}
	return out
}

// HandleRunPause handles POST /api/run/pause. Body: {"paused": bool}.
// Updates settings.run_paused and persists. The pause flag does not
// kill an in-flight run — it only suppresses future periodic ticks
// until the user resumes.
func HandleRunPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load()
	if err != nil {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cfg.Settings.RunPaused = req.Paused
	if err := cfg.Save(); err != nil {
		http.Error(w, "save config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, 200, map[string]any{
		"paused":           cfg.Settings.RunPaused,
		"interval_minutes": cfg.Settings.RunIntervalMinutes,
	})
}
