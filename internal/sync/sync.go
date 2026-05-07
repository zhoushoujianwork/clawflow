// Package sync provides shared helpers for pushing and pulling ClawFlow
// config to/from a private GitHub Gist. Both the CLI commands (sync push/pull,
// login) and the HTTP API handlers reuse these functions so the logic lives
// in exactly one place.
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

// PushToGist uploads content to an existing Gist or creates a new one.
// Returns the Gist ID (which may be newly created if none existed).
func PushToGist(gh *github.Client, gistID, content string) (string, error) {
	files := map[string]string{config.GistConfigFilename: content}

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
	if existing != nil {
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
