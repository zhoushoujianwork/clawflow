package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// executeRecorded records execution status independently of the acceptance verdict.
// A unique suffix keeps fast planner fallbacks and concurrent reviews distinct.
func (runner Runner) executeRecorded(ctx context.Context, c Contract, operator string, execute ExecuteFunc, prompt string, opts execution.Options) (execution.Result, error) {
	start := time.Now().UTC()
	dir := snapshot.RunDir(c.Repository, c.PRNumber, start) + "-" + randomID()
	result, err := execute(ctx, prompt, opts)
	end := time.Now().UTC()
	machine, _ := os.Hostname()
	meta := snapshot.RunMeta{
		Repo: c.Repository, IssueNumber: c.PRNumber, Operator: operator,
		StartedAt: start, EndedAt: &end, Machine: machine,
		Profile: result.Profile, Backend: result.Backend, Model: result.Model,
		HeadSHA: c.HeadSHA, Status: "success", Usage: runUsage(result.Usage),
	}
	switch {
	case result.Status == "timeout" || execution.IsKind(err, "timeout") || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		meta.Status = "timeout"
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) || execution.IsKind(err, "canceled"):
		meta.Status = "cancelled"
	case err != nil || result.Status != "ok":
		meta.Status = "failed"
	}
	if err != nil {
		meta.Error = err.Error()
	}
	// WriteRunMeta uses a fresh context so cancellation cannot discard usage.
	if saveErr := snapshot.WriteRunMeta(dir, meta); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	return result, err
}

// runUsage maps the Offload result's token names to the shared runs schema.
// Missing usage/cost remains unknown, rather than becoming a measured zero.
func runUsage(raw json.RawMessage) *snapshot.Usage {
	var fields struct {
		Input  *int64   `json:"input_tokens"`
		Output *int64   `json:"output_tokens"`
		Cached *int64   `json:"cached_input_tokens"`
		Read   *int64   `json:"cache_read_input_tokens"`
		Cost   *float64 `json:"total_cost_usd"`
	}
	var usage snapshot.Usage
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &usage) != nil || string(raw) == "null" {
		return nil
	}
	usage.TokensUnavailable = usage.TokensUnavailable || (fields.Input == nil && fields.Output == nil && fields.Cached == nil && fields.Read == nil)
	usage.CostUnavailable = usage.CostUnavailable || fields.Cost == nil
	if fields.Cached != nil {
		usage.CacheReadInputTokens = *fields.Cached
	}
	return &usage
}
