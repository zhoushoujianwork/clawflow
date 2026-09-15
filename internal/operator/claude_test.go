package operator

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestStderrTailRetainsLastBytes(t *testing.T) {
	tail := newStderrTail(8)
	if _, err := tail.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := tail.String(); got != "89ABCDEF" {
		t.Fatalf("got %q, want last 8 bytes %q", got, "89ABCDEF")
	}

	// Multiple writes across the cap boundary keep only the tail.
	tail = newStderrTail(4)
	tail.Write([]byte("ab"))
	tail.Write([]byte("cdef"))
	if got := tail.String(); got != "cdef" {
		t.Fatalf("got %q, want %q", got, "cdef")
	}
}

func TestAnnotateClaudeErr(t *testing.T) {
	base := errors.New("exit status 1")

	t.Run("folds stderr tail into error", func(t *testing.T) {
		err := annotateClaudeErr(base, "API error: 403\nrequest not allowed", "")
		msg := err.Error()
		if !strings.Contains(msg, "exit status 1") {
			t.Fatalf("lost original error: %q", msg)
		}
		if !strings.Contains(msg, "403") || !strings.Contains(msg, "request not allowed") {
			t.Fatalf("stderr tail not folded in: %q", msg)
		}
		// errors.Is must still see the wrapped sentinel through %w.
		if !errors.Is(err, base) {
			t.Fatalf("errors.Is broken after annotation")
		}
	})

	t.Run("classifiers now see stderr-only auth message", func(t *testing.T) {
		// Before #222 the auth text lived only on stderr, so IsAuthError(err,"")
		// returned false. Folding it into err makes the classifier fire.
		annotated := annotateClaudeErr(base, "Error: api error: 403", "")
		if !IsAuthError(annotated, "") {
			t.Fatalf("auth message on stderr not classified")
		}
		rl := annotateClaudeErr(base, "You've hit your limit", "")
		if !IsRateLimitError(rl, "") {
			t.Fatalf("rate-limit message on stderr not classified")
		}
	})

	t.Run("scrubs api key", func(t *testing.T) {
		err := annotateClaudeErr(base, "auth failed for sk-secret-123", "sk-secret-123")
		if strings.Contains(err.Error(), "sk-secret-123") {
			t.Fatalf("api key leaked: %q", err.Error())
		}
	})

	t.Run("empty tail returns original error unchanged", func(t *testing.T) {
		if got := annotateClaudeErr(base, "   \n  ", ""); got != base {
			t.Fatalf("empty tail should return original error, got %v", got)
		}
	})
}

// buildAssistantEvent returns a stream-json line for an "assistant" event
// carrying a single text content block.
func buildAssistantEvent(text string) string {
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Content []contentBlock `json:"content"`
	}
	type event struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
	}
	e := event{
		Type: "assistant",
		Message: message{
			Content: []contentBlock{{Type: "text", Text: text}},
		},
	}
	b, _ := json.Marshal(e)
	return string(b)
}

// buildResultEvent returns a stream-json line for a "result" event.
func buildResultEvent(result string) string {
	type event struct {
		Type   string `json:"type"`
		Result string `json:"result"`
	}
	b, _ := json.Marshal(event{Type: "result", Result: result})
	return string(b)
}

// TestParseClaudeStream_MarkerInFinalTurn is the happy path: marker is in the
// final result, no fallback needed.
func TestParseClaudeStream_MarkerInFinalTurn(t *testing.T) {
	body := "## Eval\n\nRepro: 8/10\n\n<!-- clawflow:outcome=agent-evaluated -->\n"
	stream := strings.Join([]string{
		buildAssistantEvent(body),
		buildResultEvent(body),
	}, "\n") + "\n"

	got, err := parseClaudeStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != body {
		t.Errorf("got %q, want %q", got, body)
	}
	// Outcome must be parseable from the returned text.
	label, _ := parseOutcome(got)
	if label != "agent-evaluated" {
		t.Errorf("parseOutcome label = %q, want %q", label, "agent-evaluated")
	}
}

// TestParseClaudeStream_MarkerInIntermediateTurn is the bug scenario from
// issue #75: the outcome marker appears in an intermediate assistant turn;
// the final "result" event carries only a short wrap-up with no marker.
// parseClaudeStream must return the intermediate turn so the runner can
// extract the outcome label.
func TestParseClaudeStream_MarkerInIntermediateTurn(t *testing.T) {
	fullEval := "## Eval\n\nRepro: 8/10\n\n<!-- clawflow:outcome=agent-evaluated -->\n"
	wrapUp := "All done — the evaluation was completed successfully earlier."

	stream := strings.Join([]string{
		buildAssistantEvent(fullEval), // turn N: full output with marker
		buildAssistantEvent(wrapUp),   // turn N+1: short wrap-up, no marker
		buildResultEvent(wrapUp),      // result event mirrors the final turn
	}, "\n") + "\n"

	got, err := parseClaudeStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The returned text must contain the outcome marker so the runner can act.
	label, _ := parseOutcome(got)
	if label != "agent-evaluated" {
		t.Errorf("parseOutcome label = %q, want %q — intermediate turn marker was not recovered", label, "agent-evaluated")
	}
}

// TestParseClaudeStream_MarkerInIntermediateTurn_EmptyResult covers the
// variant where the "result" event is empty (pure tool_use final turn) and
// the marker lives in an earlier assistant turn.
func TestParseClaudeStream_MarkerInIntermediateTurn_EmptyResult(t *testing.T) {
	fullEval := "## Eval\n\nRepro: 9/10\n\n<!-- clawflow:outcome=agent-evaluated -->\n"

	stream := strings.Join([]string{
		buildAssistantEvent(fullEval), // turn N: full output with marker
		buildResultEvent(""),          // empty result (trailing tool_use)
	}, "\n") + "\n"

	got, err := parseClaudeStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	label, _ := parseOutcome(got)
	if label != "agent-evaluated" {
		t.Errorf("parseOutcome label = %q, want %q — intermediate turn marker was not recovered", label, "agent-evaluated")
	}
}

// TestParseClaudeStream_NoMarkerAnywhere verifies that when no turn contains
// a marker, the function still returns the final result text unchanged (no
// regression on the existing no-marker path).
func TestParseClaudeStream_NoMarkerAnywhere(t *testing.T) {
	wrapUp := "All done."
	stream := strings.Join([]string{
		buildAssistantEvent("Some intermediate text."),
		buildAssistantEvent(wrapUp),
		buildResultEvent(wrapUp),
	}, "\n") + "\n"

	got, err := parseClaudeStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != wrapUp {
		t.Errorf("got %q, want %q", got, wrapUp)
	}
	label, _ := parseOutcome(got)
	if label != "" {
		t.Errorf("expected no label, got %q", label)
	}
}

// TestParseClaudeStream_MultipleMarkerTurns_LastWins verifies that when
// multiple intermediate turns contain markers, the last one wins (consistent
// with parseOutcome's "last wins" contract).
func TestParseClaudeStream_MultipleMarkerTurns_LastWins(t *testing.T) {
	turn1 := "Draft\n<!-- clawflow:outcome=agent-skipped -->\n"
	turn2 := "Final eval\n<!-- clawflow:outcome=agent-evaluated -->\n"
	wrapUp := "Summary."

	stream := strings.Join([]string{
		buildAssistantEvent(turn1),
		buildAssistantEvent(turn2),
		buildAssistantEvent(wrapUp),
		buildResultEvent(wrapUp),
	}, "\n") + "\n"

	got, err := parseClaudeStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	label, _ := parseOutcome(got)
	if label != "agent-evaluated" {
		t.Errorf("last marker turn should win; got label %q", label)
	}
}

// TestParseClaudeStream_QuotedMarkerTurnNotPreferred guards the #75/#326
// interaction. The multi-turn fallback picks the last turn "containing a
// marker"; once #326 anchored the marker to the trailing lines, that predicate
// had to be anchored too. Otherwise a turn that merely quotes a marker mid-body
// looks like a marker turn, gets chosen over the real verdict turn, and is then
// parsed as no-marker — turning the #75 fix into a new bug.
func TestParseClaudeStream_QuotedMarkerTurnNotPreferred(t *testing.T) {
	fullEval := "## Eval\n\nRepro: 8/10\n\n<!-- clawflow:outcome=agent-evaluated -->\n"
	// A later turn that discusses the marker but does not end with one.
	quoting := "顺带说明：正文里缺 `<!-- clawflow:outcome=... -->` 行时会走 no-marker。\n\n已完成。"

	stream := strings.Join([]string{
		buildAssistantEvent(fullEval), // turn N: the real verdict
		buildAssistantEvent(quoting),  // turn N+1: quotes a marker, no verdict
		buildResultEvent(quoting),
	}, "\n") + "\n"

	got, err := parseClaudeStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	label, _ := parseOutcome(got)
	if label != "agent-evaluated" {
		t.Errorf("parseOutcome label = %q, want %q — a turn that merely quotes a marker was preferred over the real verdict turn", label, "agent-evaluated")
	}
}

func TestIsAuthError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		output string
		want   bool
	}{
		{
			name:   "nil error",
			err:    nil,
			output: "",
			want:   false,
		},
		{
			name:   "generic exit status 1 no auth pattern",
			err:    errors.New("claude: exit status 1"),
			output: "some unrelated error",
			want:   false,
		},
		{
			name:   "403 in output",
			err:    errors.New("claude: exit status 1"),
			output: "Failed to authenticate. API Error: 403 Request not allowed",
			want:   true,
		},
		{
			name:   "request not allowed case-insensitive",
			err:    errors.New("claude: exit status 1"),
			output: "REQUEST NOT ALLOWED",
			want:   true,
		},
		{
			name:   "failed to authenticate in output",
			err:    errors.New("claude: exit status 1"),
			output: "Failed to authenticate with the API",
			want:   true,
		},
		{
			name:   "api error 403 in output",
			err:    errors.New("claude: exit status 1"),
			output: "api error: 403 forbidden",
			want:   true,
		},
		{
			name:   "authentication failed in output",
			err:    errors.New("claude: exit status 1"),
			output: "Authentication failed: invalid session",
			want:   true,
		},
		{
			name:   "rate limit 429 should not match auth",
			err:    errors.New("claude: exit status 1"),
			output: "HTTP 429 Too Many Requests",
			want:   false,
		},
		{
			name:   "rate limit hit your limit should not match auth",
			err:    errors.New("claude: exit status 1"),
			output: "You've hit your limit",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAuthError(tc.err, tc.output)
			if got != tc.want {
				t.Errorf("IsAuthError(%v, %q) = %v, want %v", tc.err, tc.output, got, tc.want)
			}
		})
	}
}

func TestIsOutputLimitError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		output string
		want   bool
	}{
		{name: "nil error", err: nil, output: "", want: false},
		{
			name:   "exact claude output token maximum message",
			err:    errors.New("claude: exit status 1"),
			output: "API Error: Claude's response exceeded the 64000 output token maximum.",
			want:   true,
		},
		{
			name:   "case-insensitive output token maximum",
			err:    errors.New("claude: exit status 1"),
			output: "EXCEEDED THE 64000 OUTPUT TOKEN MAXIMUM",
			want:   true,
		},
		{
			name:   "rate limit should not match",
			err:    errors.New("claude: exit status 1"),
			output: "You've hit your limit",
			want:   false,
		},
		{
			name:   "generic failure should not match",
			err:    errors.New("claude: exit status 1"),
			output: "some unrelated compilation error",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsOutputLimitError(tc.err, tc.output)
			if got != tc.want {
				t.Errorf("IsOutputLimitError(%v, %q) = %v, want %v", tc.err, tc.output, got, tc.want)
			}
		})
	}
}

func TestIsRateLimitError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		output string
		want   bool
	}{
		{
			name:   "nil error",
			err:    nil,
			output: "",
			want:   false,
		},
		{
			name:   "generic exit status 1",
			err:    errors.New("claude: exit status 1"),
			output: "some unrelated error",
			want:   false,
		},
		{
			name:   "hit your limit in output",
			err:    errors.New("claude: exit status 1"),
			output: "You've hit your limit · resets 3:20am (Asia/Shanghai)",
			want:   true,
		},
		{
			name:   "hit your limit case-insensitive",
			err:    errors.New("claude: exit status 1"),
			output: "YOU'VE HIT YOUR LIMIT",
			want:   true,
		},
		{
			name:   "rate_limit_error in err",
			err:    errors.New("claude: rate_limit_error"),
			output: "",
			want:   true,
		},
		{
			name:   "429 in output",
			err:    errors.New("claude: exit status 1"),
			output: "HTTP 429 Too Many Requests",
			want:   true,
		},
		{
			name:   "usage limit reached",
			err:    errors.New("claude: exit status 1"),
			output: "Usage limit reached for this billing period",
			want:   true,
		},
		{
			name:   "credit balance is too low",
			err:    errors.New("claude: exit status 1"),
			output: "Credit balance is too low to run this request",
			want:   true,
		},
		{
			name:   "quota exceeded",
			err:    errors.New("claude: exit status 1"),
			output: "quota exceeded",
			want:   true,
		},
		{
			name:   "overloaded_error",
			err:    errors.New("claude: exit status 1"),
			output: "overloaded_error: API is temporarily overloaded",
			want:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsRateLimitError(tc.err, tc.output)
			if got != tc.want {
				t.Errorf("IsRateLimitError(%v, %q) = %v, want %v", tc.err, tc.output, got, tc.want)
			}
		})
	}
}

// TestIsCostLimitError covers the billing-cap classifier added for issue #308.
// The exact report that regressed came back in Chinese from a proxy while every
// pattern table was English, so the real-world string is asserted verbatim.
func TestIsCostLimitError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		output string
		want   bool
	}{
		{
			name:   "nil error",
			err:    nil,
			output: "",
			want:   false,
		},
		{
			// Verbatim from ~/.clawflow/data/runs/.../issue-306 meta.json.
			name:   "402 chinese daily cost limit from summary",
			err:    errors.New("claude: exit status 1"),
			output: "API Error: 402 已达到每日费用限制 ($500)",
			want:   true,
		},
		{
			name:   "402 folded into stderr tail by annotateClaudeErr",
			err:    annotateClaudeErr(errors.New("exit status 1"), "API Error: 402 已达到每日费用限制 ($500)", ""),
			output: "",
			want:   true,
		},
		{
			name:   "402 payment required english",
			err:    errors.New("claude: exit status 1"),
			output: "402 Payment Required",
			want:   true,
		},
		{
			name:   "daily cost limit english",
			err:    errors.New("claude: exit status 1"),
			output: "You have reached your daily cost limit",
			want:   true,
		},
		{
			// A bare "402" must NOT match: diffs, line numbers and token
			// counts routinely contain it, and up to 5 lines of free-form
			// claude stderr are part of the match surface (issue #308).
			name:   "bare 402 in unrelated text does not match",
			err:    errors.New("claude: exit status 1"),
			output: "main.go:402: undefined variable foo",
			want:   false,
		},
		{
			name:   "token count containing 402 does not match",
			err:    errors.New("claude: exit status 1"),
			output: "used 40200 output tokens",
			want:   false,
		},
		{
			name:   "rate limit is not a cost limit",
			err:    errors.New("claude: exit status 1"),
			output: "You've hit your limit · resets 3:20am",
			want:   false,
		},
		{
			name:   "403 auth error is not a cost limit",
			err:    errors.New("claude: exit status 1"),
			output: "API Error: 403 request not allowed",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsCostLimitError(tc.err, tc.output)
			if got != tc.want {
				t.Errorf("IsCostLimitError(%v, %q) = %v, want %v", tc.err, tc.output, got, tc.want)
			}
		})
	}
}

// TestCostLimitPrecedence pins the ordering guarantees the runner depends on:
// a 402 must classify as a cost limit and NOT as an auth/output/rate-limit
// error, since each of those routes to a different status and circuit-breaker
// decision (issue #308).
func TestCostLimitPrecedence(t *testing.T) {
	err := errors.New("claude: exit status 1")
	out := "API Error: 402 已达到每日费用限制 ($500)"

	if !IsCostLimitError(err, out) {
		t.Fatal("402 must classify as cost limit")
	}
	if IsAuthError(err, out) {
		t.Error("402 must not classify as auth error")
	}
	if IsOutputLimitError(err, out) {
		t.Error("402 must not classify as output limit")
	}
	if IsRateLimitError(err, out) {
		t.Error("402 must not classify as rate limit — the recovery window differs")
	}
}
