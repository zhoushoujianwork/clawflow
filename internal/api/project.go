package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/projectgen"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// generateJob tracks the state of one in-flight or recently-finished
// project context.md generation. The map is keyed by project name and
// guarded by a mutex.
//
// Async lifecycle: POST /api/project/generate-context inserts a job
// with status="running" and spawns a goroutine that calls
// projectgen.Generate. The goroutine flips status to "done" or
// "error" on completion. Browsers poll
// GET /api/project/generate-context/status?project=X — both for
// initial confirmation and on detail-page mount, so navigating away
// mid-run and coming back picks up the spinner where it left off.
//
// "done"/"error" entries linger in memory, AND get persisted to disk
// so a `clawflow web` restart doesn't make a recent error message
// disappear (the success result is already durable as context.md
// itself; persistence here is mostly to preserve the failure path).
// "running" is intentionally NOT persisted — if the process dies
// mid-run the goroutine is gone, and resuming a stale "running" on
// next start would just leave the dashboard spinning forever.
type generateJob struct {
	Status    string    `json:"status"` // "running" | "done" | "error"
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
}

var (
	generateJobsMu sync.Mutex
	generateJobs   = map[string]*generateJob{}
)

// generateJobStorePath is the on-disk JSON for the most recent
// completed generate-context run. Sits next to context.md /
// testing.md / health-check.json under the project directory so all
// per-project state cleans up together when the project is deleted.
func generateJobStorePath(projectName string) string {
	return filepath.Join(project.ProjectDir(projectName), "generate-context.json")
}

// saveGenerateJob writes the job to disk atomically (tmp + rename).
// Best-effort: caller logs and continues on failure since the
// in-memory map remains the source of truth during the live session.
func saveGenerateJob(projectName string, job *generateJob) error {
	path := generateJobStorePath(projectName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
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

// loadGenerateJob hydrates the latest persisted job from disk.
// Returns (nil, nil) when the file doesn't exist so callers can
// distinguish "never generated" from a real read error.
func loadGenerateJob(projectName string) (*generateJob, error) {
	path := generateJobStorePath(projectName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var job generateJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &job, nil
}

type projectCreateRequest struct {
	Name string `json:"name"`
}

type projectRepoRequest struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
}

func HandleProjectCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}
	p, err := project.Create(req.Name)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	_ = snapshot.WriteProjects()
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"project": map[string]any{
			"name":       p.Name,
			"repos":      p.Repos,
			"created_at": p.CreatedAt,
		},
	})
}

func HandleProjectDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}
	if err := project.Delete(req.Name); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	_ = snapshot.WriteProjects()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func HandleProjectAddRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(req.Project) == "" || strings.TrimSpace(req.Repo) == "" {
		writeJSON(w, 400, map[string]string{"error": "project and repo are required"})
		return
	}
	if err := project.AddRepo(req.Project, req.Repo); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	_ = snapshot.WriteProjects()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

type projectGenerateRequest struct {
	Project      string `json:"project"`
	Model        string `json:"model,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

// HandleProjectGenerateContext kicks off context.md generation for a
// project and returns immediately. The actual `claude -p` call (30s–
// 2min typical) runs in a background goroutine; clients learn about
// completion via HandleProjectGenerateContextStatus polling. Tabs
// closing or navigating away therefore do not abort the work.
//
// Returns 202 with the job entry on accept, 409 if a job for that
// project is already running.
func HandleProjectGenerateContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	name := strings.TrimSpace(req.Project)
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "project is required"})
		return
	}

	generateJobsMu.Lock()
	if existing, ok := generateJobs[name]; ok && existing.Status == "running" {
		generateJobsMu.Unlock()
		writeJSON(w, 409, map[string]any{
			"error": "generation already running for this project",
			"job":   existing,
		})
		return
	}
	job := &generateJob{Status: "running", StartedAt: time.Now()}
	generateJobs[name] = job
	generateJobsMu.Unlock()

	model := req.Model
	instructions := req.Instructions
	go func() {
		_, err := projectgen.Generate(name, model, instructions)
		generateJobsMu.Lock()
		job.EndedAt = time.Now()
		if err != nil {
			job.Status = "error"
			job.Error = err.Error()
		} else {
			job.Status = "done"
			_ = snapshot.WriteProjects()
		}
		// Snapshot under the lock so the disk write below can't race
		// with a concurrent reader. Persistence here is best-effort —
		// the dashboard reads from the in-memory map first, so a save
		// failure only costs us cross-restart visibility into errors.
		snapshotJob := *job
		generateJobsMu.Unlock()
		if err := saveGenerateJob(name, &snapshotJob); err != nil {
			fmt.Fprintf(os.Stderr, "[generate-context] %s: persist failed: %v\n", name, err)
		}
	}()

	writeJSON(w, 202, map[string]any{
		"status": "accepted",
		"job":    job,
	})
}

// HandleProjectGenerateContextStatus reports the current job state
// for `?project=X`. Returns 404 if no job has ever been recorded
// (so a fresh page load can distinguish "never generated" from
// "running") — the frontend treats 404 as "idle".
func HandleProjectGenerateContextStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("project"))
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "project is required"})
		return
	}
	generateJobsMu.Lock()
	job, ok := generateJobs[name]
	generateJobsMu.Unlock()
	if ok {
		writeJSON(w, 200, job)
		return
	}
	// Cold cache (e.g. after `clawflow web` restart): hydrate from
	// the persisted snapshot so a recent error or completion is
	// still visible. We populate the in-memory map under lock to
	// avoid every poll re-reading the file.
	persisted, err := loadGenerateJob(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[generate-context] %s: load persisted failed: %v\n", name, err)
		writeJSON(w, 404, map[string]string{"status": "idle"})
		return
	}
	if persisted == nil {
		writeJSON(w, 404, map[string]string{"status": "idle"})
		return
	}
	generateJobsMu.Lock()
	if _, raced := generateJobs[name]; !raced {
		generateJobs[name] = persisted
	}
	generateJobsMu.Unlock()
	writeJSON(w, 200, persisted)
}

// HandleProjectGet returns the LIVE view of a single project by
// reading project.yaml + context.md + testing.md fresh from disk.
//
// This exists alongside the static /data/projects.json snapshot
// because the snapshot is only refreshed on `clawflow run` and on
// HTTP mutations — out-of-band file edits (the most common one is
// `clawflow project chat` writing back an updated context.md or
// testing.md) don't touch the snapshot, so the dashboard would
// otherwise show stale content until the next run.
//
// The dashboard's project detail page polls this endpoint instead
// of /data/projects.json so a browser refresh always reflects the
// latest on-disk state, regardless of how the file was edited.
// The project list page can keep using the snapshot — listing many
// projects is read-heavier and freshness matters less there.
func HandleProjectGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}
	p, err := project.Get(name)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	ctx, _ := project.ReadContext(name)
	testing, _ := project.ReadTesting(name)
	view := snapshot.ProjectView{
		Name:    p.Name,
		Repos:   p.Repos,
		Context: ctx,
		Testing: testing,
		Automation: snapshot.ProjectAutomationView{
			Enabled:         p.Automation.Enabled,
			CooldownMinutes: p.Automation.CooldownMinutes,
			LastWokenAt:     p.Automation.LastWokenAt,
		},
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
	writeJSON(w, 200, view)
}

type projectAutomationRequest struct {
	Project         string `json:"project"`
	Enabled         bool   `json:"enabled"`
	CooldownMinutes int    `json:"cooldown_minutes"`
}

// HandleProjectAutomation flips the per-project PM auto-wakeup
// toggle. The dashboard's project detail page sends one of these
// every time the user moves the switch or edits the cooldown.
//
// Implementation note: the request always carries both `enabled` and
// `cooldown_minutes` (the UI sends the full current state), so we
// pass cooldown straight through. The CLI's "disable" path uses
// SetAutomation(name, false, -1) to keep the prior cooldown — that's
// for terminal use where the user shouldn't have to retype the value.
// The dashboard never has that ambiguity since the form holds the
// canonical value.
func HandleProjectAutomation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectAutomationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	name := strings.TrimSpace(req.Project)
	if name == "" {
		writeJSON(w, 400, map[string]string{"error": "project is required"})
		return
	}
	if req.CooldownMinutes < 0 {
		writeJSON(w, 400, map[string]string{"error": "cooldown_minutes must be >= 0"})
		return
	}
	if err := project.SetAutomation(name, req.Enabled, req.CooldownMinutes); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	_ = snapshot.WriteProjects()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func HandleProjectRemoveRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req projectRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(req.Project) == "" || strings.TrimSpace(req.Repo) == "" {
		writeJSON(w, 400, map[string]string{"error": "project and repo are required"})
		return
	}
	if err := project.RemoveRepo(req.Project, req.Repo); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	_ = snapshot.WriteProjects()
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
