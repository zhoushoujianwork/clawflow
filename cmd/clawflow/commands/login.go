package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clawsync "github.com/zhoushoujianwork/clawflow/internal/sync"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// NewLoginCmd returns the `clawflow login` command.
//
// Flow:
//  1. Accept a GitHub token (arg or --token flag).
//  2. Validate it against GET /user.
//  3. Search the user's Gists for description="clawflow-config".
//     - Found  → store Gist ID, pull config from Gist.
//     - Not found → create a new private Gist, store its ID, push current config.
//  4. Persist the token + Gist ID in credentials.yaml.
func NewLoginCmd() *cobra.Command {
	var tokenFlag string

	cmd := &cobra.Command{
		Use:   "login [gh-token]",
		Short: "Authenticate and set up config Gist sync",
		Long: `Saves your GitHub token and discovers (or creates) your private
clawflow-config Gist for multi-machine config sync.

Required token scopes: repo (full), read:org, gist

On first login on a new machine the config is pulled from the existing Gist
automatically. On first login ever a new private Gist is created.`,
		Args:    cobra.MaximumNArgs(1),
		Example: "  clawflow login ghp_xxxxxxxxxxxx\n  clawflow login --token ghp_xxxxxxxxxxxx",
		RunE: func(cmd *cobra.Command, args []string) error {
			token := tokenFlag
			if token == "" && len(args) > 0 {
				token = args[0]
			}
			if token == "" {
				// Fall back to already-stored token so `clawflow login` (no
				// args) can re-run the Gist discovery without re-entering the
				// token.
				creds, _ := config.LoadCredentials()
				if creds != nil {
					token = creds.GHToken
				}
			}
			if token == "" {
				return fmt.Errorf("no token provided — pass it as an argument or use --token")
			}

			gh := github.New(token, "")

			// 1. Validate token.
			login, err := gh.GetAuthenticatedUser()
			if err != nil {
				return fmt.Errorf("token validation failed: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Authenticated as %s\n", login)

			// 2. Load existing credentials (to preserve other fields).
			creds, err := config.LoadCredentials()
			if err != nil {
				return fmt.Errorf("cannot load credentials: %w", err)
			}
			if creds == nil {
				creds = &config.Credentials{}
			}
			creds.GHToken = token

			// 3. Discover or create the sync Gist.
			gistID, err := discoverOrCreateGist(gh, creds)
			if err != nil {
				return err
			}
			creds.GistID = gistID

			// 4. Persist credentials.
			if err := config.SaveCredentials(creds); err != nil {
				return fmt.Errorf("cannot save credentials: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Credentials saved to %s\n", config.CredentialsPath())
			return nil
		},
	}

	cmd.Flags().StringVar(&tokenFlag, "token", "", "GitHub personal access token")
	return cmd
}

// discoverOrCreateGist finds the clawflow-config Gist or creates one.
// It returns the Gist ID and handles the pull/push of config content.
func discoverOrCreateGist(gh *github.Client, creds *config.Credentials) (string, error) {
	// If we already have a stored Gist ID, verify it still exists.
	if creds.GistID != "" {
		gist, err := gh.GetGist(creds.GistID)
		if err == nil && gist != nil {
			fmt.Fprintf(os.Stderr, "Using existing config Gist (id: %s)\n", gist.ID)
			return gist.ID, nil
		}
		// Gist gone (deleted externally) — fall through to search.
		fmt.Fprintf(os.Stderr, "Stored Gist ID %s no longer accessible, searching...\n", creds.GistID)
	}

	// Search for an existing Gist with the canonical description.
	gist, err := gh.FindGistByDescription(config.GistDescription)
	if err != nil {
		// Gist scope missing is a common mistake — surface a clear message.
		if strings.Contains(err.Error(), "missing the 'gist' scope") {
			return "", fmt.Errorf("%w\n\nAdd the 'gist' scope to your token at https://github.com/settings/tokens", err)
		}
		return "", fmt.Errorf("cannot search gists: %w", err)
	}

	if gist != nil {
		// Found an existing Gist — pull config from it.
		fmt.Fprintf(os.Stderr, "Found existing config Gist (id: %s), pulling...\n", gist.ID)
		if err := pullConfigFromGist(gh, gist.ID); err != nil {
			// Non-fatal: warn but don't abort login.
			fmt.Fprintf(os.Stderr, "Warning: could not pull config from Gist: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Config pulled from Gist → %s\n", config.ConfigPath())
		}
		return gist.ID, nil
	}

	// No existing Gist — create one and push current local config.
	fmt.Fprintf(os.Stderr, "No config Gist found, creating new private Gist...\n")
	content, err := buildGistContent()
	if err != nil {
		// If there's no local config yet, seed with an empty placeholder.
		content = "# clawflow config — managed by clawflow login\nrepos: {}\n"
	}
	newGist, err := gh.CreateGist(config.GistDescription, map[string]string{
		config.GistConfigFilename: content,
	})
	if err != nil {
		if strings.Contains(err.Error(), "missing the 'gist' scope") {
			return "", fmt.Errorf("%w\n\nAdd the 'gist' scope to your token at https://github.com/settings/tokens", err)
		}
		return "", fmt.Errorf("cannot create config Gist: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Created new config Gist (id: %s)\n", newGist.ID)
	return newGist.ID, nil
}

// pullConfigFromGist fetches the Gist content and merges it into the local config.
func pullConfigFromGist(gh *github.Client, gistID string) error {
	content, err := clawsync.FetchGistContent(gh, gistID)
	if err != nil {
		return err
	}
	return config.ApplyGistConfig(content)
}

// buildGistContent serialises the current local config for upload.
func buildGistContent() (string, error) {
	return clawsync.BuildGistContent()
}
