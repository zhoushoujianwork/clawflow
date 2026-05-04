package api

import (
	"encoding/json"
	"net/http"
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
// "done"/"error" entries linger so that a poll arriving after
// completion can still see the result instead of a 404. They get
// reaped on the next POST for the same project, so memory growth is
// bounded by the number of distinct project names ever generated.
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
		defer generateJobsMu.Unlock()
		job.EndedAt = time.Now()
		if err != nil {
			job.Status = "error"
			job.Error = err.Error()
		} else {
			job.Status = "done"
			_ = snapshot.WriteProjects()
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
	if !ok {
		writeJSON(w, 404, map[string]string{"status": "idle"})
		return
	}
	writeJSON(w, 200, job)
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
