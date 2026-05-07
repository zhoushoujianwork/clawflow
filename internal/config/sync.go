package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// GistConfigFilename is the filename used inside the clawflow-config Gist.
const GistConfigFilename = "config.yaml"

// GistDescription is the Gist description used to identify the sync Gist.
const GistDescription = "clawflow-config"

// MarshalForGist serialises the config into YAML bytes suitable for storing
// in the sync Gist. Credentials are intentionally excluded — only repos and
// settings are synced.
func MarshalForGist(cfg *Config) ([]byte, error) {
	// Build a sanitised copy that omits credentials.
	type gistConfig struct {
		Repos    map[string]Repo `yaml:"repos,omitempty"`
		Settings Settings        `yaml:"settings,omitempty"`
	}
	safe := gistConfig{
		Repos:    cfg.Repos,
		Settings: cfg.Settings,
	}
	return yaml.Marshal(safe)
}

// ApplyGistConfig merges the YAML content pulled from the sync Gist into the
// local config file. The merge strategy is a union of repos: remote repos are
// added or overwritten, but repos that exist only locally are preserved. This
// lets machine B gain machine A's repos without losing its own.
func ApplyGistConfig(content []byte) error {
	type gistConfig struct {
		Repos    map[string]Repo `yaml:"repos,omitempty"`
		Settings Settings        `yaml:"settings,omitempty"`
	}
	var remote gistConfig
	if err := yaml.Unmarshal(content, &remote); err != nil {
		return fmt.Errorf("cannot parse gist config: %w", err)
	}

	// Load (or initialise) the local config.
	local, err := Load()
	if errors.Is(err, os.ErrNotExist) || (err != nil && local == nil) {
		local = &Config{Repos: make(map[string]Repo)}
	} else if err != nil {
		return fmt.Errorf("cannot load local config: %w", err)
	}
	if local.Repos == nil {
		local.Repos = make(map[string]Repo)
	}

	// Merge: remote repos overwrite local entries with the same key.
	for k, v := range remote.Repos {
		local.Repos[k] = v
	}

	// Merge settings only when the local file has none yet (first pull).
	// We don't overwrite settings on subsequent pulls to avoid clobbering
	// machine-specific preferences (terminal, IDE, billing day, etc.).
	// Settings contains a []string field so direct struct comparison is not
	// allowed; we use the numeric fields as a proxy for "zero / unset".
	if local.Settings.PollInterval == 0 && local.Settings.ConfidenceThreshold == 0 &&
		local.Settings.AgentTimeout == 0 && local.Settings.MaxConcurrentAgents == 0 {
		local.Settings = remote.Settings
	}

	return local.Save()
}
