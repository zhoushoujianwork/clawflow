package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

func TestOffloadExecutionEvidence(t *testing.T) {
	for _, fixture := range []struct {
		name, raw string
		code      *int
		elapsed   *int64
	}{
		{name: "legacy", raw: `{}`},
		{name: "null", raw: `{"exit_code":null,"elapsed_ms":null}`},
		{name: "failed", raw: `{"run_id":"attempt-1","session":"shared","exit_code":7,"error_kind":"execution","status":"error","elapsed_ms":123}`, code: intPointer(7), elapsed: int64Pointer(123)},
		{name: "timeout", raw: `{"run_id":"attempt-2","session":"shared","exit_code":124,"error_kind":"timeout","status":"timeout","elapsed_ms":0}`, code: intPointer(124), elapsed: int64Pointer(0)},
		{name: "success", raw: `{"run_id":"attempt-3","session":"shared","exit_code":0,"status":"ok"}`, code: intPointer(0)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			var result execution.Result
			if err := json.Unmarshal([]byte(fixture.raw), &result); err != nil {
				t.Fatal(err)
			}
			meta := RunMeta{ExecutionMetadata: ExecutionFromResult(result), Repo: "o/r", Operator: "implement", Status: "failed", StartedAt: time.Now().UTC()}
			dir := t.TempDir()
			if err := WriteRunMeta(dir, meta); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			row, _, err := store.DecodeRun(dir, "run", raw)
			if err != nil {
				t.Fatal(err)
			}
			equalInt := func(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
			equal64 := func(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
			if !equalInt(row.ExitCode, fixture.code) || !equal64(row.ElapsedMs, fixture.elapsed) || row.OffloadRunID != result.RunID || row.SessionID != result.NativeSessionID || row.ErrorKind != result.ErrorKind {
				t.Fatalf("evidence lost: %+v", row)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
				t.Fatal(err)
			}
			if fixture.code == nil && payload["exit_code"] != nil {
				t.Fatalf("missing exit became success: %s", row.Payload)
			}
		})
	}
}
func intPointer(n int) *int       { return &n }
func int64Pointer(n int64) *int64 { return &n }
