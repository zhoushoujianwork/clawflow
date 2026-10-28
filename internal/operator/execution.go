package operator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

var ErrRateLimit = errors.New("execution rate limit")

// ErrCapacity marks provider capacity/overload refusals. It is joined with
// ErrRateLimit so it is handled as a provider-limit class.
var ErrCapacity = errors.New("execution provider capacity")
var ErrAuthError = errors.New("execution auth error")
var ErrOutputLimit = errors.New("execution output limit")
var ErrCostLimit = errors.New("execution cost limit")
var ErrBudgetExceeded = errors.New("execution budget exceeded")

type budgetKey struct{}

// WithMaxBudgetUSD forwards the caller's budget unchanged; Offload rejects unsupported caps.
func WithMaxBudgetUSD(ctx context.Context, usd float64) context.Context {
	if usd <= 0 {
		return ctx
	}
	return context.WithValue(ctx, budgetKey{}, usd)
}
func IsRateLimitError(err error, _ string) bool {
	return errors.Is(err, ErrRateLimit) || execution.IsKind(err, "rate_limit") || execution.IsKind(err, "capacity")
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

// RunOffload executes one structured Offload request, without provider replay.
func RunOffload(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, role string, instructions ...string) (string, error) {
	opts := execution.Options{Workspace: workdir, Role: role, Timeout: timeout, Events: events, PermissionMode: "bypassPermissions"}
	if len(instructions) > 0 {
		opts.Instructions = instructions[0]
	}
	opts.MaxBudgetUSD, _ = ctx.Value(budgetKey{}).(float64)
	result, err := execution.Execute(ctx, prompt, opts)
	return result.Text, classifyExecutionError(err)
}

func classifyExecutionError(err error) error {
	if execution.IsKind(err, "capacity") {
		return errors.Join(ErrRateLimit, ErrCapacity, err)
	}
	for kind, sentinel := range map[string]error{"rate_limit": ErrRateLimit, "auth": ErrAuthError, "output_limit": ErrOutputLimit, "cost_limit": ErrCostLimit, "budget_exceeded": ErrBudgetExceeded} {
		if execution.IsKind(err, kind) {
			return errors.Join(sentinel, err)
		}
	}
	return err
}

// runDevelopment retries a rejected native resume once, retaining the full fresh prompt.
func runDevelopment(ctx context.Context, prompt string, opts RunOptions, instructions ...string) (string, error) {
	execute := opts.ExecuteFunc
	if execute == nil {
		execute = execution.Execute
	}
	activity := &developmentActivity{dst: opts.EventWriter}
	options := execution.Options{Workspace: opts.Workdir, Role: opts.Role, Timeout: opts.Timeout, Events: activity, PermissionMode: "bypassPermissions"}
	if len(instructions) > 0 {
		options.Instructions = instructions[0]
	}
	options.MaxBudgetUSD, _ = ctx.Value(budgetKey{}).(float64)
	options.Resume = opts.SessionKey != "" && opts.ResumePrompt != ""
	options.SessionKey = opts.SessionKey
	selected := prompt
	if options.Resume {
		selected = opts.ResumePrompt
	}
	result, err := execute(ctx, selected, options)
	resumed := options.Resume
	if resumed && err != nil && ctx.Err() == nil && ((strings.TrimSpace(result.Text) == "" && !activity.progress) || resumeSpecificError(err)) {
		if opts.PrepareFreshSession != nil {
			if resetErr := opts.PrepareFreshSession(); resetErr != nil {
				if opts.OnExecutionResult != nil {
					opts.OnExecutionResult(result, resetErr, true)
				}
				return result.Text, resetErr
			}
		}
		if opts.OnSessionResume != nil {
			opts.OnSessionResume("fresh", executionErrorKind(err))
		}
		options.Resume, options.SessionKey = false, ""
		result, err = execute(ctx, prompt, options)
		resumed = false
	} else if resumed && opts.OnSessionResume != nil {
		opts.OnSessionResume("resumed", "eligible")
	}
	if opts.OnExecutionResult != nil {
		opts.OnExecutionResult(result, err, resumed)
	}
	return result.Text, classifyExecutionError(err)
}

func executionErrorKind(err error) string {
	var e *execution.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if err != nil {
		return "execution"
	}
	return ""
}

func resumeSpecificError(err error) bool {
	kind := executionErrorKind(err)
	return strings.Contains(kind, "session") || strings.Contains(kind, "resume") || strings.Contains(kind, "ownership") || strings.Contains(kind, "backend") || strings.Contains(kind, "profile")
}

// Execution supplies validated sanitized event records. Any native tool activity
// is conservatively treated as work; do not replay a partially executed turn.
type developmentActivity struct {
	dst      io.Writer
	progress bool
}

func (w *developmentActivity) Write(p []byte) (int, error) {
	var event execution.Event
	if json.Unmarshal(p, &event) == nil && event.Type == "progress" {
		w.progress = true
	}
	if w.dst != nil {
		return w.dst.Write(p)
	}
	return len(p), nil
}
