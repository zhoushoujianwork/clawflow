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

// ErrAuthError is returned by RunClaude when the claude CLI exits with a
// 403 / authentication-failure response. Unlike rate limits this is NOT
// transient in the same way — retrying immediately will reproduce the same
// error. Callers should stop retrying and surface the failure loudly (WARN
// log + a distinct outcome label) so operators can investigate the session
// or API-key configuration rather than burning quota on an infinite loop.
var ErrAuthError = errors.New("claude auth error")

// ErrOutputLimit is returned by RunClaude when the claude CLI aborts because
// its response exceeded the configured CLAUDE_CODE_MAX_OUTPUT_TOKENS ceiling
// ("Claude's response exceeded the N output token maximum"). Like rate limits
// this is NOT a permanent operator failure — the issue keeps its trigger
// labels and blindly retrying just burns more tokens. Callers should record a
// distinct status and surface it so the owner can raise max_output_tokens
// rather than counting it toward the circuit breaker (issue #286).
var ErrOutputLimit = errors.New("claude output token limit")

// outputLimitPatterns are substrings (case-insensitive) that identify an
// output-token-ceiling abort from the claude CLI. The CLI emits a message of
// the form "Claude's response exceeded the 64000 output token maximum."
var outputLimitPatterns = []string{
	"output token maximum",
	"exceeded the maximum output",
}

// authErrorPatterns are substrings (case-insensitive) that identify a
// 403 / authentication-failure response from the claude CLI. Matching any
// one of them triggers ErrAuthError rather than the generic failure path.
var authErrorPatterns = []string{
	"403",
	"request not allowed",
	"failed to authenticate",
	"authentication failed",
	"api error: 403",
}

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

// IsAuthError reports whether err or the captured claude output text indicates
// a 403 / authentication-failure response. Both are checked because the claude
// CLI writes the human-readable message to stdout while the Go error only
// carries "exit status 1".
func IsAuthError(err error, output string) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(err.Error() + " " + output)
	for _, pat := range authErrorPatterns {
		if strings.Contains(combined, strings.ToLower(pat)) {
			return true
		}
	}
	return false
}

// IsOutputLimitError reports whether err or the captured claude output text
// indicates the response exceeded the CLAUDE_CODE_MAX_OUTPUT_TOKENS ceiling.
// Both are checked because the claude CLI writes the human-readable message
// to stdout while the Go error only carries "exit status 1".
func IsOutputLimitError(err error, output string) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(err.Error() + " " + output)
	for _, pat := range outputLimitPatterns {
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
// `role` selects which per-provider model slot to read
// (config.RoleChat / RoleEval / RoleOperator); inside the failover loop
// each provider resolves its own `--model` from that role, so e.g.
// provider A can run an eval on `opus` while provider B runs the same
// eval on `claude-opus-4-6`. Empty or unknown role falls back to the
// operator slot — the safest default for user-supplied skills.
//
// --dangerously-skip-permissions is used because operators run unattended;
// the subprocess cwd is `workdir`, so callers must scope that carefully
// (tempdir for read-only ops, repo clone for code-writing ops).
func RunClaude(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, role string, systemPrompt ...string) (string, error) {
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
		model := p.modelForRole(role)
		output, err := runClaudeWithProvider(ctx, prompt, workdir, model, p.apiKey, p.baseURL, p.maxOutputTokens, events, systemPrompt...)
		if err == nil {
			if i > 0 {
				fmt.Fprintf(os.Stderr, "  ✓ provider %q succeeded (after %d failed attempt(s))\n", p.name, i)
			}
			return output, nil
		}

		// Auth errors (403 / "request not allowed") are distinct from both
		// failover and rate-limit: retrying immediately or switching provider
		// won't help — the session token or account permission is the issue.
		// Return ErrAuthError immediately so the caller can surface a loud
		// warning and stop retrying without counting toward the circuit breaker.
		if IsAuthError(err, output) {
			wrapped := fmt.Errorf("claude: %w", err)
			return output, fmt.Errorf("%w: %w", ErrAuthError, wrapped)
		}

		// Output-token-limit aborts ("response exceeded the N output token
		// maximum") are not fixable by retrying or switching provider — the
		// operator needs a higher max_output_tokens ceiling. Return
		// ErrOutputLimit immediately so the caller records a distinct status
		// and skips the circuit breaker instead of blindly re-firing every
		// run and burning tokens (issue #286).
		if IsOutputLimitError(err, output) {
			wrapped := fmt.Errorf("claude: %w", err)
			return output, fmt.Errorf("%w: %w", ErrOutputLimit, wrapped)
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

// providerEntry is the resolved (name, apiKey, baseURL, per-role models)
// tuple used during a single RunClaude invocation. Constructed from
// config.ClaudeProvider or the legacy single-provider fields.
type providerEntry struct {
	name            string
	apiKey          string
	baseURL         string
	chatModel       string
	evalModel       string
	operatorModel   string
	lightModel      string
	maxOutputTokens int
}

// modelForRole returns the model this provider should use for role,
// applying the built-in DefaultModelForRole when the per-role slot is
// empty. The failover loop calls this once per attempt so each provider
// can pin a different model ID.
func (p providerEntry) modelForRole(role string) string {
	var v string
	switch role {
	case config.RoleChat:
		v = p.chatModel
	case config.RoleEval:
		v = p.evalModel
	case config.RoleLight:
		v = p.lightModel
	default:
		v = p.operatorModel
	}
	if v == "" {
		return config.DefaultModelForRole(role)
	}
	return v
}

// buildProviderList returns the ordered list of providers to try. When
// ClaudeProviders is populated, only enabled entries are included. When the
// list is empty (no providers configured, no legacy fields), a single
// zero-value entry is returned so the caller falls through to OAuth/keychain.
func buildProviderList(creds *config.Credentials) []providerEntry {
	if creds == nil {
		return []providerEntry{{name: "default", maxOutputTokens: config.DefaultMaxOutputTokens}}
	}
	enabled := creds.EnabledProviders()
	if len(enabled) > 0 {
		out := make([]providerEntry, len(enabled))
		for i, p := range enabled {
			out[i] = providerEntry{
				name:            p.Name,
				apiKey:          p.APIKey,
				baseURL:         p.BaseURL,
				chatModel:       p.ChatModel,
				evalModel:       p.EvalModel,
				operatorModel:   p.OperatorModel,
				lightModel:      p.LightModel,
				maxOutputTokens: p.EffectiveMaxOutputTokens(),
			}
		}
		return out
	}
	// No providers list — use legacy fields (may both be empty = OAuth).
	return []providerEntry{{
		name:            "default",
		apiKey:          creds.ClaudeAPIKey,
		baseURL:         creds.ClaudeBaseURL,
		maxOutputTokens: config.DefaultMaxOutputTokens,
	}}
}

// runClaudeWithProvider executes a single claude subprocess with the given
// provider credentials. It is the inner loop body extracted from RunClaude.
func runClaudeWithProvider(ctx context.Context, prompt, workdir, model, apiKey, baseURL string, maxOutputTokens int, events io.Writer, systemPrompt ...string) (string, error) {
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
	// Raise the output-token ceiling so large-diff operators (implement)
	// don't fail with "Claude's response exceeded the N output token
	// maximum" (issue #286). A pre-existing CLAUDE_CODE_MAX_OUTPUT_TOKENS in
	// the inherited env wins, so explicit user overrides are preserved.
	cmd.Env = claude.EnvWithMaxOutputTokens(cmd.Env, maxOutputTokens)
	// Tee claude's stderr: the user still sees it live on os.Stderr, but we
	// also retain its tail so a non-zero exit carries claude's actual failure
	// text (rate-limit / auth 403 / panic) instead of a bare "exit status 1".
	// Without this, cmd.Wait yields no claude context, so meta.Error is
	// undiagnosable AND the rate-limit/auth classifiers below — which only
	// inspect err.Error()+stdout — never see the stderr message (issue #222).
	tail := newStderrTail(4096)
	cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	// Run claude as its own process-group leader so the deadline kill below
	// can reap the entire subtree, not just the direct child (issue #213).
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("claude stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("claude start: %w", err)
	}

	// Guard: when the context fires (deadline / cancellation), exec.CommandContext
	// only SIGKILLs the direct claude child — its Bash tool grandchildren
	// (`git fetch`, `clawflow issue list`, …) are orphaned and keep the stdout
	// pipe open, hanging the run far past the deadline (issue #213). Signal the
	// whole process group to bring the subtree down, then close stdout so the
	// scanner unblocks even if something still holds the write end.
	pipeGuardDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminateProcessGroup(cmd)
			_ = stdout.Close()
		case <-pipeGuardDone:
		}
	}()

	result, parseErr := parseClaudeStream(stdout, events)
	close(pipeGuardDone)
	if err := cmd.Wait(); err != nil {
		// cmd.Wait has joined the stderr copy goroutine, so the tail is
		// complete and race-free to read here.
		return result, annotateClaudeErr(err, tail.String(), apiKey)
	}
	if parseErr != nil {
		return result, fmt.Errorf("parse stream: %w", parseErr)
	}
	return result, nil
}

// stderrTail is a bounded io.Writer that retains only the last `max` bytes
// written to it. claude's stderr is tee'd through it so a non-zero exit can
// fold the tail (rate-limit / auth / panic text) into the returned error —
// otherwise cmd.Wait yields only "exit status 1" with no claude context, and
// the rate-limit/auth classifiers (which inspect err.Error()) stay blind to
// messages claude writes to stderr (issue #222). Only the os/exec stderr-copy
// goroutine writes to it, and reads happen after cmd.Wait joins that
// goroutine, so no locking is needed.
type stderrTail struct {
	buf []byte
	max int
}

func newStderrTail(max int) *stderrTail { return &stderrTail{max: max} }

func (t *stderrTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *stderrTail) String() string { return strings.TrimSpace(string(t.buf)) }

// annotateClaudeErr folds the captured claude stderr tail into err so both the
// downstream classifiers (IsRateLimitError / IsAuthError / isFailoverError) and
// the persisted meta.Error / pilot/end log see claude's real failure text
// rather than a bare "exit status 1" (issue #222). The tail is API-key-scrubbed
// and clipped to its last few non-empty lines to stay readable.
func annotateClaudeErr(err error, tail, apiKey string) error {
	tail = scrubAPIKey(strings.TrimSpace(tail), apiKey)
	tail = lastLines(tail, 5)
	if tail == "" {
		return err
	}
	return fmt.Errorf("%w; claude stderr: %s", err, tail)
}

// lastLines returns the last n non-empty, trimmed lines of s joined by " | ".
func lastLines(s string, n int) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
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
