package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// NewSyncCmd returns the `clawflow sync` command tree.
//
// Subcommands:
//
//	clawflow sync push  — upload local config to the private clawflow-config Gist
//	clawflow sync pull  — download Gist config and merge into local config
//	clawflow sync       — show diff and confirm before applying (interactive)
func NewSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync config with your private GitHub Gist",
		Long: `Synchronise ~/.clawflow/config/config.yaml with your private
clawflow-config GitHub Gist, enabling multi-machine config portability.

Credentials (credentials.yaml) and local_path fields are never synced.

Merge rules:
  settings.*  → cloud wins on pull
  repos       → union merge; local_path always kept from local copy
  credentials → never synced

Run 'clawflow sync' (no subcommand) to preview the diff and confirm.
Run 'clawflow sync push' to upload immediately.
Run 'clawflow sync pull' to download and merge immediately.`,
		RunE: runSyncInteractive,
	}

	cmd.AddCommand(newSyncPushCmd())
	cmd.AddCommand(newSyncPullCmd())
	return cmd
}

// newSyncPushCmd returns `clawflow sync push`.
func newSyncPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Upload local config to the clawflow-config Gist",
		Long: `Serialises ~/.clawflow/config/config.yaml (excluding credentials and
local_path fields) and uploads it to your private clawflow-config Gist.

If no Gist exists yet, one is created automatically. The Gist ID is stored
in credentials.yaml so subsequent pushes update the same Gist.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			gh, gistID, err := syncClient()
			if err != nil {
				return err
			}

			content, err := buildGistContent()
			if err != nil {
				return fmt.Errorf("cannot build config payload: %w", err)
			}

			gistID, err = pushToGist(gh, gistID, content)
			if err != nil {
				return err
			}

			// Persist the Gist ID if it was just created.
			if err := saveGistID(gistID); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not persist Gist ID: %v\n", err)
			}

			fmt.Fprintf(os.Stderr, "Config pushed to Gist %s\n", gistID)
			return nil
		},
	}
}

// newSyncPullCmd returns `clawflow sync pull`.
func newSyncPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Download Gist config and merge into local config",
		Long: `Fetches the clawflow-config Gist and merges it into
~/.clawflow/config/config.yaml using the field-level merge strategy:

  settings.*  → cloud wins
  repos       → union merge (local_path preserved from local copy)
  credentials → never touched`,
		RunE: func(cmd *cobra.Command, args []string) error {
			gh, gistID, err := syncClient()
			if err != nil {
				return err
			}
			if gistID == "" {
				return fmt.Errorf("no Gist ID found — run 'clawflow sync push' first or 'clawflow login' to set up sync")
			}

			remoteYAML, err := fetchGistContent(gh, gistID)
			if err != nil {
				return err
			}

			if err := config.ApplyGistConfig(remoteYAML); err != nil {
				return fmt.Errorf("merge failed: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Config pulled and merged from Gist %s → %s\n", gistID, config.ConfigPath())
			return nil
		},
	}
}

// runSyncInteractive is the bare `clawflow sync` handler: shows a diff and
// asks for confirmation before applying the pull.
func runSyncInteractive(cmd *cobra.Command, args []string) error {
	gh, gistID, err := syncClient()
	if err != nil {
		return err
	}
	if gistID == "" {
		return fmt.Errorf("no Gist ID found — run 'clawflow sync push' first or 'clawflow login' to set up sync")
	}

	remoteYAML, err := fetchGistContent(gh, gistID)
	if err != nil {
		return err
	}

	local, err := config.Load()
	if err != nil {
		return fmt.Errorf("cannot load local config: %w", err)
	}

	diff, err := config.DiffConfigs(local, remoteYAML)
	if err != nil {
		return fmt.Errorf("cannot compute diff: %w", err)
	}

	if diff == "" {
		fmt.Println("Local config is already in sync with the Gist — nothing to do.")
		return nil
	}

	fmt.Println("Changes that would be applied (- removed, + added):")
	fmt.Println()
	fmt.Print(diff)
	fmt.Println()

	if !confirmPrompt("Apply these changes? [y/N] ") {
		fmt.Println("Aborted.")
		return nil
	}

	if err := config.ApplyGistConfig(remoteYAML); err != nil {
		return fmt.Errorf("merge failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Config merged from Gist %s → %s\n", gistID, config.ConfigPath())
	return nil
}

// syncClient loads credentials and returns a GitHub client plus the stored Gist ID.
func syncClient() (*github.Client, string, error) {
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

// fetchGistContent retrieves the config.yaml content from the given Gist.
func fetchGistContent(gh *github.Client, gistID string) ([]byte, error) {
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

// pushToGist uploads content to an existing Gist or creates a new one.
// Returns the Gist ID (which may be new if none existed).
func pushToGist(gh *github.Client, gistID, content string) (string, error) {
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

// saveGistID persists the Gist ID into credentials.yaml.
func saveGistID(gistID string) error {
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

// confirmPrompt prints prompt and returns true if the user types "y" or "yes".
func confirmPrompt(prompt string) bool {
	fmt.Print(prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}
