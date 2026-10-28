// Package execution implements the direct Offload CLI boundary.
package execution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/agentcapacity"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

const MaxRequestBytes = 4 << 20
const MaxInstructionsBytes = 256 << 10
const MaxEventBytes = 4 << 20
const MaxOutputBytes = 32 << 20

type Config = config.ExecutionSettings

func LoadConfig() (Config, error) {
	var c Config
	cfg, err := config.Load()
	if err == nil {
		c = cfg.Settings.Execution
	} else if os.Getenv("CLAWFLOW_OFFLOAD_PROFILE") == "" {
		return c, errors.New("Offload settings unavailable: configure settings.execution.profile or CLAWFLOW_OFFLOAD_PROFILE")
	}
	return WithEnvironment(c), nil
}

func WithEnvironment(c Config) Config {
	for _, v := range []struct {
		key string
		dst *string
	}{
		{"CLAWFLOW_OFFLOAD_COMMAND", &c.Command}, {"CLAWFLOW_OFFLOAD_PROFILE", &c.Profile}, {"CLAWFLOW_OFFLOAD_CODEX_SANDBOX", &c.CodexSandbox},
	} {
		if s, ok := os.LookupEnv(v.key); ok {
			*v.dst = s
		}
	}
	if c.Command == "" {
		c.Command = "offload"
	}
	return c
}

// ProfileForRole resolves a role override with the same environment precedence as execution.
func ProfileForRole(c Config, role string) string {
	profile := c.Profile
	if override := c.RoleProfiles[role]; override != "" {
		profile = override
	}
	if override, ok := os.LookupEnv("CLAWFLOW_OFFLOAD_PROFILE"); ok {
		profile = override
	}
	return profile
}

func ValidateConfig(c Config) error {
	command := c.Command
	if command == "" {
		command = "offload"
	}
	if _, err := exec.LookPath(command); err != nil {
		return errors.New("Offload unavailable: install offload or set settings.execution.command / CLAWFLOW_OFFLOAD_COMMAND")
	}
	if !validProfile(c.Profile) {
		return errors.New("Offload readiness: configure an explicit settings.execution.profile / CLAWFLOW_OFFLOAD_PROFILE")
	}
	for _, profile := range c.RoleProfiles {
		if !validProfile(profile) {
			return errors.New("Offload readiness: invalid role profile")
		}
	}
	if c.CodexSandbox != "" && c.CodexSandbox != "workspace-write" && c.CodexSandbox != "danger-full-access" {
		return errors.New("Offload readiness: codex_sandbox must be workspace-write or danger-full-access")
	}
	return nil
}

func validProfile(profile string) bool {
	return strings.TrimSpace(profile) != "" && !strings.ContainsAny(profile, "\x00\r\n")
}

func Ready() error {
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	return ValidateConfig(c)
}

// Request is an internal admission record, not a payload sent to a provider.
type Request struct {
	Version                                                int
	RequestID, SessionKey                                  string
	Resume                                                 bool
	Workspace, Model, Instructions, Prompt, PermissionMode string
	ToolsDisabled                                          bool
	AllowedTools, DisallowedTools                          []string
	TimeoutMS                                              int64
	MaxBudgetUSD                                           float64
}

type Observation struct {
	Type            string          `json:"type"`
	Message         string          `json:"message,omitempty"`
	Delta           string          `json:"delta,omitempty"`
	Model           string          `json:"model,omitempty"`
	NativeSessionID string          `json:"native_session_id,omitempty"`
	Raw             json.RawMessage `json:"raw,omitempty"`
	Tool            json.RawMessage `json:"tool,omitempty"`
}

type Result struct {
	Status           string      `json:"status"`
	Text             string      `json:"result"`
	Model            string      `json:"model"`
	SessionKey       string      `json:"-"`
	NativeSessionID  string      `json:"session"`
	Profile          string      `json:"profile"`
	Backend          string      `json:"backend"`
	RunID            string      `json:"run_id"`
	CallerRunID      string      `json:"caller_run_id"`
	ExecutionPolicy  string      `json:"execution_policy"`
	ExitCode         int         `json:"exit_code"`
	ErrorKind        string      `json:"error_kind"`
	Error            string      `json:"error"`
	Usage            *TokenUsage `json:"usage"`
	CostUSD          *float64    `json:"cost_usd"`
	CostIsEstimate   bool        `json:"cost_is_estimate"`
	CostBasis        string      `json:"cost_basis"`
	CostUnavailable  string      `json:"cost_unavailable"`
	UsageUnavailable string      `json:"usage_unavailable"`
}

// TokenUsage contains this run's inclusive input counts, not cumulative session usage.
type TokenUsage struct {
	InputTokens           *int64 `json:"input_tokens"`
	CachedInputTokens     *int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
	OutputTokens          *int64 `json:"output_tokens"`
}

// Event contains sanitized metadata only; final answer text comes from stdout.
type Event struct {
	Version      int    `json:"schema_version"`
	Type         string `json:"type"`
	RunID        string `json:"run_id"`
	CallerRunID  string `json:"caller_run_id"`
	Seq          int    `json:"seq"`
	Timestamp    string `json:"timestamp"`
	Session      string `json:"session"`
	Backend      string `json:"backend"`
	Model        string `json:"model"`
	NativeType   string `json:"native_type,omitempty"`
	ToolCategory string `json:"tool_category,omitempty"`
	Status       string `json:"status,omitempty"`
	ExitCode     int    `json:"exit_code"`
	ErrorKind    string `json:"error_kind,omitempty"`
}

type Error struct{ Kind string }

func (e *Error) Error() string           { return "Offload execution: " + e.Kind }
func IsKind(err error, kind string) bool { var e *Error; return errors.As(err, &e) && e.Kind == kind }

func NewSessionKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func validateRequest(r Request) error {
	if r.Version != 1 || !idPattern.MatchString(r.RequestID) || (r.Resume && !idPattern.MatchString(r.SessionKey)) {
		return errors.New("Offload request: invalid caller identity or resume session")
	}
	if !filepath.IsAbs(r.Workspace) || strings.ContainsAny(r.Workspace, "\x00\r\n") {
		return errors.New("Offload request: workspace must be absolute")
	}
	st, err := os.Stat(r.Workspace)
	if err != nil || !st.IsDir() {
		return errors.New("Offload request: workspace directory unavailable")
	}
	if r.TimeoutMS <= 0 || r.TimeoutMS > 86400000 {
		return errors.New("Offload request: timeout must be 1ms..24h")
	}
	if r.PermissionMode != "default" && r.PermissionMode != "bypassPermissions" {
		return errors.New("Offload request: unsupported permission_mode")
	}
	if math.IsNaN(r.MaxBudgetUSD) || math.IsInf(r.MaxBudgetUSD, 0) || r.MaxBudgetUSD < 0 {
		return errors.New("Offload request: invalid max_budget_usd")
	}
	if r.MaxBudgetUSD > 0 {
		return &Error{Kind: "unsupported_budget"}
	}
	if r.ToolsDisabled && len(r.AllowedTools) > 0 {
		return errors.New("Offload request: tools_disabled conflicts with allowed_tools")
	}
	for _, t := range append(append([]string{}, r.AllowedTools...), r.DisallowedTools...) {
		if strings.TrimSpace(t) == "" || strings.ContainsAny(t, ",\x00\r\n") {
			return errors.New("Offload request: invalid tool restriction")
		}
	}
	if len(r.Instructions) > MaxInstructionsBytes || len(r.Prompt)+len(r.Instructions) > MaxRequestBytes {
		return &Error{Kind: "request_limit"}
	}
	return nil
}

// acquireOwnership holds advisory locks in a separate namespace from issue locks.
// Lock files remain in place; closing the descriptors or process death releases ownership.
func acquireOwnership(workspace, profile, session string, resume bool) (func(), error) {
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	workspace = canonical
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".clawflow", "execution-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	keys := []string{"workspace:" + workspace}
	if resume {
		keys = append(keys, "session:"+profile+":"+session)
	}
	var files []*os.File
	release := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, key := range keys {
		hash := sha256.Sum256([]byte(key))
		path := filepath.Join(dir, fmt.Sprintf("%x.lock", hash))
		f, err := acquireOwnershipFile(path)
		if err != nil {
			release()
			return nil, err
		}
		files = append(files, f)
		if err := f.Truncate(0); err != nil {
			release()
			return nil, &Error{Kind: "ownership_write"}
		}
		if _, err := fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
			release()
			return nil, &Error{Kind: "ownership_write"}
		}
	}

	return release, nil
}

// Run starts exactly one Offload process. Uncertain failures never replay work.
func Run(ctx context.Context, c Config, req Request, env []string, events io.Writer, observe func(Observation)) (Result, error) {
	var result Result
	if err := validateRequest(req); err != nil {
		return result, err
	}
	if err := ValidateConfig(c); err != nil {
		return result, err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	canonical, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil {
		return result, err
	}
	req.Workspace = canonical
	releaseSlot, err := agentcapacity.Acquire(ctx)
	if err != nil {
		return result, fmt.Errorf("acquire agent capacity: %w", err)
	}
	defer releaseSlot()
	release, err := acquireOwnership(req.Workspace, c.Profile, req.SessionKey, req.Resume)
	if err != nil {
		return result, err
	}
	defer release()
	dir, err := os.MkdirTemp("", "clawflow-offload-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	instructions := filepath.Join(dir, "instructions.md")
	if err := os.WriteFile(instructions, []byte(req.Instructions), 0600); err != nil {
		return result, err
	}
	eventPath := filepath.Join(dir, "events.ndjson") // Offload creates exclusively with mode 0600.
	args := []string{"-json", "-no-salvage", "-p", c.Profile, "-d", req.Workspace, "-t", (time.Duration(req.TimeoutMS) * time.Millisecond).String(), "-caller-run-id", req.RequestID, "-events-file", eventPath, "-instructions-file", instructions}
	if req.Resume {
		args = append(args, "-r", req.SessionKey)
	}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.PermissionMode == "bypassPermissions" {
		args = append(args, "-w", "-execution-policy", "managed-development")
		if c.CodexSandbox != "" {
			args = append(args, "-codex-sandbox", c.CodexSandbox)
		}
	}
	if req.ToolsDisabled {
		args = append(args, "-tools-disabled")
	}
	if len(req.AllowedTools) > 0 {
		args = append(args, "-allowed-tools", strings.Join(req.AllowedTools, ","))
	}
	if len(req.DisallowedTools) > 0 {
		args = append(args, "-disallowed-tools", strings.Join(req.DisallowedTools, ","))
	}
	command := c.Command
	if command == "" {
		command = "offload"
	}
	if env == nil {
		env = os.Environ()
	}
	if redact(req.Prompt, env) != req.Prompt || redact(req.Instructions, env) != req.Instructions || redact(strings.Join(args, " "), env) != strings.Join(args, " ") {
		return result, errors.New("Offload request contains an environment secret")
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return result, errors.New("Offload executable unavailable")
	}
	command, err = filepath.Abs(resolved)
	if err != nil {
		return result, errors.New("Offload executable path unavailable")
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = req.Workspace
	cmd.Env = env
	// File descriptors avoid os/exec copy goroutines whose pipes can be held
	// open by native descendants after Offload has already returned a result.
	taskInput, err := os.OpenFile(filepath.Join(dir, "task.txt"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	defer taskInput.Close()
	if _, err := io.WriteString(taskInput, req.Prompt); err != nil {
		return result, err
	}
	if _, err := taskInput.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	cmd.Stdin = taskInput
	nullOutput, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return result, err
	}
	defer nullOutput.Close()
	cmd.Stderr = nullOutput
	stdout, err := os.OpenFile(filepath.Join(dir, "stdout.json"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, err
	}
	defer stdout.Close()
	cmd.Stdout = stdout
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return result, &Error{Kind: "launch"}
	}
	registerActiveCmd(cmd)
	defer unregisterActiveCmd(cmd)
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	exceeded := false
	tail := newEventTail(eventPath, req, events, env, observe)
	defer tail.close()
	var eventErr error
	waiting := true
	for waiting {
		select {
		case waitErr = <-done:
			terminateProcessGroupSync(cmd, terminateGrace)
			waiting = false
		case <-runCtx.Done():
			terminateProcessGroupSync(cmd, terminateGrace)
			waitErr = <-done
			waiting = false
		case <-ticker.C:
			if eventErr == nil {
				eventErr = tail.poll()
				if eventErr != nil {
					cancel()
				}
			}
			if st, err := stdout.Stat(); err != nil {
				eventErr = &Error{Kind: "events_io"}
				cancel()
			} else if st.Size() > MaxOutputBytes {
				exceeded = true
				cancel()
			}

		}
	}

	// Drain only newly completed records, even on timeout or invalid stdout.
	if eventErr == nil {
		eventErr = tail.poll()
	}
	if eventErr != nil {
		return result, eventErr
	}
	// Preserve validated stream identity even if interruption prevents a final
	// event or stdout. Never recover answer text/outcomes from events.
	if tail.session != "" {
		result = Result{NativeSessionID: tail.session, SessionKey: tail.session, Profile: c.Profile, Backend: tail.backend, Model: tail.model, RunID: tail.runID, CallerRunID: req.RequestID}
	}
	if tail.final != nil {
		e := tail.final
		model := e.Model
		if model == "" {
			model = tail.model
		}
		result = Result{NativeSessionID: e.Session, SessionKey: e.Session, Profile: c.Profile, Backend: e.Backend, Model: model, RunID: e.RunID, CallerRunID: e.CallerRunID, Status: e.Status, ErrorKind: e.ErrorKind, ExitCode: e.ExitCode}
	}
	if exceeded {
		return result, &Error{Kind: "protocol_output_limit"}
	}
	if runCtx.Err() != nil {
		return result, runCtx.Err()
	}

	code := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			return result, &Error{Kind: "transport"}
		}
		code = ee.ExitCode()
	}
	if st, err := stdout.Stat(); err != nil || st.Size() > MaxOutputBytes {
		exceeded = true
	}
	if exceeded {
		return result, &Error{Kind: "protocol_output_limit"}
	}
	if _, err := stdout.Seek(0, io.SeekStart); err != nil {
		return result, &Error{Kind: "transport"}
	}
	output, err := io.ReadAll(io.LimitReader(stdout, MaxOutputBytes+1))
	if err != nil {
		return result, &Error{Kind: "transport"}
	}
	raw := bytes.TrimSpace(output)
	var decoded Result
	if !validJSON(raw) || json.Unmarshal(raw, &decoded) != nil || !hasFields(raw, "status", "exit_code", "profile", "run_id", "caller_run_id", "execution_policy") {
		if runCtx.Err() != nil {
			return result, runCtx.Err()
		}
		return result, &Error{Kind: "protocol_invalid_result"}
	}
	if decoded.Model == "" {
		decoded.Model = result.Model
	}
	result = decoded
	result.Text = redact(result.Text, env)
	result.Error = redact(result.Error, env)
	result.SessionKey = result.NativeSessionID
	eventErr = tail.finish(result)
	if runCtx.Err() != nil {
		return result, runCtx.Err()
	}
	if eventErr != nil {
		return result, eventErr
	}
	if !idPattern.MatchString(result.RunID) || result.CallerRunID != req.RequestID || result.Profile != c.Profile || (result.Status == "ok" && !idPattern.MatchString(result.NativeSessionID)) || (result.NativeSessionID != "" && !idPattern.MatchString(result.NativeSessionID)) || (req.Resume && result.NativeSessionID != "" && result.NativeSessionID != req.SessionKey) || result.ExitCode != code || (result.Status != "ok" && result.Status != "error" && result.Status != "timeout") || (result.Status == "ok" && (code != 0 || result.ErrorKind != "" || result.Error != "")) || (result.Status != "ok" && code == 0) {
		return result, &Error{Kind: "protocol_invalid_result"}
	}
	if req.PermissionMode == "bypassPermissions" && result.ExecutionPolicy != "managed-development" {
		return result, &Error{Kind: "protocol_policy_mismatch"}
	}
	if req.PermissionMode == "default" && result.ExecutionPolicy != "default" {
		return result, &Error{Kind: "protocol_policy_mismatch"}
	}
	if result.Status != "ok" {
		kind := safeKind(result.ErrorKind)
		if kind == "execution" && capacityMessage.MatchString(result.Error) {
			// Fallback until Offload exposes error_kind=capacity: only the fixed
			// kind is retained, never the raw provider message.
			kind = "capacity"
		}
		return result, &Error{Kind: kind}
	}
	return result, nil
}

// capacityMessage matches provider capacity/overload refusals reported under
// the generic "execution" kind by Offload versions without a capacity kind.
var capacityMessage = regexp.MustCompile(`(?i)\bat capacity\b|\boverloaded\b|\bcapacity (?:exceeded|limit)\b`)

func safeKind(s string) string {
	switch s {
	case "rate_limit", "capacity", "auth", "output_limit", "cost_limit", "budget_exceeded", "unsupported_budget", "unsupported_tools", "configuration", "session_busy", "unknown_session", "session_expired", "session_not_found", "ownership_mismatch", "backend_mismatch", "profile_mismatch", "resume_rejected", "context_exhausted", "context_limit", "context_length_exceeded", "timeout", "canceled", "execution", "launch", "events_io":
		return s
	}
	return "execution"
}

// PermissionMode preserves the old API spelling: default maps to read-only;
// bypassPermissions explicitly opts into Offload managed development delivery.
type Options struct {
	Workspace, Model, Role, Profile, Instructions, SessionKey, PermissionMode string
	Resume, ToolsDisabled                                                     bool
	AllowedTools, DisallowedTools                                             []string
	Timeout                                                                   time.Duration
	MaxBudgetUSD                                                              float64
	Events                                                                    io.Writer
	Provider                                                                  *config.ClaudeProvider // Retired: rejected instead of overriding profile authentication.
	OnSession                                                                 func(string)
	OnResult                                                                  func(Result)
}

func Execute(ctx context.Context, prompt string, opts Options) (Result, error) {
	return execute(ctx, prompt, opts, nil)
}
func execute(ctx context.Context, prompt string, opts Options, observe func(Observation)) (Result, error) {
	if opts.Provider != nil {
		return Result{}, errors.New("ClawFlow provider overrides are retired; configure an Offload profile with offload config")
	}
	if opts.MaxBudgetUSD > 0 {
		return Result{}, &Error{Kind: "unsupported_budget"}
	}
	c, err := LoadConfig()
	if err != nil {
		return Result{}, err
	}
	c.Profile = ProfileForRole(c, opts.Role)
	if opts.Profile != "" {
		c.Profile = opts.Profile
	} // Pin returned profile on resume.
	if opts.Workspace == "" {
		opts.Workspace, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	opts.Workspace, err = filepath.Abs(opts.Workspace)
	if err != nil {
		return Result{}, err
	}
	// Supply repository instructions explicitly instead of depending on a native
	// backend's automatic instruction discovery.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(opts.Workspace, name)
		f, openErr := os.Open(path)
		if os.IsNotExist(openErr) {
			continue
		}
		if openErr != nil {
			return Result{}, errors.New("Offload repository instructions unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, MaxInstructionsBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return Result{}, errors.New("Offload repository instructions unreadable")
		}
		if len(data) > MaxInstructionsBytes {
			return Result{}, &Error{Kind: "request_limit"}
		}
		opts.Instructions += "\n\nRepository instructions (" + name + "):\n" + string(data)
		if len(opts.Instructions) > MaxInstructionsBytes {
			return Result{}, &Error{Kind: "request_limit"}
		}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = time.Hour
	}
	if opts.PermissionMode == "" {
		opts.PermissionMode = "default"
	}
	req := Request{Version: 1, RequestID: NewSessionKey(), SessionKey: opts.SessionKey, Resume: opts.Resume, Workspace: opts.Workspace, Model: opts.Model, Instructions: opts.Instructions, Prompt: prompt, PermissionMode: opts.PermissionMode, ToolsDisabled: opts.ToolsDisabled, AllowedTools: opts.AllowedTools, DisallowedTools: opts.DisallowedTools, TimeoutMS: opts.Timeout.Milliseconds(), MaxBudgetUSD: opts.MaxBudgetUSD}
	// Credentials and models come only from Offload profiles or explicit model input.
	return Run(ctx, c, req, os.Environ(), opts.Events, observe)
}

func Chat(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	var err error
	if opts.Workspace == "" {
		opts.Workspace, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	opts.Workspace, err = filepath.Abs(opts.Workspace)
	if err != nil {
		return err
	}
	opts.Workspace, err = filepath.EvalSymlinks(opts.Workspace)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Ready(); err != nil {
		return err
	}
	type inputLine struct {
		text string
		err  error
		eof  bool
	}
	lines := make(chan inputLine)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 4096), MaxRequestBytes)
		for sc.Scan() {
			select {
			case lines <- inputLine{text: sc.Text()}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case lines <- inputLine{err: sc.Err(), eof: true}:
		case <-ctx.Done():
		}
	}()
	for {
		fmt.Fprint(out, "> ")
		var line inputLine
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line = <-lines:
		}
		if line.eof {
			return line.err
		}
		if line.text == "/exit" || line.text == "/quit" {
			return nil
		}
		if strings.TrimSpace(line.text) == "" {
			continue
		}
		r, err := execute(ctx, line.text, opts, nil)
		if r.Text != "" {
			fmt.Fprintln(out, r.Text)
		}
		if err != nil {
			return err
		}
		opts.SessionKey = r.NativeSessionID
		opts.Profile = r.Profile
		opts.Resume = true
		if opts.OnResult != nil {
			opts.OnResult(r)
		}
		if opts.OnSession != nil {
			opts.OnSession(r.NativeSessionID)
		}
	}
}

func redact(s string, env []string) string {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.ToUpper(k)
		if ok && v != "" && (strings.HasSuffix(k, "_TOKEN") || strings.HasSuffix(k, "_KEY") || strings.HasSuffix(k, "_SECRET") || strings.HasSuffix(k, "_PASSWORD")) {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

func validJSON(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	var value func() bool
	value = func() bool {
		t, err := d.Token()
		if err != nil {
			return false
		}
		if delim, ok := t.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return false
					}
					key, ok := k.(string)
					if !ok || seen[key] {
						return false
					}
					seen[key] = true
					if !value() {
						return false
					}
				}
				end, err := d.Token()
				return err == nil && end == json.Delim('}')
			case '[':
				for d.More() {
					if !value() {
						return false
					}
				}
				end, err := d.Token()
				return err == nil && end == json.Delim(']')
			default:
				return false
			}
		}
		return true
	}
	if !value() {
		return false
	}
	_, err := d.Token()
	return errors.Is(err, io.EOF)
}

func redactEvidence(line []byte, env []string) string {
	var v any
	if json.Unmarshal(line, &v) != nil {
		return redact(string(line), env)
	}
	changed := false
	var visit func(any) any
	visit = func(value any) any {
		switch x := value.(type) {
		case string:
			y := redact(x, env)
			changed = changed || y != x
			return y
		case []any:
			for i, item := range x {
				x[i] = visit(item)
			}
		case map[string]any:
			for k, item := range x {
				x[k] = visit(item)
			}
		}
		return value
	}
	visit(v)
	if changed {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return string(line)
}

func hasFields(raw []byte, keys ...string) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false
	}
	for _, k := range keys {
		v, ok := obj[k]
		if !ok || bytes.Equal(v, []byte("null")) {
			return false
		}
	}
	return true
}
