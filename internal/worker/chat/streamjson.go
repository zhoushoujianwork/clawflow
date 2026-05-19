package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// parseStreamJSON reads claude's `--output-format stream-json --verbose`
// output line-by-line, emits each assistant text delta via onText, and
// returns the terminal `"type":"result"` event's token/cost breakdown
// (or nil when claude exited without one).
//
// Lines that aren't valid JSON or don't carry a recognized event type
// are silently skipped — claude-cli is allowed to interleave debug
// breadcrumbs in --verbose mode, and a future event-type addition
// shouldn't break the parser.
func parseStreamJSON(r io.Reader, onText func(string)) *cloud.Usage {
	sc := bufio.NewScanner(r)
	// Lift the scanner cap so a long result event isn't truncated.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var usage *cloud.Usage
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			continue
		}
		switch probe.Type {
		case "assistant", "user":
			// {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}}
			var ev struct {
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &ev) != nil {
				continue
			}
			for _, c := range ev.Message.Content {
				if c.Type == "text" && c.Text != "" {
					onText(c.Text)
				}
			}
		case "result":
			var ev struct {
				DurationMs   int64   `json:"duration_ms"`
				NumTurns     int     `json:"num_turns"`
				TotalCostUSD float64 `json:"total_cost_usd"`
				Usage        struct {
					InputTokens              int64 `json:"input_tokens"`
					OutputTokens             int64 `json:"output_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				} `json:"usage"`
				ModelUsage map[string]struct {
					InputTokens              int64   `json:"inputTokens"`
					OutputTokens             int64   `json:"outputTokens"`
					CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
					CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
					CostUSD                  float64 `json:"costUSD"`
				} `json:"modelUsage"`
			}
			if json.Unmarshal(line, &ev) != nil {
				continue
			}
			u := &cloud.Usage{
				DurationMs:               ev.DurationMs,
				NumTurns:                 ev.NumTurns,
				TotalCostUSD:             ev.TotalCostUSD,
				InputTokens:              ev.Usage.InputTokens,
				OutputTokens:             ev.Usage.OutputTokens,
				CacheReadInputTokens:     ev.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: ev.Usage.CacheCreationInputTokens,
			}
			if len(ev.ModelUsage) > 0 {
				u.ModelUsage = make(map[string]cloud.ModelUsage, len(ev.ModelUsage))
				for name, m := range ev.ModelUsage {
					u.ModelUsage[name] = cloud.ModelUsage{
						InputTokens:              m.InputTokens,
						OutputTokens:             m.OutputTokens,
						CacheReadInputTokens:     m.CacheReadInputTokens,
						CacheCreationInputTokens: m.CacheCreationInputTokens,
						CostUSD:                  m.CostUSD,
					}
				}
			}
			usage = u
		}
	}
	return usage
}
