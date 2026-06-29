package claude

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// EnvWithCredentials extends CleanedEnv with optional ANTHROPIC_*
// overrides sourced from clawflow's credentials.yaml. When apiKey or
// baseURL are non-empty, any pre-existing value in `env` for that
// key is dropped and replaced — so clawflow's config wins over
// whatever the user's shell inherited (which may be OAuth's empty
// placeholder, or a key meant for a different account).
//
// Empty arguments are a no-op for that field — passing apiKey=""
// keeps env's existing ANTHROPIC_API_KEY (if any) so users without
// custom config still go through OAuth/keychain unchanged.
func EnvWithCredentials(env []string, apiKey, baseURL string) []string {
	cleaned := CleanedEnv(env)
	out := make([]string, 0, len(cleaned)+2)
	for _, kv := range cleaned {
		if apiKey != "" && strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			continue
		}
		if baseURL != "" && strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
			continue
		}
		out = append(out, kv)
	}
	if apiKey != "" {
		out = append(out, "ANTHROPIC_API_KEY="+apiKey)
	}
	if baseURL != "" {
		out = append(out, "ANTHROPIC_BASE_URL="+baseURL)
	}
	return out
}

// EnvWithMaxOutputTokens returns env with CLAUDE_CODE_MAX_OUTPUT_TOKENS set to
// maxTokens, UNLESS the variable is already present in env (an explicit
// user/shell override always wins — env > config > built-in default, matching
// the precedence used elsewhere in clawflow).
//
// maxTokens <= 0 is a no-op: the caller has nothing to inject, so the claude
// CLI keeps its own built-in default (the historical behaviour). Operators
// that emit large diffs (e.g. `implement`) hit the default 64000 ceiling and
// fail with "Claude's response exceeded the N output token maximum" — raising
// this ceiling via config is the fix (issue #286).
func EnvWithMaxOutputTokens(env []string, maxTokens int) []string {
	if maxTokens <= 0 {
		return env
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CODE_MAX_OUTPUT_TOKENS=") {
			return env // explicit override present — don't clobber it.
		}
	}
	out := make([]string, len(env), len(env)+1)
	copy(out, env)
	return append(out, "CLAUDE_CODE_MAX_OUTPUT_TOKENS="+strconv.Itoa(maxTokens))
}
