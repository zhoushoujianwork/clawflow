// Package execution is ClawFlow's execution-only Agent-Host protocol client.
// It neither discovers Host homes nor launches a native AI backend itself.
package execution

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
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

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

const MaxRequestBytes = 4 << 20
const MaxEventBytes = 4 << 20
const MaxOutputBytes = 32 << 20

// Config uses only explicitly supplied machine-local runtime paths.
type Config = config.ExecutionSettings

func LoadConfig() (Config, error) {
	var c Config
	// Fully explicit environment settings work without a ClawFlow config file.
	if os.Getenv("CLAWFLOW_AGH_CONFIG") == "" || os.Getenv("CLAWFLOW_AGH_NATIVE") == "" || os.Getenv("CLAWFLOW_AGH_PROFILE") == "" {
		cfg, err := config.Load()
		if err != nil {
			return Config{}, errors.New("AGH settings unavailable: configure settings.execution or all CLAWFLOW_AGH_CONFIG/NATIVE/PROFILE paths")
		}
		c = cfg.Settings.Execution
	}
	return WithEnvironment(c), nil
}

// WithEnvironment applies only the four explicit execution overrides without reading files.
func WithEnvironment(c Config) Config {
	for _, v := range []struct {
		key string
		dst *string
	}{
		{"CLAWFLOW_AGH_COMMAND", &c.Command}, {"CLAWFLOW_AGH_CONFIG", &c.ConfigPath},
		{"CLAWFLOW_AGH_NATIVE", &c.NativeCommand}, {"CLAWFLOW_AGH_PROFILE", &c.NativeProfile},
	} {
		if s, ok := os.LookupEnv(v.key); ok {
			*v.dst = s
		}
	}
	if c.Command == "" {
		c.Command = "agent-host"
	}
	return c
}

func ValidateConfig(c Config) error {
	command := c.Command
	if command == "" {
		command = "agent-host"
	}
	if _, err := exec.LookPath(command); err != nil {
		return errors.New("AGH unavailable: install agent-host or set settings.execution.command / CLAWFLOW_AGH_COMMAND")
	}
	for _, p := range []struct {
		name, path      string
		dir, executable bool
	}{
		{"config_path / CLAWFLOW_AGH_CONFIG", c.ConfigPath, false, false},
		{"native_command / CLAWFLOW_AGH_NATIVE", c.NativeCommand, false, true},
		{"native_profile / CLAWFLOW_AGH_PROFILE", c.NativeProfile, true, false},
	} {
		if !filepath.IsAbs(p.path) || strings.ContainsAny(p.path, "\x00\r\n") {
			return fmt.Errorf("AGH readiness: settings.execution.%s must be an explicit absolute path", p.name)
		}
		st, err := os.Stat(p.path)
		if err != nil || st.IsDir() != p.dir || (!p.dir && !st.Mode().IsRegular()) {
			return fmt.Errorf("AGH readiness: settings.execution.%s is unavailable or has the wrong type", p.name)
		}
		if p.executable && st.Mode().Perm()&0111 == 0 {
			return errors.New("AGH readiness: native_command must be executable")
		}
	}
	return nil
}

func Ready() error {
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	return ValidateConfig(c)
}

// Request contains no auth fields. Credentials travel only in the child environment.
type Request struct {
	Version         int      `json:"version"`
	RequestID       string   `json:"request_id"`
	SessionKey      string   `json:"session_key"`
	Resume          bool     `json:"resume"`
	Workspace       string   `json:"workspace"`
	Model           string   `json:"model"`
	Instructions    string   `json:"instructions"`
	Prompt          string   `json:"prompt"`
	PermissionMode  string   `json:"permission_mode"`
	ToolsDisabled   bool     `json:"tools_disabled"`
	AllowedTools    []string `json:"allowed_tools"`
	DisallowedTools []string `json:"disallowed_tools"`
	TimeoutMS       int64    `json:"timeout_ms"`
	MaxBudgetUSD    float64  `json:"max_budget_usd,omitempty"`
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
	Status          string          `json:"status"`
	Text            string          `json:"text"`
	Model           string          `json:"model"`
	SessionKey      string          `json:"session_key"`
	NativeSessionID string          `json:"native_session_id"`
	ErrorKind       string          `json:"error_kind"`
	Error           string          `json:"error"`
	Usage           json.RawMessage `json:"usage"`
}
type Event struct {
	Version     int          `json:"version"`
	RequestID   string       `json:"request_id"`
	Seq         int          `json:"seq"`
	Type        string       `json:"type"`
	Observation *Observation `json:"observation,omitempty"`
	Result      *Result      `json:"result,omitempty"`
}

// Error exposes stable protocol classifications; arbitrary native prose never drives retries.
type Error struct{ Kind string }

func (e *Error) Error() string           { return "AGH execution: " + e.Kind }
func IsKind(err error, kind string) bool { var e *Error; return errors.As(err, &e) && e.Kind == kind }

func NewSessionKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func validateRequest(r Request) error {
	if r.Version != 1 || !idPattern.MatchString(r.RequestID) || !uuidPattern.MatchString(r.SessionKey) {
		return errors.New("AGH request: invalid version, request_id or session_key")
	}
	if !filepath.IsAbs(r.Workspace) || strings.ContainsAny(r.Workspace, "\x00\r\n") {
		return errors.New("AGH request: workspace must be absolute")
	}
	st, err := os.Stat(r.Workspace)
	if err != nil || !st.IsDir() {
		return errors.New("AGH request: workspace directory unavailable")
	}
	if r.TimeoutMS <= 0 || r.TimeoutMS > 86400000 {
		return errors.New("AGH request: timeout_ms must be 1..86400000")
	}
	if r.PermissionMode != "default" && r.PermissionMode != "bypassPermissions" {
		return errors.New("AGH request: unsupported permission_mode")
	}
	if math.IsNaN(r.MaxBudgetUSD) || math.IsInf(r.MaxBudgetUSD, 0) || r.MaxBudgetUSD < 0 {
		return errors.New("AGH request: invalid max_budget_usd")
	}
	// The current execution contract's ACP path explicitly rejects budgets before native launch.
	// Forward valid budgets unchanged: AGH owns capability admission and must fail unsupported_budget.
	if r.ToolsDisabled && len(r.AllowedTools) > 0 {
		return errors.New("AGH request: tools_disabled conflicts with allowed_tools")
	}
	for _, t := range append(append([]string{}, r.AllowedTools...), r.DisallowedTools...) {
		if strings.TrimSpace(t) == "" || strings.ContainsAny(t, "\x00\r\n") {
			return errors.New("AGH request: invalid tool restriction")
		}
	}
	return nil
}

// Run starts exactly one AGH child. Parse/transport failures never cause replay.
func Run(ctx context.Context, c Config, req Request, env []string, events io.Writer, observe func(Observation)) (Result, error) {
	var result Result
	if err := validateRequest(req); err != nil {
		return result, err
	}
	if err := ValidateConfig(c); err != nil {
		return result, err
	}
	if req.AllowedTools == nil {
		req.AllowedTools = []string{}
	}
	if req.DisallowedTools == nil {
		req.DisallowedTools = []string{}
	}
	payload, err := json.Marshal(req)
	if err != nil || len(payload) > MaxRequestBytes {
		return result, errors.New("AGH request exceeds 4 MiB or cannot be encoded")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
	defer cancel()
	command := c.Command
	if command == "" {
		command = "agent-host"
	}
	cmd := exec.Command(command, "execute", "--execution-config", c.ConfigPath, "--native", c.NativeCommand, "--native-profile", c.NativeProfile)
	cmd.Dir = req.Workspace
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = env
	if redactEvidence(payload, env) != string(payload) || redact(c.Command, env) != c.Command || redact(c.ConfigPath, env) != c.ConfigPath || redact(c.NativeCommand, env) != c.NativeCommand || redact(c.NativeProfile, env) != c.NativeProfile {
		return result, errors.New("AGH request contains an environment secret; remove credentials from prompt/instructions")
	}
	cmd.Stdin = bytes.NewReader(payload)
	// Native stderr can include secrets. Never relay or persist it.
	cmd.Stderr = io.Discard
	setProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, errors.New("AGH stdout unavailable")
	}
	if err = cmd.Start(); err != nil {
		return result, errors.New("AGH start failed; check command and execution settings")
	}
	registerActiveCmd(cmd)
	defer unregisterActiveCmd(cmd)
	done := make(chan struct{})
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		select {
		case <-ctx.Done():
			terminateProcessGroupSync(cmd, terminateGrace)
			_ = stdout.Close()
		case <-done:
		}
	}()
	result, parseErr := parseStream(stdout, req, events, env, observe)
	externalErr := ctx.Err()
	if parseErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	close(done)
	<-guardDone
	if externalErr != nil {
		return result, externalErr
	}
	if parseErr != nil {
		return result, parseErr
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	code := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			return result, &Error{Kind: "transport"}
		}
		code = ee.ExitCode()
	}
	expected := 0
	switch result.Status {
	case "completed":
		expected = 0
	case "failed":
		expected = 1
	case "canceled":
		expected = 130
		if result.ErrorKind == "timeout" {
			expected = 124
		}
	}
	if result.Status == "failed" && code == 2 {
		expected = 2
	}
	if code != expected {
		return result, &Error{Kind: "protocol_exit_mismatch"}
	}
	if result.Status != "completed" {
		return result, &Error{Kind: safeKind(result.ErrorKind)}
	}
	return result, nil
}

func safeKind(s string) string {
	switch s {
	case "rate_limit", "auth", "output_limit", "cost_limit", "budget_exceeded", "unsupported_budget", "unsupported_tools", "configuration", "session_busy", "timeout", "canceled", "execution":
		return s
	}
	return "execution"
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

func parseStream(r io.Reader, req Request, events io.Writer, env []string, observe func(Observation)) (result Result, parseErr error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), MaxEventBytes)
	seq, total := 0, 0
	final := false
	observedNative := ""
	var partial strings.Builder
	defer func() {
		if result.Text == "" {
			result.Text = partial.String()
		}
		result.Text = redact(result.Text, env)
		result.Error = redact(result.Error, env)
	}()
	for sc.Scan() {
		line := sc.Bytes()
		total += len(line) + 1
		if total > MaxOutputBytes {
			return result, &Error{Kind: "protocol_output_limit"}
		}
		// Persist actual AGH evidence, redacted at the boundary, without fabricating native envelopes.
		if events != nil {
			if _, err := io.WriteString(events, redactEvidence(line, env)+"\n"); err != nil {
				return result, &Error{Kind: "evidence_write"}
			}
		}
		var e Event
		if !validJSON(line) || json.Unmarshal(line, &e) != nil || e.Version != 1 || e.RequestID != req.RequestID || e.Seq != seq+1 || final {
			return result, &Error{Kind: "protocol_invalid_event"}
		}
		var envelope map[string]json.RawMessage
		_ = json.Unmarshal(line, &envelope)
		if _, ok := envelope["result"]; e.Type == "observation" && ok {
			return result, &Error{Kind: "protocol_invalid_observation"}
		}
		if _, ok := envelope["observation"]; e.Type == "result" && ok {
			return result, &Error{Kind: "protocol_invalid_result"}
		}
		seq++
		switch e.Type {
		case "observation":
			if e.Observation == nil || e.Result != nil || e.Observation.Type == "" {
				return result, &Error{Kind: "protocol_invalid_observation"}
			}
			o := *e.Observation
			if o.NativeSessionID != "" {
				if observedNative != "" && observedNative != o.NativeSessionID {
					return result, &Error{Kind: "protocol_native_session_mismatch"}
				}
				observedNative = o.NativeSessionID
			}
			partial.WriteString(o.Delta)
			if observe != nil {
				o.Message = redact(o.Message, env)
				o.Delta = redact(o.Delta, env)
				observe(o)
			}
		case "result":
			if e.Result == nil || e.Observation != nil {
				return result, &Error{Kind: "protocol_invalid_result"}
			}
			result = *e.Result
			var fields struct {
				Result map[string]json.RawMessage `json:"result"`
			}
			_ = json.Unmarshal(line, &fields)
			for _, key := range []string{"status", "text", "model", "session_key", "native_session_id", "error_kind", "error", "usage"} {
				v, ok := fields.Result[key]
				if !ok || (key != "usage" && bytes.Equal(v, []byte("null"))) {
					return result, &Error{Kind: "protocol_invalid_result"}
				}
			}
			if (observedNative != "" && result.NativeSessionID != observedNative) || result.SessionKey != req.SessionKey || (result.Status != "completed" && result.Status != "failed" && result.Status != "canceled") || len(result.Usage) == 0 || string(result.Usage) != "null" || (result.Status == "completed" && (result.ErrorKind != "" || result.Error != "")) {
				return result, &Error{Kind: "protocol_invalid_result"}
			}
			final = true
		default:
			return result, &Error{Kind: "protocol_unknown_event"}
		}
	}
	if sc.Err() != nil {
		return result, &Error{Kind: "protocol_read"}
	}
	if !final {
		return result, &Error{Kind: "protocol_missing_result"}
	}
	return result, nil
}

// Options is shared by one-shot execution and the terminal conversation loop.
type Options struct {
	Workspace, Model, Role, Instructions, SessionKey, PermissionMode string
	Resume, ToolsDisabled                                            bool
	AllowedTools, DisallowedTools                                    []string
	Timeout                                                          time.Duration
	MaxBudgetUSD                                                     float64
	Events                                                           io.Writer
	Provider                                                         *config.ClaudeProvider // explicit probe/auth choice; never a fallback chain
}

func Execute(ctx context.Context, prompt string, opts Options) (Result, error) {
	c, err := LoadConfig()
	if err != nil {
		return Result{}, err
	}
	if opts.Workspace == "" {
		opts.Workspace, err = os.Getwd()
		if err != nil {
			return Result{}, err
		}
	}
	opts.Workspace, err = filepath.Abs(opts.Workspace)
	if err != nil {
		return Result{}, errors.New("AGH workspace unavailable")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = time.Hour
	}
	if opts.SessionKey == "" {
		opts.SessionKey = NewSessionKey()
	}
	if opts.PermissionMode == "" {
		opts.PermissionMode = "default"
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return Result{}, errors.New("execution credentials unavailable; check credentials.yaml")
	}
	model := opts.Model
	if model == "" {
		model = config.ResolveModelForRole(creds, opts.Role)
	}
	env := os.Environ()
	apiKey, baseURL := "", ""
	if opts.Provider != nil {
		apiKey, baseURL = opts.Provider.APIKey, opts.Provider.BaseURL
	} else {
		policy := *creds
		if len(policy.ClaudeProviders) > 0 {
			policy.ClaudeAPIKey = ""
			policy.ClaudeBaseURL = ""
		}
		apiKey, baseURL = config.ResolveClaudeCredentials(&policy)
	}
	// No explicit ClawFlow provider means AGH's native profile owns native auth.
	cleanEnv := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.HasPrefix(kv, "ANTHROPIC_AUTH_TOKEN=") || strings.HasPrefix(kv, "ANTHROPIC_BASE_URL=") {
			continue
		}
		cleanEnv = append(cleanEnv, kv)
	}
	env = EnvWithCredentials(cleanEnv, apiKey, baseURL)
	req := Request{Version: 1, RequestID: NewSessionKey(), SessionKey: opts.SessionKey, Resume: opts.Resume, Workspace: opts.Workspace, Model: model, Instructions: opts.Instructions, Prompt: prompt, PermissionMode: opts.PermissionMode, ToolsDisabled: opts.ToolsDisabled, AllowedTools: opts.AllowedTools, DisallowedTools: opts.DisallowedTools, TimeoutMS: opts.Timeout.Milliseconds(), MaxBudgetUSD: opts.MaxBudgetUSD}
	return Run(ctx, c, req, env, opts.Events, func(o Observation) {
		if o.Delta != "" {
			fmt.Fprint(os.Stderr, o.Delta)
		}
	})
}

// Chat owns a plain terminal loop. Each successful turn resumes the same logical session.
func Chat(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Ready(); err != nil {
		return err
	}
	if opts.SessionKey == "" {
		opts.SessionKey = NewSessionKey()
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
		prompt := line.text
		if prompt == "/exit" || prompt == "/quit" {
			return nil
		}
		if strings.TrimSpace(prompt) == "" {
			continue
		}
		r, err := Execute(ctx, prompt, opts)
		if r.Text != "" {
			fmt.Fprintln(out, r.Text)
		}
		if err != nil {
			return err
		}
		opts.Resume = true
	}
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
