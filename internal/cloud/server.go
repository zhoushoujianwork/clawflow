package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// AuthHandler is the contract NewServerWithAuth expects: a registrar that can
// mount its own routes on a parent mux and middleware factories that gate
// arbitrary handlers behind user / machine credentials. internal/cloud/auth.Handler
// implements this — cloud avoids importing the auth package directly to keep
// the dependency arrow pointing the right way.
//
// UserFromContext returns the authenticated user that the surrounding
// RequireUser/RequireMachine middleware injected, or nil if no auth ran.
// Cloud handlers use it to attribute writes (e.g. worker registration → owner).
type AuthHandler interface {
	RegisterRoutes(mux *http.ServeMux)
	RequireUser(http.Handler) http.Handler
	RequireMachine(http.Handler) http.Handler
	UserFromContext(context.Context) *User
}

// NewServer returns an HTTP handler for the worker protocol with NO real
// auth. Cloud config routes accept any non-empty Bearer token; worker routes
// are open. Suitable for tests and `cloud serve --no-auth` development.
// Production callers should use NewServerWithAuth.
//
// operators is an optional operator registry used by the webhook handler to
// determine which operators a given GitHub event should trigger. Pass nil to
// create a server with no operators registered.
func NewServer(store Store, operators *operator.Registry) http.Handler {
	return NewServerWithAuth(store, operators, nil)
}

// NewServerWithAuth wires the cloud server with a real auth.Handler. When
// auth is non-nil:
//   - All cloud-config routes call auth.RequireUser.
//   - Worker protocol routes call auth.RequireMachine.
//   - Auth's own routes (login / callback / device / me / logout) are mounted
//     on the same mux.
//
// When auth is nil the server falls back to the legacy "any non-empty bearer"
// check for cloud-config and leaves worker routes open. Tests rely on that.
func NewServerWithAuth(store Store, operators *operator.Registry, auth AuthHandler) http.Handler {
	if store == nil {
		store = NewMemoryStore()
	}
	if operators == nil {
		operators = operator.NewRegistry()
	}
	s := &server{store: store, operators: operators, auth: auth}
	mux := http.NewServeMux()

	// Worker protocol:
	//   - /register: caller is a logged-in user (personal token); cloud
	//     mints a machine token and returns it. RequireUser when configured.
	//   - heartbeat/lease/runs: caller is the registered worker
	//     (machine token). RequireMachine when configured.
	gateUser := func(h http.HandlerFunc) http.HandlerFunc {
		if auth == nil {
			return h
		}
		return auth.RequireUser(h).ServeHTTP
	}
	gateMachine := func(h http.HandlerFunc) http.HandlerFunc {
		if auth == nil {
			return h
		}
		return auth.RequireMachine(h).ServeHTTP
	}
	mux.HandleFunc("/api/worker/register", gateUser(s.handleRegister))
	mux.HandleFunc("/api/worker/heartbeat", gateMachine(s.handleHeartbeat))
	mux.HandleFunc("/api/worker/lease", gateMachine(s.handleLease))
	mux.HandleFunc("/api/worker/runs/", gateMachine(s.handleRun))
	mux.HandleFunc("/api/cloud/dev/jobs", s.handleDevJobs)

	// Cloud config — auth.RequireUser when configured, fallback to legacy
	// bearer check otherwise. The s.withAuth wrapper handles both paths.
	mux.HandleFunc("/api/cloud/config", s.withAuth(s.handleCloudConfig))
	mux.HandleFunc("/api/cloud/projects", s.withAuth(s.handleCloudProjects))
	mux.HandleFunc("/api/cloud/repos", s.withAuth(s.handleCloudRepos))
	mux.HandleFunc("/api/cloud/repos/", s.withAuth(s.handleCloudRepoByID))
	mux.HandleFunc("/api/cloud/bindings", s.withAuth(s.handleCloudBindings))
	mux.HandleFunc("/api/cloud/bindings/", s.withAuth(s.handleCloudBindingByID))
	mux.HandleFunc("/api/cloud/machines", s.withAuth(s.handleCloudMachines))
	mux.HandleFunc("/api/cloud/jobs", s.withAuth(s.handleCloudJobs))
	mux.HandleFunc("/api/cloud/runs", s.withAuth(s.handleCloudRuns))

	// Webhook route matches the GitHub App's configured Webhook URL exactly.
	mux.HandleFunc("/api/v1/github/app/webhook", s.handleWebhookGitHub)

	// Mount auth routes (login / callback / device flow / me / logout) when
	// an auth handler is present.
	if auth != nil {
		auth.RegisterRoutes(mux)
	}
	return mux
}

type server struct {
	store     Store
	operators *operator.Registry
	auth      AuthHandler
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
	// When real auth is wired, mint a kind=machine API token so subsequent
	// worker calls (heartbeat / lease / runs) can authenticate. The token
	// plaintext is the same opaque string that RegisterWorker returned —
	// we hash it into api_tokens, the worker keeps the plaintext locally
	// and never sees a second secret.
	if s.auth != nil {
		user := s.auth.UserFromContext(r.Context())
		if user == nil {
			writeCloudError(w, http.StatusInternalServerError, "auth user missing after RequireUser")
			return
		}
		if _, err := s.store.CreateAPIToken(CreateAPITokenRequest{
			UserID:    user.ID,
			Kind:      APITokenKindMachine,
			Plaintext: resp.WorkerToken,
			MachineID: resp.MachineID,
			Label:     "worker:" + req.Hostname,
		}); err != nil {
			writeCloudError(w, http.StatusInternalServerError, "mint machine token: "+err.Error())
			return
		}
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

// withAuth gates a handler behind the cloud server's auth model. When the
// server was constructed with a real AuthHandler, requests must carry either
// a session cookie or a Bearer API token that resolves to a user (the
// auth.Handler.RequireUser path). Otherwise — tests and `cloud serve` in
// no-auth mode — the request just needs any non-empty Bearer token.
func (s *server) withAuth(h http.HandlerFunc) http.HandlerFunc {
	if s.auth != nil {
		return s.auth.RequireUser(h).ServeHTTP
	}
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, prefix) || strings.TrimSpace(strings.TrimPrefix(authz, prefix)) == "" {
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
