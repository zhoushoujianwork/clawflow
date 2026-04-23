// GitHub token resolution for worker-context calls (issue #30).
//
// From v0.28 the CLI prefers a SaaS-minted GitHub App installation token
// over a locally-configured PAT:
//
//   1. Ask SaaS:  GET /api/v1/worker/repos/{full_name}/installation-token
//      - 200 → use it, cache until `expires_at - 5m`
//      - 404 → SaaS doesn't manage this repo via an App installation
//              (PAT-backed, or not in the caller's org). Fall back.
//      - 503 → SaaS env missing App config (dev/staging). Fall back.
//      - 502/other → SaaS ↔ GitHub hop failed. Fall back.
//   2. Fall back to creds.GHToken from ~/.clawflow/config/credentials.yaml.
//   3. If both paths fail, return an error so the caller can skip the repo
//      with a clear log rather than silently dropping issues.
//
// The App token is 60-minute lifetime; the 5-minute safety margin avoids
// racing against expiry mid-request. Cache is in-memory only: a worker
// restart re-mints, which is the whole point of having SaaS own the App
// private key.
package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// ghInstallationTokenResp matches the SaaS response for
// /worker/repos/{id}/installation-token (SaaS commit 3c9f74e).
type ghInstallationTokenResp struct {
	Token          string    `json:"token"`
	ExpiresAt      time.Time `json:"expires_at"`
	InstallationID int64     `json:"installation_id"`
}

// ghTokenEntry is one cached row. `source` rides along so we can surface
// App-vs-PAT origin in discover/reconcile logs without a second map.
type ghTokenEntry struct {
	token     string
	expiresAt time.Time
	source    string // "app" | "pat"
}

// ghTokenCache is keyed on full_name (owner/repo) — never on SaaS repo_id
// because the CLI doesn't carry those yet. A repo moving between App and
// PAT ownership would cache-miss naturally on the next TTL expiry.
var ghTokenCache sync.Map // map[string]ghTokenEntry

// ghTokenRemintMargin is how close to `expires_at` we'll still treat a
// cached App token as usable. Matches the SaaS-side policy (re-mint in
// the final 5 minutes of the 60-minute window).
const ghTokenRemintMargin = 5 * time.Minute

// getGitHubToken returns a usable GitHub REST API token for `fullName`,
// transparently choosing App installation token or PAT. The second return
// value is a short source tag ("app" | "pat") for structured logs.
//
// On cache hit: no network I/O, no log line.
// On cache miss: tries SaaS first, falls back to PAT, and prints one
// "[ghtoken] <repo> → <source>" line so operators can see which route
// authenticated the next round of API calls.
func getGitHubToken(wc *config.WorkerConfig, fullName string) (token, source string, err error) {
	if e, ok := cachedGHToken(fullName); ok {
		return e.token, e.source, nil
	}

	// Path 1: SaaS-minted App installation token.
	t, exp, ierr := fetchInstallationToken(wc, fullName)
	if ierr == nil {
		storeGHToken(fullName, ghTokenEntry{token: t, expiresAt: exp, source: "app"})
		fmt.Printf("[ghtoken] %s → App installation token (expires %s)\n",
			fullName, exp.UTC().Format(time.RFC3339))
		return t, "app", nil
	}

	// Path 2: local PAT fallback. Cache with a far-future expiry so we
	// don't hammer /credentials.yaml every call; a user who rotates PATs
	// restarts the worker anyway.
	creds, _ := config.LoadCredentials()
	if creds != nil && creds.GHToken != "" {
		storeGHToken(fullName, ghTokenEntry{
			token:     creds.GHToken,
			expiresAt: time.Now().Add(365 * 24 * time.Hour),
			source:    "pat",
		})
		fmt.Printf("[ghtoken] %s → local PAT (installation-token: %v)\n", fullName, ierr)
		return creds.GHToken, "pat", nil
	}

	return "", "", fmt.Errorf(
		"no GitHub credentials available for %s — installation-token: %v; credentials.yaml has no gh_token",
		fullName, ierr,
	)
}

// invalidateGHToken drops the cached entry for a repo. Not currently
// wired to anything; exposed for future use if we add a "token rejected
// by GitHub" retry path. The TTL-based eviction in cachedGHToken is the
// primary expiry mechanism.
func invalidateGHToken(fullName string) {
	ghTokenCache.Delete(fullName)
}

func cachedGHToken(fullName string) (ghTokenEntry, bool) {
	v, ok := ghTokenCache.Load(fullName)
	if !ok {
		return ghTokenEntry{}, false
	}
	e := v.(ghTokenEntry)
	if time.Until(e.expiresAt) < ghTokenRemintMargin {
		return ghTokenEntry{}, false
	}
	return e, true
}

func storeGHToken(fullName string, e ghTokenEntry) {
	ghTokenCache.Store(fullName, e)
}

// fetchInstallationToken hits the SaaS endpoint and returns the token +
// expiry on 200, or an error describing the non-200 (caller logs it so
// operators can distinguish "no App config" from "transient GitHub hiccup").
func fetchInstallationToken(wc *config.WorkerConfig, fullName string) (string, time.Time, error) {
	// url.PathEscape turns "owner/repo" into "owner%2Frepo" — single path
	// segment, matching the endpoint's `{repo_id}` placeholder (SaaS
	// accepts the full_name slug per issue #30's Recommendation B).
	u := wc.SaasURL + "/api/v1/worker/repos/" + url.PathEscape(fullName) + "/installation-token"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	setWorkerHeaders(req, wc)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", time.Time{}, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var out ghInstallationTokenResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, fmt.Errorf("decode: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, fmt.Errorf("empty token in response")
	}
	return out.Token, out.ExpiresAt, nil
}
