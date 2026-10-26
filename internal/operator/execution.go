package operator

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

var ErrRateLimit = errors.New("execution rate limit")
var ErrAuthError = errors.New("execution auth error")
var ErrOutputLimit = errors.New("execution output limit")
var ErrCostLimit = errors.New("execution cost limit")
var ErrBudgetExceeded = errors.New("execution budget exceeded")

type budgetKey struct{}

// WithMaxBudgetUSD forwards the caller's budget unchanged; AGH rejects unsupported caps.
func WithMaxBudgetUSD(ctx context.Context, usd float64) context.Context {
	if usd <= 0 {
		return ctx
	}
	return context.WithValue(ctx, budgetKey{}, usd)
}
func IsRateLimitError(err error, _ string) bool {
	return errors.Is(err, ErrRateLimit) || execution.IsKind(err, "rate_limit")
}
func IsAuthError(err error, _ string) bool {
	return errors.Is(err, ErrAuthError) || execution.IsKind(err, "auth")
}
func IsOutputLimitError(err error, _ string) bool {
	return errors.Is(err, ErrOutputLimit) || execution.IsKind(err, "output_limit")
}
func IsCostLimitError(err error, _ string) bool {
	return errors.Is(err, ErrCostLimit) || execution.IsKind(err, "cost_limit")
}

// RunAgentHost executes one structured AGH request, without provider replay.
func RunAgentHost(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, role string, instructions ...string) (string, error) {
	opts := execution.Options{Workspace: workdir, Role: role, Timeout: timeout, Events: events, PermissionMode: "bypassPermissions"}
	if len(instructions) > 0 {
		opts.Instructions = instructions[0]
	}
	opts.MaxBudgetUSD, _ = ctx.Value(budgetKey{}).(float64)
	result, err := execution.Execute(ctx, prompt, opts)
	for kind, sentinel := range map[string]error{"rate_limit": ErrRateLimit, "auth": ErrAuthError, "output_limit": ErrOutputLimit, "cost_limit": ErrCostLimit, "budget_exceeded": ErrBudgetExceeded} {
		if execution.IsKind(err, kind) {
			return result.Text, errors.Join(sentinel, err)
		}
	}
	return result.Text, err
}
