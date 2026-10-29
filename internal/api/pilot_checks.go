package api

import (
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"net/http"
)

func HandleScheduledChecks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, err := pilot.OpenCheckStore(r.Context())
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "scheduled checks store unavailable"})
		return
	}
	defer s.Close()
	jobs, err := s.ScheduledChecks(r.Context())
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "scheduled checks unavailable"})
		return
	}
	runs, err := s.ScheduledCheckRuns(r.Context(), r.URL.Query().Get("id"))
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "scheduled results unavailable"})
		return
	}
	writeJSON(w, 200, map[string]any{"checks": jobs, "runs": runs})
}
