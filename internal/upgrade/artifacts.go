package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// FileSHA returns the hex SHA-256 of a regular file.
func FileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PreserveFile stores an immutable, content-addressed copy of a.Path under
// dir/<sha256>/clawflow and returns the artifact pointing at it. The source
// must hash to a.SHA256. An existing copy is reused only if it still hashes
// to the same value, so a tampered or truncated store entry is an error, not
// a silent substitution.
func PreserveFile(dir string, a Artifact) (Artifact, error) {
	if !shaPattern.MatchString(a.SHA256) {
		return a, fmt.Errorf("artifact has no valid SHA-256")
	}
	dest := filepath.Join(dir, a.SHA256, "clawflow")
	if got, err := FileSHA(dest); err == nil {
		if got != a.SHA256 {
			return a, fmt.Errorf("preserved artifact %s is corrupt", short(a.SHA256))
		}
		a.Path = dest
		return a, nil
	}
	got, err := FileSHA(a.Path)
	if err != nil {
		return a, fmt.Errorf("read artifact: %w", err)
	}
	if got != a.SHA256 {
		return a, fmt.Errorf("artifact content %s does not match the recorded %s", short(got), short(a.SHA256))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return a, err
	}
	src, err := os.Open(a.Path)
	if err != nil {
		return a, err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".artifact-*")
	if err != nil {
		return a, err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return a, err
	}
	if err := tmp.Chmod(0o500); err != nil {
		tmp.Close()
		return a, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return a, err
	}
	if err := tmp.Close(); err != nil {
		return a, err
	}
	if got, err := FileSHA(tmp.Name()); err != nil || got != a.SHA256 {
		return a, fmt.Errorf("copied artifact failed verification")
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return a, err
	}
	a.Path = dest
	return a, nil
}
