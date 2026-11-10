package api

import (
	"context"
	"net/http"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/decisions"
)

const decisionsTimeout = 12 * time.Second

// DecisionsSource builds the report from local evidence. It is bound by
// `clawflow web`; an unbound server cannot answer.
var DecisionsSource func(ctx context.Context) decisions.Report

// HandleDecisions serves GET /api/decisions: what only a person can decide.
// It is read-only and makes no provider request.
func HandleDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if DecisionsSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "decisions source is not available"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), decisionsTimeout)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	report := DecisionsSource(ctx)
	if report.Decisions == nil {
		report.Decisions = []decisions.Decision{}
	}
	writeJSON(w, http.StatusOK, report)
}
