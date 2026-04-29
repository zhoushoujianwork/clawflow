package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
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
