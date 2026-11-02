package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ReadinessSchemaVersion versions Readiness and StatusDoc.
const ReadinessSchemaVersion = 1

// Stable reason codes for a blocked Offload dependency.
const (
	ReasonMissing           = "missing_executable"
	ReasonNotExecutable     = "not_executable"
	ReasonTimeout           = "probe_timeout"
	ReasonProbeFailed       = "probe_failed"
	ReasonIncompatible      = "incompatible_capabilities"
	ReasonProfileMissing    = "profile_not_selected"
	ReasonProfileNotFound   = "profile_not_found"
	ReasonConfigInvalid     = "config_invalid"
	ReasonConfigUnavailable = "config_unavailable"
)

// Readiness states.
const (
	StateReady       = "ready"
	StateBlocked     = "blocked"
	StateNotRequired = "not_required"
	StateUnreported  = "unreported"
)

// CheckProfileConfig is the stable ID of the read-only profile listing check.
const CheckProfileConfig = "profile_config"

// Official installation source. Verified against the Offload repository
// README ("Quick start", repository zhoushoujianwork/offload): the documented
// installer is `go install`, which needs Go 1.24+. No other installer command
// is documented there, so none is suggested here.
const (
	OffloadRepositoryURL = "https://github.com/zhoushoujianwork/offload"
	OffloadInstallDocs   = OffloadRepositoryURL + "#quick-start"
	OffloadInstallCmd    = "go install github.com/zhoushoujianwork/offload@latest"
)

// UnavailableError is a typed readiness failure with a stable reason code.
type UnavailableError struct{ Reason, Message string }

func (e *UnavailableError) Error() string { return e.Message }

// IsUnavailable reports whether err is an Offload readiness failure.
func IsUnavailable(err error) (string, bool) {
	var u *UnavailableError
	if errors.As(err, &u) {
		return u.Reason, true
	}
	return "", false
}

func lookupKind(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return ReasonNotExecutable
	}
	return ReasonMissing
}

func probeProblemReason(problem string) string {
	switch {
	case problem == "":
		return ""
	case strings.Contains(problem, "timed out"):
		return ReasonTimeout
	case strings.Contains(problem, "could not start"):
		return ReasonNotExecutable
	case strings.Contains(problem, "unsupported status"):
		return ReasonIncompatible
	default:
		return ReasonProbeFailed
	}
}

// lookPathIn resolves command against the PATH of an explicit environment.
// A nil getter uses the current process (exec.LookPath semantics).
func lookPathIn(file string, get func(string) (string, bool)) (string, error) {
	if get == nil {
		return exec.LookPath(file)
	}
	if strings.ContainsRune(file, os.PathSeparator) {
		if err := checkExecutable(file); err != nil {
			return "", &exec.Error{Name: file, Err: err}
		}
		return file, nil
	}
	path, _ := get("PATH")
	var denied error
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, file)
		err := checkExecutable(candidate)
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, fs.ErrPermission) {
			denied = err
		}
	}
	if denied != nil {
		return "", &exec.Error{Name: file, Err: denied}
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return fs.ErrPermission
	}
	return nil
}

// Guidance is actionable setup text shared verbatim by the CLI and native app.
type Guidance struct {
	Steps   []string `json:"steps"`
	Link    string   `json:"link,omitempty"`
	Recheck string   `json:"recheck"`
}

// Readiness is one worker's (or shell's) shared Offload dependency result.
// It never contains credentials, environment values or raw subprocess output.
type Readiness struct {
	SchemaVersion     int       `json:"schema_version"`
	State             string    `json:"state"`
	Reason            string    `json:"reason,omitempty"`
	Summary           string    `json:"summary"`
	Worker            string    `json:"worker,omitempty"`
	Roles             []string  `json:"roles,omitempty"`
	Environment       string    `json:"environment,omitempty"` // "service" or "interactive"
	ConfiguredCommand string    `json:"configured_command,omitempty"`
	Command           string    `json:"command,omitempty"`
	Profiles          []string  `json:"profiles,omitempty"`
	Hint              string    `json:"hint,omitempty"`
	Guidance          *Guidance `json:"guidance,omitempty"`
	Report            *Report   `json:"report,omitempty"`
	Unverified        []string  `json:"unverified,omitempty"`
	CheckedAt         time.Time `json:"checked_at"`
}

// Blocked reports whether executable work must not be claimed.
func (r Readiness) Blocked() bool { return r.State == StateBlocked }

// Key identifies a distinct condition, so repeats can be logged once.
func (r Readiness) Key() string { return r.State + "|" + r.Reason + "|" + r.Command + "|" + r.Summary }

// ReadinessOptions selects the environment that is inspected.
type ReadinessOptions struct {
	// Env supplies PATH and CLAWFLOW_OFFLOAD_* overrides. Nil inspects the
	// current process, which for a worker is its real launchd environment.
	Env         func(string) (string, bool)
	Worker      string
	Environment string
	Now         func() time.Time
}

// CheckReadiness runs the existing bounded capability probe plus a read-only
// profile listing for every distinct profile the roles select. It starts no
// task and contacts no provider.
func CheckReadiness(ctx context.Context, c Config, roles []string, opts ReadinessOptions) Readiness {
	if ctx == nil {
		ctx = context.Background()
	}
	get := opts.Env
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	lookup := get
	if lookup == nil {
		lookup = os.LookupEnv
	}
	c = WithEnvironmentFrom(c, lookup)
	if len(roles) == 0 {
		roles = []string{"operator"}
	}
	var profiles []string
	seen := map[string]bool{}
	for _, role := range roles {
		p := ProfileForRoleFrom(c, role, lookup)
		if !seen[p] {
			seen[p] = true
			profiles = append(profiles, p)
		}
	}
	r := Readiness{SchemaVersion: ReadinessSchemaVersion, Worker: opts.Worker, Roles: roles, Environment: opts.Environment,
		ConfiguredCommand: c.Command, CheckedAt: now().UTC()}
	for _, p := range profiles {
		if p != "" {
			r.Profiles = append(r.Profiles, sanitizeText(p, 128))
		}
	}
	base := c
	base.Profile = profiles[0]
	report, help := probeCapabilities(ctx, base, get)
	if report.Status == StatusReady {
		check := Check{ID: CheckProfileConfig, Status: CheckSkipped, Message: "profile listing unavailable; selected profile not verified"}
		snap, ok := listProfiles(ctx, report.Command, help, get)
		report.Unverified = append(report.Unverified, "live_authentication")
		if !ok {
			report.Unverified = append(report.Unverified, "profile_existence")
		} else {
			check.Status, check.Message = CheckPassed, "selected profile is defined in Offload configuration (credentials not verified)"
			for _, p := range profiles {
				if reason, msg := snap.check(p); reason != "" {
					check.Status, check.Message = CheckBlocked, msg
					report.Status, report.Reason = StatusBlocked, reason
					break
				}
			}
		}
		report.Checks = append(report.Checks, check)
	}
	r.Report = &report
	r.Command = report.Command
	r.Unverified = report.Unverified
	if report.Status == StatusReady {
		r.State = StateReady
		r.Summary = "Offload is installed and compatible; live authentication is not verified."
		return r
	}
	r.State, r.Reason = StateBlocked, report.Reason
	if r.Reason == "" {
		r.Reason = ReasonConfigInvalid
	}
	r.Summary, r.Guidance = describeBlocked(r.Reason, c.Command, report)
	return r
}

// ConfigBlockedReadiness converts a settings-load failure into a Readiness.
func ConfigBlockedReadiness(message string, opts ReadinessOptions) Readiness {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	report := ConfigBlockedReport(message)
	report.Reason = ReasonConfigUnavailable
	return Readiness{SchemaVersion: ReadinessSchemaVersion, State: StateBlocked, Reason: ReasonConfigUnavailable, Worker: opts.Worker,
		Environment: opts.Environment, Summary: sanitizeText(message, 200), Report: &report, CheckedAt: now().UTC(),
		Guidance: &Guidance{Steps: []string{"Set settings.execution.profile in ~/.clawflow/config/config.yaml, or CLAWFLOW_OFFLOAD_PROFILE for the service environment."},
			Recheck: RecheckCommand}}
}

// RecheckCommand is the explicit user action that re-runs the bounded probe.
const RecheckCommand = "clawflow execution status --recheck"

func describeBlocked(reason, configured string, report Report) (string, *Guidance) {
	if configured == "" {
		configured = "offload"
	}
	configured = sanitizeText(configured, 200)
	g := &Guidance{Recheck: RecheckCommand}
	detail := ""
	for _, c := range report.Checks {
		if c.Status == CheckBlocked {
			detail = c.Message
		}
	}
	install := []string{
		"Install Offload explicitly (requires Go 1.24+): " + OffloadInstallCmd,
		"If it installs outside the service PATH, set settings.execution.command (or CLAWFLOW_OFFLOAD_COMMAND for the worker) to its absolute path.",
	}
	switch reason {
	case ReasonMissing:
		g.Steps, g.Link = install, OffloadInstallDocs
		return fmt.Sprintf("Offload executable not found (configured command: %s).", configured), g
	case ReasonNotExecutable:
		g.Steps = append([]string{"Make the configured Offload command executable, or point settings.execution.command at a working binary."}, install...)
		g.Link = OffloadInstallDocs
		return fmt.Sprintf("Offload command is not executable (configured command: %s).", configured), g
	case ReasonTimeout:
		g.Steps = []string{"Run the configured command with -h in a terminal; it must print usage within 3 seconds.", "Reinstall or replace a hung binary, then recheck."}
		g.Link = OffloadInstallDocs
		return "Offload did not answer the help probe in time.", g
	case ReasonIncompatible:
		g.Steps = append([]string{"Update Offload to a version that declares the flags ClawFlow uses (" + detail + ")."}, install...)
		g.Link = OffloadInstallDocs
		return "Offload does not declare the capabilities ClawFlow needs.", g
	case ReasonProfileMissing:
		g.Steps = []string{"Set settings.execution.profile in ~/.clawflow/config/config.yaml, or CLAWFLOW_OFFLOAD_PROFILE for the worker environment.", "List profiles with: offload profiles"}
		return "No Offload profile is selected for ClawFlow.", g
	case ReasonProfileNotFound:
		g.Steps = []string{"Create or select a profile with: offload config", "Then make settings.execution.profile match a profile name or alias."}
		g.Link = OffloadInstallDocs
		return sanitizeText(detail, 200), g
	case ReasonConfigInvalid:
		g.Steps = []string{"Correct settings.execution (" + detail + ")."}
		return "Offload settings are invalid.", g
	default:
		g.Steps = []string{"Run the configured command with -h and check it prints Go-style usage."}
		g.Link = OffloadInstallDocs
		return "Offload probe failed: " + sanitizeText(detail, 160), g
	}
}

// --- profile listing ---------------------------------------------------

const maxProfilesBytes = 1 << 20

var profilesDeclaration = regexp.MustCompile(`(?m)^\s*offload profiles \[--json\](?:\s|$)`)

type profileSnapshot struct {
	SchemaVersion int  `json:"schema_version"`
	ConfigExists  bool `json:"config_exists"`
	Profiles      []struct {
		Name string `json:"name"`
	} `json:"profiles"`
	aliases map[string]bool
}

func (s profileSnapshot) check(profile string) (string, string) {
	if strings.TrimSpace(profile) == "" {
		return ReasonProfileMissing, "No Offload profile is selected for ClawFlow."
	}
	if !s.ConfigExists {
		return ReasonProfileNotFound, "Offload has no profile configuration (run offload config)."
	}
	for _, p := range s.Profiles {
		if p.Name == profile {
			return "", ""
		}
	}
	if s.aliases[profile] {
		return "", ""
	}
	return ReasonProfileNotFound, fmt.Sprintf("Profile %q is not defined in Offload configuration.", sanitizeText(profile, 64))
}

// listProfiles runs `offload profiles --json` (read-only: it never runs token
// commands) when help declares it, plus the plain listing for alias names,
// which the JSON schema omits. ok is false whenever the API is unsupported or
// unavailable, so absence of support never blocks.
func listProfiles(ctx context.Context, command string, help []byte, get func(string) (string, bool)) (profileSnapshot, bool) {
	var snap profileSnapshot
	declared := false
	for _, l := range helpLines(help) {
		if profilesDeclaration.MatchString(l) || strings.Contains(l, "offload profiles") {
			declared = true
		}
	}
	if !declared {
		return snap, false
	}
	env := func(dir string) []string { return profileEnvironment(dir, get) }
	out, problem := runProbe(ctx, probeSpec{command: command, args: []string{"profiles", "--json"}, what: "profile", limit: maxProfilesBytes, env: env, okExit: func(c int) bool { return c == 0 }})
	if problem != "" || json.Unmarshal(out, &snap) != nil || snap.SchemaVersion != 1 {
		return profileSnapshot{}, false
	}
	snap.aliases = map[string]bool{}
	if text, problem := runProbe(ctx, probeSpec{command: command, args: []string{"profiles"}, what: "profile", limit: maxProfilesBytes, env: env, okExit: func(c int) bool { return c == 0 }}); problem == "" {
		for _, l := range strings.Split(string(text), "\n") {
			if i := strings.LastIndex(l, "(aliases: "); i >= 0 && strings.HasSuffix(strings.TrimSpace(l), ")") {
				body := strings.TrimSuffix(strings.TrimSpace(l[i+len("(aliases: "):]), ")")
				for _, a := range strings.Split(body, ",") {
					snap.aliases[strings.TrimSpace(a)] = true
				}
			}
		}
	}
	return snap, true
}

// profileEnvironment keeps the real HOME and XDG_CONFIG_HOME so Offload finds
// its own configuration. It deliberately drops OFFLOAD_PROFILE and any
// credential-bearing variables.
func profileEnvironment(dir string, get func(string) (string, bool)) []string {
	if get == nil {
		get = os.LookupEnv
	}
	env := []string{"LC_ALL=C"}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "PATH", "SystemRoot"} {
		if v, ok := get(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

// --- cache and shared files --------------------------------------------

// Cache coalesces and caches readiness so renders and polls never spawn a
// probe each. Blocked results expire sooner so recovery is noticed quickly.
type Cache struct {
	ReadyTTL, BlockedTTL time.Duration
	Now                  func() time.Time
	Run                  func(context.Context) Readiness

	mu      sync.Mutex
	cached  *Readiness
	expires time.Time
	pending chan struct{}
}

// Get returns the cached result, or runs one shared probe. force bypasses the
// TTL (explicit recheck) but still joins an in-flight probe.
func (c *Cache) Get(ctx context.Context, force bool) Readiness {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	for {
		c.mu.Lock()
		if c.cached != nil && !force && now().Before(c.expires) {
			r := *c.cached
			c.mu.Unlock()
			return r
		}
		if c.pending != nil {
			wait := c.pending
			c.mu.Unlock()
			select {
			case <-wait:
				force = false
				continue
			case <-ctx.Done():
				return Readiness{SchemaVersion: ReadinessSchemaVersion, State: StateBlocked, Reason: ReasonProbeFailed, Summary: "Offload readiness probe canceled.", CheckedAt: now().UTC()}
			}
		}
		c.pending = make(chan struct{})
		done := c.pending
		c.mu.Unlock()
		r := c.Run(ctx)
		ttl := c.ReadyTTL
		if r.State != StateReady {
			ttl = c.BlockedTTL
		}
		c.mu.Lock()
		if ctx.Err() == nil {
			c.cached, c.expires = &r, now().Add(ttl)
		}
		c.pending = nil
		close(done)
		c.mu.Unlock()
		return r
	}
}

// StatusDoc is the credential-free document the CLI prints and the native app decodes.
type StatusDoc struct {
	SchemaVersion int         `json:"schema_version"`
	GeneratedAt   time.Time   `json:"generated_at"`
	State         string      `json:"state"`
	Summary       string      `json:"summary"`
	Workers       []Readiness `json:"workers"`
}

// Summarize derives the overall state: blocked beats unreported beats ready.
func Summarize(now time.Time, workers []Readiness) StatusDoc {
	doc := StatusDoc{SchemaVersion: ReadinessSchemaVersion, GeneratedAt: now.UTC(), Workers: workers}
	if doc.Workers == nil {
		doc.Workers = []Readiness{}
	}
	var blocked, unreported []string
	for _, w := range workers {
		name := w.Worker
		if name == "" {
			name = "this machine"
		}
		switch w.State {
		case StateBlocked:
			blocked = append(blocked, name)
		case StateUnreported:
			unreported = append(unreported, name)
		}
	}
	sort.Strings(blocked)
	sort.Strings(unreported)
	switch {
	case len(blocked) > 0:
		doc.State = StateBlocked
		doc.Summary = "Offload is unavailable for: " + strings.Join(blocked, ", ") + "."
	case len(unreported) > 0:
		doc.State = StateUnreported
		doc.Summary = "No recent Offload readiness report from: " + strings.Join(unreported, ", ") + "."
	case len(workers) == 0 || allNotRequired(workers):
		doc.State = StateNotRequired
		doc.Summary = "No execution worker runs on this machine; a local Offload is not required."
	default:
		doc.State = StateReady
		doc.Summary = "Offload is ready; live authentication is not verified."
	}
	return doc
}

func allNotRequired(ws []Readiness) bool {
	for _, w := range ws {
		if w.State != StateNotRequired {
			return false
		}
	}
	return true
}

// StatusCacheFile is the shared, credential-free snapshot the CLI writes and
// every presenter reads, so polling never spawns a probe.
const StatusCacheFile = "execution-status.json"

// WriteStatusCache atomically stores doc with private permissions.
func WriteStatusCache(dir string, doc StatusDoc) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".status-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, StatusCacheFile))
}

// ReadStatusCache returns a stored document that is still within its TTL.
func ReadStatusCache(dir string, now time.Time) (StatusDoc, bool) {
	var doc StatusDoc
	data, err := os.ReadFile(filepath.Join(dir, StatusCacheFile))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &doc) != nil || doc.SchemaVersion != ReadinessSchemaVersion {
		return StatusDoc{}, false
	}
	ttl := StatusReadyTTL
	if doc.State == StateBlocked || doc.State == StateUnreported {
		ttl = StatusBlockedTTL
	}
	age := now.Sub(doc.GeneratedAt)
	return doc, age >= 0 && age < ttl
}

// Cache lifetimes shared by workers, CLI and native presenters.
const (
	StatusReadyTTL   = 60 * time.Second
	StatusBlockedTTL = 15 * time.Second
)
