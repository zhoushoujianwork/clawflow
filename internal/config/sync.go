package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// GistConfigFilename is the filename used inside the clawflow-config Gist.
const GistConfigFilename = "config.yaml"

// GistDescription is the Gist description used to identify the sync Gist.
const GistDescription = "clawflow-config"

// syncableRepo is a Repo with local_path stripped for upload.
// We keep the same field tags so it round-trips cleanly through YAML.
//
// bound_machine IS synced: it's a fleet-wide directive ("only machine X
// should process this repo"), not a per-machine local fact. Without sync,
// every machine in the fleet would have to be re-bound by hand, which
// defeats the purpose of the binding.
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
	BoundMachine          string            `yaml:"bound_machine,omitempty"`
	UpdatedAt             time.Time         `yaml:"updated_at,omitempty"`
	UpdatedBy             string            `yaml:"updated_by,omitempty"`
	// local_path is intentionally absent — genuinely machine-local
	// (paths differ across OSes and home dirs).
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
		BoundMachine:          r.BoundMachine,
		UpdatedAt:             r.UpdatedAt,
		UpdatedBy:             r.UpdatedBy,
	}
}

// fromSyncable converts a syncableRepo back to a Repo, preserving the given
// local_path (the only genuinely machine-local field).
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
		BoundMachine:          s.BoundMachine,
		UpdatedAt:             s.UpdatedAt,
		UpdatedBy:             s.UpdatedBy,
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
// Any repo entry without an updated_at timestamp is stamped with the current
// time before serialisation (one-shot migration for legacy configs).
func MarshalForGist(cfg *Config) ([]byte, error) {
	// Migrate any legacy entries in-place so the Gist always carries timestamps.
	MigrateTimestamps(cfg)

	payload := gistPayload{
		Repos:    make(map[string]syncableRepo, len(cfg.Repos)),
		Settings: cfg.Settings,
	}
	for k, v := range cfg.Repos {
		payload.Repos[k] = toSyncable(v)
	}
	return yaml.Marshal(payload)
}

// MergeResult summarises what the LWW merge did, for logging.
type MergeResult struct {
	Replaced   int // remote entry was newer → replaced local
	Kept       int // local entry was newer → kept local
	Added      int // entry only existed on one side → unioned in
	Conflicted int // same timestamp but different content → conflict file written
}

// MergeConfigs applies the entry-level Last-Write-Wins (LWW) merge strategy:
//
//   - settings.*   → cloud wins (remote overwrites local)
//   - repos list   → per-entry LWW using updated_at:
//     • Gist newer  → replace local entry wholesale
//     • local newer → keep local entry (will be pushed next time)
//     • same ts, same content → no-op
//     • same ts, different content → write config.conflict.yaml, return error
//     • entry only on one side → union in (preserve)
//     • local_path always comes from the local copy
//
// Entries without updated_at (zero time) are treated as "oldest possible" so
// any timestamped entry wins over them. This handles legacy configs gracefully.
//
// The returned Config is the merged result; it is NOT saved to disk.
// Call cfg.Save() to persist.
func MergeConfigs(local *Config, remoteYAML []byte) (*Config, error) {
	return mergeConfigsInternal(local, remoteYAML, true)
}

// mergeConfigsInternal is the shared implementation. When conflictFatal is
// true a same-timestamp divergence returns an error; when false it is
// recorded in the MergeResult but does not block the merge (used by
// DiffConfigs which only needs the merged view).
func mergeConfigsInternal(local *Config, remoteYAML []byte, conflictFatal bool) (*Config, error) {
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

	var conflictKeys []string

	// Apply remote repos using per-entry LWW.
	for k, remoteRepo := range remote.Repos {
		var localPath string
		if existing, ok := local.Repos[k]; ok {
			localPath = existing.LocalPath
		}

		localEntry, localExists := local.Repos[k]

		if !localExists {
			// Entry only in remote → add it (union).
			merged.Repos[k] = fromSyncable(remoteRepo, "")
			continue
		}

		// Both sides have the entry — compare timestamps.
		localTs := localEntry.UpdatedAt
		remoteTs := remoteRepo.UpdatedAt

		switch {
		case remoteTs.IsZero() && localTs.IsZero():
			// Neither side has a timestamp (legacy). Remote wins to match
			// the old "cloud wins" behaviour and avoid silent local drift.
			merged.Repos[k] = fromSyncable(remoteRepo, localPath)

		case remoteTs.IsZero():
			// Remote is legacy, local has a timestamp → local is newer.
			// Keep local (already in merged.Repos from the seed loop).

		case localTs.IsZero():
			// Local is legacy, remote has a timestamp → remote wins.
			merged.Repos[k] = fromSyncable(remoteRepo, localPath)

		case remoteTs.After(localTs):
			// Remote is newer → replace local entry wholesale.
			merged.Repos[k] = fromSyncable(remoteRepo, localPath)

		case localTs.After(remoteTs):
			// Local is newer → keep local (already in merged.Repos).

		default:
			// Same timestamp. Check if content actually differs.
			localSync := toSyncable(localEntry)
			if syncableEqual(localSync, remoteRepo) {
				// Identical — no-op.
			} else {
				// Same timestamp, different content → conflict.
				conflictKeys = append(conflictKeys, k)
				// Keep local as the tiebreaker (conservative).
			}
		}
	}

	if len(conflictKeys) > 0 {
		// Write the conflict artifact regardless of conflictFatal so the
		// user always has a file to inspect.
		_ = writeConflictFile(conflictKeys, local, remote.Repos)
		if conflictFatal {
			return merged, fmt.Errorf(
				"sync conflict: %d repo entry(ies) have the same updated_at but different content: %s — see %s",
				len(conflictKeys), strings.Join(conflictKeys, ", "), ConflictPath(),
			)
		}
	}

	return merged, nil
}

// syncableEqual reports whether two syncableRepo values are semantically
// identical (ignoring UpdatedAt/UpdatedBy which are the tiebreaker fields,
// not content fields).
func syncableEqual(a, b syncableRepo) bool {
	// Marshal both to YAML and compare — simple, correct, and avoids a
	// field-by-field comparison that would need updating every time a new
	// field is added. We zero out the timestamp fields before comparing.
	a.UpdatedAt = time.Time{}
	a.UpdatedBy = ""
	b.UpdatedAt = time.Time{}
	b.UpdatedBy = ""
	ab, _ := yaml.Marshal(a)
	bb, _ := yaml.Marshal(b)
	return string(ab) == string(bb)
}

// writeConflictFile writes a human-readable conflict artifact to
// ~/.clawflow/config/config.conflict.yaml. The file contains both sides of
// each conflicting entry so the user can decide which to keep.
func writeConflictFile(keys []string, local *Config, remoteRepos map[string]syncableRepo) error {
	var sb strings.Builder
	sb.WriteString("# ClawFlow sync conflict — same updated_at but different content\n")
	sb.WriteString("# Resolve by editing config.yaml and running 'clawflow sync push'.\n")
	sb.WriteString("# This file is deleted automatically on the next successful conflict-free sync.\n\n")

	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("# --- conflict: %s ---\n", k))
		sb.WriteString("# LOCAL:\n")
		if r, ok := local.Repos[k]; ok {
			b, _ := yaml.Marshal(map[string]syncableRepo{k: toSyncable(r)})
			for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
				sb.WriteString("#   " + line + "\n")
			}
		}
		sb.WriteString("# REMOTE:\n")
		if r, ok := remoteRepos[k]; ok {
			b, _ := yaml.Marshal(map[string]syncableRepo{k: r})
			for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
				sb.WriteString("#   " + line + "\n")
			}
		}
		sb.WriteByte('\n')
	}

	path := ConflictPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// MigrateTimestamps stamps any repo entry that has a zero UpdatedAt with the
// current time and the current hostname. Returns true when at least one entry
// was stamped (caller should push the normalised config to Gist).
func MigrateTimestamps(cfg *Config) bool {
	hostname, _ := os.Hostname()
	now := time.Now().UTC()
	migrated := false
	for name, r := range cfg.Repos {
		if r.UpdatedAt.IsZero() {
			r.UpdatedAt = now
			r.UpdatedBy = hostname
			cfg.Repos[name] = r
			migrated = true
		}
	}
	return migrated
}

// ApplyGistConfig merges the YAML content pulled from the sync Gist into the
// local config file and saves it. Uses LWW MergeConfigs for the merge strategy.
// On a same-timestamp conflict, writes config.conflict.yaml and returns an error.
// On success, deletes any stale config.conflict.yaml from a previous run and
// records the pull timestamp so manual-edit detection has a fresh baseline.
func ApplyGistConfig(content []byte) error {
	// Load (or initialise) the local config.
	local, err := Load()
	if errors.Is(err, os.ErrNotExist) || (err != nil && local == nil) {
		local = &Config{Repos: make(map[string]Repo)}
	} else if err != nil {
		return fmt.Errorf("cannot load local config: %w", err)
	}

	// Migrate legacy entries (no updated_at) before merging so the LWW
	// comparison has timestamps on both sides. If any entries were stamped,
	// the caller should push the normalised config back to Gist — but we
	// don't do that here (ApplyGistConfig is pull-only). The next AutoPush
	// or manual push will propagate the normalised timestamps.
	MigrateTimestamps(local)

	merged, err := mergeConfigsInternal(local, content, true)
	if err != nil {
		return err
	}

	if saveErr := merged.Save(); saveErr != nil {
		return saveErr
	}

	// Record the pull timestamp so manual-edit detection has a fresh baseline.
	// Best-effort: a failure here doesn't undo the successful pull.
	_ = RecordLastPulled()

	// Clean up a stale conflict file from a previous run now that this
	// pull completed without conflicts.
	_ = os.Remove(ConflictPath())
	return nil
}

// RecordLastPulled stamps the current UTC time into credentials.yaml as
// LastPulledAt. Called after every successful pull so manual-edit detection
// has a fresh baseline. Best-effort: callers should ignore the error.
func RecordLastPulled() error {
	creds, err := LoadCredentials()
	if err != nil {
		return err
	}
	if creds == nil {
		creds = &Credentials{}
	}
	creds.LastPulledAt = time.Now().UTC().Format(time.RFC3339)
	return SaveCredentials(creds)
}

// DiffConfigs produces a human-readable diff between the local config and the
// result of merging in remoteYAML. Returns an empty string when there are no
// changes. The diff is line-oriented and suitable for terminal display.
func DiffConfigs(local *Config, remoteYAML []byte) (string, error) {
	// Use non-fatal merge so DiffConfigs can show the diff even when there
	// are conflicts (the conflict file is still written as a side-effect).
	merged, err := mergeConfigsInternal(local, remoteYAML, false)
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
