package agentcapacity

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestCapacityProcessHelper(t *testing.T) {
	if os.Getenv("CLAWFLOW_CAPACITY_HELPER") != "1" {
		return
	}
	release, err := Acquire(context.Background())
	if err != nil {
		os.Exit(2)
	}
	fmt.Println("ready")
	var signal [1]byte
	_, _ = io.ReadFull(os.Stdin, signal[:])
	release()
	os.Exit(0)
}

func TestCapacityAcrossProcessesAndCrash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Settings: config.Settings{MaxConcurrentAgents: 2}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type holder struct {
		cmd   *exec.Cmd
		input io.WriteCloser
	}
	var holders []holder
	for i := 0; i < 2; i++ {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityProcessHelper$")
		cmd.Env = append(os.Environ(), "CLAWFLOW_CAPACITY_HELPER=1", "GORACE=atexit_sleep_ms=0")
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()
		line, err := bufio.NewReader(output).ReadString('\n')
		if err != nil || line != "ready\n" {
			t.Fatalf("ready: %q %v", line, err)
		}
		holders = append(holders, holder{cmd, input})
	}
	if used := Used(); used != 2 {
		t.Fatalf("used=%d", used)
	}
	blocked, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if release, err := Acquire(blocked); !errors.Is(err, context.DeadlineExceeded) {
		if release != nil {
			release()
		}
		t.Fatalf("third holder: %v", err)
	}
	if err := holders[0].cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holders[0].cmd.Wait()
	release, err := Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if Used() != 2 {
		t.Fatal("crashed slot was not reused")
	}
	release()
	release()
	_ = holders[1].input.Close()
	if err := holders[1].cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if Used() != 0 {
		t.Fatal("released slots still held")
	}
}

func TestCapacityCancellationAndConfigErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if total, err := Total(); err != nil || total != DefaultTotal {
		t.Fatalf("default: %d %v", total, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cfg := &config.Config{Settings: config.Settings{MaxConcurrentAgents: 1}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ConfigPath(), []byte("settings: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background()); err == nil {
		t.Fatal("invalid config must fail closed")
	}
}
