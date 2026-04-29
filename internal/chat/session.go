package chat

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// SessionPath returns the absolute path where claude stores the session
// transcript for `sessionID` launched from `cwd`. Returns "" if the home
// directory can't be resolved.
//
// Claude keys its project directory on the symlink-resolved cwd (e.g.
// /var/folders/.../T -> /private/var/folders/.../T on macOS), so we
// EvalSymlinks here to match its bookkeeping.
func SessionPath(cwd, sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(cwd); rerr == nil {
		cwd = resolved
	}
	encoded := strings.ReplaceAll(cwd, "/", "-")
	return filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
}

// SessionExists is SessionPath + os.Stat.
func SessionExists(cwd, sessionID string) bool {
	p := SessionPath(cwd, sessionID)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// DeleteSessions removes every session transcript matching `sessionID`
// across all `~/.claude/projects/*` directories and returns the count
// removed. Used when the user explicitly ends a chat (X button) so the
// next open starts a fresh conversation. Globbing across project dirs
// catches stale copies left over from earlier cwd evolutions (e.g. a
// session that was created when the chat ran from a different workdir).
func DeleteSessions(sessionID string) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}
	pattern := filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range matches {
		if err := os.Remove(p); err == nil {
			n++
		}
	}
	return n, nil
}
