// Periodic "is this repo still reachable" probe. The SaaS can't run this for
// private-network GitLab instances (Aliyun → corp net is blocked), so the
// worker does the probe and pushes the pass/fail into
// POST /api/v1/worker/health-report. The repo list UI renders the result as
// a badge; a red "Error" tells the user the worker's stored PAT is stale or
// the instance is unreachable, without them needing to remember to click
// "Test connection" by hand.
//
// Only runs for self-hosted GitLab. Public GitHub / gitlab.com are checked
// server-side already (SaaS can reach them directly).
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// healthCheckInterval: 5 minutes. Less frequent than discover (90s) because
// a stale status badge is low-stakes and the probe is one API call per repo.
const healthCheckInterval = 5 * time.Minute

func healthCheckLoop(wc *config.WorkerConfig, stopCh <-chan struct{}) {
	healthCheckLoopWithWS(wc, nil, stopCh)
}

func healthCheckLoopWithWS(wc *config.WorkerConfig, ws *wsChannel, stopCh <-chan struct{}) {
	t := time.NewTicker(healthCheckInterval)
	defer t.Stop()
	initial := time.After(20 * time.Second)
	for {
		select {
		case <-stopCh:
			return
		case <-initial:
			runHealthCheckPass(wc, ws)
			initial = nil
		case <-t.C:
			runHealthCheckPass(wc, ws)
		}
	}
}

func runHealthCheckPass(wc *config.WorkerConfig, ws *wsChannel) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	creds, _ := config.LoadCredentials()
	if creds.GitLabToken == "" {
		return // no token → nothing to probe
	}

	client := &http.Client{Timeout: 15 * time.Second}
	for fullName, repo := range cfg.EnabledRepos() {
		if repo.Platform != "gitlab" || repo.BaseURL == "" {
			continue
		}
		ok, msg := probeGitLabRepo(client, repo.BaseURL, creds.GitLabToken, fullName)
		if err := pushHealthReport(wc, ws, "gitlab", fullName, ok, msg); err != nil {
			fmt.Printf("[hc] push for %s failed: %v\n", fullName, err)
		}
	}
}

// probeGitLabRepo runs the same GET /projects/:encoded endpoint the manual
// "Test Connection" button uses. Returns `(ok, message)` matching what SaaS
// renders in the badge.
func probeGitLabRepo(client *http.Client, baseURL, token, fullName string) (bool, string) {
	encoded := url.PathEscape(fullName)
	target := strings.TrimRight(baseURL, "/") + "/api/v4/projects/" + encoded
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Sprintf("unreachable: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return true, "reachable"
	case 401:
		return false, "token rejected by GitLab (401)"
	case 404:
		return false, "repo not found or no access (404)"
	default:
		return false, fmt.Sprintf("gitlab returned %d", resp.StatusCode)
	}
}

func pushHealthReport(wc *config.WorkerConfig, ws *wsChannel, platform, fullName string, ok bool, message string) error {
	if ws != nil && ws.SendHealthReport(platform, fullName, ok, message) {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"platform":  platform,
		"full_name": fullName,
		"ok":        ok,
		"message":   message,
	})
	req, _ := http.NewRequest(http.MethodPost, wc.SaasURL+"/api/v1/worker/health-report", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setWorkerHeaders(req, wc)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("saas returned %d", resp.StatusCode)
	}
	return nil
}
