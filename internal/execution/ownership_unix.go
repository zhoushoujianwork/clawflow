//go:build darwin || linux

package execution

import (
	"errors"
	"os"
	"syscall"
)

// Keep the inode and descriptor: unlinking a held lock would allow split ownership.
func acquireOwnershipFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, &Error{Kind: "session_busy"}
		}
		return nil, err
	}
	return f, nil
}
