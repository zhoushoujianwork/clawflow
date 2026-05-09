package operator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// ErrRateLimit is returned by RunClaude (and propagated through operator.Run)
// when the claude CLI exits with a transient rate-limit or quota error.
// Callers should NOT mark the run as permanently failed — instead they should
// stop processing the current queue and let the next scheduled run retry.
var ErrRateLimit = errors.New("claude rate limit")

// rateLimitPatterns are substrings (case-insensitive) that identify a
// transient rate-limit / quota response from the claude CLI. The list covers:
//   - Claude Code CLI English output: "You've hit your limit"
//   - Claude Code CLI Chinese locale: "您已达到限制" (unlikely but defensive)
//   - HTTP 429 / API error codes surfaced in stderr
//   - Anthropic credit/quota messages
//
// Deprecated: use config.DefaultFailoverPatterns for the canonical list.
// This var is kept for backward-compat with IsRateLimitError callers.
var rateLimitPatterns = []string{
	"hit your limit",
	"you've hit your limit",
	"usage limit reached",
	"rate_limit_error",
	"rate limit",
	"429",
	"quota exceeded",
	"credit balance is too low",
	"overloaded_error",
}

// IsRateLimitError reports whether err or the captured claude output text
// indicates a transient rate-limit condition. Both are checked because the
// claude CLI sometimes writes the human-readable message to stdout (captured
// in output) while the Go error only carries "exit status 1".
func IsRateLimitError(err error, output string) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(err.Error() + " " + output)
	for _, pat := range rateLimitPatterns {
		if strings.Contains(combined, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

// isFailoverError reports whether the combined error + output text matches
// any of the given failover patterns (case-insensitive substring match).
// Returns true when the provider should be skipped and the next one tried.
func isFailoverError(err error, output string, patterns []string) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(err.Error() + " " + output)
	for _, pat := range patterns {
		if strings.Contains(combined, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

// providerAttempt records the result of a single provider invocation.
type providerAttempt struct {
	name   string
	errMsg string // first line of error, api_key scrubbed
}

// scrubAPIKey removes any occurrence of apiKey from s. Used to prevent
// accidental key leakage in error messages that some providers echo back.
func scrubAPIKey(s, apiKey string) string {
	if apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, apiKey, "***")
}

// RunClaude executes `claude -p --output-format stream-json` in a subprocess
// and returns the final result text. It iterates over enabled providers in
// priority order, failing over to the next provider when a transient error
// (rate limit, auth failure, network error, 5xx) is detected.
//
// If `events` is non-nil, every raw stream-json line is teed to it so the
// dashboard can replay the run post-mortem. Text deltas are also printed live
// to os.Stderr so the user sees progress during long runs.
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

	creds, _ := config.LoadCredentials()

	// Build the ordered list of providers to try. If no providers are
	// configured, fall back to the legacy single-provider behavior (empty
	// apiKey + baseURL = OAuth/keychain).
	providers := buildProviderList(creds)
	failoverPatterns := creds.EffectiveFailoverPatterns()

	var attempts []providerAttempt

	for i, p := range providers {
		output, err := runClaudeWithProvider(ctx, prompt, workdir, model, p.apiKey, p.baseURL, events, systemPrompt...)
		if err == nil {
			if i > 0 {
				fmt.Fprintf(os.Stderr, "  ✓ provider %q succeeded (after %d failed attempt(s))\n", p.name, i)
			}
			return output, nil
		}

		// Determine whether this is a provider-level failure (failover) or
		// a genuine operator failure (bail out immediately).
		if isFailoverError(err, output, failoverPatterns) {
			firstLine := firstLineOf(scrubAPIKey(err.Error(), p.apiKey))
			attempts = append(attempts, providerAttempt{name: p.name, errMsg: firstLine})
			fmt.Fprintf(os.Stderr, "  ⚠ provider %q failed (failover): %s\n", p.name, firstLine)
			continue
		}

		// Non-failover error: treat as genuine operator failure. Wrap with
		// ErrRateLimit if it matches the legacy rate-limit patterns so the
		// upstream circuit breaker still works correctly.
		wrapped := fmt.Errorf("claude: %w", err)
		if IsRateLimitError(err, output) {
			return output, fmt.Errorf("%w: %w", ErrRateLimit, wrapped)
		}
		return output, wrapped
	}

	// All providers exhausted.
	if len(attempts) > 0 {
		summary := buildFailureSummary(attempts)
		return "", fmt.Errorf("%w: all %d provider(s) failed\n%s", ErrRateLimit, len(attempts), summary)
	}

	// No providers configured at all — this shouldn't happen after buildProviderList
	// but guard defensively.
	return "", fmt.Errorf("no Claude providers configured")
}

// providerEntry is the resolved (name, apiKey, baseURL) triple used during
// a single RunClaude invocation. Constructed from config.ClaudeProvider or
// the legacy single-provider fields.
type providerEntry struct {
	name    string
	apiKey  string
	baseURL string
}

// buildProviderList returns the ordered list of providers to try. When
// ClaudeProviders is populated, only enabled entries are included. When the
// list is empty (no providers configured, no legacy fields), a single
// zero-value entry is returned so the caller falls through to OAuth/keychain.
func buildProviderList(creds *config.Credentials) []providerEntry {
	if creds == nil {
		return []providerEntry{{name: "default"}}
	}
	enabled := creds.EnabledProviders()
	if len(enabled) > 0 {
		out := make([]providerEntry, len(enabled))
		for i, p := range enabled {
			out[i] = providerEntry{name: p.Name, apiKey: p.APIKey, baseURL: p.BaseURL}
		}
		return out
	}
	// No providers list — use legacy fields (may both be empty = OAuth).
	return []providerEntry{{
		name:    "default",
		apiKey:  creds.ClaudeAPIKey,
		baseURL: creds.ClaudeBaseURL,
	}}
}

// runClaudeWithProvider executes a single claude subprocess with the given
// provider credentials. It is the inner loop body extracted from RunClaude.
func runClaudeWithProvider(ctx context.Context, prompt, workdir, model, apiKey, baseURL string, events io.Writer, systemPrompt ...string) (string, error) {
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

	// Guard: if the context fires while we're reading (e.g. orphaned child
	// processes keep the pipe open after the main claude process is killed),
	// close stdout to unblock the scanner. exec.CommandContext kills the
	// direct child but not its grandchildren, so without this the scanner
	// can block indefinitely past the deadline.
	pipeGuardDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = stdout.Close()
		case <-pipeGuardDone:
		}
	}()

	result, parseErr := parseClaudeStream(stdout, events)
	close(pipeGuardDone)
	if err := cmd.Wait(); err != nil {
		return result, err
	}
	if parseErr != nil {
		return result, fmt.Errorf("parse stream: %w", parseErr)
	}
	return result, nil
}

// firstLineOf returns the first non-empty line of s, trimmed.
func firstLineOf(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return s
}

// buildFailureSummary formats a human-readable list of provider attempts
// for inclusion in the failure comment. API keys are never included.
func buildFailureSummary(attempts []providerAttempt) string {
	var sb strings.Builder
	sb.WriteString("Providers tried:\n")
	for i, a := range attempts {
		sb.WriteString(fmt.Sprintf("  %d. %s — %s\n", i+1, a.name, a.errMsg))
	}
	return sb.String()
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
//
// Multi-turn outcome recovery: when a long session emits the outcome marker
// in an intermediate assistant turn and then produces a short wrap-up in the
// final turn, the "result" event (or lastAssistantText fallback) contains no
// marker. In that case we return lastAssistantTextWithMarker — the last
// assistant turn that contained a valid outcome marker — so the runner can
// still parse the outcome and fire post-automation. A warning is printed to
// stderr so the prompt can be improved upstream.
func parseClaudeStream(r io.Reader, events io.Writer) (string, error) {
	sc := bufio.NewScanner(r)
	// Claude stream-json lines can carry full assistant messages; bump the
	// default 64KB cap to something that won't truncate on long responses.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var finalResult string
	var lastAssistantText string           // last assistant turn that emitted any text
	var lastAssistantTextWithMarker string // last assistant turn that contained an outcome marker
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
			// Only keep the first non-empty result. Claude CLI emits
			// additional result events for background tasks (origin.kind
			// == "task-notification") which carry a short summary without
			// the outcome marker. Letting those overwrite the primary
			// session result causes the runner to miss the outcome label.
			if finalResult == "" {
				finalResult = env.Result
			}
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
				// Track the last turn that contains an outcome marker
				// separately. This lets us recover when the marker appears
				// in an intermediate turn and the final turn is a short
				// wrap-up with no marker (see multi-turn outcome recovery
				// comment above).
				if outcomeRE.MatchString(turn) {
					lastAssistantTextWithMarker = turn
				}
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
	// If the resolved result has no outcome marker but an earlier assistant
	// turn did, fall back to that turn. This handles the multi-turn case
	// where the model emits the full structured output (including the marker)
	// in turn N and then produces a brief wrap-up in turn N+1.
	if !outcomeRE.MatchString(finalResult) && lastAssistantTextWithMarker != "" {
		fmt.Fprintf(os.Stderr,
			"  ⚠ outcome marker found in intermediate assistant turn but not in final result — using intermediate turn (consider tightening the operator prompt)\n")
		finalResult = lastAssistantTextWithMarker
	}
	return finalResult, nil
}

