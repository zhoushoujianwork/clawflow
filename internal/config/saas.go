package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SaasConfig holds the connection info written by `clawflow connect`.
type SaasConfig struct {
	URL       string `json:"url"`
	OrgID     string `json:"org_id"`
	AgentID   string `json:"agent_id"`
	SyncToken string `json:"sync_token"`
	LastSync  string `json:"last_sync,omitempty"`
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

// WorkerConfig holds the SaaS worker connection info written by `clawflow login`
// or `clawflow config set --saas-url --token`.
type WorkerConfig struct {
	SaasURL     string `yaml:"saas_url,omitempty"`
	WorkerToken string `yaml:"worker_token,omitempty"`
}

// workerConfigPath returns the dedicated worker config file path.
func workerConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "config", "worker.yaml")
}

// LoadWorkerConfig reads ~/.clawflow/config/worker.yaml.
func LoadWorkerConfig() (*WorkerConfig, error) {
	data, err := os.ReadFile(workerConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &WorkerConfig{}, nil
		}
		return nil, err
	}
	var c WorkerConfig
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the WorkerConfig to ~/.clawflow/config/worker.yaml (mode 0600).
func (c *WorkerConfig) Save() error {
	path := workerConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
