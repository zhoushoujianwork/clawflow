package operator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// RunClaude executes `claude -p --output-format stream-json` in a subprocess
// and returns the final result text. If `events` is non-nil, every raw
// stream-json line is teed to it so the dashboard can replay the run
// post-mortem. Text deltas are also printed live to os.Stderr so the user
// sees progress during long runs.
//
// `model` is forwarded as `--model <model>`; the empty string skips the
// flag and lets the claude CLI pick whatever ~/.claude/settings.json
// says. Operator callers should always supply a non-empty model so a
// broken global default can't silently break clawflow.
//
// --dangerously-skip-permissions is used because operators run unattended;
// the subprocess cwd is `workdir`, so callers must scope that carefully
// (tempdir for read-only ops, repo clone for code-writing ops).
func RunClaude(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, model string, systemPrompt ...string) (string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// API key / base URL come from credentials.yaml and flow through
	// env (ANTHROPIC_*). Model is CLI-only because claude doesn't
	// honor an env override for it.
	creds, _ := config.LoadCredentials()
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}

	args := []string{
		"-p",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose", // stream-json requires --verbose with -p
		"--include-partial-messages",
	}
	// When the user configured an API key (likely pointing at a
	// corporate proxy), --bare keeps claude from silently preferring
	// the keychain/OAuth login and ignoring both ANTHROPIC_API_KEY and
	// ANTHROPIC_BASE_URL. Operators don't need hooks/plugins/auto-memory
	// — the prompt is fully self-contained from SKILL.md, so --bare's
	// trade-offs are a net win here.
	if apiKey != "" {
		args = append(args, "--bare")
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if len(systemPrompt) > 0 && systemPrompt[0] != "" {
		args = append(args, "--system-prompt", systemPrompt[0])
	}
	args = append(args, prompt)
	cmd := exec.CommandContext(ctx, claude.Resolve(), args...)
	cmd.Dir = workdir
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)
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
	Type    string `json:"type"`
	Result  string `json:"result"` // present on terminal "result" events
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Event struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"content_block"`
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
// Returns the operator's final stdout. Prefers the terminating "result"
// event's `result` field, but falls back to the last assistant turn that
// carried text content. The fallback exists because claude-cli sets
// `result.result == ""` whenever the very last assistant turn is a pure
// tool_use (e.g. the model emits its answer, then calls one more tool to
// touch labels and ends). Without the fallback, a trailing tool call
// silently wipes out the operator's output.
func parseClaudeStream(r io.Reader, events io.Writer) (string, error) {
	sc := bufio.NewScanner(r)
	// Claude stream-json lines can carry full assistant messages; bump the
	// default 64KB cap to something that won't truncate on long responses.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var finalResult string
	var lastAssistantText string // last assistant turn that emitted any text
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
		case env.Type == "assistant":
			// Concatenate every text block in this assistant turn. Skip
			// thinking/tool_use blocks — those are not part of the user-
			// facing answer. If the turn carried any text at all, treat it
			// as the most recent candidate answer.
			var turn string
			for _, c := range env.Message.Content {
				if c.Type == "text" && c.Text != "" {
					if turn != "" {
						turn += "\n\n"
					}
					turn += c.Text
				}
			}
			if turn != "" {
				lastAssistantText = turn
			}
		case env.Type == "stream_event" &&
			env.Event.Type == "content_block_delta" &&
			env.Event.Delta.Type == "text_delta":
			fmt.Fprint(os.Stderr, env.Event.Delta.Text)
			printedAnyDelta = true
		case env.Type == "stream_event" &&
			env.Event.Type == "content_block_start" &&
			env.Event.ContentBlock.Type == "tool_use":
			// End any in-flight text line before the tool banner, then print
			// a one-liner so the user sees the operator actually doing work.
			if printedAnyDelta {
				fmt.Fprintln(os.Stderr)
				printedAnyDelta = false
			}
			fmt.Fprintf(os.Stderr, "  [tool] %s\n", env.Event.ContentBlock.Name)
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
	if finalResult == "" {
		finalResult = lastAssistantText
	}
	return finalResult, nil
}

