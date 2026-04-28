package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
)

var (
	runMu      sync.Mutex
	runActive  bool
)

type runRequest struct {
	Repo  string `json:"repo,omitempty"`
	Issue int    `json:"issue,omitempty"`
}

type runResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// HandleRun handles POST /api/run — triggers `clawflow run` in background.
func HandleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	runMu.Lock()
	if runActive {
		runMu.Unlock()
		writeJSON(w, 409, runResponse{Status: "busy", Message: "a run is already in progress"})
		return
	}
	runActive = true
	runMu.Unlock()

	var req runRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}

	args := []string{"run"}
	if req.Repo != "" {
		args = append(args, "--repo", req.Repo)
	}
	if req.Issue > 0 {
		args = append(args, "--issue", fmt.Sprintf("%d", req.Issue))
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

	writeJSON(w, 200, runResponse{Status: "started"})
}

// HandleRunStatus handles GET /api/run/status
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
	writeJSON(w, 200, runResponse{Status: status})
}
