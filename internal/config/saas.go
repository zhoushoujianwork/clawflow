package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// SaasConfig holds the connection info written by `clawflow connect`.
type SaasConfig struct {
	URL       string `json:"url"`
	OrgID     string `json:"org_id"`
	AgentID   string `json:"agent_id"`
	SyncToken string `json:"sync_token"`
}

func SaasConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "saas.json")
}

func LoadSaasConfig() (*SaasConfig, error) {
	data, err := os.ReadFile(SaasConfigPath())
	if err != nil {
		return nil, err
	}
	var c SaasConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *SaasConfig) Save() error {
	path := SaasConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
