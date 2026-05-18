// Package chat implements the cloud-side chat backend for ClawFlow.
//
// Unlike the local internal/chat package — which runs on the user's
// worker — this package runs inside the cloud server itself. It clones
// the user's repos onto the cloud VM and spawns `claude -p` subprocesses
// against those clones, streaming the output back to the browser via
// Server-Sent Events. Motivation: the user wants to chat with claude
// about their repos from any device, including phones, without needing
// their worker box to be online.
//
// Self-hosted single-user model: one cloud server owns its own
// ANTHROPIC_API_KEY and mints installation tokens via the configured
// GitHub App. No resume, no transcript archive — each browser tab opens
// a session, streams until done, and gets GC'd a few minutes after the
// tab closes.
package chat

import (
	"net/http"
	"os/exec"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

const (
	defaultClonesDir         = "/var/lib/clawflow/clones"
	defaultClaudeBin         = "claude"
	defaultSessionIdle       = 5 * time.Minute
	defaultSubprocessTTL     = 30 * time.Minute
	defaultGCSweepEvery      = 30 * time.Second
	defaultSSEHeartbeatEvery = 25 * time.Second
)

// Config is the cloud-chat handler's dependency bundle. Zero values are
// filled in with defaults (see withDefaults). Secrets are loaded by the
// cloud-serve operator at process startup; this package never reads env
// directly.
type Config struct {
	// ClonesDir is the parent directory under which per-repo clones
	// live, one per "owner/name" pair.
	ClonesDir string

	// ClaudeBin is the absolute or PATH-resolvable name of the claude
	// CLI binary. Default "claude" (resolved via exec.LookPath).
	ClaudeBin string

	// AnthropicAPIKey is passed to subprocesses as ANTHROPIC_API_KEY.
	// Empty disables chat — the handler returns 503 on every request.
	AnthropicAPIKey string

	// GitHubAppPrivateKeyPath points at a PEM-encoded RSA private key
	// for the cloud's GitHub App. Used to mint installation tokens when
	// cloning private repos. Empty means public-repo-only.
	GitHubAppPrivateKeyPath string

	// GitHubAppID is the numeric App ID that pairs with the private
	// key above. Required when the private key is set.
	GitHubAppID int64

	// Store is the cloud config store. Used read-only via
	// GetConnectionByRepo to find the installation_id for a repo.
	Store cloud.Store

	// Now is the clock. Tests inject; production uses time.Now.
	Now func() time.Time

	// HTTPClient is used for outbound GitHub API calls (installation
	// token mint). Defaults to a 15-second-timeout client.
	HTTPClient *http.Client
}

func (c Config) withDefaults() Config {
	if c.ClonesDir == "" {
		c.ClonesDir = defaultClonesDir
	}
	if c.ClaudeBin == "" {
		if resolved, err := exec.LookPath(defaultClaudeBin); err == nil {
			c.ClaudeBin = resolved
		} else {
			c.ClaudeBin = defaultClaudeBin
		}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return c
}
