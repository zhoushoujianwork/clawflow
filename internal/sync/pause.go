package sync

import (
	"fmt"
	"os"
	"path/filepath"
)

// PausePath is deliberately outside directories archived by reset.
func PausePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) || filepath.Clean(home) == string(filepath.Separator) {
		return "", fmt.Errorf("a non-root absolute HOME is required")
	}
	root := filepath.Join(home, ".clawflow")
	// Only the host installer's standard migration alias is supported. Do
	// not follow arbitrary links or allow a linked pause marker.
	if st, statErr := os.Lstat(root); statErr == nil && st.Mode()&os.ModeSymlink != 0 {
		target, linkErr := os.Readlink(root)
		standard := filepath.Join(home, "Library", "Application Support", "ClawFlow")
		if linkErr != nil || target != standard {
			return "", fmt.Errorf("refusing symlink %s", root)
		}
		for _, part := range []string{"Library", "Library/Application Support", "Library/Application Support/ClawFlow"} {
			path := filepath.Join(home, part)
			info, err := os.Lstat(path)
			if err != nil {
				return "", err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("refusing non-directory or symlink %s", path)
			}
		}
		root = standard
	}
	for _, path := range []string{home, root, filepath.Join(root, "sync-paused")} {
		st, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink %s", path)
		}
	}
	return filepath.Join(root, "sync-paused"), nil
}

func IsPaused() (bool, error) {
	path, err := PausePath()
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func CheckNotPaused() error {
	paused, err := IsPaused()
	if err != nil {
		return err
	}
	if paused {
		return fmt.Errorf("local sync is paused; review remote state before 'clawflow sync resume'")
	}
	return nil
}

func Pause() error {
	path, err := PausePath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// Resume only removes the marker. It never contacts the server.
func Resume() error {
	path, err := PausePath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
