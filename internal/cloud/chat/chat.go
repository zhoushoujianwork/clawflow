// Package chat implements the cloud-side chat router for ClawFlow.
//
// PR 3 architecture: the cloud server no longer runs `claude -p` itself.
// It is a router between the browser (which opens an SSE stream) and a
// long-polling worker (which actually clones the repo, invokes `claude`,
// and streams output back). The user's ANTHROPIC_API_KEY, gh_token, and
// project clone all live on the worker; the cloud never sees them.
//
// Wire flow (one message):
//
//  1. Browser POST /api/cloud/chat/sessions {repo, message}
//  2. Cloud resolves repo → binding → machine_id, creates a Session,
//     pushes a ChatAssignment onto that machine's ready queue.
//  3. Browser GET /api/cloud/chat/sessions/{id}/stream  (SSE).
//  4. Worker POST /api/worker/chat/poll long-polls for assignments.
//     Cloud returns the queued ChatAssignment.
//  5. Worker POST /api/worker/chat/sessions/{id}/events as it streams
//     stdout. Cloud relays each event to the browser SSE stream.
//  6. End / error events close the SSE stream.
//
// Sessions are in-memory; the cloud restart drops every in-flight chat
// — acceptable for the MVP (the browser EventSource will get an error
// frame and the user retries).
package chat

import (
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

const (
	// defaultSessionIdle is how long a session may sit with no events
	// and no SSE writer before the GC reaps it. 5 minutes covers a
	// "user walked away mid-conversation" with margin.
	defaultSessionIdle = 5 * time.Minute

	// defaultGCSweepEvery controls how often the GC loop wakes up.
	defaultGCSweepEvery = 30 * time.Second

	// defaultSSEHeartbeatEvery is the cadence of `: ping` comments
	// pushed over SSE to keep proxies from closing idle streams.
	// Production-tuned default; tests override via Config.HeartbeatEvery.
	defaultSSEHeartbeatEvery = 25 * time.Second

	// defaultPollWait is the upper bound on /poll long-polling. A
	// worker that polls without WaitSeconds gets up to this long.
	defaultPollWait = 30 * time.Second

	// maxPollWait clamps an over-eager client. Beyond 60s most
	// proxies drop idle connections anyway.
	maxPollWait = 60 * time.Second
)

// Config is the cloud-chat router's dependency bundle. Slim by design:
// no API keys, no clone roots, no JWT material. All "do work" state
// lives on the worker.
type Config struct {
	// Store is the cloud config store. We use it read-only to look
	// up bindings (repo → machine_id) and repo metadata (platform).
	Store cloud.Store

	// Now is the clock. Tests inject; production uses time.Now.
	Now func() time.Time

	// HeartbeatEvery overrides the SSE keep-alive ping cadence. Zero
	// uses defaultSSEHeartbeatEvery (25s). Tests dial this down to
	// keep wall-clock test runtime low.
	HeartbeatEvery time.Duration
}

func (c Config) withDefaults() Config {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.HeartbeatEvery <= 0 {
		c.HeartbeatEvery = defaultSSEHeartbeatEvery
	}
	return c
}
