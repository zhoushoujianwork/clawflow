//go:build darwin

package store

import "syscall"

// mntLocal is MNT_LOCAL from <sys/mount.h>: set on locally attached volumes.
const mntLocal = 0x1000

func requireLocalFilesystem(dir string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	if st.Flags&mntLocal == 0 {
		return ErrNetworkFilesystem
	}
	return nil
}
