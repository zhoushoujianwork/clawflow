package config

import (
	"fmt"
	"os"
)

// GitHubAppSettings is machine-local and must never be exported through JSON or sync.
type GitHubAppSettings struct {
	AppID          int64  `yaml:"app_id" json:"-"`
	InstallationID int64  `yaml:"installation_id,omitempty" json:"-"`
	PrivateKeyPath string `yaml:"private_key_path" json:"-"`
}

func (s GitHubAppSettings) Enabled() bool {
	return s.AppID != 0 || s.PrivateKeyPath != "" || s.InstallationID != 0
}

// ReadPrivateKey checks the opened file, not a separate pathname stat.
// Errors intentionally omit paths and OS errors that could disclose them.
func (s GitHubAppSettings) ReadPrivateKey() ([]byte, error) {
	f, err := os.Open(expandHome(s.PrivateKeyPath))
	if err != nil {
		return nil, fmt.Errorf("github_app: private key unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("github_app: private key must be a regular file with mode 0600")
	}
	data := make([]byte, info.Size())
	_, err = f.ReadAt(data, 0)
	if err != nil {
		return nil, fmt.Errorf("github_app: cannot read private key")
	}
	return data, nil
}

func (s GitHubAppSettings) Validate() error {
	if !s.Enabled() {
		return nil
	}
	if s.AppID <= 0 || s.InstallationID < 0 || s.PrivateKeyPath == "" {
		return fmt.Errorf("github_app: app_id and private_key_path are required")
	}
	_, err := s.ReadPrivateKey()
	return err
}
