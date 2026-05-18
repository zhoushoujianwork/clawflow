// Package chat implements the worker-side half of the cloud chat
// protocol described in internal/cloud/types.go.
//
// The worker long-polls the cloud for chat assignments, ensures the
// target repo is cloned locally (using the user's gh_token /
// gitlab_token), spawns `claude -p <message>` in the clone dir with
// the user's local ANTHROPIC_API_KEY, and streams the subprocess
// output back to the cloud as ChatEvents.
//
// All long-lived state (session bookkeeping, browser SSE relay) lives
// on the cloud side. The worker is stateless: each session is one
// goroutine whose lifetime ends when claude exits or the per-session
// timeout fires.
package chat

import (
	"os"
	"path/filepath"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// Config is the dependency bundle a Loop needs at construction time.
// All fields except Client+Creds have sensible defaults applied via
// NewLoop.
type Config struct {
	// Client is the cloud HTTP client used for poll and event POST. The
	// client's bearer token must be the worker token (the default when
	// it is constructed from a worker-registered cloud.Config).
	Client *cloud.Client

	// Creds is the local credentials bundle — provides gh_token /
	// gitlab_token for git clone and ClaudeProviders for the
	// ANTHROPIC_API_KEY.
	Creds *config.Credentials

	// ClonesDir is the fallback parent directory for repos that don't
	// have a config.Repos[name].LocalPath. Defaults to
	// ~/.clawflow/clones.
	ClonesDir string

	// ClaudeBin is the claude executable. Defaults to whatever
	// internal/claude.Resolve() finds (PATH, then
	// ~/.claude/local/claude).
	ClaudeBin string
}

// NewLoop builds a chat Loop. The caller owns the *cloud.Client and
// the credentials; this constructor only applies the documented
// defaults for ClonesDir and ClaudeBin.
func NewLoop(cfg Config) *Loop {
	if cfg.ClonesDir == "" {
		home, _ := os.UserHomeDir()
		cfg.ClonesDir = filepath.Join(home, ".clawflow", "clones")
	}
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = claude.Resolve()
	}
	return &Loop{cfg: cfg}
}
