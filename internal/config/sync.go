package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// GistConfigFilename is the filename used inside the clawflow-config Gist.
const GistConfigFilename = "config.yaml"

// GistDescription is the Gist description used to identify the sync Gist.
const GistDescription = "clawflow-config"

// syncableRepo is a Repo with local_path stripped for upload.
// We keep the same field tags so it round-trips cleanly through YAML.
type syncableRepo struct {
	Enabled               bool              `yaml:"enabled"`
	Platform              string            `yaml:"platform,omitempty"`
	BaseURL               string            `yaml:"base_url,omitempty"`
	BaseBranch            string            `yaml:"base_branch"`
	Owner                 string            `yaml:"owner"`
	Description           string            `yaml:"description"`
	AddedAt               string            `yaml:"added_at"`
	WebhookConfigured     bool              `yaml:"webhook_configured"`
	Labels                map[string]string `yaml:"labels"`
	TestCommand           string            `yaml:"test_command,omitempty"`
	CIRequired            bool              `yaml:"ci_required,omitempty"`
	CITimeout             int               `yaml:"ci_timeout,omitempty"`
	AutoMerge             bool              `yaml:"auto_merge,omitempty"`
	AutoApprove           bool              `yaml:"auto_approve,omitempty"`
	AutoEvaluateAllIssues bool              `yaml:"auto_evaluate_all_issues,omitempty"`
	// local_path is intentionally absent — never synced.
}

// toSyncable converts a Repo to its syncable representation (strips local_path).
func toSyncable(r Repo) syncableRepo {
	return syncableRepo{
		Enabled:               r.Enabled,
		Platform:              r.Platform,
		BaseURL:               r.BaseURL,
		BaseBranch:            r.BaseBranch,
		Owner:                 r.Owner,
		Description:           r.Description,
		AddedAt:               r.AddedAt,
		WebhookConfigured:     r.WebhookConfigured,
		Labels:                r.Labels,
		TestCommand:           r.TestCommand,
		CIRequired:            r.CIRequired,
		CITimeout:             r.CITimeout,
		AutoMerge:             r.AutoMerge,
		AutoApprove:           r.AutoApprove,
		AutoEvaluateAllIssues: r.AutoEvaluateAllIssues,
	}
}

// fromSyncable converts a syncableRepo back to a Repo, preserving the given local_path.
func fromSyncable(s syncableRepo, localPath string) Repo {
	return Repo{
		Enabled:               s.Enabled,
		Platform:              s.Platform,
		BaseURL:               s.BaseURL,
		BaseBranch:            s.BaseBranch,
		LocalPath:             localPath,
		Owner:                 s.Owner,
		Description:           s.Description,
		AddedAt:               s.AddedAt,
		WebhookConfigured:     s.WebhookConfigured,
		Labels:                s.Labels,
		TestCommand:           s.TestCommand,
		CIRequired:            s.CIRequired,
		CITimeout:             s.CITimeout,
		AutoMerge:             s.AutoMerge,
		AutoApprove:           s.AutoApprove,
		AutoEvaluateAllIssues: s.AutoEvaluateAllIssues,
	}
}

// gistPayload is the structure stored in the Gist file.
type gistPayload struct {
	Repos    map[string]syncableRepo `yaml:"repos,omitempty"`
	Settings Settings                `yaml:"settings,omitempty"`
}

// MarshalForGist serialises the config into YAML bytes suitable for storing
// in the sync Gist. Credentials and local_path fields are intentionally
// excluded — only repos (without local paths) and settings are synced.
func MarshalForGist(cfg *Config) ([]byte, error) {
	payload := gistPayload{
		Repos:    make(map[string]syncableRepo, len(cfg.Repos)),
		Settings: cfg.Settings,
	}
	for k, v := range cfg.Repos {
		payload.Repos[k] = toSyncable(v)
	}
	return yaml.Marshal(payload)
}

// MergeConfigs applies the field-level merge strategy defined in issue #90:
//
//   - settings.*   → cloud wins (remote overwrites local)
//   - repos list   → union merge: remote repos are added/updated; repos that
//     exist only locally are preserved; per-repo local_path always
//     comes from the local copy (local wins for that field).
//
// The returned Config is the merged result; it is NOT saved to disk.
// Call cfg.Save() to persist.
func MergeConfigs(local *Config, remoteYAML []byte) (*Config, error) {
	var remote gistPayload
	if err := yaml.Unmarshal(remoteYAML, &remote); err != nil {
		return nil, fmt.Errorf("cannot parse remote config: %w", err)
	}

	merged := &Config{
		// Settings: cloud wins entirely.
		Settings: remote.Settings,
		Repos:    make(map[string]Repo, len(local.Repos)),
	}

	// Start with all local repos (preserves local_path and local-only repos).
	for k, v := range local.Repos {
		merged.Repos[k] = v
	}

	// Apply remote repos: union merge, local_path preserved from local copy.
	for k, remoteRepo := range remote.Repos {
		localPath := ""
		if existing, ok := local.Repos[k]; ok {
			localPath = existing.LocalPath
		}
		merged.Repos[k] = fromSyncable(remoteRepo, localPath)
	}

	return merged, nil
}

// ApplyGistConfig merges the YAML content pulled from the sync Gist into the
// local config file and saves it. Uses MergeConfigs for the field-level strategy.
func ApplyGistConfig(content []byte) error {
	// Load (or initialise) the local config.
	local, err := Load()
	if errors.Is(err, os.ErrNotExist) || (err != nil && local == nil) {
		local = &Config{Repos: make(map[string]Repo)}
	} else if err != nil {
		return fmt.Errorf("cannot load local config: %w", err)
	}

	merged, err := MergeConfigs(local, content)
	if err != nil {
		return err
	}
	return merged.Save()
}

// DiffConfigs produces a human-readable diff between the local config and the
// result of merging in remoteYAML. Returns an empty string when there are no
// changes. The diff is line-oriented and suitable for terminal display.
func DiffConfigs(local *Config, remoteYAML []byte) (string, error) {
	merged, err := MergeConfigs(local, remoteYAML)
	if err != nil {
		return "", err
	}

	localBytes, err := yaml.Marshal(local)
	if err != nil {
		return "", fmt.Errorf("cannot marshal local config: %w", err)
	}
	mergedBytes, err := yaml.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("cannot marshal merged config: %w", err)
	}

	localStr := string(localBytes)
	mergedStr := string(mergedBytes)
	if localStr == mergedStr {
		return "", nil
	}

	return lineDiff(localStr, mergedStr), nil
}

// lineDiff produces a simple +/- line diff between two multi-line strings.
// It is intentionally minimal — no context lines, no unified header — just
// enough for a human to see what would change before confirming a sync pull.
func lineDiff(before, after string) string {
	beforeLines := strings.Split(strings.TrimRight(before, "\n"), "\n")
	afterLines := strings.Split(strings.TrimRight(after, "\n"), "\n")

	// Build lookup sets for quick membership tests.
	beforeSet := make(map[string]bool, len(beforeLines))
	afterSet := make(map[string]bool, len(afterLines))
	for _, l := range beforeLines {
		beforeSet[l] = true
	}
	for _, l := range afterLines {
		afterSet[l] = true
	}

	var removed, added []string
	for _, l := range beforeLines {
		if !afterSet[l] {
			removed = append(removed, "- "+l)
		}
	}
	for _, l := range afterLines {
		if !beforeSet[l] {
			added = append(added, "+ "+l)
		}
	}

	sort.Strings(removed)
	sort.Strings(added)

	var sb strings.Builder
	for _, l := range removed {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	for _, l := range added {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	return sb.String()
}
