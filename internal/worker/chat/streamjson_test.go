package chat

import (
	"strings"
	"testing"
)

// TestParseStreamJSON covers the worker-side parser standalone: text
// deltas come out via the callback in order; the terminal result
// event's usage is returned; non-recognized / non-JSON lines are
// tolerated silently.
func TestParseStreamJSON(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,                                                                                                                                                                              // ignored
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello "}]}}`,                                                                                                                                    // delta 1
		`debug breadcrumb that is not JSON`,                                                                                                                                                                               // ignored (not JSON)
		`{"type":"assistant","message":{"content":[{"type":"text","text":"world!"}]}}`,                                                                                                                                    // delta 2
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"..."}]}}`,                                                                                                                                  // ignored (no text content)
		`{"type":"result","duration_ms":2500,"num_turns":2,"total_cost_usd":0.0099,"usage":{"input_tokens":300,"output_tokens":75,"cache_read_input_tokens":1000,"cache_creation_input_tokens":20},"modelUsage":{"claude-opus-4-7":{"inputTokens":300,"outputTokens":75,"cacheReadInputTokens":1000,"cacheCreationInputTokens":20,"costUSD":0.0099}}}`,
	}, "\n")

	var sb strings.Builder
	usage := parseStreamJSON(strings.NewReader(transcript), func(s string) { sb.WriteString(s) })

	if sb.String() != "Hello world!" {
		t.Errorf("text deltas = %q, want %q", sb.String(), "Hello world!")
	}
	if usage == nil {
		t.Fatal("usage is nil; expected result event to be parsed")
	}
	if usage.TotalCostUSD != 0.0099 {
		t.Errorf("TotalCostUSD = %v, want 0.0099", usage.TotalCostUSD)
	}
	if usage.InputTokens != 300 || usage.OutputTokens != 75 {
		t.Errorf("tokens mismatch: %+v", usage)
	}
	if usage.DurationMs != 2500 || usage.NumTurns != 2 {
		t.Errorf("metadata mismatch: %+v", usage)
	}
	m, ok := usage.ModelUsage["claude-opus-4-7"]
	if !ok {
		t.Fatalf("model breakdown missing: %+v", usage.ModelUsage)
	}
	if m.CostUSD != 0.0099 || m.InputTokens != 300 {
		t.Errorf("model usage round-trip wrong: %+v", m)
	}
}

// TestParseStreamJSON_NoResultEvent verifies the parser returns nil
// when claude exits before emitting a result event (e.g. crash). The
// worker treats this as "session ran but no usage to report".
func TestParseStreamJSON_NoResultEvent(t *testing.T) {
	transcript := `{"type":"assistant","message":{"content":[{"type":"text","text":"partial..."}]}}`
	var got string
	usage := parseStreamJSON(strings.NewReader(transcript), func(s string) { got = s })
	if usage != nil {
		t.Errorf("usage = %+v, want nil", usage)
	}
	if got != "partial..." {
		t.Errorf("got = %q, want %q", got, "partial...")
	}
}
