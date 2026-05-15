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
func NewServer(store *MemoryStore) http.Handler {
	if store == nil {
		store = NewMemoryStore()
	}
	s := &server{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/worker/register", s.handleRegister)
	mux.HandleFunc("/api/worker/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("/api/worker/lease", s.handleLease)
	mux.HandleFunc("/api/worker/runs/", s.handleRun)
	mux.HandleFunc("/api/cloud/dev/jobs", s.handleDevJobs)
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
