package commands

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// NewCloudCmd contains cloud-config migration and authentication commands.
func NewCloudCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Manage ClawFlow SaaS connection",
		Long: `Manage the ClawFlow SaaS connection used by registered workers.

Gist sync remains available under 'clawflow sync' as a legacy migration path.`,
	}
	cmd.AddCommand(newCloudLoginCmd())
	cmd.AddCommand(newCloudPullCmd())
	cmd.AddCommand(newCloudPushCmd())
	cmd.AddCommand(newCloudServeCmd())
	cmd.AddCommand(newCloudStatusCmd())
	return cmd
}

func newCloudLoginCmd() *cobra.Command {
	var (
		baseURL string
		token   string
	)
	cmd := &cobra.Command{
		Use:   "login [token]",
		Short: "Store ClawFlow SaaS URL and access token",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && token == "" {
				token = args[0]
			}
			token = strings.TrimSpace(token)
			if token == "" {
				return fmt.Errorf("cloud access token is required")
			}
			creds, err := config.LoadCredentials()
			if err != nil {
				return err
			}
			cfg := cloud.FromCredentials(creds)
			if baseURL != "" {
				cfg.BaseURL = strings.TrimRight(baseURL, "/")
			}
			cfg.AccessToken = token
			if err := config.SaveCredentials(cfg.ApplyToCredentials(creds)); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Cloud login saved for %s\n", cfg.BaseURL)
			return nil
		},
	}
	cmd.Flags().StringVar(&baseURL, "url", cloud.DefaultBaseURL, "ClawFlow SaaS API base URL")
	cmd.Flags().StringVar(&token, "token", "", "Cloud access token")
	return cmd
}

func newCloudPullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Pull cloud config into the local cache",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("cloud pull is reserved for the SaaS config API phase")
		},
	}
}

func newCloudPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Migrate local config into cloud config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("cloud push is reserved for the SaaS config API phase")
		},
	}
}

func newCloudStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local cloud connection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cloud.LoadConfig()
			if err != nil {
				return err
			}
			fmt.Printf("cloud_url: %s\n", cfg.BaseURL)
			fmt.Printf("access_token_configured: %t\n", cfg.AccessToken != "")
			fmt.Printf("machine_id: %s\n", cfg.MachineID)
			fmt.Printf("worker_id: %s\n", cfg.WorkerID)
			fmt.Printf("worker_token_configured: %t\n", cfg.WorkerToken != "")
			return nil
		},
	}
}

func newCloudServeCmd() *cobra.Command {
	var (
		host      string
		port      int
		storeFlag string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run a local development ClawFlow SaaS API",
		Long: `Run a local development ClawFlow SaaS API.

By default the server uses an in-memory store (data is lost on exit).
Pass --store to select a persistent backend:

  --store memory                          in-memory (default)
  --store sqlite:///path/to/state.db      persistent SQLite file
  --store sqlite://:memory:               ephemeral SQLite (for testing)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openCloudStore(storeFlag)
			if err != nil {
				return err
			}
			addr := fmt.Sprintf("%s:%d", host, port)
			fmt.Fprintf(os.Stderr, "ClawFlow cloud dev API listening on http://%s (store: %s)\n", addr, storeFlag)
			return http.ListenAndServe(addr, cloud.NewServer(store))
		},
	}
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Host to bind")
	cmd.Flags().IntVar(&port, "port", 8790, "Port to bind")
	cmd.Flags().StringVar(&storeFlag, "store", "memory",
		`Store backend: "memory" (default) or "sqlite:///path/to/file.db"`)
	return cmd
}

// openCloudStore returns a Store for the given flag value.
func openCloudStore(storeFlag string) (cloud.Store, error) {
	switch {
	case storeFlag == "" || storeFlag == "memory":
		return cloud.NewMemoryStore(), nil
	case strings.HasPrefix(storeFlag, "sqlite://"):
		path := strings.TrimPrefix(storeFlag, "sqlite://")
		return cloud.NewSQLiteStore(path)
	default:
		return nil, fmt.Errorf("unknown store %q: use \"memory\" or \"sqlite:///path/to/file.db\"", storeFlag)
	}
}
