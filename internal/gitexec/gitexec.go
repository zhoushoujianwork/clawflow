// Package gitexec bounds Git transports while preserving local command behavior.
package gitexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	logpkg "github.com/zhoushoujianwork/clawflow/internal/log"
)

type repoKey struct{}

// WithRepo supplies the repository identity when a command runs in a temp tree.
func WithRepo(ctx context.Context, repo string) context.Context {
	return context.WithValue(ctx, repoKey{}, repo)
}

var ErrTimeout = errors.New("git network timeout")

// Timeout reads the machine configuration; non-positive values use five minutes.
func Timeout() time.Duration {
	if cfg, err := config.Load(); err == nil && cfg.Settings.GitTimeout > 0 {
		return time.Duration(cfg.Settings.GitTimeout) * time.Second
	}
	return 5 * time.Minute
}

// Env preserves user SSH options and adds non-interactive transport guards.
func Env(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	result := make([]string, 0, len(base)+2)
	ssh := "ssh"
	for _, e := range base {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			if v := strings.TrimPrefix(e, "GIT_SSH_COMMAND="); v != "" {
				ssh = v
			}
		} else if !strings.HasPrefix(e, "GIT_TERMINAL_PROMPT=") {
			result = append(result, e)
		}
	}
	for _, option := range []string{"BatchMode=yes", "ConnectTimeout=10", "ServerAliveInterval=15", "ServerAliveCountMax=4"} {
		if !strings.Contains(" "+ssh+" ", " -o "+option+" ") {
			ssh += " -o " + option
		}
	}
	return append(result, "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND="+ssh)
}

func operation(args []string) (string, string) {
	repo := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-C":
			i++
			if i < len(args) {
				repo = args[i]
			}
		case "-c", "--git-dir", "--work-tree":
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				continue
			}
			return args[i], repo
		}
	}
	return "", repo
}

// IsNetwork identifies the transport commands used by ClawFlow.
func IsNetwork(args ...string) bool {
	op, _ := operation(args)
	return network(op)
}

func network(op string) bool {
	switch op {
	case "fetch", "push", "pull", "clone", "ls-remote":
		return true
	}
	return false
}

// execute retains the supplied command's IO and environment. Only network
// operations receive a deadline, keepalives and a separate process group.
func execute(ctx context.Context, original *exec.Cmd, mode string) ([]byte, error) {
	op, repo := operation(original.Args[1:])
	run := func(c *exec.Cmd) ([]byte, error) {
		switch mode {
		case "output":
			return c.Output()
		case "combined":
			return c.CombinedOutput()
		}
		return nil, c.Run()
	}
	if !network(op) {
		return run(original)
	}
	if repo == "" {
		repo = original.Dir
	}
	if name, ok := ctx.Value(repoKey{}).(string); ok && name != "" {
		repo = name
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout())
	defer cancel()
	args, env, authErr := githubCredentials(ctx, original, original.Args[1:], Env(original.Env))
	if authErr != nil {
		return nil, authErr
	}
	c := exec.CommandContext(ctx, original.Path, append([]string{"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=60"}, args...)...)
	c.Dir, c.Env = original.Dir, env
	c.Stdin, c.Stdout, c.Stderr = original.Stdin, original.Stdout, original.Stderr
	c.ExtraFiles = original.ExtraFiles
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	c.WaitDelay = time.Second
	start := time.Now()
	out, err := run(c)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// Do not log arguments, remote URLs, config values or subprocess output:
		// any of them may contain credentials.
		lg, _ := logpkg.Open("run")
		lg.Warn("run/git_timeout", "command", "git "+op, "repo", repo, "duration", time.Since(start))
		_ = lg.Close()
		return nil, fmt.Errorf("%w: git %s: %w", ErrTimeout, op, context.DeadlineExceeded)
	}
	return out, err
}

func Run(ctx context.Context, c *exec.Cmd) error              { _, err := execute(ctx, c, "run"); return err }
func Output(ctx context.Context, c *exec.Cmd) ([]byte, error) { return execute(ctx, c, "output") }
func CombinedOutput(ctx context.Context, c *exec.Cmd) ([]byte, error) {
	return execute(ctx, c, "combined")
}
