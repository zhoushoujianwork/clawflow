package candidate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Layer statuses follow the testing-environment contract.
const (
	Passed  = "passed"
	Failed  = "failed"
	Blocked = "blocked"
	NotRun  = "not-run"
	Pending = "pending"
)

// CommandResult records one command with redacted arguments.
type CommandResult struct {
	Name     string   `json:"name"`
	Args     []string `json:"args"`
	ExitCode int      `json:"exit_code"`
	Result   string   `json:"result"`
}

// Evidence binds results to the candidate commit, runtime identity,
// environment and commands. It never contains tokens, DSNs or credentials.
type Evidence struct {
	Commit       string            `json:"candidate_commit"`
	ID           string            `json:"runtime_id"`
	Port         int               `json:"port"`
	Schema       string            `json:"schema"`
	BundleID     string            `json:"bundle_id"`
	Environment  map[string]string `json:"environment"`
	SOPDigest    string            `json:"sop_digest,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Readiness    Readiness         `json:"readiness"`
	Layers       map[string]string `json:"layers"`
	Workers      map[string]string `json:"workers,omitempty"`
	Commands     []CommandResult   `json:"commands"`
	StableDiff   []string          `json:"stable_changes"`
	Recorded     string            `json:"recorded"`
	Status       string            `json:"status"`
	NextOwner    string            `json:"next_owner,omitempty"`
	NextAction   string            `json:"next_action,omitempty"`

	secrets []string
}

// NewEvidence starts a record. Native visual and live layers begin as
// pending / not-run; no layer is implied by another.
func NewEvidence(p Plan, binarySHA string) *Evidence {
	return &Evidence{
		Commit: p.Commit, ID: p.ID, Port: p.Port, Schema: p.Schema, BundleID: p.BundleID,
		Environment: map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(), "binary_sha256": binarySHA},
		SOPDigest:   p.Pin.SOPDigest, Capabilities: p.Pin.Capabilities,
		Layers: map[string]string{
			"offline": NotRun, "environment-ready": NotRun, "candidate-loaded": NotRun,
			"lifecycle": NotRun, "live-write": NotRun, "save-reload": NotRun, "native-visual": Pending,
		},
		Status: NotRun,
	}
}

// Redact registers values that must never appear in the record.
func (e *Evidence) Redact(values ...string) {
	for _, v := range values {
		if len(v) >= 6 {
			e.secrets = append(e.secrets, v)
		}
	}
}

var (
	urlPassword = regexp.MustCompile(`(://[^/?#@:\s]*:)([^/?#\s]*)@`)
	kvPassword  = regexp.MustCompile(`(?i)(password\s*=\s*)([^\s"]+)`)
	bearer      = regexp.MustCompile(`(?i)(bearer\s+)[^\s"]+`)
)

func (e *Evidence) scrub(s string) string {
	for _, v := range e.secrets {
		s = strings.ReplaceAll(s, v, "****")
	}
	s = urlPassword.ReplaceAllString(s, "${1}****@")
	s = kvPassword.ReplaceAllString(s, "${1}****")
	return bearer.ReplaceAllString(s, "${1}****")
}

// Command appends a command result.
func (e *Evidence) Command(name string, args []string, code int, result string) {
	clean := make([]string, len(args))
	for i, a := range args {
		clean[i] = e.scrub(a)
	}
	e.Commands = append(e.Commands, CommandResult{Name: name, Args: clean, ExitCode: code, Result: e.scrub(result)})
}

// Finish sets the overall status: passed only when every executed layer
// passed and none failed or blocked; unrun live layers do not count as passes.
func (e *Evidence) Finish() {
	e.Recorded = time.Now().UTC().Format(time.RFC3339)
	var failed, blocked, unrun bool
	for _, layer := range []string{"offline", "environment-ready", "candidate-loaded", "lifecycle"} {
		switch e.Layers[layer] {
		case Passed:
		case Blocked:
			blocked = true
		case NotRun:
			unrun = true
		default:
			failed = true
		}
	}
	// Steps after a blocked one legitimately stay not-run; they never pass.
	switch {
	case failed:
		e.Status = Failed
	case blocked:
		e.Status = Blocked
	case unrun:
		e.Status = NotRun
	default:
		e.Status = Passed
	}
	if len(e.StableDiff) > 0 {
		e.Status, e.NextOwner, e.NextAction = Failed, "developer", "stable host state changed during the candidate run; investigate before accepting"
	}
	if e.Status == Blocked && e.NextOwner == "" {
		e.NextOwner, e.NextAction = e.Readiness.NextOwner, e.Readiness.Action
	}
}

// Write stores the record with owner-only permissions.
func (e *Evidence) Write(path string) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Fields were scrubbed on entry; this pass removes registered literals
	// only, so the JSON structure cannot be damaged by pattern matching.
	text := string(data)
	for _, v := range e.secrets {
		text = strings.ReplaceAll(text, v, "****")
	}
	return os.WriteFile(path, []byte(text+"\n"), 0o600)
}

// Verify checks a stored record against the commit under review.
func Verify(path, commit string) (*Evidence, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var e Evidence
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	switch {
	case !ValidCommit(commit) || e.Commit != commit:
		return nil, fmt.Errorf("evidence is for %s, not the pinned candidate %s", e.Commit, commit)
	case e.ID != IDFor(e.Commit):
		return nil, fmt.Errorf("evidence runtime identity does not match its commit")
	case e.Status != Passed:
		return nil, fmt.Errorf("evidence status is %s", e.Status)
	}
	return &e, nil
}
