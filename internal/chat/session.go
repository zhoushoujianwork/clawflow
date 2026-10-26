package chat

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// SessionID returns the deterministic UUID-shaped session id for a
// (repo, issue) pair. issue=0 means a repo-level chat. The hash inputs
// are stable across machines so the same repo+issue always resumes the
// same Claude session.
//
// Deprecated for chat-icon spawns: NewSessionID gives a fresh id per
// click, which is what we want now that chat lives in the user's
// native terminal — there's no clawflow-side hook to "destroy" a
// resumed session, so resume mode strands stale issue context AND
// risks two claude processes contending for the same jsonl. Kept
// here in case anything outside the dashboard still wants the
// deterministic shape.
func SessionID(repo string, issue int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("clawflow-chat:%s:%d", repo, issue)))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// NewSessionID returns a per-launch UUID-shaped session id seeded by
// (repo, issue, time.Now().UnixNano()). Each spawn lands in its own
// jsonl, so VCS data gets re-fetched fresh and Terminal windows for
// the same issue don't fight over a shared transcript file.
func NewSessionID(repo string, issue int) string {
	seed := fmt.Sprintf("clawflow-chat:%s:%d:%d", repo, issue, time.Now().UnixNano())
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// DeleteSessions deliberately leaves AGH-owned native transcripts alone.
// New chats always use fresh logical keys; no Host delete protocol is exposed.
func DeleteSessions(sessionID string) (int, error) { return 0, nil }
