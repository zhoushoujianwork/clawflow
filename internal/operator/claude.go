package operator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// RunClaude executes `claude -p --output-format stream-json` in a subprocess
// and returns the final result text. If `events` is non-nil, every raw
// stream-json line is teed to it so the dashboard can replay the run
// post-mortem. Text deltas are also printed live to os.Stderr so the user
// sees progress during long runs.
//
// --dangerously-skip-permissions is used because operators run unattended;
// the subprocess cwd is `workdir`, so callers must scope that carefully
// (tempdir for read-only ops, repo clone for code-writing ops).
func RunClaude(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer) (string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx,
		resolveClaude(),
		"-p",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose", // stream-json requires --verbose with -p
		"--include-partial-messages",
		prompt,
	)
	cmd.Dir = workdir
	cmd.Env = cleanedEnv(os.Environ())
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("claude stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("claude start: %w", err)
	}

	// Parse stream line-by-line so we can tee to events.jsonl and extract
	// text deltas for user-facing progress.
	result, parseErr := parseClaudeStream(stdout, events)
	if err := cmd.Wait(); err != nil {
		return result, fmt.Errorf("claude: %w", err)
	}
	if parseErr != nil {
		return result, fmt.Errorf("parse stream: %w", parseErr)
	}
	return result, nil
}

// streamEnvelope is the minimal shape we peek at inside each stream-json
// line. Unknown events still pass through verbatim to the events writer.
type streamEnvelope struct {
	Type   string `json:"type"`
	Result string `json:"result"` // present on terminal "result" events
	Event  struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
}

// parseClaudeStream reads JSONL from r. Side effects:
//   - every raw line is written to events (when non-nil)
//   - text_delta events are streamed to os.Stderr for live user feedback
//
// Returns the final result.result text from the terminating "result" event.
func parseClaudeStream(r io.Reader, events io.Writer) (string, error) {
	sc := bufio.NewScanner(r)
	// Claude stream-json lines can carry full assistant messages; bump the
	// default 64KB cap to something that won't truncate on long responses.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var finalResult string
	printedAnyDelta := false

	for sc.Scan() {
		line := sc.Bytes()
		if events != nil {
			// Best-effort tee. We do not want a flaky dashboard writer to
			// break claude execution, so ignore write errors.
			_, _ = events.Write(line)
			_, _ = events.Write([]byte("\n"))
		}

		var env streamEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			// Non-JSON line (e.g., a bare warning from claude). Pass
			// through and keep parsing.
			continue
		}
		switch {
		case env.Type == "result":
			finalResult = env.Result
		case env.Type == "stream_event" &&
			env.Event.Type == "content_block_delta" &&
			env.Event.Delta.Type == "text_delta":
			fmt.Fprint(os.Stderr, env.Event.Delta.Text)
			printedAnyDelta = true
		}
	}
	if err := sc.Err(); err != nil {
		return finalResult, err
	}
	if printedAnyDelta {
		// Cap the live-delta stream with a newline so the runner's next log
		// line isn't glued to the last chunk of claude text.
		fmt.Fprintln(os.Stderr)
	}
	return finalResult, nil
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

// cleanedEnv strips ANTHROPIC_API_KEY when set to an empty string. Nested
// Claude Code sessions export the key as "" which the claude subprocess
// treats as a malformed key and short-circuits to 401 instead of falling
// back to OAuth/keychain.
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
