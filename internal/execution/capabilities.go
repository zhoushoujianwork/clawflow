package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
)

// CapabilitySchemaVersion is the Report schema version.
const CapabilitySchemaVersion = 1

// MaxProbeOutputBytes caps combined help output.
const MaxProbeOutputBytes = 128 << 10

// Timeout, grace and wait delay are variables only so tests can shorten them.
var (
	probeTimeout   = 3 * time.Second
	probeGrace     = 300 * time.Millisecond
	probeWaitDelay = 500 * time.Millisecond
)

// Report statuses.
const (
	StatusReady   = "ready"
	StatusBlocked = "blocked"
)

// Check statuses.
const (
	CheckPassed  = "passed"
	CheckBlocked = "blocked"
	CheckSkipped = "skipped"
)

// Stable capability check identifiers.
const (
	CheckConfig           = "config"
	CheckCommand          = "command"
	CheckHelpProbe        = "help_probe"
	CheckFlagDeclarations = "flag_declarations"
)

// Check is one stable-ID capability result. Messages are fixed or sanitized
// strings; they never contain raw subprocess output.
type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Report is the structured outcome of ProbeCapabilities.
type Report struct {
	SchemaVersion int      `json:"schema_version"`
	Status        string   `json:"status"`
	Command       string   `json:"command"`
	Profile       string   `json:"profile"`
	Checks        []Check  `json:"checks"`
	Unverified    []string `json:"unverified"`
}

// RequiredFlags lists the flags ClawFlow may pass to Offload at execution time.
var RequiredFlags = []string{
	"json", "no-salvage", "p", "d", "t", "caller-run-id", "events-file", "instructions-file",
	"execution-policy", "w", "tools-disabled", "allowed-tools", "disallowed-tools",
	"codex-sandbox", "r", "m",
}

func newReport(profile string) Report {
	r := Report{
		SchemaVersion: CapabilitySchemaVersion,
		Status:        StatusBlocked,
		Profile:       sanitizeText(profile, 128),
		Unverified:    []string{"provider_execution", "permission_enforcement"},
	}
	for _, id := range []string{CheckConfig, CheckCommand, CheckHelpProbe, CheckFlagDeclarations} {
		r.Checks = append(r.Checks, Check{ID: id, Status: CheckSkipped, Message: "not evaluated"})
	}
	return r
}

func (r *Report) set(id, status, message string) {
	for i := range r.Checks {
		if r.Checks[i].ID == id {
			r.Checks[i].Status = status
			r.Checks[i].Message = sanitizeText(message, 200)
		}
	}
}

// ConfigBlockedReport builds a blocked Report for a failure that happened
// before a Config could be assembled. The message is sanitized and bounded.
func ConfigBlockedReport(message string) Report {
	r := newReport("")
	r.set(CheckConfig, CheckBlocked, message)
	return r
}

// sanitizeText replaces non-printable runes and bounds the length in runes.
func sanitizeText(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		if !unicode.IsPrint(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// ProbeCapabilities runs a bounded, local capability check of the configured
// Offload command. It validates the configuration, resolves the command to an
// absolute path and runs only `<command> -h` (empty stdin, empty private
// working directory, minimal environment, no task, profile or credential
// arguments) to inspect declared flags. Provider execution and permission
// enforcement are never exercised and are reported as unverified.
func ProbeCapabilities(ctx context.Context, c Config) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	r := newReport(c.Profile)
	if err := ValidateConfig(c); err != nil {
		r.set(CheckConfig, CheckBlocked, err.Error())
		return r
	}
	r.set(CheckConfig, CheckPassed, "command and profile settings are well formed")

	command := c.Command
	if command == "" {
		command = "offload"
	}
	resolved, err := exec.LookPath(command)
	if err == nil {
		resolved, err = filepath.Abs(resolved)
	}
	if err != nil {
		r.set(CheckCommand, CheckBlocked, "Offload executable unavailable")
		return r
	}
	r.Command = resolved
	r.set(CheckCommand, CheckPassed, "configured command resolved to an absolute path")

	output, problem := runHelpProbe(ctx, resolved)
	if problem == "" {
		problem = validateHelp(output)
	}
	if problem != "" {
		r.set(CheckHelpProbe, CheckBlocked, problem)
		return r
	}
	r.set(CheckHelpProbe, CheckPassed, "help output recognized")

	declared := declaredFlags(output)
	var missing []string
	for _, name := range RequiredFlags {
		if !declared[name] {
			missing = append(missing, "-"+name)
		}
	}
	if len(missing) > 0 {
		r.set(CheckFlagDeclarations, CheckBlocked, "required flags not declared: "+strings.Join(missing, ", "))
		return r
	}
	r.set(CheckFlagDeclarations, CheckPassed, "all required flags are declared")
	r.Status = StatusReady
	return r
}

// boundedBuffer keeps at most max bytes and signals once when more is written.
type boundedBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	n    int
	max  int
	over chan struct{}
	once sync.Once
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{max: max, over: make(chan struct{})}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.n += len(p)
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		b.buf.Write(p[:room])
	}
	if b.n > b.max {
		b.once.Do(func() { close(b.over) })
	}
	return len(p), nil
}

func (b *boundedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func probeEnvironment(home string) []string {
	env := []string{"HOME=" + home, "LC_ALL=C"}
	for _, key := range []string{"PATH", "SystemRoot"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// runHelpProbe runs `<command> -h` and returns its combined output. A non-empty
// problem string is a fixed, safe message. Exit status 0 and 2 (Go flag help)
// are the only accepted outcomes.
func runHelpProbe(ctx context.Context, command string) ([]byte, string) {
	release, err := housekeeping.Gate(ctx, os.TempDir(), false)
	if err != nil {
		return nil, "probe workspace unavailable"
	}
	defer release()
	dir, err := os.MkdirTemp("", "clawflow-capability-*")
	if err != nil {
		return nil, "probe workspace unavailable"
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, "probe workspace unavailable"
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, probeContextProblem(ctx)
	}

	out := newBoundedBuffer(MaxProbeOutputBytes)
	cmd := exec.Command(command, "-h")
	cmd.Dir = dir
	cmd.Env = probeEnvironment(dir)
	cmd.Stdin = nil // null device: empty stdin
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = probeWaitDelay
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, "Offload help probe could not start"
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var waitErr error
	reason := ""
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		reason = probeContextProblem(ctx)
	case <-out.over:
		reason = "help output exceeded 128 KiB"
	}
	// Always signal the whole group: descendants may outlive the leader.
	terminateProcessGroupSync(cmd, probeGrace)
	if reason != "" {
		select {
		case <-done:
		case <-time.After(probeGrace + probeWaitDelay + 2*time.Second):
		}
		return nil, reason
	}
	select {
	case <-out.over:
		return nil, "help output exceeded 128 KiB"
	default:
	}
	// ErrWaitDelay means a descendant held the output pipes after the leader
	// exited. The group was already signalled above and the leader's output is
	// complete, so it is tolerated (exit-status rules below still apply).
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
		var ee *exec.ExitError
		if !errors.As(waitErr, &ee) {
			return nil, "help probe I/O failed"
		}
	}
	if cmd.ProcessState == nil {
		return nil, "help probe did not finish"
	}
	switch cmd.ProcessState.ExitCode() {
	case 0, 2:
		return out.bytes(), ""
	default:
		return nil, "help probe exited with an unsupported status"
	}
}

func probeContextProblem(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "help probe timed out"
	}
	return "help probe canceled"
}

var (
	usageLine = regexp.MustCompile(`(?i)^[ \t]*usage\b`)
	// Exact diagnostic prefixes block at any indentation (matched after
	// TrimSpace). Go's "flag: help requested" is not a diagnostic.
	diagnosticLine = regexp.MustCompile(`(?i)^(unknown flag:|flag provided but not defined:|unsupported flag:|(error|fatal|fatal error|panic)\s*:|goroutine \d+ \[)`)
	// Looser phrases are checked on unindented lines only, because flag
	// descriptions are indented and may legitimately use such words.
	errorLine = regexp.MustCompile(`(?i)^(unknown (flag|option|shorthand)|unrecognized (flag|option|argument)|unsupported ?(flag|option)|invalid (flag|option)|unexpected argument|bad flag|no such (flag|option))\b`)
	// Go flag.PrintDefaults layout: "  -name [type]" followed by an indented
	// "    \tdescription" line, or "  -x\tdescription" on one line.
	flagDecl      = regexp.MustCompile(`^ {1,8}--?([A-Za-z][A-Za-z0-9-]*)(?: [A-Za-z][A-Za-z0-9_<>.\[\]-]*)?(?:\t(.*))?$`)
	flagDescLine  = regexp.MustCompile(`^ *\t[ \t]*\S`)
	flagDescEmpty = ""
)

func helpLines(output []byte) []string {
	lines := strings.Split(string(output), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}
	return lines
}

// binaryOutput reports invalid UTF-8, NUL or control characters other than
// tab, CR and LF.
func binaryOutput(output []byte) bool {
	if !utf8.Valid(output) {
		return true
	}
	for _, r := range string(output) {
		if r == '\t' || r == '\r' || r == '\n' {
			continue
		}
		if r == 0 || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validateHelp(output []byte) string {
	if binaryOutput(output) {
		return "help output is not valid text"
	}
	usage := false
	for _, l := range helpLines(output) {
		if usageLine.MatchString(l) {
			usage = true
		}
		trimmed := strings.TrimSpace(l)
		if diagnosticLine.MatchString(trimmed) || (l != "" && l[0] != ' ' && l[0] != '\t' && errorLine.MatchString(trimmed)) {
			return "help output reports an unsupported flag or error"
		}
	}
	if !usage {
		return "help output not recognized as usage text"
	}
	return ""
}

// declaredFlags returns flag names that appear as line-anchored declarations.
// Prose mentions never count: a declaration needs a description, either after
// a tab on the same line or on the following tab-indented line.
func declaredFlags(output []byte) map[string]bool {
	lines := helpLines(output)
	seen := map[string]bool{}
	for i, l := range lines {
		m := flagDecl.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if strings.TrimSpace(m[2]) != flagDescEmpty {
			seen[m[1]] = true
			continue
		}
		if i+1 < len(lines) && flagDescLine.MatchString(lines[i+1]) {
			seen[m[1]] = true
		}
	}
	return seen
}
