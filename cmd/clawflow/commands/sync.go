package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clawsync "github.com/zhoushoujianwork/clawflow/internal/sync"
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
			gh, gistID, err := clawsync.Client()
			if err != nil {
				return err
			}

			content, err := clawsync.BuildGistContent()
			if err != nil {
				return fmt.Errorf("cannot build config payload: %w", err)
			}

			gistID, err = clawsync.PushToGist(gh, gistID, content)
			if err != nil {
				return err
			}

			// Persist the Gist ID if it was just created.
			if err := clawsync.SaveGistID(gistID); err != nil {
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
			gh, gistID, err := clawsync.Client()
			if err != nil {
				return err
			}
			if gistID == "" {
				return fmt.Errorf("no Gist ID found — run 'clawflow sync push' first or 'clawflow login' to set up sync")
			}

			remoteYAML, err := clawsync.FetchGistContent(gh, gistID)
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
	gh, gistID, err := clawsync.Client()
	if err != nil {
		return err
	}
	if gistID == "" {
		return fmt.Errorf("no Gist ID found — run 'clawflow sync push' first or 'clawflow login' to set up sync")
	}

	remoteYAML, err := clawsync.FetchGistContent(gh, gistID)
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
