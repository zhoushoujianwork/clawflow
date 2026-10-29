package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// pilotMu guards the single global legacy manual invocation.
var (
	pilotMu     sync.Mutex
	pilotActive = make(map[string]bool)
)

type pilotWakeRequest struct {
	Project string `json:"project"`
}

type pilotWakeResponse struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// HandlePilotWake preserves the old manual endpoint as one global invocation.
// The project argument is ignored; new conversations use /api/chat/spawn.
func HandlePilotWake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req pilotWakeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	// Legacy endpoint invokes the same global steward, ignoring project settings.
	name := "platform"

	pilotMu.Lock()
	if pilotActive[name] {
		pilotMu.Unlock()
		writeJSON(w, http.StatusConflict, pilotWakeResponse{Status: "busy", Message: "a steward invocation is already in progress"})
		return
	}
	pilotActive[name] = true
	pilotMu.Unlock()

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, "pilot", "ask", "review platform state")
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	go func() {
		defer func() {
			pilotMu.Lock()
			delete(pilotActive, name)
			pilotMu.Unlock()

			_ = snapshot.WriteProjects()
		}()
		_ = cmd.Run()
	}()

	writeJSON(w, http.StatusOK, pilotWakeResponse{Status: "started"})
}

// PilotWakeActive reports whether a manual wake is currently in flight
// for the given project. Exposed so the run-status handler (or a
// dedicated pilot-status endpoint) can surface it to the dashboard.
func PilotWakeActive(name string) bool {
	pilotMu.Lock()
	defer pilotMu.Unlock()
	return pilotActive["platform"]
}
