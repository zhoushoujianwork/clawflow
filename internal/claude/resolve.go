package claude

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Resolve finds the claude binary, tolerating the common case where
// ~/.claude/local/claude is installed but PATH is inherited from a
// non-interactive shell that doesn't source the user's aliases.
func Resolve() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		alt := filepath.Join(home, ".claude", "local", "claude")
		if st, err := os.Stat(alt); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return alt
		}
	}
	return "claude"
}

// CleanedEnv strips ANTHROPIC_API_KEY when set to an empty string. Nested
// Claude Code sessions export the key as "" which the claude subprocess
// treats as a malformed key and short-circuits to 401 instead of falling
// back to OAuth/keychain.
func CleanedEnv(env []string) []string {
	out := env[:0:len(env)]
	for _, kv := range env {
		if kv == "ANTHROPIC_API_KEY=" {
			continue
		}
		out = append(out, kv)
	}
	return out
}
