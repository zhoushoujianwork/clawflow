package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// pilotMu guards pilotActive. Pilot wakes are heavy claude calls
// (minutes) so we never want two for the same project running
// concurrently. The map is keyed by project name; absence means idle.
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

// HandlePilotWake handles POST /api/project/pilot/wake. Fires a single
// on-demand Pilot wake for the named project, bypassing cooldown but
// still respecting bound_machine + require_binding.
//
// Why the binding checks live in this handler (and again in the CLI
// subprocess via pilot.WakeOne): a click-then-see-error round trip is
// much better UX than a click-then-silent-no-op. The CLI re-checks
// because it can also be invoked directly from a terminal, where this
// handler is not in the path.
//
// Spawn pattern mirrors TriggerRun: the wake itself runs in a
// `clawflow pilot wake` subprocess so the web process stays light and
// claude's lifetime is decoupled from any single HTTP connection. The
// per-project pilotActive gate prevents wake-spamming the same
// project; a different project is unaffected (manual wakes for
// different projects can run in parallel — pilot.WakeOne uses its own
// per-wake working directories).
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
	name := strings.TrimSpace(req.Project)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project is required"})
		return
	}

	// Pre-flight: project exists + automation enabled + binding ok.
	// Fails fast with a useful error rather than spawning a subprocess
	// just to print it on stderr.
	p, err := project.Get(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if !p.Automation.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "automation is disabled for this project — enable it first"})
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}
	hostname, _ := os.Hostname()
	if p.Automation.BoundMachine != "" && hostname != "" && p.Automation.BoundMachine != hostname {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "project is bound to " + p.Automation.BoundMachine + ", current machine is " + hostname + " — wake from there or rebind",
		})
		return
	}
	if cfg.Settings.RequireBinding && p.Automation.BoundMachine == "" && hostname != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no bound_machine and require_binding=true — bind the project first",
		})
		return
	}

	pilotMu.Lock()
	if pilotActive[name] {
		pilotMu.Unlock()
		writeJSON(w, http.StatusConflict, pilotWakeResponse{Status: "busy", Message: "a wake is already in progress for this project"})
		return
	}
	pilotActive[name] = true
	pilotMu.Unlock()

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	cmd := exec.Command(self, "pilot", "wake", name)
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	go func() {
		defer func() {
			pilotMu.Lock()
			delete(pilotActive, name)
			pilotMu.Unlock()
			// Refresh projects.json once the subprocess exits so the
			// dashboard's project list view picks up the new
			// LastWokenAt without waiting for an unrelated mutation.
			// WakeOne already does this from inside the subprocess,
			// but a belt-and-braces write here covers the case where
			// the subprocess crashed before its defer ran.
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
	return pilotActive[name]
}
