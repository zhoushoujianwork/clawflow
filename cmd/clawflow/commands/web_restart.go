package commands

import (
	"fmt"
	"net"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

// Per-call seams keep restart tests independent of real processes and launchd.
type webRestartOps struct {
	managed   func(webPidInfo) (bool, error)
	kickstart func() error
	alive     func(int) bool
	signal    func(int) error
	exec      func(string, int) error
	ready     func(string) bool
	free      func(string) bool
	now       func() time.Time
	sleep     func(time.Duration)
}

func defaultWebRestartOps() webRestartOps {
	var service webService
	return webRestartOps{
		managed: func(info webPidInfo) (bool, error) {
			if runtime.GOOS != "darwin" {
				return false, nil
			}
			var err error
			service, err = newWebService()
			if err != nil {
				return false, err
			}
			return service.manages(info)
		},
		kickstart: func() error { return service.kickstart() },
		alive:     pidAlive,
		signal:    func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) },
		exec:      execSelfWeb,
		ready: func(addr string) bool {
			conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
			if err != nil {
				return false
			}
			conn.Close()
			return true
		},
		free: func(addr string) bool {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return false
			}
			ln.Close()
			return true
		},
		now: time.Now, sleep: time.Sleep,
	}
}

func restartWeb(info webPidInfo, wait time.Duration, ops webRestartOps) error {
	if wait <= 0 {
		return fmt.Errorf("restart wait must be positive")
	}
	managed, err := ops.managed(info)
	if err != nil {
		return fmt.Errorf("detect web service: %w", err)
	}
	addr := net.JoinHostPort(info.Host, strconv.Itoa(info.Port))
	if managed {
		if err := ops.kickstart(); err != nil {
			return err
		}
		host := info.Host
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		if host == "::" {
			host = "::1"
		}
		addr = net.JoinHostPort(host, strconv.Itoa(info.Port))
	} else {
		if err := ops.signal(info.PID); err != nil {
			return fmt.Errorf("send SIGTERM to pid %d: %w", info.PID, err)
		}
	}
	deadline := ops.now().Add(wait)
	for ops.now().Before(deadline) {
		if managed {
			// The old listener must have exited before its port counts as ready.
			if !ops.alive(info.PID) && ops.ready(addr) {
				return nil
			}
		} else if !ops.alive(info.PID) && ops.free(addr) {
			return ops.exec(info.Host, info.Port)
		}
		ops.sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("web restart timed out after %s on %s", wait, addr)
}
