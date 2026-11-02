package candidate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

// BuildBinary compiles the pinned commit from a disposable checkout into the
// candidate's own bin directory. It refuses a checkout whose HEAD differs from
// the pinned commit, and never writes outside the candidate root.
func BuildBinary(ctx context.Context, p Plan, checkout string) (path, sha string, err error) {
	head, err := gitOutput(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("read checkout revision: %w", err)
	}
	if head != p.Commit {
		return "", "", fmt.Errorf("checkout is at %s, not the pinned candidate %s", head, p.Commit)
	}
	if dirty, _ := gitOutput(ctx, checkout, "status", "--porcelain"); dirty != "" {
		return "", "", fmt.Errorf("checkout has uncommitted changes; a candidate must build exactly the pinned commit")
	}
	path = filepath.Join(p.Bin, "clawflow")
	const pkg = "github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands."
	flags := fmt.Sprintf("-s -w -X %sVersion=candidate-%s -X %sBuildCommit=%s -X %sBuildChannel=source -X %sSourceBranch=candidate", pkg, p.ID, pkg, p.Commit, pkg, pkg)
	cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", flags, "-o", path, "./cmd/clawflow")
	cmd.Dir = checkout
	cmd.Env = p.Env("")
	cmd.Env = append(cmd.Env, "GOFLAGS=-mod=readonly", "GOPATH="+filepath.Join(p.Cache, "gopath"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("build failed: %w: %s", err, tail(out.String(), 600))
	}
	if dirty, checkErr := gitOutput(ctx, checkout, "status", "--porcelain"); checkErr != nil || dirty != "" {
		return "", "", fmt.Errorf("candidate checkout changed during build; refusing the resulting binary")
	}
	builtHead, checkErr := gitOutput(ctx, checkout, "rev-parse", "HEAD")
	if checkErr != nil || builtHead != p.Commit {
		return "", "", fmt.Errorf("candidate revision changed during build")
	}
	sha, err = FileSHA(path)
	return path, sha, err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// FileSHA returns the hex SHA-256 of a file.
func FileSHA(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// Start launches a service in its own process group, with the candidate
// environment and log file, and records it in the manifest before returning.
func (m *Manifest) Start(role, binary string, args []string, dsn string) (int, error) {
	p := m.Plan
	if !devmode.Within(p.Bin, binary) {
		return 0, fmt.Errorf("%w: %s is not inside the candidate bin directory", ErrCollision, binary)
	}
	log, err := os.OpenFile(filepath.Join(p.Logs, role+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(binary, args...)
	cmd.Dir = p.Root
	cmd.Env = p.Env(dsn)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	if err := m.Record(role, pid, binary); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return 0, err
	}
	return pid, nil
}

// Output runs a one-shot command with the candidate environment and returns
// stdout and stderr separately.
func (m *Manifest) Output(ctx context.Context, binary string, args []string, dsn string) (stdout, stderr string, err error) {
	if !devmode.Within(m.Plan.Bin, binary) {
		return "", "", fmt.Errorf("%w: %s is not inside the candidate bin directory", ErrCollision, binary)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir, cmd.Env = m.Plan.Root, m.Plan.Env(dsn)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return strings.TrimSpace(o.String()), e.String(), err
}

// WaitFor polls fn until it reports true or the deadline passes.
func WaitFor(ctx context.Context, d time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if fn() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
