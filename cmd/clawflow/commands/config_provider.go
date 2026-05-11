package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// newConfigProviderCmd returns the `clawflow config provider` command group.
func newConfigProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage Claude API providers (multi-provider failover)",
		Long: `Manage the ordered list of Claude API providers used by the operator runner.
Providers are tried in priority order (index 0 first); when one fails with a
transient error (rate limit, auth failure, network error, 5xx), the runner
automatically fails over to the next enabled provider.`,
	}
	cmd.AddCommand(newProviderListCmd())
	cmd.AddCommand(newProviderAddCmd())
	cmd.AddCommand(newProviderRemoveCmd())
	cmd.AddCommand(newProviderEnableCmd())
	cmd.AddCommand(newProviderDisableCmd())
	cmd.AddCommand(newProviderMoveCmd())
	cmd.AddCommand(newProviderTestCmd())
	return cmd
}

func newProviderListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured Claude API providers",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			if len(creds.ClaudeProviders) == 0 {
				fmt.Println("No providers configured.")
				if creds.ClaudeAPIKey != "" || creds.ClaudeBaseURL != "" {
					fmt.Println("(Legacy single-provider config detected — run any provider command to trigger migration.)")
				}
				return nil
			}
			fmt.Printf("%-4s %-24s %-40s %-16s %-8s\n", "IDX", "NAME", "BASE_URL", "MODEL", "ENABLED")
			fmt.Println(strings.Repeat("-", 96))
			for i, p := range creds.ClaudeProviders {
				model := p.Model
				if model == "" {
					model = "(global default)"
				}
				baseURL := p.BaseURL
				if baseURL == "" {
					baseURL = "(api.anthropic.com)"
				}
				enabled := "yes"
				if !p.Enabled {
					enabled = "no"
				}
				keyHint := ""
				if p.APIKey != "" {
					n := len(p.APIKey)
					if n >= 4 {
						keyHint = " key:…" + p.APIKey[n-4:]
					} else {
						keyHint = " key:set"
					}
				}
				fmt.Printf("%-4d %-24s %-40s %-16s %-8s%s\n", i, p.Name, baseURL, model, enabled, keyHint)
			}
			return nil
		},
	}
}

func newProviderAddCmd() *cobra.Command {
	var (
		baseURL  string
		apiKey   string
		model    string
		disabled bool
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a new Claude API provider",
		Args:  cobra.ExactArgs(1),
		Example: `  clawflow config provider add "Anthropic Official" --base-url https://api.anthropic.com --api-key sk-ant-...
  clawflow config provider add "OpenRouter" --base-url https://openrouter.ai/api/v1 --api-key sk-or-...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("name cannot be empty")
			}
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			p := config.ClaudeProvider{
				Name:    name,
				BaseURL: strings.TrimSpace(baseURL),
				APIKey:  apiKey,
				Model:   strings.TrimSpace(model),
				Enabled: !disabled,
			}
			creds.ClaudeProviders = append(creds.ClaudeProviders, p)
			if err := config.SaveCredentials(creds); err != nil {
				return fmt.Errorf("failed to save: %w", err)
			}
			idx := len(creds.ClaudeProviders) - 1
			fmt.Printf("Provider %q added at index %d\n", name, idx)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseURL, "base-url", "", "API base URL (default: api.anthropic.com)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key")
	cmd.Flags().StringVar(&model, "model", "", "Model override (default: use global default)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Add provider in disabled state")
	return cmd
}

func newProviderRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name-or-index>",
		Short: "Remove a Claude API provider by name or index",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			idx, err := resolveProviderArg(creds, args[0])
			if err != nil {
				return err
			}
			name := creds.ClaudeProviders[idx].Name
			creds.ClaudeProviders = append(creds.ClaudeProviders[:idx], creds.ClaudeProviders[idx+1:]...)
			if err := config.SaveCredentials(creds); err != nil {
				return fmt.Errorf("failed to save: %w", err)
			}
			fmt.Printf("Provider %q (index %d) removed\n", name, idx)
			return nil
		},
	}
}

func newProviderEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name-or-index>",
		Short: "Enable a Claude API provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setProviderEnabled(args[0], true)
		},
	}
}

func newProviderDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name-or-index>",
		Short: "Disable a Claude API provider (keeps it in the list but skips it during runs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setProviderEnabled(args[0], false)
		},
	}
}

func setProviderEnabled(nameOrIdx string, enabled bool) error {
	creds, err := config.LoadCredentials()
	if err != nil {
		return err
	}
	idx, err := resolveProviderArg(creds, nameOrIdx)
	if err != nil {
		return err
	}
	p := creds.ClaudeProviders[idx]
	p.Enabled = enabled
	creds.ClaudeProviders[idx] = p
	if err := config.SaveCredentials(creds); err != nil {
		return fmt.Errorf("failed to save: %w", err)
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("Provider %q (index %d) %s\n", p.Name, idx, state)
	return nil
}

func newProviderMoveCmd() *cobra.Command {
	var toIndex int
	cmd := &cobra.Command{
		Use:   "move <name-or-index> --to <index>",
		Short: "Move a provider to a specific priority position",
		Long: `Move a provider to a specific index in the priority list.
Index 0 is the highest priority (tried first).`,
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow config provider move \"OpenRouter\" --to 0",
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			fromIdx, err := resolveProviderArg(creds, args[0])
			if err != nil {
				return err
			}
			n := len(creds.ClaudeProviders)
			if toIndex < 0 || toIndex >= n {
				return fmt.Errorf("--to index %d out of range [0, %d]", toIndex, n-1)
			}
			// Remove from current position and insert at target.
			p := creds.ClaudeProviders[fromIdx]
			providers := append(creds.ClaudeProviders[:fromIdx], creds.ClaudeProviders[fromIdx+1:]...)
			// Insert at toIndex.
			providers = append(providers, config.ClaudeProvider{}) // grow
			copy(providers[toIndex+1:], providers[toIndex:])
			providers[toIndex] = p
			creds.ClaudeProviders = providers
			if err := config.SaveCredentials(creds); err != nil {
				return fmt.Errorf("failed to save: %w", err)
			}
			fmt.Printf("Provider %q moved to index %d\n", p.Name, toIndex)
			return nil
		},
	}
	cmd.Flags().IntVar(&toIndex, "to", 0, "Target index (0 = highest priority)")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func newProviderTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <name-or-index>",
		Short: "Test connectivity for a specific provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			idx, err := resolveProviderArg(creds, args[0])
			if err != nil {
				return err
			}
			p := creds.ClaudeProviders[idx]
			fmt.Printf("Testing provider %q (index %d)…\n", p.Name, idx)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			probeArgs := []string{"-p"}
			// Only force --bare when an explicit API key is set. Empty key
			// means OAuth/keychain, and --bare would skip that lookup.
			if p.APIKey != "" {
				probeArgs = append(probeArgs, "--bare")
			}
			probeArgs = append(probeArgs,
				"--model", config.DefaultChatModel,
				"--output-format", "text",
				"say PONG",
			)
			c := exec.CommandContext(ctx, claude.Resolve(), probeArgs...)
			c.Env = claude.EnvWithCredentials(os.Environ(), p.APIKey, p.BaseURL)
			var stdout, stderr bytes.Buffer
			c.Stdout = &stdout
			c.Stderr = &stderr

			if err := c.Run(); err != nil {
				detail := strings.TrimSpace(stderr.String())
				if detail == "" {
					detail = strings.TrimSpace(stdout.String())
				}
				if detail == "" {
					detail = err.Error()
				}
				fmt.Fprintf(os.Stderr, "✗ FAIL: %s\n", detail)
				return fmt.Errorf("provider test failed")
			}
			fmt.Printf("✓ OK — reply: %s\n", strings.TrimSpace(stdout.String()))
			return nil
		},
	}
}

// resolveProviderArg resolves a name-or-index argument to a provider index.
// If the argument is a valid integer, it's used as an index directly.
// Otherwise, it's matched against provider names (case-insensitive).
func resolveProviderArg(creds *config.Credentials, arg string) (int, error) {
	// Try numeric index first.
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 0 || n >= len(creds.ClaudeProviders) {
			return 0, fmt.Errorf("index %d out of range (have %d providers)", n, len(creds.ClaudeProviders))
		}
		return n, nil
	}
	// Name match (case-insensitive).
	lower := strings.ToLower(arg)
	for i, p := range creds.ClaudeProviders {
		if strings.ToLower(p.Name) == lower {
			return i, nil
		}
	}
	return 0, fmt.Errorf("provider %q not found (use 'clawflow config provider list' to see available providers)", arg)
}
