//go:build linux

package store

import "syscall"

// Network filesystem magic numbers from statfs(2).
var networkFS = map[int64]bool{0x6969: true, 0x517B: true, 0xFF534D42: true, 0xFE534D42: true, 0x65735546: true, 0x00C36400: true}

func requireLocalFilesystem(dir string) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return err
	}
	if networkFS[int64(st.Type)] {
		return ErrNetworkFilesystem
	}
	return nil
}
