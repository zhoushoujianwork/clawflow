// Resolve where the `claude` CLI actually lives. The Claude Code native
// installer (`claude -m install` flow) drops the binary at
// `~/.claude/local/claude` and relies on the user's shell rc exporting an
// alias, so `claude` resolves in an interactive shell but NOT in any
// non-shell child process — which is exactly what the worker daemon is.
//
// Without this helper the worker hits
//   exec: "claude": executable file not found in $PATH
// on every task and we never see the real failure mode.
package commands

import (
	"os"
	"os/exec"
	"path/filepath"
)

// resolveClaudeBinary returns the full path to a runnable `claude` binary.
// Preference order:
//  1. PATH (works when the user installed via brew / asdf / npm etc.)
//  2. ~/.claude/local/claude (the Claude Code native installer target)
//  3. Literal "claude" — lets exec.Command fail with the original error so
//     users see "not found in $PATH" when they have neither.
func resolveClaudeBinary() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		fallback := filepath.Join(home, ".claude", "local", "claude")
		if st, err := os.Stat(fallback); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o111 != 0 {
			return fallback
		}
	}
	return "claude"
}

// cleanClaudeEnv returns a copy of `env` with `ANTHROPIC_API_KEY=` (empty
// string) stripped. When a user starts `clawflow worker` from inside a
// Claude Code session the harness exports that var as an empty string, and
// the subagent's `claude` subprocess interprets "set but empty" as a
// malformed API key and short-circuits to 401 instead of falling back to
// OAuth via keychain. Removing the empty entry lets OAuth take over.
//
// Non-empty values are preserved — users who explicitly export their own
// key continue to authenticate that way.
func cleanClaudeEnv(env []string) []string {
	out := env[:0:len(env)] // reuse backing array; we only shrink
	for _, kv := range env {
		if kv == "ANTHROPIC_API_KEY=" {
			continue
		}
		out = append(out, kv)
	}
	return out
}
