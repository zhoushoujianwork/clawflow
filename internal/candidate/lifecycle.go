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
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

// BuildBinary compiles the pinned commit from a disposable checkout into the
// candidate's own bin directory. It refuses a checkout whose HEAD differs from
// the pinned commit, and never writes outside the candidate root.
func BuildBinary(ctx context.Context, p Plan, checkout string) (path, sha string, err error) {
	return BuildBinaryWith(ctx, p, BuildOptions{Checkout: checkout})
}

// BuildError is a failed compile of the pinned commit. Compile is true when the
// compiler rejected the source (a defect of the commit); false means the
// toolchain or environment failed and says nothing about the commit.
type BuildError struct {
	Compile bool
	Output  string
	Err     error
}

func (e *BuildError) Error() string { return "build failed: " + e.Err.Error() + ": " + e.Output }
func (e *BuildError) Unwrap() error { return e.Err }

var compilerDiagnostic = regexp.MustCompile(`\.go:\d+:\d+: `)

func buildError(err error, output string) error {
	out := tail(output, 600)
	return &BuildError{Compile: compilerDiagnostic.MatchString(output), Output: out, Err: err}
}

// BuildOptions tunes how the pinned commit is compiled. The zero value keeps the
// strictest behaviour: every cache lives under the candidate root.
type BuildOptions struct {
	Checkout string
	// GoCache is a content-addressed build cache shared between candidates. It
	// must never be the stable host's cache directory; it only speeds up the
	// repeated build of nearly identical sources.
	GoCache string
	// ModCache is an existing module cache reused with GOPROXY=off: nothing is
	// downloaded into it and the build needs no network. A dependency missing
	// from it is not a defect of the commit, so the build is retried once with
	// the normal module settings.
	ModCache string
}

// BuildBinaryWith is BuildBinary with explicit BuildOptions.
func BuildBinaryWith(ctx context.Context, p Plan, o BuildOptions) (path, sha string, err error) {
	checkout := o.Checkout
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
	if o.GoCache != "" {
		cmd.Env = append(cmd.Env, "GOCACHE="+o.GoCache)
	}
	if o.ModCache != "" {
		cmd.Env = append(cmd.Env, "GOMODCACHE="+o.ModCache, "GOPROXY=off")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		// A dependency the PR adds is absent from an offline module cache. That
		// says nothing about the commit, so retry once with the normal module
		// settings before reporting a build failure.
		if o.ModCache == "" || !strings.Contains(out.String(), "GOPROXY=off") && !strings.Contains(out.String(), "module lookup disabled") {
			return "", "", buildError(err, out.String())
		}
		retry := exec.CommandContext(ctx, "go", "build", "-ldflags", flags, "-o", path, "./cmd/clawflow")
		retry.Dir = checkout
		retry.Env = p.Env("")
		retry.Env = append(retry.Env, "GOFLAGS=-mod=readonly", "GOPATH="+filepath.Join(p.Cache, "gopath"))
		if o.GoCache != "" {
			retry.Env = append(retry.Env, "GOCACHE="+o.GoCache)
		}
		out.Reset()
		retry.Stdout, retry.Stderr = &out, &out
		if err := retry.Run(); err != nil {
			return "", "", buildError(err, out.String())
		}
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
