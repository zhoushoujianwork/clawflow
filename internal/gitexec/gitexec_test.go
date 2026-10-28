package gitexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func fakeGit(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTimeoutConfiguration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if Timeout() != 5*time.Minute {
		t.Fatal(Timeout())
	}
	cfg := &config.Config{Settings: config.Settings{GitTimeout: 1}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if Timeout() != time.Second {
		t.Fatal(Timeout())
	}
	// The default deadline, not a caller deadline, cancels this operation.
	git := fakeGit(t, "sleep 30 &\nwait\n")
	start := time.Now()
	err := Run(WithRepo(context.Background(), "owner/repo"), exec.Command(git, "fetch", "https://user:secret@example.com/r"))
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("process group or deadline did not terminate")
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".clawflow/logs/run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "run/git_timeout") || !strings.Contains(string(data), "git fetch") || !strings.Contains(string(data), "owner/repo") || strings.Contains(string(data), "secret") {
		t.Fatalf("unsafe or absent log: %s", data)
	}
}

func TestNetworkCommandsAndKeepalives(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /custom/key -p 2222")
	git := fakeGit(t, "printf '%s\\n' \"$GIT_SSH_COMMAND\" \"$@\"\n")
	for _, op := range []string{"fetch", "push", "pull", "clone", "ls-remote"} {
		out, err := Output(context.Background(), exec.Command(git, "-C", t.TempDir(), op, "origin"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"ssh -i /custom/key -p 2222", "ServerAliveInterval=15", "ServerAliveCountMax=4", "http.lowSpeedLimit=1000", "http.lowSpeedTime=60"} {
			if !strings.Contains(string(out), want) {
				t.Fatalf("%s missing %s: %s", op, want, out)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := Output(ctx, exec.Command(git, "status"))
	if err != nil || strings.Contains(string(out), "http.lowSpeed") || strings.Contains(string(out), "ServerAlive") {
		t.Fatalf("local command changed: %s %v", out, err)
	}
}

func TestCallerDeadlineKillsDescendants(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	marker := filepath.Join(t.TempDir(), "survived")
	git := fakeGit(t, "(sleep 1; touch '"+marker+"') &\nwait\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := CombinedOutput(ctx, exec.Command(git, "fetch", "origin"))
	if !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("descendant survived cancellation")
	}
}

func TestNetworkExitAndCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, code := range []string{"0", "7"} {
		git := fakeGit(t, "exit "+code+"\n")
		err := Run(context.Background(), exec.Command(git, "push", "origin"))
		if (err == nil) != (code == "0") || errors.Is(err, ErrTimeout) {
			t.Fatalf("exit %s: %v", code, err)
		}
	}
	git := fakeGit(t, "sleep 30 &\nwait\n")
	for _, op := range []string{"fetch", "push", "pull", "clone", "ls-remote"} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err := Run(ctx, exec.Command(git, op))
		cancel()
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("%s: %v", op, err)
		}
	}
}

func TestEnvPreservesExplicitOptions(t *testing.T) {
	env := Env([]string{"PATH=/bin", "GIT_SSH_COMMAND=ssh -F custom-config", "GIT_TERMINAL_PROMPT=1"})
	if strings.Join(Env(env), "\n") != strings.Join(env, "\n") {
		t.Fatal("hardening is not idempotent")
	}
	if !strings.Contains(strings.Join(env, "\n"), "GIT_SSH_COMMAND=ssh -F custom-config -o") {
		t.Fatal(env)
	}
}
