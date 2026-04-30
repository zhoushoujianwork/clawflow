package api

import (
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// VersionInfo is set by the web command at startup.
var VersionInfo struct {
	Current string
	Fetch   func() string              // FetchLatestTag
	IsNewer func(cur, lat string) bool // IsNewerVersion
	// Respawn, when non-nil, is invoked after a successful `/api/update`
	// to restart `clawflow web` — without this, the running process keeps
	// reporting the pre-update version and the dashboard banner is sticky.
	// The web command wires this up; tests and callers that don't need
	// auto-restart can leave it nil.
	Respawn func()
}

type versionResponse struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
}

// HandleVersion handles GET /api/version
func HandleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	latest := ""
	if VersionInfo.Fetch != nil {
		latest = VersionInfo.Fetch()
	}
	avail := false
	if latest != "" && VersionInfo.IsNewer != nil {
		avail = VersionInfo.IsNewer(VersionInfo.Current, latest)
	}
	writeJSON(w, 200, versionResponse{
		Current:         VersionInfo.Current,
		Latest:          latest,
		UpdateAvailable: avail,
	})
}

var (
	updateMu     sync.Mutex
	updateActive bool
)

// HandleUpdate handles POST /api/update — triggers `clawflow update`.
func HandleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	updateMu.Lock()
	if updateActive {
		updateMu.Unlock()
		writeJSON(w, 409, map[string]string{"status": "busy", "message": "update already in progress"})
		return
	}
	updateActive = true
	updateMu.Unlock()

	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}

	cmd := exec.Command(self, "update")
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()

	updateMu.Lock()
	updateActive = false
	updateMu.Unlock()

	if err != nil {
		writeJSON(w, 500, map[string]string{
			"status":  "failed",
			"message": string(output),
		})
		return
	}
	respawning := VersionInfo.Respawn != nil
	writeJSON(w, 200, map[string]any{
		"status":     "ok",
		"message":    string(output),
		"respawning": respawning,
	})
	// Defer the actual restart by a short beat so the response above
	// has a chance to flush before we tear down the listener. The
	// frontend uses `respawning: true` to switch into a poll-and-reload
	// state.
	if respawning {
		go func() {
			time.Sleep(150 * time.Millisecond)
			VersionInfo.Respawn()
		}()
	}
}
