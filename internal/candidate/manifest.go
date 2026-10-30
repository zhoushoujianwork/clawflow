package candidate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

const (
	manifestName = "manifest.json"
	markerName   = ".clawflow-candidate"
)

// Process is a service started for this candidate.
type Process struct {
	Role    string `json:"role"`
	PID     int    `json:"pid"`
	Command string `json:"command"` // absolute path of the executable
	Started string `json:"started"`
}

// Manifest proves ownership of a candidate root. Cleanup and shutdown act only
// on resources named here, and only while the marker and manifest agree.
type Manifest struct {
	Plan      Plan      `json:"plan"`
	Token     string    `json:"token"` // random; also stored in the marker file
	Created   string    `json:"created"`
	Processes []Process `json:"processes,omitempty"`
}

// Prepare validates the plan, then creates its directories (mode 0700) and
// ownership files. It refuses an existing root that is not already this
// candidate's, and never writes credentials.
func Prepare(p Plan, stable Stable, probe Probe) (*Manifest, error) {
	existing, err := ReadManifest(p.Root)
	switch {
	case err == nil:
		if existing.Plan.Commit != p.Commit || existing.Plan.ID != p.ID {
			return nil, collide("%s is owned by a different candidate", p.Root)
		}
		// Re-preparing keeps the same identity; the port is already ours.
		probe = nil
	case errors.Is(err, os.ErrNotExist):
		if entries, derr := os.ReadDir(p.Root); derr == nil && len(entries) > 0 {
			return nil, collide("%s exists and is not a candidate root", p.Root)
		}
	default:
		return nil, collide("%s has an unreadable ownership record", p.Root)
	}
	if err := p.Validate(stable, probe); err != nil {
		return nil, err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	m := &Manifest{Plan: p, Token: hex.EncodeToString(token), Created: time.Now().UTC().Format(time.RFC3339)}
	if existing != nil {
		m.Token, m.Created, m.Processes = existing.Token, existing.Created, existing.Processes
	}
	for _, dir := range []string{p.Root, p.Home, p.State, p.ReviewDir, p.Config, p.Locks, p.Worktrees, p.Cache, p.Logs, p.Bin, p.Tmp, filepath.Dir(p.AppPath)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(p.Root, markerName), []byte(p.ID+"\n"+m.Token+"\n"), 0o600); err != nil {
		return nil, err
	}
	return m, m.Save()
}

// Save writes the manifest atomically.
func (m *Manifest) Save() error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.Plan.Root, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
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
	return os.Rename(tmp.Name(), filepath.Join(m.Plan.Root, manifestName))
}

// ReadManifest loads and cross-checks a candidate's ownership record.
func ReadManifest(root string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, manifestName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest unreadable: %w", err)
	}
	marker, err := os.ReadFile(filepath.Join(root, markerName))
	if err != nil {
		return nil, fmt.Errorf("ownership marker missing: %w", err)
	}
	if strings.TrimSpace(string(marker)) != m.Plan.ID+"\n"+m.Token {
		return nil, fmt.Errorf("ownership marker does not match manifest")
	}
	if m.Plan.ID != IDFor(m.Plan.Commit) || !devmode.Within(root, m.Plan.Root) || !devmode.Within(m.Plan.Root, root) {
		return nil, fmt.Errorf("manifest identity does not match its location")
	}
	return &m, nil
}

// Seams for process inspection; replaced in tests.
var (
	processCommand = func(pid int) string { return commandOf(pid) }
	signalProcess  = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	alive          = func(pid int) bool { return syscall.Kill(pid, 0) == nil }
)

// owned reports whether pid still runs the executable this candidate started.
func (m *Manifest) owned(pr Process) bool {
	if pr.PID <= 1 || !alive(pr.PID) {
		return false
	}
	cmd := processCommand(pr.PID)
	return cmd != "" && strings.HasPrefix(cmd, pr.Command) && devmode.Within(m.Plan.Bin, pr.Command)
}

// Record adds a started service to the manifest.
func (m *Manifest) Record(role string, pid int, command string) error {
	m.Processes = append(m.Processes, Process{Role: role, PID: pid, Command: command, Started: time.Now().UTC().Format(time.RFC3339)})
	return m.Save()
}

// StopProcesses terminates only the recorded processes that still belong to
// this candidate (same pid and executable below its bin directory). Stale pids
// that were reused by other programs are never signalled. It returns the roles
// stopped.
func (m *Manifest) StopProcesses(grace time.Duration) ([]string, error) {
	var stopped []string
	var failures []string
	for _, pr := range m.Processes {
		if !m.owned(pr) {
			continue
		}
		_ = signalProcess(pr.PID, syscall.SIGTERM)
		deadline := time.Now().Add(grace)
		for alive(pr.PID) && m.owned(pr) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if alive(pr.PID) && m.owned(pr) {
			_ = signalProcess(pr.PID, syscall.SIGKILL)
			time.Sleep(50 * time.Millisecond)
		}
		if alive(pr.PID) && m.owned(pr) {
			failures = append(failures, pr.Role)
			continue
		}
		stopped = append(stopped, pr.Role)
	}
	if len(failures) > 0 {
		return stopped, fmt.Errorf("could not stop: %s", strings.Join(failures, ", "))
	}
	m.Processes = nil
	return stopped, m.Save()
}

// Remove deletes the candidate root. It refuses unless the ownership files
// agree, nothing recorded is still running, and the root is a direct child of
// the base directory (no symlink indirection).
func (m *Manifest) Remove() error {
	root := m.Plan.Root
	if filepath.Dir(root) != filepath.Clean(m.Plan.Base) || filepath.Base(root) != m.Plan.ID {
		return collide("%s is not an owned candidate root", root)
	}
	if info, err := os.Lstat(root); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return collide("%s is not a plain directory", root)
	}
	disk, err := ReadManifest(root)
	if err != nil || disk.Token != m.Token {
		return collide("ownership record does not match; refusing to delete %s", root)
	}
	for _, pr := range disk.Processes {
		if disk.owned(pr) {
			return fmt.Errorf("candidate %s still runs %s (pid %d); stop it first", m.Plan.ID, pr.Role, pr.PID)
		}
	}
	return os.RemoveAll(root)
}
