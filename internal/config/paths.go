package config

import (
	"os"
	"path/filepath"
)

// RootDir follows the compatibility alias installed by the macOS host installer.
// Existing CLI installations keep their directory until explicitly migrated.
func RootDir() string {
	home, _ := os.UserHomeDir()
	legacy := filepath.Join(home, ".clawflow")
	if info, err := os.Lstat(legacy); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if canonical, err := filepath.EvalSymlinks(legacy); err == nil {
			return canonical
		}
	}
	return legacy
}
