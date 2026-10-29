//go:build !windows

package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// The public schema 1 fixture keeps global/personal fields deliberately toxic.
const liveFixture = `{
 "schema_version":1,"generated_at":"2026-10-29T19:00:00Z","period":"today",
 "totals":{"runs":100,"sessions":99,"usage":null,"cost_usd":null},
 "projects":[{"key":"/private/project","name":"private"}],
 "active":[
 {"phase":"working","profile":"engineering","model":"gpt-6","session":"managed","started_at":"2026-10-29T18:00:01Z","updated_at":"2026-10-29T18:30:00Z","last_event_at":"2026-10-29T18:29:59Z","tool_calls":0,"stalled":false,"task":"secret prompt","dir":"/private/project","args":["secret"],"future":{"secret":"value"}},
 {"phase":"working","session":"personal","started_at":"2026-10-29T18:00:01Z","task":"personal task"}],
 "recent":[{"session":"personal","result":"secret result","ts":"2026-10-29T18:00:00Z"}],"warnings":["private warning"],"future":true}`

func liveCLI(t *testing.T, help, body string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	fixture, calls := filepath.Join(dir, "fixture"), filepath.Join(dir, "calls")
	if err := os.WriteFile(fixture, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "printf '%s\\n' \"$*\" >> " + q(calls) + "\n" +
		"if [ \"$#\" = 1 ] && [ \"$1\" = -h ]; then printf '%s\\n' " + q(help) + "; exit 2; fi\n" +
		"if [ \"$#\" = 2 ] && [ \"$1\" = stats ] && [ \"$2\" = --json ]; then cat " + q(fixture) + "; exit 0; fi\nexit 91\n"
	return writeScript(t, dir, "offload", script), fixture, calls
}

const liveHelp = "usage:\n  offload stats [--json] [--period today|week|month|all]\n"

func liveReader(t *testing.T, command string) *LiveStatsReader {
	t.Helper()
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", command)
	r := NewLiveStatsReader(Config{Command: command})
	r.machine = "test-host"
	r.timeout = 5 * time.Second
	return r
}

func managedRows() []store.Run {
	started, _ := time.Parse(time.RFC3339, "2026-10-29T18:00:00Z")
	return []store.Run{{Machine: "test-host", SessionID: "managed", StartedAt: started}}
}

func TestLiveStatsSchemaAndPrivacy(t *testing.T) {
	cmd, _, calls := liveCLI(t, liveHelp, liveFixture)
	r := liveReader(t, cmd)
	got := r.Read(context.Background(), managedRows())
	if got.State != LiveAvailable || got.Stale || got.SourceMachine != "test-host" || got.ObservedAt.IsZero() || got.GeneratedAt == nil || len(got.Activities) != 1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	a := got.Activities[0]
	if a.Status != "working" || a.Profile != "engineering" || a.Model != "gpt-6" || a.Machine != "test-host" || a.ToolCalls == nil || *a.ToolCalls != 0 || a.Stalled == nil || *a.Stalled || a.UpdatedAt == nil || a.LastEventAt == nil {
		t.Fatalf("activity: %+v", a)
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"secret", "private", "personal", "task", "result", "args", "totals", "future", "warnings"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	// Returned values cannot mutate the cache, and authorization is per request.
	*a.ToolCalls = 999
	*got.GeneratedAt = time.Time{}
	again := r.Read(context.Background(), managedRows())
	if *again.Activities[0].ToolCalls != 0 || again.GeneratedAt.IsZero() {
		t.Fatal("cache mutated by caller")
	}
	if len(r.Read(context.Background(), nil).Activities) != 0 {
		t.Fatal("cache bypassed attribution")
	}
	remote := managedRows()
	remote[0].Machine = "remote-host"
	if len(r.Read(context.Background(), remote).Activities) != 0 {
		t.Fatal("host stats attributed to remote worker")
	}
	ended := remote[0].StartedAt
	remote = managedRows()
	remote[0].EndedAt = &ended
	if len(r.Read(context.Background(), remote).Activities) != 0 {
		t.Fatal("resumed personal activity attributed to finished attempt")
	}
	data, _ := os.ReadFile(calls)
	if string(data) != "-h\nstats --json\n" {
		t.Fatalf("unexpected CLI calls: %q", data)
	}
}

func TestLiveStatsFailures(t *testing.T) {
	for _, tt := range []struct{ name, help, body, state string }{
		{"missing capability", "usage:\n  offload status", liveFixture, LiveUnsupported},
		{"prose mention", "usage:\n  note: offload stats [--json]", liveFixture, LiveUnsupported},
		{"malformed", liveHelp, `{"schema_version":`, LiveUnavailable},
		{"duplicate key", liveHelp, `{"schema_version":1,"schema_version":1}`, LiveUnavailable},
		{"future schema", liveHelp, `{"schema_version":2}`, LiveUnsupported},
		{"missing schema", liveHelp, `{}`, LiveUnsupported},
		{"missing timestamp", liveHelp, `{"schema_version":1}`, LiveUnavailable},
		{"invalid timestamp", liveHelp, `{"schema_version":1,"generated_at":"secret"}`, LiveUnavailable},
		{"oversize", liveHelp, strings.Repeat("x", maxLiveStatsBytes+1), LiveUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd, _, calls := liveCLI(t, tt.help, tt.body)
			got := liveReader(t, cmd).Read(context.Background(), managedRows())
			if got.State != tt.state || got.Stale || len(got.Activities) != 0 || got.SourceMachine != "test-host" || got.ObservedAt.IsZero() {
				t.Fatalf("result: %+v", got)
			}
			data, _ := os.ReadFile(calls)
			if strings.Contains(tt.name, "capability") || tt.name == "prose mention" {
				if string(data) != "-h\n" {
					t.Fatal("ran unsupported command")
				}
			}
		})
	}
	r := liveReader(t, filepath.Join(t.TempDir(), "missing"))
	if got := r.Read(context.Background(), nil); got.State != LiveUnavailable {
		t.Fatal(got.State)
	}
}

func TestLiveStatsMissingOptionalMetricsAndUnsafeLabels(t *testing.T) {
	t.Setenv("TEST_LIVE_TOKEN", "credential")
	fixture := `{"schema_version":1,"generated_at":"2026-10-29T19:00:00Z","active":[{"session":"managed","started_at":"2026-10-29T18:00:01Z","model":"/private/model","profile":"credential","phase":"command --secret"}]}`
	cmd, _, _ := liveCLI(t, liveHelp, fixture)
	got := liveReader(t, cmd).Read(context.Background(), managedRows())
	if got.State != LiveAvailable || len(got.Activities) != 1 {
		t.Fatal(got.State)
	}
	a := got.Activities[0]
	if a.ToolCalls != nil || a.Stalled != nil || a.UpdatedAt != nil || a.LastEventAt != nil || a.Profile != "" || a.Model != "" || a.Status != "" {
		t.Fatalf("unknown/unsafe values: %+v", a)
	}
}

func TestLiveStatsSingleFlightAndStale(t *testing.T) {
	cmd, fixture, calls := liveCLI(t, liveHelp, liveFixture)
	r := liveReader(t, cmd)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := r.Read(context.Background(), managedRows()); got.State != LiveAvailable {
				t.Errorf("state: %s", got.State)
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(calls)
	if string(data) != "-h\nstats --json\n" {
		t.Fatalf("requests not coalesced: %q", data)
	}
	good := r.Read(context.Background(), managedRows())
	if err := os.WriteFile(fixture, []byte("invalid secret result"), 0600); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.expires = time.Time{}
	r.mu.Unlock()
	got := r.Read(context.Background(), managedRows())
	if got.State != LiveUnavailable || !got.Stale || len(got.Activities) != 1 || !got.ObservedAt.Equal(good.ObservedAt) || !got.GeneratedAt.Equal(*good.GeneratedAt) {
		t.Fatalf("last good lost: %+v", got)
	}
	if len(r.Read(context.Background(), nil).Activities) != 0 {
		t.Fatal("stale cache leaked tasks")
	}
	// A successful refresh clears stale state.
	if err := os.WriteFile(fixture, []byte(liveFixture), 0600); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.expires = time.Time{}
	r.mu.Unlock()
	if got = r.Read(context.Background(), managedRows()); got.State != LiveAvailable || got.Stale {
		t.Fatal("did not recover")
	}
}

func TestLiveStatsTimeoutAndCallerCancellation(t *testing.T) {
	for _, probe := range []bool{false, true} {
		t.Run(map[bool]string{false: "stats", true: "probe"}[probe], func(t *testing.T) {
			dir := t.TempDir()
			body := "if [ \"$1\" = -h ]; then printf 'usage:\\n  offload stats [--json]\\n'; exit 0; fi\nsleep 10\n"
			if probe {
				body = "sleep 10\n"
			}
			r := liveReader(t, writeScript(t, dir, "offload", body))
			r.timeout = 100 * time.Millisecond
			started := time.Now()
			got := r.Read(context.Background(), managedRows())
			if got.State != LiveUnavailable || time.Since(started) > 2*time.Second {
				t.Fatal("unbounded timeout", got.State)
			}
		})
	}
	cmd, _, _ := liveCLI(t, liveHelp, liveFixture)
	r := liveReader(t, cmd)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := r.Read(ctx, managedRows())
	if got.State != LiveUnavailable || got.SourceMachine != "test-host" || got.ObservedAt.IsZero() {
		t.Fatal("invalid canceled response")
	}
	if got = r.Read(context.Background(), managedRows()); got.State != LiveAvailable {
		t.Fatal("caller cancellation poisoned shared refresh")
	}
}

func TestLiveStatsConfiguredCommandAndProcessFailure(t *testing.T) {
	cmd, _, calls := liveCLI(t, liveHelp, liveFixture)
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", cmd)
	r := NewLiveStatsReader(Config{Command: "/missing-configured-command"})
	if got := r.Read(context.Background(), nil); got.State != LiveAvailable || len(got.Activities) != 0 {
		t.Fatal("configured environment command not used")
	}
	data, _ := os.ReadFile(calls)
	if string(data) != "-h\nstats --json\n" {
		t.Fatalf("unexpected command arguments: %q", data)
	}
	failure := writeScript(t, t.TempDir(), "offload", "if [ \"$1\" = -h ]; then printf 'usage:\\n  offload stats [--json]\\n'; exit 0; fi\necho 'private task and credential' >&2\nexit 9\n")
	got := liveReader(t, failure).Read(context.Background(), nil)
	raw, _ := json.Marshal(got)
	if got.State != LiveUnavailable || strings.Contains(string(raw), "credential") || strings.Contains(string(raw), "private") {
		t.Fatalf("process failure leaked diagnostics: %s", raw)
	}
}
