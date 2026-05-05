package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/projectpm"
)

// healthCheckJob is the dashboard-poll-friendly view of one in-flight
// or recently-finished health-check run. Keyed by project name in
// healthCheckJobs, mirrors the lifecycle of generateJob:
//
//	POST /api/project/health-check/run    inserts {status:"running"}
//	background goroutine calls RunHealthCheck
//	on completion the goroutine flips status to "done"|"error"
//	GET /api/project/health-check/status  returns the latest snapshot
//
// "done"/"error" entries linger in memory until the next POST for the
// same project so a polling client that arrives after completion can
// still see the result. They are also persisted to disk on completion
// so a `clawflow web` restart doesn't make them disappear — the
// status handler hydrates from disk when the in-memory map is cold.
//
// "running" is intentionally NOT persisted: if the process dies mid-
// run, the goroutine is gone too, and resuming a stale "running" on
// next start would just leave the dashboard spinning forever. The
// natural answer after a crash is "no result yet, click Run again",
// which is what idle-by-default gives us.
type healthCheckJob struct {
	Status    string                       `json:"status"`
	Error     string                       `json:"error,omitempty"`
	StartedAt time.Time                    `json:"started_at"`
	EndedAt   time.Time                    `json:"ended_at,omitzero"`
	Result    *projectpm.HealthCheckResult `json:"result,omitempty"`
	// LastApply preserves the most recent Apply call's per-file outcomes
	// so the dashboard can re-render success/failure badges (and any
	// errors) after a page reload. Set by HandleProjectHealthCheckApply
	// and cleared by the next run start.
	LastApply *projectpm.ApplyResult `json:"last_apply,omitempty"`
}

var (
	healthCheckJobsMu sync.Mutex
	healthCheckJobs   = map[string]*healthCheckJob{}
)

// healthCheckStorePath is where we persist the latest done/error
// job for a project. Sits next to context.md / testing.md inside the
// project's own directory so deleting the project (which removes
// that directory) cleans this up too.
func healthCheckStorePath(projectName string) string {
	return filepath.Join(project.ProjectDir(projectName), "health-check.json")
}

// saveHealthCheckJob writes the job to disk atomically. Best-effort:
// any error is returned to the caller (currently the run goroutine,
// which logs it to stderr and continues — losing persistence is
// preferable to losing the in-memory result).
func saveHealthCheckJob(projectName string, job *healthCheckJob) error {
	path := healthCheckStorePath(projectName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	// Write to a temp file then rename so a crash mid-write can't
	// leave a half-written health-check.json that fails to parse.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// loadHealthCheckJob hydrates the latest persisted job from disk.
// Returns (nil, nil) when the file doesn't exist (the "never run"
// case) so callers can distinguish absence from a real read error.
func loadHealthCheckJob(projectName string) (*healthCheckJob, error) {
	path := healthCheckStorePath(projectName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var job healthCheckJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &job, nil
}

// healthCheckTimeout caps a single run. Overridable via the request
// body once we have a real reason to; for now the dashboard never
// passes an override.
const healthCheckTimeout = 10 * time.Minute

type healthCheckRunRequest struct {
	Project string `json:"project"`
	Model   string `json:"model,omitempty"`
}

// HandleProjectHealthCheckRun kicks off a project-level health check
// for `project`. Returns 202 with the new job entry on accept, or
// 409 with the existing job if one is already running.
//
// The actual claude -p invocation (typically 30s–2min for a small
// project, longer for many repos) runs in a background goroutine so
// the HTTP request returns immediately. Clients learn about
// completion via /status polling — same pattern as generate-context.
func HandleProjectHealthCheckRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req healthCheckRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	name := strings.TrimSpace(req.Project)
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "project is required"})
		return
	}
	p, err := project.Get(name)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "load config: " + err.Error()})
		return
	}

	model := req.Model
	if model == "" {
		creds, _ := config.LoadCredentials()
		if creds != nil {
			model = creds.EffectiveOperatorModel()
		}
	}

	healthCheckJobsMu.Lock()
	if existing, ok := healthCheckJobs[name]; ok && existing.Status == "running" {
		healthCheckJobsMu.Unlock()
		writeJSON(w, 409, map[string]any{
			"error": "health check already running for this project",
			"job":   existing,
		})
		return
	}
	// Starting a fresh run invalidates the previous Apply outcomes —
	// they belonged to the previous proposal.
	job := &healthCheckJob{Status: "running", StartedAt: time.Now()}
	healthCheckJobs[name] = job
	healthCheckJobsMu.Unlock()

	go func() {
		// Detached context: HTTP request finishes before this does.
		// Time-bound via the timeout passed to RunHealthCheck.
		ctx := context.Background()
		result, err := projectpm.RunHealthCheck(ctx, p, cfg, model, healthCheckTimeout)
		healthCheckJobsMu.Lock()
		job.EndedAt = time.Now()
		if err != nil {
			job.Status = "error"
			job.Error = err.Error()
		} else {
			job.Status = "done"
			job.Result = result
		}
		// Snapshot the job under the lock so the disk write below
		// can't race with another goroutine mutating it. The dashboard
		// reads via the in-memory map first, so persistence is purely
		// a cold-start fallback — losing it would only cost the user
		// one accidental re-run after a server restart, which is why
		// errors here are logged and swallowed.
		snapshot := *job
		healthCheckJobsMu.Unlock()
		if err := saveHealthCheckJob(name, &snapshot); err != nil {
			fmt.Fprintf(os.Stderr, "[health-check] %s: persist failed: %v\n", name, err)
		}
	}()

	writeJSON(w, 202, map[string]any{
		"status": "accepted",
		"job":    job,
	})
}

// HandleProjectHealthCheckStatus reports the current job state for
// ?project=X. 404 (with status:"idle") means no job has ever been
// recorded for this project; the dashboard treats that the same as
// "ready to run".
func HandleProjectHealthCheckStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("project"))
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "project is required"})
		return
	}
	healthCheckJobsMu.Lock()
	job, ok := healthCheckJobs[name]
	healthCheckJobsMu.Unlock()
	if ok {
		writeJSON(w, 200, job)
		return
	}
	// Cold cache (e.g. after `clawflow web` restart): fall back to
	// the persisted last-completed job. Hydrate the in-memory map so
	// subsequent polls hit it directly without re-reading the file.
	persisted, err := loadHealthCheckJob(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[health-check] %s: load persisted failed: %v\n", name, err)
		writeJSON(w, 404, map[string]string{"status": "idle"})
		return
	}
	if persisted == nil {
		writeJSON(w, 404, map[string]string{"status": "idle"})
		return
	}
	healthCheckJobsMu.Lock()
	if _, raced := healthCheckJobs[name]; !raced {
		healthCheckJobs[name] = persisted
	}
	healthCheckJobsMu.Unlock()
	writeJSON(w, 200, persisted)
}

type healthCheckApplyRequest struct {
	Project string                     `json:"project"`
	Changes []projectpm.ProposedChange `json:"changes"`
}

// HandleProjectHealthCheckApply writes the user-approved subset of
// proposed changes and (for repo-targeted changes) commits + pushes
// them. Synchronous — file writes and git plumbing are fast enough
// that the dashboard can wait. Returns the per-change ApplyResult so
// the UI can render success/failure per file.
//
// Why pass `changes` from the client rather than re-fetching the
// previous run's result server-side: lets the UI selectively accept
// some files and reject others without a separate "filter" endpoint.
// The trust boundary is light — paths are validated against the
// project / repo dirs before any write.
func HandleProjectHealthCheckApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req healthCheckApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	name := strings.TrimSpace(req.Project)
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "project is required"})
		return
	}
	if len(req.Changes) == 0 {
		writeJSON(w, 400, map[string]string{"error": "changes is empty — nothing to apply"})
		return
	}
	p, err := project.Get(name)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "load config: " + err.Error()})
		return
	}
	result := projectpm.ApplyHealthCheckChanges(p, cfg, req.Changes)

	// Persist the apply outcomes onto the existing job so a page
	// reload can re-render the per-file badges (and tooltip errors).
	// Best-effort: failure to persist is logged but the HTTP response
	// is unaffected — the user has the result in the JSON they're
	// about to receive.
	healthCheckJobsMu.Lock()
	job, ok := healthCheckJobs[name]
	if !ok {
		// Cold cache path: hydrate from disk first so we don't lose
		// the prior Result by writing a job that only carries LastApply.
		if persisted, lerr := loadHealthCheckJob(name); lerr == nil && persisted != nil {
			job = persisted
			healthCheckJobs[name] = job
		} else {
			job = &healthCheckJob{Status: "done"}
			healthCheckJobs[name] = job
		}
	}
	job.LastApply = result
	snapshot := *job
	healthCheckJobsMu.Unlock()
	if perr := saveHealthCheckJob(name, &snapshot); perr != nil {
		fmt.Fprintf(os.Stderr, "[health-check] %s: persist apply failed: %v\n", name, perr)
	}

	writeJSON(w, 200, result)
}
