package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func recordedRows(t *testing.T) []store.Run {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(snapshot.DataDir(), "runs", "*", "*", "*", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.Run
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		row, _, err := store.DecodeRun(filepath.Dir(path), "run", raw)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestRunnerRecordsExecutions(t *testing.T) {
	for _, mode := range []string{"success", "planner-failed", "review-timeout", "cancelled", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			original := r.Execute
			calls := 0
			r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
				calls++
				if mode == "fallback" && calls == 1 {
					return execution.Result{Status: "error", ErrorKind: "unsupported_tools"}, &execution.Error{Kind: "unsupported_tools"}
				}
				if mode == "planner-failed" {
					return execution.Result{Status: "error"}, errors.New("failed")
				}
				if mode == "cancelled" {
					return execution.Result{}, context.Canceled
				}
				if mode == "review-timeout" && calls == 2 {
					return execution.Result{Status: "timeout"}, context.DeadlineExceeded
				}
				if mode == "fallback" && opts.Workspace == scratch {
					opts.ToolsDisabled = true
				}
				result, err := original(ctx, prompt, opts)
				result.Usage = json.RawMessage(`{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}`)
				return result, err
			}
			_, err := r.Run(context.Background(), contract(), scratch, workspace)
			if (err != nil) != (mode != "success" && mode != "fallback") {
				t.Fatalf("err=%v", err)
			}
			rows := recordedRows(t)
			if len(rows) != calls {
				t.Fatalf("rows=%d executions=%d", len(rows), calls)
			}
			counts := map[string]int{}
			for _, row := range rows {
				counts[row.Operator+":"+row.Status]++
				if row.Repo != contract().Repository || row.IssueNumber != contract().PRNumber || row.EndedAt == nil || row.EndedAt.Before(row.StartedAt) || row.Machine == "unknown" {
					t.Fatalf("identity/times: %+v", row)
				}
				if row.Status == "success" && (row.Profile != "test-profile" || row.Backend != "test-backend" || row.Model != "test-model" || row.InputTokens == nil || *row.InputTokens != 100 || row.CacheReadInputTokens == nil || *row.CacheReadInputTokens != 40 || row.OutputTokens == nil || *row.OutputTokens != 20 || row.CostUSD != nil) {
					t.Fatalf("metadata/usage: %+v", row)
				}
			}
			want := map[string]int{"acceptance-plan:success": 1, "acceptance-review:success": 1}
			switch mode {
			case "planner-failed":
				want = map[string]int{"acceptance-plan:failed": 1}
			case "cancelled":
				want = map[string]int{"acceptance-plan:cancelled": 1}
			case "review-timeout":
				want = map[string]int{"acceptance-plan:success": 1, "acceptance-review:timeout": 1}
			case "fallback":
				want["acceptance-plan:failed"] = 1
			}
			if len(counts) != len(want) {
				t.Fatalf("counts=%v want=%v", counts, want)
			}
			for key, n := range want {
				if counts[key] != n {
					t.Fatalf("counts=%v want=%v", counts, want)
				}
			}
		})
	}
}

func TestRunnerPersistsDatabaseRuns(t *testing.T) {
	db := storetest.New(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	snapshot.BindRunStore(db)
	t.Cleanup(func() { snapshot.BindRunStore(nil) })
	r, scratch, workspace := setupRunner(t)
	original := r.Execute
	r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		result, err := original(ctx, prompt, opts)
		result.Usage = json.RawMessage(`{"input_tokens":100,"cached_input_tokens":40,"output_tokens":20}`)
		return result, err
	}
	if _, err := r.Run(context.Background(), contract(), scratch, workspace); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Runs(context.Background(), "run", store.RunFilter{Repo: contract().Repository})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	usage, err := db.Usage(context.Background(), store.RunFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for name, aggregate := range map[string]store.UsageAggregate{"totals": usage.Totals, "repo": usage.ByRepo[contract().Repository]} {
		if aggregate.Runs != 2 || aggregate.InputTokens == nil || *aggregate.InputTokens != 200 {
			t.Fatalf("%s: %+v", name, aggregate)
		}
	}
	for _, operator := range []string{"acceptance-plan", "acceptance-review"} {
		aggregate := usage.ByOperator[operator]
		if aggregate.Runs != 1 || aggregate.CacheReadInputTokens == nil || *aggregate.CacheReadInputTokens != 40 {
			t.Fatalf("%s: %+v", operator, aggregate)
		}
	}
}

func TestRunUsageUnavailable(t *testing.T) {
	for _, raw := range []string{"", "null", "invalid"} {
		if runUsage(json.RawMessage(raw)) != nil {
			t.Fatalf("unexpected usage for %q", raw)
		}
	}
	for _, raw := range []string{`{}`, `{"tokens_unavailable":true,"input_tokens":0}`} {
		u := runUsage(json.RawMessage(raw))
		if u == nil || !u.TokensUnavailable || !u.CostUnavailable {
			t.Fatalf("usage=%+v", u)
		}
	}
	u := runUsage(json.RawMessage(`{"input_tokens":0,"output_tokens":0,"total_cost_usd":0}`))
	if u == nil || u.TokensUnavailable || u.CostUnavailable {
		t.Fatalf("measured zero=%+v", u)
	}
}
