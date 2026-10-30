package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

func historyFilter(r *http.Request) (store.RunFilter, error) {
	q := r.URL.Query()
	f := store.RunFilter{Repo: q.Get("repo"), Project: q.Get("project")}
	for _, field := range []struct {
		name string
		dest **time.Time
	}{{"from", &f.From}, {"until", &f.Until}} {
		if value := q.Get(field.name); value != "" {
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				t, err = time.Parse("2006-01-02", value)
			}
			if err != nil {
				return f, fmt.Errorf("invalid %s", field.name)
			}
			*field.dest = &t
		}
	}
	if f.From != nil && f.Until != nil && !f.From.Before(*f.Until) {
		return f, fmt.Errorf("from must be before until")
	}
	if value := q.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 10000 {
			return f, fmt.Errorf("limit must be 1..10000")
		}
		f.Limit = n
	}
	return f, nil
}

func HandleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	f, err := historyFilter(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if path := r.URL.Query().Get("path"); path != "" {
		f.RunDir = filepath.Join(snapshot.DataDir(), filepath.FromSlash(path))
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "run"
	}
	if kind != "run" && kind != "pilot" {
		http.Error(w, "invalid kind", 400)
		return
	}

	q := r.URL.Query()
	f.ExecutionStatus, f.Role, f.Profile = q.Get("execution_status"), q.Get("role"), q.Get("profile")
	f.AttemptID, f.Cursor = q.Get("attempt_id"), q.Get("cursor")
	if err := store.ValidateHistoryKeys(kind, f.AttemptID, f.Cursor); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if q.Has("cursor") && f.Cursor == "" {
		http.Error(w, "invalid cursor", 400)
		return
	}
	if q.Has("attempt_id") && f.AttemptID == "" {
		http.Error(w, "invalid attempt_id", 400)
		return
	}
	for _, name := range []string{"execution_status", "role", "profile"} {
		if q.Has(name) && (strings.TrimSpace(q.Get(name)) == "" || len(q.Get(name)) > 256) {
			http.Error(w, "invalid "+name, 400)
			return
		}
	}
	if f.Cursor != "" && f.Limit == 0 {
		f.Limit = 100
	}
	if f.Cursor != "" && (f.RunDir != "" || f.AttemptID != "") {
		http.Error(w, "cursor cannot be combined with lookup", 400)
		return
	}
	limit := f.Limit
	// One-row lookahead, no COUNT or raw-file reads.
	if limit > 0 {
		f.Limit++
	}
	s := snapshot.RunStore()
	if s == nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	rows, err := s.Runs(r.Context(), kind, f)
	if err != nil {
		http.Error(w, "read runs failed", 500)
		return
	}

	if limit > 0 && len(rows) > limit {
		w.Header().Set("X-Next-Cursor", store.RunsCursor(rows[limit-1], kind))
		rows = rows[:limit]
	}
	var payload []map[string]any
	if f.RunDir != "" || f.AttemptID != "" {
		payload, err = store.RunPayloads(rows, snapshot.DataDir())
		if err == nil {
			var metadata []map[string]any
			metadata, err = store.RunListPayloads(rows, snapshot.DataDir(), kind)
			for i := range metadata {
				for _, key := range []string{"attempt_id", "attribution", "availability"} {
					payload[i][key] = metadata[i][key]
				}
			}
		}
	} else {
		payload, err = store.RunListPayloads(rows, snapshot.DataDir(), kind)
	}
	if err != nil {
		http.Error(w, "decode runs failed", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, payload)
}

func HandleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	s := snapshot.RunStore()
	if s == nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	f, err := historyFilter(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	day := 1
	if cfg, err := config.Load(); err == nil {
		day = cfg.Settings.BillingCycleDay
	}
	if period := r.URL.Query().Get("period"); period != "" && period != "all" {
		t := time.Now().UTC()
		if period != "current" {
			var err error
			t, err = time.Parse("2006-01-02", period)
			if err != nil {
				http.Error(w, "invalid period", 400)
				return
			}
		}
		start, end := store.BillingPeriod(t, day)
		f.From = &start
		f.Until = &end
	}
	f.Role, f.Profile = r.URL.Query().Get("role"), r.URL.Query().Get("profile")
	sum, err := s.Usage(r.Context(), f, day, store.UsageSelection{Machine: r.URL.Query().Get("machine"), Model: r.URL.Query().Get("model")})
	if err != nil {
		http.Error(w, "read usage failed", 500)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, sum)
}
