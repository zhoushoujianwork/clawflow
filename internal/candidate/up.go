package candidate

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var fingerprintLine = regexp.MustCompile(`(?m)SHA-256 fingerprint:\s*([0-9A-Fa-f:]{64,95})`)

// UpResult reports what Up started. It carries no credentials.
type UpResult struct {
	Controller int               `json:"controller_pid"`
	Workers    map[string]int    `json:"workers"`
	Identities map[string]string `json:"identities"`
}

// Up migrates the candidate schema, then starts the controller and the two
// worker lanes. Tokens are written only below the candidate state directories
// (mode 0600). dsn must already be pinned to the candidate schema.
func (m *Manifest) Up(ctx context.Context, binary, dsn string) (*UpResult, error) {
	p := m.Plan
	if dsn != "" {
		if _, stderr, err := m.Output(ctx, binary, []string{"db", "migrate"}, dsn); err != nil {
			return nil, fmt.Errorf("candidate migration failed: %w: %s", err, tail(stderr, 400))
		}
	}
	controllerPID, err := m.Start("controller", binary, []string{"controller"}, dsn)
	if err != nil {
		return nil, err
	}
	res := &UpResult{Controller: controllerPID, Workers: map[string]int{}, Identities: map[string]string{}}
	fail := func(err error) (*UpResult, error) {
		_, _ = m.StopProcesses(5 * time.Second)
		return nil, err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.Port))
	if !WaitFor(ctx, 60*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	}) {
		return fail(fmt.Errorf("controller did not listen on %s; see %s", addr, filepath.Join(p.Logs, "controller.log")))
	}
	dev, review := p.Lanes()
	for _, w := range []Worker{dev, review} {
		if err := os.MkdirAll(w.StateDir, 0o700); err != nil {
			return fail(err)
		}
		stdout, stderr, err := m.Output(ctx, binary, []string{"worker", "token", "create", "--name", w.Name}, dsn)
		if err != nil {
			return fail(fmt.Errorf("create token for %s: %w", w.Name, err))
		}
		match := fingerprintLine.FindStringSubmatch(stderr)
		if stdout == "" || match == nil {
			return fail(fmt.Errorf("controller did not return a token and fingerprint for %s", w.Name))
		}
		tokenFile := filepath.Join(w.StateDir, "worker.token")
		if err := os.WriteFile(tokenFile, []byte(stdout+"\n"), 0o600); err != nil {
			return fail(err)
		}
		pid, err := m.Start("worker-"+w.Role, binary, p.WorkerArgs(w, tokenFile, strings.TrimSpace(match[1])), dsn)
		if err != nil {
			return fail(err)
		}
		res.Workers[w.Name] = pid
		res.Identities[w.Role] = w.Name
	}
	return res, nil
}

// Down stops every process this candidate owns and nothing else.
func (m *Manifest) Down() ([]string, error) { return m.StopProcesses(10 * time.Second) }
