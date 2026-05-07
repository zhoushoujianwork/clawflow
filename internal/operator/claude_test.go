package operator

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

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

	got, err := parseClaudeStream(strings.NewReader(stream), nil)
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

	got, err := parseClaudeStream(strings.NewReader(stream), nil)
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

	got, err := parseClaudeStream(strings.NewReader(stream), nil)
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

	got, err := parseClaudeStream(strings.NewReader(stream), nil)
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

	got, err := parseClaudeStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	label, _ := parseOutcome(got)
	if label != "agent-evaluated" {
		t.Errorf("last marker turn should win; got label %q", label)
	}
}

func TestIsRateLimitError(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		output  string
		want    bool
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
