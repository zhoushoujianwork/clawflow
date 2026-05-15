package api

import (
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// cloudProxy is a reusable reverse-proxy that forwards a request to the
// configured ClawFlow cloud server and streams the response back to the
// browser. It adds the stored access token so the browser never sees it.
// If the cloud is not configured (no URL or no token), it responds 503.
func cloudProxy(w http.ResponseWriter, r *http.Request, upstreamPath string) {
	cfg, err := cloud.LoadConfig()
	if err != nil || cfg.BaseURL == "" || cfg.AccessToken == "" {
		http.Error(w, `{"error":"cloud not configured"}`, http.StatusServiceUnavailable)
		return
	}

	targetURL := strings.TrimRight(cfg.BaseURL, "/") + upstreamPath
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	var bodyReader io.Reader
	if r.ContentLength != 0 && r.Body != nil {
		bodyReader = r.Body
		defer r.Body.Close()
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bodyReader)
	if err != nil {
		http.Error(w, `{"error":"proxy error"}`, http.StatusInternalServerError)
		return
	}
	// Forward content-type for POST/PATCH
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, `{"error":"cloud unreachable"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers (Content-Type in particular)
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// HandleCloudStatus returns whether the cloud is configured so the UI can
// conditionally show or hide the Cloud nav entry.
func HandleCloudStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := cloud.LoadConfig()
	configured := err == nil && cfg.AccessToken != ""
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": configured,
		"url":        cfg.BaseURL,
	})
}

// HandleCloudMachines proxies GET /api/cloud/machines to the cloud server.
func HandleCloudMachines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cloudProxy(w, r, "/api/cloud/machines")
}

// HandleCloudBindings proxies GET and POST /api/cloud/bindings.
func HandleCloudBindings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodPost:
		cloudProxy(w, r, "/api/cloud/bindings")
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleCloudBindingByID proxies PATCH /api/cloud/bindings/{id}.
func HandleCloudBindingByID(w http.ResponseWriter, r *http.Request) {
	// Extract the binding ID from the URL path
	// Path is /api/cloud/bindings/{id}
	path := r.URL.Path
	id := strings.TrimPrefix(path, "/api/cloud/bindings/")
	if id == "" {
		http.Error(w, "missing binding id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodGet:
		cloudProxy(w, r, "/api/cloud/bindings/"+id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleCloudJobs proxies GET /api/cloud/jobs to the cloud server.
func HandleCloudJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cloudProxy(w, r, "/api/cloud/jobs")
}

// HandleCloudRuns proxies GET /api/cloud/runs to the cloud server.
func HandleCloudRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cloudProxy(w, r, "/api/cloud/runs")
}

// HandleCloudConfig proxies GET /api/cloud/config to the cloud server.
func HandleCloudConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cloudProxy(w, r, "/api/cloud/config")
}
