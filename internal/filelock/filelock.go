// Package filelock implements advisory locks on persistent inodes.
package filelock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrHeld = errors.New("lock already held")

// legacyHeld recognizes both historical JSON records and plain PID records.
// Only legacy records use PID liveness; flock records are informational.
func legacyHeld(data []byte) bool {
	var record struct {
		PID      int
		Protocol string
	}
	if json.Unmarshal(data, &record) != nil {
		record.PID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if record.Protocol == "flock" || record.PID <= 0 {
		return false
	}
	err := syscall.Kill(record.PID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Try acquires a lock before publishing its informational owner record.
// The pathname must never be unlinked, including after release or process death.
func Try(path string, owner any) (*os.File, error) {
	return acquire(context.Background(), path, owner, false)
}

// Wait uses blocking flock for kernel owners. Only legacy PID owners require
// polling, since old binaries do not participate in the advisory-lock protocol.
func Wait(ctx context.Context, path string, owner any) (*os.File, error) {
	for {
		f, err := acquire(ctx, path, owner, true)
		if !errors.Is(err, ErrHeld) {
			return f, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// waitGates bound abandoned blocking syscalls to one per lock path. References
// include callers waiting for a turn and the worker owning the current turn.
var waitGates = struct {
	sync.Mutex
	paths map[string]*waitGate
}{paths: make(map[string]*waitGate)}

type waitGate struct {
	turn chan struct{}
	refs int
}

func takeWaitTurn(ctx context.Context, path string) (func(), error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	waitGates.Lock()
	gate := waitGates.paths[path]
	if gate == nil {
		gate = &waitGate{turn: make(chan struct{}, 1)}
		gate.turn <- struct{}{}
		waitGates.paths[path] = gate
	}
	gate.refs++
	waitGates.Unlock()
	drop := func() {
		waitGates.Lock()
		defer waitGates.Unlock()
		gate.refs--
		if gate.refs == 0 {
			delete(waitGates.paths, path)
		}
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-gate.turn:
		return func() { gate.turn <- struct{}{}; drop() }, nil
	}
}

func acquire(ctx context.Context, path string, owner any, wait bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	var finishWait func()
	if wait {
		var err error
		finishWait, err = takeWaitTurn(ctx, path)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			finishWait()
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		if finishWait != nil {
			finishWait()
		}
		return nil, err
	}
	if wait {
		// Closing a descriptor does not cancel flock portably. The worker owns
		// it until the syscall returns, and releases it if the caller timed out.
		// Never publish an owner record from an abandoned wait.
		result := make(chan error)
		go func() {
			defer finishWait()
			var lockErr error
			for {
				lockErr = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
				if !errors.Is(lockErr, syscall.EINTR) {
					break
				}
			}
			select {
			case result <- lockErr:
			case <-ctx.Done():
				Release(f)
			}
		}()
		select {
		case err = <-result:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		Release(f)
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err == nil && legacyHeld(data) {
		Release(f)
		return nil, ErrHeld
	}
	if err != nil {
		Release(f)
		return nil, err
	}
	data, err = json.Marshal(owner)
	var record map[string]any
	if err == nil {
		err = json.Unmarshal(data, &record)
	}
	if err == nil {
		record["protocol"] = "flock"
		data, err = json.Marshal(record)
	}
	if err == nil {
		err = f.Truncate(0)
	}
	if err == nil {
		_, err = f.WriteAt(data, 0)
	}
	if err != nil {
		Release(f)
		return nil, fmt.Errorf("write lock owner: %w", err)
	}
	return f, nil
}

func Release(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// Held probes kernel ownership, falling back to PID liveness only for legacy
// records. Unexpected I/O errors are conservatively treated as held.
func Held(path string) bool {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return true
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	data, err := io.ReadAll(f)
	return err != nil || legacyHeld(data)
}
