package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

const (
	LiveAvailable     = "available"
	LiveUnavailable   = "unavailable"
	LiveUnsupported   = "unsupported"
	maxLiveStatsBytes = 1 << 20
)

// LiveActivity is an explicit presentation allowlist, never a raw Offload record.
// Nil counters and signals mean unknown, not zero or false.
type LiveActivity struct {
	Status      string     `json:"status,omitempty"`
	Profile     string     `json:"profile,omitempty"`
	Model       string     `json:"model,omitempty"`
	Session     string     `json:"session"`
	Machine     string     `json:"machine"`
	StartedAt   *time.Time `json:"started_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
	LastEventAt *time.Time `json:"last_event_at"`
	ToolCalls   *int64     `json:"tool_calls"`
	Stalled     *bool      `json:"stalled"`
}

// LiveStats describes only this host. It is not a remote-worker inventory or
// usage ledger; those remain backed by existing attributed store/worker APIs.
type LiveStats struct {
	State         string         `json:"state"`
	Reason        string         `json:"reason,omitempty"`
	SourceMachine string         `json:"source_machine"`
	ObservedAt    time.Time      `json:"observed_at"`
	GeneratedAt   *time.Time     `json:"generated_at"`
	Stale         bool           `json:"stale"`
	Activities    []LiveActivity `json:"activities"`
}

// LiveStatsReader coalesces host reads. Construct once per configured command,
// then reuse it across requests. No execution/profile/model arguments are sent.
type LiveStatsReader struct {
	config       Config
	machine      string
	timeout, ttl time.Duration
	mu           sync.Mutex
	pending      chan struct{}
	cached       LiveStats
	expires      time.Time
}

func NewLiveStatsReader(c Config) *LiveStatsReader {
	machine, _ := os.Hostname()
	return &LiveStatsReader{config: WithEnvironment(c), machine: liveIdentifier(machine), timeout: 3 * time.Second, ttl: 5 * time.Second}
}

// Read accepts already-authorized rows from Store.Runs (including pilot rows if
// appropriate). Filter on every request, including cache hits: a cached host
// response must never disclose personal tasks or another request's scope.
// A missing session/machine attribution fails closed. Resumed sessions outside
// a recorded attempt's time interval are not attributed to that attempt.
func (r *LiveStatsReader) Read(ctx context.Context, rows []store.Run) LiveStats {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if time.Now().Before(r.expires) {
		result := r.filtered(r.cached, rows)
		r.mu.Unlock()
		return result
	}
	if r.pending == nil {
		r.pending = make(chan struct{})
		go r.refresh()
	}
	pending := r.pending
	r.mu.Unlock()
	select {
	case <-pending:
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.filtered(r.cached, rows)
	case <-ctx.Done():
		r.mu.Lock()
		defer r.mu.Unlock()
		result := r.filtered(r.cached, rows)
		result.State, result.Reason = LiveUnavailable, "request canceled"
		result.Stale = result.GeneratedAt != nil
		return result
	}
}

func (r *LiveStatsReader) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	result := r.fetch(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if result.State != LiveAvailable && r.cached.GeneratedAt != nil {
		result.GeneratedAt = r.cached.GeneratedAt
		result.ObservedAt = r.cached.ObservedAt
		result.Activities = r.cached.Activities
		result.Stale = true
	}
	r.cached = result
	r.expires = time.Now().Add(r.ttl)
	close(r.pending)
	r.pending = nil
}

func (r *LiveStatsReader) filtered(result LiveStats, rows []store.Run) LiveStats {
	result.SourceMachine = r.machine
	if result.ObservedAt.IsZero() {
		result.ObservedAt = time.Now().UTC()
	}
	result.GeneratedAt = copyLiveValue(result.GeneratedAt)
	activities := make([]LiveActivity, 0)
	for _, a := range result.Activities {
		for _, row := range rows {
			if r.machine == "" || row.Machine != r.machine || row.SessionID != a.Session || a.Session == "" || a.StartedAt == nil || row.StartedAt.IsZero() || a.StartedAt.Before(row.StartedAt) || (row.EndedAt != nil && a.StartedAt.After(*row.EndedAt)) {
				continue
			}
			a.StartedAt, a.UpdatedAt, a.LastEventAt = copyLiveValue(a.StartedAt), copyLiveValue(a.UpdatedAt), copyLiveValue(a.LastEventAt)
			a.ToolCalls, a.Stalled = copyLiveValue(a.ToolCalls), copyLiveValue(a.Stalled)
			activities = append(activities, a)
			break
		}
	}
	result.Activities = activities
	return result
}

func copyLiveValue[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}

var statsDeclaration = regexp.MustCompile(`^\s*offload stats \[--json\](?:\s|$)`)

func (r *LiveStatsReader) fetch(ctx context.Context) LiveStats {
	result := LiveStats{State: LiveUnavailable, SourceMachine: r.machine, ObservedAt: time.Now().UTC(), Activities: []LiveActivity{}}
	if r.machine == "" {
		result.Reason = "source machine unavailable"
		return result
	}
	command, err := exec.LookPath(r.config.Command)
	if err == nil {
		command, err = filepath.Abs(command)
	}
	if err != nil {
		result.Reason = "Offload command unavailable"
		return result
	}
	help, problem := runHelpProbe(ctx, command)
	if problem != "" {
		result.Reason = "Offload capability probe unavailable"
		return result
	}
	supported := false
	if validateHelp(help) == "" {
		for _, line := range helpLines(help) {
			if statsDeclaration.MatchString(line) {
				supported = true
			}
		}
	}
	if !supported {
		result.State, result.Reason = LiveUnsupported, "Offload stats JSON capability not declared"
		return result
	}
	output, ok := runLiveStats(ctx, command)
	if !ok {
		result.Reason = "Offload stats unavailable"
		return result
	}
	// Ignore all aggregate, project, recent-task and unknown fields. Global
	// totals include personal activity and cannot be presented as ClawFlow usage.
	var wire struct {
		SchemaVersion int        `json:"schema_version"`
		GeneratedAt   *time.Time `json:"generated_at"`
		Active        []struct {
			Phase       string     `json:"phase"`
			Profile     string     `json:"profile"`
			Model       string     `json:"model"`
			Session     string     `json:"session"`
			StartedAt   *time.Time `json:"started_at"`
			UpdatedAt   *time.Time `json:"updated_at"`
			LastEventAt *time.Time `json:"last_event_at"`
			ToolCalls   *int64     `json:"tool_calls"`
			Stalled     *bool      `json:"stalled"`
		} `json:"active"`
	}
	if !validJSON(output) || json.Unmarshal(output, &wire) != nil {
		result.Reason = "invalid Offload stats JSON"
		return result
	}
	if wire.SchemaVersion != 1 {
		result.State, result.Reason = LiveUnsupported, "unsupported Offload stats schema"
		return result
	}
	if wire.GeneratedAt == nil || wire.GeneratedAt.IsZero() {
		result.Reason = "Offload observation timestamp missing"
		return result
	}
	result.GeneratedAt = wire.GeneratedAt
	for _, a := range wire.Active {
		if a.ToolCalls != nil && *a.ToolCalls < 0 {
			a.ToolCalls = nil
		}
		result.Activities = append(result.Activities, LiveActivity{Status: liveIdentifier(a.Phase), Profile: liveIdentifier(a.Profile), Model: liveIdentifier(a.Model), Session: liveIdentifier(a.Session), Machine: r.machine, StartedAt: a.StartedAt, UpdatedAt: a.UpdatedAt, LastEventAt: a.LastEventAt, ToolCalls: a.ToolCalls, Stalled: a.Stalled})
	}
	result.State = LiveAvailable
	result.ObservedAt = time.Now().UTC()
	return result
}

// Restrict labels to bounded identifiers; never publish arbitrary text, paths,
// URLs, command lines or environment credentials even inside allowlisted fields.
func liveIdentifier(s string) string {
	if !idPattern.MatchString(s) || redact(s, os.Environ()) != s {
		return ""
	}
	return s
}

// runLiveStats uses the host's normal environment so Offload reads its public
// runtime. Stdout is bounded; stderr and all raw errors stay private. The only
// permitted arguments are the declared read-only stats contract.
func runLiveStats(ctx context.Context, command string) ([]byte, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	out := newBoundedBuffer(maxLiveStatsBytes)
	cmd := exec.Command(command, "stats", "--json")
	cmd.Stdout = out
	cmd.Stdin = nil
	cmd.WaitDelay = 100 * time.Millisecond
	setProcessGroup(cmd)
	if cmd.Start() != nil {
		return nil, false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	case <-out.over:
		err = context.Canceled
	}
	terminateProcessGroupSync(cmd, 100*time.Millisecond)
	if err != nil {
		return nil, false
	}
	select {
	case <-out.over:
		return nil, false
	default:
	}
	return out.bytes(), true
}
