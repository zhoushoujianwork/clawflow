// Package sync provides shared helpers for pushing and pulling ClawFlow
// config to/from a private GitHub Gist. Both the CLI commands (sync push/pull,
// login) and the HTTP API handlers reuse these functions so the logic lives
// in exactly one place.
//
// In addition to config.yaml, the sync package also handles project-level
// knowledge assets stored under ~/.clawflow/projects/<name>/. Since Gist is
// flat (no directories), a "--" delimiter convention maps directory paths to
// filenames:
//
//	projects/<name>/context.md  →  projects--<name>--context.md
//
// See codec.go for the encoding details and projects.go for discovery/apply.
package sync

import (
	"fmt"
	"os"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// Client returns a GitHub client and the stored Gist ID from credentials.
// Returns an error when no token is configured.
func Client() (*github.Client, string, error) {
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, "", fmt.Errorf("cannot load credentials: %w", err)
	}
	if creds == nil || creds.GHToken == "" {
		return nil, "", fmt.Errorf("no GitHub token found — run 'clawflow login <token>' first")
	}
	gh := github.New(creds.GHToken, "")
	return gh, creds.GistID, nil
}

// BuildGistContent serialises the current local config into a YAML string
// suitable for uploading to the sync Gist. Credentials and local_path are
// intentionally excluded.
func BuildGistContent() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	b, err := config.MarshalForGist(cfg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// BuildAllGistFiles returns the complete set of files to upload to the Gist:
// config.yaml plus all eligible asset files discovered under
// ~/.clawflow/projects/ and ~/.clawflow/skills/. The map key is the Gist
// filename; the value is the file content.
func BuildAllGistFiles() (map[string]string, error) {
	configContent, err := BuildGistContent()
	if err != nil {
		return nil, fmt.Errorf("cannot build config payload: %w", err)
	}

	files := map[string]string{
		config.GistConfigFilename: configContent,
	}

	projectFiles, err := DiscoverProjectAssets()
	if err != nil {
		// Non-fatal: log and continue with config-only push.
		fmt.Fprintf(os.Stderr, "⚠ sync: cannot discover project assets: %v\n", err)
	} else {
		for k, v := range projectFiles {
			files[k] = v
		}
	}

	skillFiles, err := DiscoverSkillAssets()
	if err != nil {
		// Non-fatal: a malformed skills dir shouldn't block the rest of the push.
		fmt.Fprintf(os.Stderr, "⚠ sync: cannot discover skill assets: %v\n", err)
	} else {
		for k, v := range skillFiles {
			files[k] = v
		}
	}

	return files, nil
}

// FetchGistContent retrieves the config.yaml content from the given Gist.
func FetchGistContent(gh *github.Client, gistID string) ([]byte, error) {
	gist, err := gh.GetGist(gistID)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch Gist %s: %w", gistID, err)
	}
	f, ok := gist.Files[config.GistConfigFilename]
	if !ok {
		return nil, fmt.Errorf("Gist %s has no %s file", gistID, config.GistConfigFilename)
	}
	return []byte(f.Content), nil
}

// FetchAndApplyProjectAssets fetches the Gist and writes any synced asset
// files — project assets (projects--*) and user-defined operator skills
// (skills--*) — back to their correct local paths under ~/.clawflow/.
// Directories are created as needed. Existing files are overwritten
// (cloud-wins, matching config.yaml behaviour).
//
// Called alongside FetchGistContent + ApplyGistConfig during pull so that
// both project knowledge and custom operators are restored on a new machine.
//
// The name retains "ProjectAssets" for backwards compatibility; the
// underlying ApplyProjectAssets handles both prefixes.
func FetchAndApplyProjectAssets(gh *github.Client, gistID string) error {
	gist, err := gh.GetGist(gistID)
	if err != nil {
		return fmt.Errorf("cannot fetch Gist %s: %w", gistID, err)
	}

	// Convert github.GistFile map to our internal GistFileContent map.
	files := make(map[string]GistFileContent, len(gist.Files))
	for name, f := range gist.Files {
		files[name] = GistFileContent{Content: f.Content}
	}

	return ApplyProjectAssets(files)
}

// PushToGist uploads content to an existing Gist or creates a new one.
// Returns the Gist ID (which may be newly created if none existed).
//
// Deprecated: prefer PushAllToGist which also syncs project assets.
func PushToGist(gh *github.Client, gistID, content string) (string, error) {
	files := map[string]string{config.GistConfigFilename: content}
	return pushFiles(gh, gistID, files)
}

// PushAllToGist uploads config.yaml plus all project asset files to the Gist.
// Returns the Gist ID (which may be newly created if none existed).
func PushAllToGist(gh *github.Client, gistID string, files map[string]string) (string, error) {
	return pushFiles(gh, gistID, files)
}

// hasNonEmptyFile reports whether files contains at least one entry with
// non-empty content. GitHub's Gist API rejects a push whose effective file
// set is empty, so callers must not send one.
func hasNonEmptyFile(files map[string]string) bool {
	for _, content := range files {
		if content != "" {
			return true
		}
	}
	return false
}

// pushFiles is the shared implementation for PushToGist and PushAllToGist.
func pushFiles(gh *github.Client, gistID string, files map[string]string) (string, error) {
	// Guard against an empty payload up front. Without this, a config with no
	// real content would produce a {"files":{}} PATCH that GitHub rejects with
	// 422 missing_field "files" on every cycle (issue #195).
	if !hasNonEmptyFile(files) {
		return "", fmt.Errorf("no non-empty files to push to Gist (refusing to send an empty payload)")
	}

	if gistID != "" {
		// Try to update the existing Gist.
		_, err := gh.UpdateGist(gistID, files)
		if err == nil {
			return gistID, nil
		}
		// Gist may have been deleted — fall through to create.
		fmt.Fprintf(os.Stderr, "Stored Gist %s not accessible (%v), creating new one...\n", gistID, err)
	}

	// Search for an existing Gist with the canonical description before creating.
	existing, err := gh.FindGistByDescription(config.GistDescription)
	if err != nil {
		return "", fmt.Errorf("cannot search Gists: %w", err)
	}
	// Only retry against a *different* Gist. If the lookup returns the same ID
	// we already failed to update, retrying with the identical payload would
	// just reproduce the same error — fall through to create a fresh Gist.
	if existing != nil && existing.ID != gistID {
		_, err := gh.UpdateGist(existing.ID, files)
		if err != nil {
			return "", fmt.Errorf("cannot update Gist: %w", err)
		}
		return existing.ID, nil
	}

	// Create a brand-new private Gist.
	newGist, err := gh.CreateGist(config.GistDescription, files)
	if err != nil {
		return "", fmt.Errorf("cannot create Gist: %w", err)
	}
	return newGist.ID, nil
}

// SaveGistID persists the Gist ID into credentials.yaml.
func SaveGistID(gistID string) error {
	creds, err := config.LoadCredentials()
	if err != nil {
		return err
	}
	if creds == nil {
		creds = &config.Credentials{}
	}
	if creds.GistID == gistID {
		return nil // already stored
	}
	creds.GistID = gistID
	return config.SaveCredentials(creds)
}
