package operator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// RunClaude executes `claude -p` in a subprocess with the prompt passed as a
// positional argument. Returns claude's full stdout (the model's final text).
// stderr is streamed to the user so they see live progress.
//
// --dangerously-skip-permissions bypasses all tool prompts — operators run
// unattended and we can't interactively approve each git/clawflow call. The
// subprocess is scoped to `workdir`, so scope each operator's workdir
// carefully (tempdir for read-only ops, repo clone for code-writing ops).
func RunClaude(ctx context.Context, prompt string, workdir string, timeout time.Duration) (string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, resolveClaude(), "-p", "--dangerously-skip-permissions", prompt)
	cmd.Dir = workdir
	cmd.Env = cleanedEnv(os.Environ())

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("claude: %w", err)
	}
	return stdout.String(), nil
}

// resolveClaude finds the claude binary, tolerating the common case where
// `~/.claude/local/claude` is installed but PATH is inherited from a
// non-interactive shell that doesn't source the user's aliases.
func resolveClaude() string {
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

// cleanedEnv strips ANTHROPIC_API_KEY when it's set to the empty string.
// Nested Claude Code sessions export the key as an empty value, which the
// claude subprocess interprets as a malformed key and short-circuits to 401
// instead of falling back to OAuth/keychain.
func cleanedEnv(env []string) []string {
	out := env[:0:len(env)]
	for _, kv := range env {
		if kv == "ANTHROPIC_API_KEY=" {
			continue
		}
		out = append(out, kv)
	}
	return out
}
