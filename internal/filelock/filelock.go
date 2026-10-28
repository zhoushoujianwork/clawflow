// Package filelock implements advisory locks on persistent inodes.
package filelock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
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
