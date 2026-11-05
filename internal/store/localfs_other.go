//go:build !darwin && !linux

package store

func requireLocalFilesystem(string) error { return nil }
