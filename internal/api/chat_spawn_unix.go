//go:build !windows

package api

import "syscall"

// detachAttr returns SysProcAttr fields that put the spawned process
// in its own session, so it survives clawflow web exiting. Unix-only;
// Windows uses a different mechanism (see chat_spawn_windows.go if
// support is added later).
func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
