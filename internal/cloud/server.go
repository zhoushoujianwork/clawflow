package cloud

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// NewServer returns an HTTP handler for the worker protocol. The handler is a
// dev/reference server around MemoryStore; production can keep these routes and
// replace the store implementation.
//
// Worker routes (/api/worker/*, /api/cloud/dev/jobs) are unauthenticated for
// backward compatibility. Cloud config routes (/api/cloud/config, /api/cloud/repos,
// etc.) require a non-empty Authorization: Bearer <token> header.
// TODO(rbac): validate tokens against workspace credentials in a follow-up issue.
func NewServer(store *MemoryStore) http.Handler {
	if store == nil {
		store = NewMemoryStore()
	}
	s := &server{store: store}
	mux := http.NewServeMux()

	// Worker protocol — no auth (backward compatible).
	mux.HandleFunc("/api/worker/register", s.handleRegister)
	mux.HandleFunc("/api/worker/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/api/worker/lease", s.handleLease)
	mux.HandleFunc("/api/worker/runs/", s.handleRun)
	mux.HandleFunc("/api/cloud/dev/jobs", s.handleDevJobs)

	// Cloud config — bearer auth required.
	mux.HandleFunc("/api/cloud/config", s.withAuth(s.handleCloudConfig))
	mux.HandleFunc("/api/cloud/projects", s.withAuth(s.handleCloudProjects))
	mux.HandleFunc("/api/cloud/repos", s.withAuth(s.handleCloudRepos))
	mux.HandleFunc("/api/cloud/repos/", s.withAuth(s.handleCloudRepoByID))
	mux.HandleFunc("/api/cloud/bindings", s.withAuth(s.handleCloudBindings))
	mux.HandleFunc("/api/cloud/bindings/", s.withAuth(s.handleCloudBindingByID))
	mux.HandleFunc("/api/cloud/machines", s.withAuth(s.handleCloudMachines))
	mux.HandleFunc("/api/cloud/jobs", s.withAuth(s.handleCloudJobs))
	mux.HandleFunc("/api/cloud/runs", s.withAuth(s.handleCloudRuns))

	return mux
}

type server struct {
	store *MemoryStore
}

func (s *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req RegisterWorkerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	resp, err := s.store.RegisterWorker(req)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusOK, resp)
}

func (s *server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	resp, err := s.store.Heartbeat(req)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusOK, resp)
}

func (s *server) handleLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req LeaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	job, err := s.store.Lease(req, DefaultLeaseDuration)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusOK, LeaseResponse{Job: job})
}

func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/worker/runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		writeCloudError(w, http.StatusNotFound, "run route not found")
		return
	}
	runID, action := parts[0], parts[1]
	switch action {
	case "events":
		var req RunEventsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeCloudError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := s.store.AppendRunEvents(runID, req.Events); err != nil {
			writeCloudError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "finish":
		var req FinishRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeCloudError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if err := s.store.FinishRun(runID, req); err != nil {
			writeCloudError(w, http.StatusNotFound, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeCloudError(w, http.StatusNotFound, "run route not found")
	}
}

func (s *server) handleDevJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Job            JobSpec `json:"job"`
		BoundMachineID string  `json:"bound_machine_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Job.Target == "" {
		req.Job.Target = "issue"
	}
	if req.Job.State == "" {
		req.Job.State = "open"
	}
	rec, err := s.store.EnqueueJob(req.Job, req.BoundMachineID)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusCreated, rec)
}

func writeCloudJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeCloudError(w http.ResponseWriter, status int, msg string) {
	writeCloudJSON(w, status, map[string]string{
		"error": msg,
		"time":  time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- Cloud config auth middleware ----

// withAuth wraps a handler to require a non-empty Authorization: Bearer <token>
// header. It accepts any non-empty token for now.
// TODO(rbac): validate token against registered workers or user credentials.
func (s *server) withAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) || strings.TrimSpace(strings.TrimPrefix(auth, prefix)) == "" {
			writeCloudError(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		h(w, r)
	}
}

// ---- Cloud config handlers ----

// handleCloudConfig returns an aggregated summary of all cloud config resources.
// GET /api/cloud/config
func (s *server) handleCloudConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeCloudJSON(w, http.StatusOK, s.store.Summary())
}

// handleCloudProjects handles project creation.
// POST /api/cloud/projects
func (s *server) handleCloudProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	p, err := s.store.CreateProject(req)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusCreated, p)
}

// handleCloudRepos handles repo creation.
// POST /api/cloud/repos
func (s *server) handleCloudRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CreateRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	repo, err := s.store.CreateRepo(req)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusCreated, repo)
}

// handleCloudRepoByID handles partial updates to a repo.
// PATCH /api/cloud/repos/{id}
func (s *server) handleCloudRepoByID(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/cloud/repos/"), "/")
	if id == "" {
		writeCloudError(w, http.StatusNotFound, "repo id required")
		return
	}
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req UpdateRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	repo, err := s.store.UpdateRepo(id, req)
	if err != nil {
		writeCloudError(w, http.StatusNotFound, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusOK, repo)
}

// handleCloudBindings handles binding creation.
// POST /api/cloud/bindings
func (s *server) handleCloudBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req CreateBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	b, err := s.store.CreateBinding(req)
	if err != nil {
		writeCloudError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusCreated, b)
}

// handleCloudBindingByID handles partial updates to a binding.
// PATCH /api/cloud/bindings/{id}
func (s *server) handleCloudBindingByID(w http.ResponseWriter, r *http.Request) {
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/cloud/bindings/"), "/")
	if id == "" {
		writeCloudError(w, http.StatusNotFound, "binding id required")
		return
	}
	if r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req UpdateBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeCloudError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	b, err := s.store.UpdateBinding(id, req)
	if err != nil {
		writeCloudError(w, http.StatusNotFound, err.Error())
		return
	}
	writeCloudJSON(w, http.StatusOK, b)
}

// handleCloudMachines returns all registered machines.
// GET /api/cloud/machines
func (s *server) handleCloudMachines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeCloudJSON(w, http.StatusOK, ListMachinesResponse{Machines: s.store.ListMachines()})
}

// handleCloudJobs returns all job records.
// GET /api/cloud/jobs
func (s *server) handleCloudJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeCloudJSON(w, http.StatusOK, ListJobsResponse{Jobs: s.store.ListJobs()})
}

// handleCloudRuns returns all run records.
// GET /api/cloud/runs
func (s *server) handleCloudRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeCloudJSON(w, http.StatusOK, ListRunsResponse{Runs: s.store.ListRuns()})
}
